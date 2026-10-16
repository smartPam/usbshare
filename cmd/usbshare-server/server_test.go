package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usbshare/internal/proto"
	"usbshare/internal/tlsutil"
	"usbshare/internal/usbip"
)

// makeFakeSysfs buduje minimalne /sys z jednym urządzeniem 1-1 i hubem 1-2.
func makeFakeSysfs(t *testing.T) string {
	root := t.TempDir()
	w := func(p, v string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dev := "bus/usb/devices/1-1/"
	for k, v := range map[string]string{
		"idVendor": "0403\n", "idProduct": "6001\n", "bcdDevice": "0600\n", "busnum": "1\n", "devnum": "5\n",
		"speed": "12\n", "bDeviceClass": "00\n", "bDeviceSubClass": "00\n", "bDeviceProtocol": "00\n",
		"bConfigurationValue": "1\n", "bNumConfigurations": "1\n", "bNumInterfaces": " 1\n",
		"manufacturer": "FTDI\n", "product": "FT232R USB UART\n", "serial": "A50285BI\n",
		"usbip_status": "1\n", "usbip_sockfd": "",
	} {
		w(dev+k, v)
	}
	w(dev+"1-1:1.0/bInterfaceClass", "ff\n")
	w(dev+"1-1:1.0/bInterfaceSubClass", "ff\n")
	w(dev+"1-1:1.0/bInterfaceProtocol", "ff\n")
	hub := "bus/usb/devices/1-2/"
	for k, v := range map[string]string{"idVendor": "05e3", "idProduct": "0608", "bDeviceClass": "09", "bNumInterfaces": "1", "bConfigurationValue": "1", "speed": "480"} {
		w(hub+k, v)
	}
	w("bus/usb/devices/usb1/idVendor", "1d6b")
	for _, f := range []string{"bind", "unbind", "match_busid"} {
		w("bus/usb/drivers/usbip-host/"+f, "")
		w("bus/usb/drivers/usb/"+f, "")
	}
	w("bus/usb/drivers_probe", "")
	if err := os.Symlink("../../drivers/usb", filepath.Join(root, dev, "driver")); err != nil {
		t.Fatal(err)
	}
	return root
}

func read(t *testing.T, root, p string) string {
	b, _ := os.ReadFile(filepath.Join(root, p))
	return strings.TrimSpace(string(b))
}

func startServer(t *testing.T, root string) string {
	dir := t.TempDir()
	cert, _, err := tlsutil.LoadOrCreate(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{Password: "tajne", Name: "test", SysfsRoot: root, DataDir: dir, ReturnToHost: true}, tlsutil.ServerConfig(cert))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return ln.Addr().String()
}

func dial(t *testing.T, addr, pw, mode string) (*tls.Conn, proto.HelloReply) {
	c, err := tls.Dial("tcp", addr, tlsutil.ClientConfig("", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = proto.WriteLine(c, proto.Hello{V: proto.Version, Password: pw, Mode: mode, Client: "testpc"})
	var r proto.HelloReply
	if err := proto.ReadJSONUnbuffered(c, &r); err != nil {
		t.Fatal(err)
	}
	return c, r
}

func list(t *testing.T, addr string) []proto.DeviceInfo {
	c, r := dial(t, addr, "tajne", proto.ModeControl)
	defer c.Close()
	if !r.OK {
		t.Fatalf("hello: %+v", r)
	}
	_ = proto.WriteLine(c, proto.Request{Cmd: "list"})
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	_ = json.Unmarshal(line, &resp)
	if !resp.OK {
		t.Fatalf("list: %+v", resp)
	}
	return resp.Devices
}

func TestEndToEnd(t *testing.T) {
	root := makeFakeSysfs(t)
	addr := startServer(t, root)

	// błędne hasło
	c, r := dial(t, addr, "zle", proto.ModeControl)
	c.Close()
	if r.OK || r.Error == "" {
		t.Fatalf("oczekiwano odrzucenia hasła: %+v", r)
	}

	// lista sterująca – hub i root hub mają być pominięte
	devs := list(t, addr)
	if len(devs) != 1 || devs[0].BusID != "1-1" || devs[0].VendorID != "0403" || devs[0].Product != "FT232R USB UART" {
		t.Fatalf("lista: %+v", devs)
	}
	if devs[0].State != proto.StateAvailable || len(devs[0].IfClasses) != 1 || devs[0].IfClasses[0] != 0xff || devs[0].Class != 0 {
		t.Fatalf("stan: %+v", devs[0])
	}

	// OP_REQ_DEVLIST
	c, _ = dial(t, addr, "tajne", proto.ModeUSBIP)
	_, _ = c.Write(usbip.Header(usbip.OpReqDevlist, 0))
	_, code, st, err := usbip.ReadHeader(c)
	if err != nil || code != usbip.OpRepDevlist || st != 0 {
		t.Fatalf("devlist hdr %x %d %v", code, st, err)
	}
	ul, err := usbip.ParseDevlistReply(c)
	c.Close()
	if err != nil || len(ul) != 1 || ul[0].IDProduct != 0x6001 || ul[0].Speed != usbip.SpeedFull || ul[0].NumInterfaces != 1 || ul[0].Interfaces[0].Class != 0xff {
		t.Fatalf("devlist: %+v %v", ul, err)
	}

	// OP_REQ_IMPORT
	c, _ = dial(t, addr, "tajne", proto.ModeUSBIP)
	_, _ = c.Write(usbip.ImportRequest("1-1"))
	_, code, st, err = usbip.ReadHeader(c)
	if err != nil || code != usbip.OpRepImport || st != usbip.StOK {
		t.Fatalf("import hdr %x %d %v", code, st, err)
	}
	buf := make([]byte, usbip.DeviceSize)
	if _, err := c.Read(buf); err != nil {
		t.Fatal(err)
	}
	d, _ := usbip.UnmarshalDevice(buf)
	if d.BusID != "1-1" || d.DevNum != 5 || d.IDVendor != 0x0403 {
		t.Fatalf("import dev: %+v", d)
	}
	if got := read(t, root, "bus/usb/drivers/usb/unbind"); got != "1-1" {
		t.Fatalf("unbind z usb: %q", got)
	}
	if got := read(t, root, "bus/usb/drivers/usbip-host/bind"); got != "1-1" {
		t.Fatalf("bind: %q", got)
	}
	fd := read(t, root, "bus/usb/devices/1-1/usbip_sockfd")
	if fd == "" || fd == "-1" {
		t.Fatalf("sockfd: %q", fd)
	}
	c.Close()

	// po zakończeniu urządzenie wraca do systemu serwera
	deadline := time.Now().Add(3 * time.Second)
	for read(t, root, "bus/usb/drivers_probe") != "1-1" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := read(t, root, "bus/usb/drivers_probe"); got != "1-1" {
		t.Fatalf("drivers_probe: %q", got)
	}
	if got := read(t, root, "bus/usb/drivers/usbip-host/match_busid"); got != "del 1-1" {
		t.Fatalf("match_busid: %q", got)
	}

	// import nieistniejącego / huba
	for _, b := range []string{"9-9", "1-2", "../x"} {
		c, _ = dial(t, addr, "tajne", proto.ModeUSBIP)
		_, _ = c.Write(usbip.ImportRequest(b))
		_, _, st, _ = usbip.ReadHeader(c)
		c.Close()
		if st != usbip.StNoDev {
			t.Fatalf("import %s: status %d", b, st)
		}
	}
}
