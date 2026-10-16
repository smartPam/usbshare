// Package usbip implementuje fazę sterującą protokołu USB/IP (OP_REQ_DEVLIST,
// OP_REQ_IMPORT) zgodną z linuksowym usbip oraz usbip-win2.
// Wszystkie pola są w kolejności bajtów big-endian (network order).
package usbip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	Version uint16 = 0x0111

	OpReqDevlist uint16 = 0x8005
	OpRepDevlist uint16 = 0x0005
	OpReqImport  uint16 = 0x8003
	OpRepImport  uint16 = 0x0003

	StOK      uint32 = 0
	StNA      uint32 = 1
	StDevBusy uint32 = 2
	StDevErr  uint32 = 3
	StNoDev   uint32 = 4
	StError   uint32 = 5

	// Rozmiar struktury usbip_usb_device na drucie.
	DeviceSize = 312
	// Rozmiar struktury usbip_usb_interface na drucie.
	InterfaceSize = 4
	// Rozmiar nagłówka op_common.
	HeaderSize = 8
	// Długość pola busid.
	BusIDSize = 32
	pathSize  = 256
)

// Prędkości USB w numeracji jądra Linux (enum usb_device_speed).
const (
	SpeedUnknown   uint32 = 0
	SpeedLow       uint32 = 1
	SpeedFull      uint32 = 2
	SpeedHigh      uint32 = 3
	SpeedWireless  uint32 = 4
	SpeedSuper     uint32 = 5
	SpeedSuperPlus uint32 = 6
)

type Interface struct {
	Class    uint8 `json:"class"`
	SubClass uint8 `json:"subclass"`
	Protocol uint8 `json:"protocol"`
}

type Device struct {
	Path               string      `json:"path"`
	BusID              string      `json:"busid"`
	BusNum             uint32      `json:"busnum"`
	DevNum             uint32      `json:"devnum"`
	Speed              uint32      `json:"speed"`
	IDVendor           uint16      `json:"id_vendor"`
	IDProduct          uint16      `json:"id_product"`
	BcdDevice          uint16      `json:"bcd_device"`
	DeviceClass        uint8       `json:"device_class"`
	DeviceSubClass     uint8       `json:"device_subclass"`
	DeviceProtocol     uint8       `json:"device_protocol"`
	ConfigurationValue uint8       `json:"configuration_value"`
	NumConfigurations  uint8       `json:"num_configurations"`
	NumInterfaces      uint8       `json:"num_interfaces"`
	Interfaces         []Interface `json:"interfaces"`
}

func fixedStr(s string, n int) []byte {
	b := make([]byte, n)
	copy(b[:n-1], s) // zawsze zostaw miejsce na NUL
	return b
}

func cStr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

// Marshal zwraca 312-bajtową strukturę usbip_usb_device.
func (d *Device) Marshal() []byte {
	b := make([]byte, 0, DeviceSize)
	b = append(b, fixedStr(d.Path, pathSize)...)
	b = append(b, fixedStr(d.BusID, BusIDSize)...)
	b = binary.BigEndian.AppendUint32(b, d.BusNum)
	b = binary.BigEndian.AppendUint32(b, d.DevNum)
	b = binary.BigEndian.AppendUint32(b, d.Speed)
	b = binary.BigEndian.AppendUint16(b, d.IDVendor)
	b = binary.BigEndian.AppendUint16(b, d.IDProduct)
	b = binary.BigEndian.AppendUint16(b, d.BcdDevice)
	b = append(b, d.DeviceClass, d.DeviceSubClass, d.DeviceProtocol,
		d.ConfigurationValue, d.NumConfigurations, d.NumInterfaces)
	return b
}

// MarshalWithInterfaces zwraca strukturę urządzenia i jego interfejsy (dla OP_REP_DEVLIST).
func (d *Device) MarshalWithInterfaces() []byte {
	b := d.Marshal()
	for i := 0; i < int(d.NumInterfaces); i++ {
		var it Interface
		if i < len(d.Interfaces) {
			it = d.Interfaces[i]
		}
		b = append(b, it.Class, it.SubClass, it.Protocol, 0)
	}
	return b
}

