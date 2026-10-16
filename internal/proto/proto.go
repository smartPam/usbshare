// Package proto definiuje protokół USBShare działający wewnątrz połączenia TLS.
//
// Po zestawieniu TLS klient wysyła jedną linię JSON (Hello) zakończoną '\n'.
// Serwer odpowiada linią HelloReply. Dalej:
//   - mode "control": wymiana linii JSON Request/Response,
//   - mode "usbip":   surowy strumień USB/IP (OP_REQ_DEVLIST / OP_REQ_IMPORT,
//     a po imporcie ruch URB obsługiwany przez jądro).
package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	Version     = 1
	DefaultPort = 7600

	ModeControl = "control"
	ModeUSBIP   = "usbip"

	StateAvailable = "available"
	StateInUse     = "in_use"
	StateError     = "error"

	MaxLine = 1 << 20
)

type Hello struct {
	V        int    `json:"v"`
	Password string `json:"password"`
	Mode     string `json:"mode"`
	Client   string `json:"client"`
}

type HelloReply struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Server  string `json:"server"`
	Version string `json:"version"`
}

// Request – polecenia kanału sterującego: "list", "release", "ping".
type Request struct {
	Cmd   string `json:"cmd"`
	BusID string `json:"busid,omitempty"`
}

type Response struct {
	OK      bool         `json:"ok"`
	Error   string       `json:"error,omitempty"`
	Devices []DeviceInfo `json:"devices,omitempty"`
}

type DeviceInfo struct {
	BusID        string `json:"busid"`
	VendorID     string `json:"vendor_id"`
	ProductID    string `json:"product_id"`
	Manufacturer string `json:"manufacturer"`
	Product      string `json:"product"`
	Serial       string `json:"serial"`
	Speed        string `json:"speed"`
	Class        uint8  `json:"class"`
	IfClasses    []int  `json:"if_classes,omitempty"`
	State        string `json:"state"`
	UsedBy       string `json:"used_by,omitempty"`
	Since        int64  `json:"since,omitempty"` // unix
	Note         string `json:"note,omitempty"`
}

// WriteLine zapisuje v jako jedną linię JSON.
func WriteLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadLineUnbuffered czyta bajt po bajcie do '\n'. Nie czyta ani bajtu dalej,
// dzięki czemu po Hello można przejść na surowy strumień USB/IP.
func ReadLineUnbuffered(r io.Reader, max int) ([]byte, error) {
	var buf bytes.Buffer
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return buf.Bytes(), nil
			}
			buf.WriteByte(one[0])
			if buf.Len() > max {
				return nil, errors.New("proto: za długa linia")
			}
		}
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

func ReadJSONUnbuffered(r io.Reader, v any) error {
	line, err := ReadLineUnbuffered(r, MaxLine)
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}
