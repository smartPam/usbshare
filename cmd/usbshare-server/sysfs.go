package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"usbshare/internal/usbip"
)

// Stany z /sys/bus/usb/devices/<busid>/usbip_status (drivers/usb/usbip/usbip_common.h).
const (
	sdevAvailable = 1
	sdevUsed      = 2
	sdevError     = 3
)

const stubDriver = "usbip-host"

// Sysfs obsługuje urządzenia USB i sterownik usbip-host przez /sys.
type Sysfs struct{ Root string }

type LocalDevice struct {
	usbip.Device
	Manufacturer string
	Product      string
	Serial       string
	Driver       string // sterownik urządzenia (np. "usb", "usbip-host")
	UsbipStatus  int    // 0 = brak atrybutu (nie jest podpięte pod usbip-host)
}

func (s *Sysfs) devicesDir() string { return filepath.Join(s.Root, "bus/usb/devices") }
func (s *Sysfs) driverDir(d string) string {
	return filepath.Join(s.Root, "bus/usb/drivers", d)
}

func (s *Sysfs) StubLoaded() bool {
	_, err := os.Stat(s.driverDir(stubDriver))
	return err == nil
}

func readAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func hex16(s string) uint16 { v, _ := strconv.ParseUint(s, 16, 16); return uint16(v) }
func hex8(s string) uint8   { v, _ := strconv.ParseUint(s, 16, 8); return uint8(v) }
func dec8(s string) uint8   { v, _ := strconv.ParseUint(s, 10, 8); return uint8(v) }
func dec32(s string) uint32 { v, _ := strconv.ParseUint(s, 10, 32); return uint32(v) }

// validBusID akceptuje tylko nazwy w rodzaju "1-1" lub "3-2.4.1".
func validBusID(b string) bool {
	if b == "" || len(b) >= usbip.BusIDSize || strings.HasPrefix(b, "usb") {
		return false
	}
	for _, c := range b {
		if !(c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return strings.Contains(b, "-")
}

// List zwraca wszystkie urządzenia USB poza hubami głównymi i interfejsami.
func (s *Sysfs) List() ([]LocalDevice, error) {
	ents, err := os.ReadDir(s.devicesDir())
	if err != nil {
		return nil, err
	}
	var out []LocalDevice
	for _, e := range ents {
		if !validBusID(e.Name()) {
			continue
		}
		d, err := s.Get(e.Name())
		if err == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BusID < out[j].BusID })
	return out, nil
}

func (s *Sysfs) Get(busid string) (LocalDevice, error) {
	var d LocalDevice
	if !validBusID(busid) {
		return d, fmt.Errorf("nieprawidłowy busid %q", busid)
	}
	dir := filepath.Join(s.devicesDir(), busid)
	if _, err := os.Stat(filepath.Join(dir, "idVendor")); err != nil {
		return d, fmt.Errorf("brak urządzenia %s", busid)
	}
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		path = dir
	}
	d.Path = path
	d.BusID = busid
	d.BusNum = dec32(readAttr(dir, "busnum"))
	d.DevNum = dec32(readAttr(dir, "devnum"))
	d.Speed = usbip.SpeedFromSysfs(readAttr(dir, "speed"))
	d.IDVendor = hex16(readAttr(dir, "idVendor"))
	d.IDProduct = hex16(readAttr(dir, "idProduct"))
	d.BcdDevice = hex16(readAttr(dir, "bcdDevice"))
	d.DeviceClass = hex8(readAttr(dir, "bDeviceClass"))
	d.DeviceSubClass = hex8(readAttr(dir, "bDeviceSubClass"))
	d.DeviceProtocol = hex8(readAttr(dir, "bDeviceProtocol"))
	d.ConfigurationValue = dec8(readAttr(dir, "bConfigurationValue"))
	d.NumConfigurations = dec8(readAttr(dir, "bNumConfigurations"))
	d.NumInterfaces = dec8(readAttr(dir, "bNumInterfaces"))
	for i := 0; i < int(d.NumInterfaces); i++ {
		idir := filepath.Join(dir, fmt.Sprintf("%s:%d.%d", busid, d.ConfigurationValue, i))
		d.Interfaces = append(d.Interfaces, usbip.Interface{
			Class:    hex8(readAttr(idir, "bInterfaceClass")),
			SubClass: hex8(readAttr(idir, "bInterfaceSubClass")),
			Protocol: hex8(readAttr(idir, "bInterfaceProtocol")),
		})
	}
	d.Manufacturer = readAttr(dir, "manufacturer")
	d.Product = readAttr(dir, "product")
	d.Serial = readAttr(dir, "serial")
	if link, err := os.Readlink(filepath.Join(dir, "driver")); err == nil {
		d.Driver = filepath.Base(link)
	}
	if st := readAttr(dir, "usbip_status"); st != "" {
		d.UsbipStatus, _ = strconv.Atoi(st)
	}
	return d, nil
}

func writeAttr(path, val string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(val)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("zapis %q do %s: %w", val, path, werr)
	}
	return cerr
}

// Bind odłącza urządzenie od lokalnego sterownika i podpina pod usbip-host
// (to samo co `usbip bind -b <busid>`).
func (s *Sysfs) Bind(busid string) error {
	if !s.StubLoaded() {
		return errors.New("moduł jądra usbip-host nie jest załadowany (uruchom na hoście: sudo modprobe usbip-host)")
	}
	d, err := s.Get(busid)
	if err != nil {
		return err
	}
	if d.Driver == stubDriver {
		return nil
	}
	if d.Driver != "" {
		if err := writeAttr(filepath.Join(s.driverDir(d.Driver), "unbind"), busid); err != nil {
			return err
		}
	}
	if err := writeAttr(filepath.Join(s.driverDir(stubDriver), "match_busid"), "add "+busid); err != nil {
		return err
	}
	if err := writeAttr(filepath.Join(s.driverDir(stubDriver), "bind"), busid); err != nil {
		// po "add" jądro mogło już samo podpiąć urządzenie
		if d2, e2 := s.Get(busid); e2 != nil || d2.Driver != stubDriver {
			return err
		}
	}
	return nil
}

// Unbind oddaje urządzenie lokalnym sterownikom (`usbip unbind -b <busid>`).
func (s *Sysfs) Unbind(busid string) error {
	d, err := s.Get(busid)
	if err != nil {
		return err
	}
	if d.Driver == stubDriver {
		if err := writeAttr(filepath.Join(s.driverDir(stubDriver), "unbind"), busid); err != nil {
			return err
		}
	}
	_ = writeAttr(filepath.Join(s.driverDir(stubDriver), "match_busid"), "del "+busid)
	return writeAttr(filepath.Join(s.Root, "bus/usb/drivers_probe"), busid)
}

// Export przekazuje jądru deskryptor gniazda TCP – od tej chwili ruch URB
// obsługuje sterownik usbip-host.
func (s *Sysfs) Export(busid string, fd uintptr) error {
	return writeAttr(filepath.Join(s.devicesDir(), busid, "usbip_sockfd"), strconv.Itoa(int(fd)))
}

// StopExport każe jądru zakończyć połączenie (zapis -1).
func (s *Sysfs) StopExport(busid string) error {
	return writeAttr(filepath.Join(s.devicesDir(), busid, "usbip_sockfd"), "-1")
}

func (s *Sysfs) Status(busid string) int {
	st, _ := strconv.Atoi(readAttr(filepath.Join(s.devicesDir(), busid), "usbip_status"))
	return st
}

// WaitStatus czeka, aż usbip_status przyjmie wartość want.
func (s *Sysfs) WaitStatus(busid string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if s.Status(busid) == want {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