func UnmarshalDevice(b []byte) (Device, error) {
	var d Device
	if len(b) < DeviceSize {
		return d, errors.New("usbip: za krótka struktura urządzenia")
	}
	d.Path = cStr(b[0:256])
	d.BusID = cStr(b[256:288])
	d.BusNum = binary.BigEndian.Uint32(b[288:])
	d.DevNum = binary.BigEndian.Uint32(b[292:])
	d.Speed = binary.BigEndian.Uint32(b[296:])
	d.IDVendor = binary.BigEndian.Uint16(b[300:])
	d.IDProduct = binary.BigEndian.Uint16(b[302:])
	d.BcdDevice = binary.BigEndian.Uint16(b[304:])
	d.DeviceClass = b[306]
	d.DeviceSubClass = b[307]
	d.DeviceProtocol = b[308]
	d.ConfigurationValue = b[309]
	d.NumConfigurations = b[310]
	d.NumInterfaces = b[311]
	return d, nil
}

func Header(code uint16, status uint32) []byte {
	b := make([]byte, 0, HeaderSize)
	b = binary.BigEndian.AppendUint16(b, Version)
	b = binary.BigEndian.AppendUint16(b, code)
	b = binary.BigEndian.AppendUint32(b, status)
	return b
}

func ReadHeader(r io.Reader) (version, code uint16, status uint32, err error) {
	var h [HeaderSize]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	return binary.BigEndian.Uint16(h[0:]), binary.BigEndian.Uint16(h[2:]), binary.BigEndian.Uint32(h[4:]), nil
}

// ImportRequest buduje OP_REQ_IMPORT.
func ImportRequest(busid string) []byte {
	return append(Header(OpReqImport, 0), fixedStr(busid, BusIDSize)...)
}

// ReadBusID czyta 32-bajtowe pole busid.
func ReadBusID(r io.Reader) (string, error) {
	var b [BusIDSize]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return cStr(b[:]), nil
}

// DevlistReply buduje OP_REP_DEVLIST.
func DevlistReply(devs []Device) []byte {
	b := Header(OpRepDevlist, StOK)
	b = binary.BigEndian.AppendUint32(b, uint32(len(devs)))
	for i := range devs {
		b = append(b, devs[i].MarshalWithInterfaces()...)
	}
	return b
}

// ParseDevlistReply czyta treść OP_REP_DEVLIST (po nagłówku).
func ParseDevlistReply(r io.Reader) ([]Device, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	count := binary.BigEndian.Uint32(n[:])
	if count > 1024 {
		return nil, fmt.Errorf("usbip: podejrzana liczba urządzeń %d", count)
	}
	devs := make([]Device, 0, count)
	buf := make([]byte, DeviceSize)
	for i := uint32(0); i < count; i++ {
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		d, _ := UnmarshalDevice(buf)
		for j := 0; j < int(d.NumInterfaces); j++ {
			var it [InterfaceSize]byte
			if _, err := io.ReadFull(r, it[:]); err != nil {
				return nil, err
			}
			d.Interfaces = append(d.Interfaces, Interface{it[0], it[1], it[2]})
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// SpeedFromSysfs zamienia wartość z /sys/.../speed (Mb/s) na enum jądra.
func SpeedFromSysfs(s string) uint32 {
	switch strings.TrimSpace(s) {
	case "1.5":
		return SpeedLow
	case "12":
		return SpeedFull
	case "480":
		return SpeedHigh
	case "53.3-480":
		return SpeedWireless
	case "5000":
		return SpeedSuper
	case "10000", "20000":
		return SpeedSuperPlus
	}
	return SpeedUnknown
}

func SpeedName(s uint32) string {
	switch s {
	case SpeedLow:
		return "USB 1.0 (1,5 Mb/s)"
	case SpeedFull:
		return "USB 1.1 (12 Mb/s)"
	case SpeedHigh:
		return "USB 2.0 (480 Mb/s)"
	case SpeedWireless:
		return "Wireless USB"
	case SpeedSuper:
		return "USB 3.0 (5 Gb/s)"
	case SpeedSuperPlus:
		return "USB 3.1+ (10+ Gb/s)"
	}
	return "nieznana"
}
