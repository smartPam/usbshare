package main

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"usbshare/internal/proto"
	"usbshare/internal/usbip"
)

type Config struct {
	Listen       string
	Password     string
	Name         string
	DataDir      string
	SysfsRoot    string
	Allow        []string // "vid:pid" (hex) lub busid; puste = wszystkie
	Deny         []string
	ShareHubs    bool
	ReturnToHost bool
}

type session struct {
	busid  string
	peer   string
	client string
	since  time.Time
	conn   net.Conn
	ready  bool // jądro przejęło połączenie
}

type Server struct {
	cfg      Config
	sys      *Sysfs
	tlsConf  *tls.Config
	pwHash   [32]byte
	mu       sync.Mutex
	sessions map[string]*session
	failMu   sync.Mutex
	fails    map[string]int
}

func NewServer(cfg Config, tlsConf *tls.Config) *Server {
	return &Server{
		cfg:      cfg,
		sys:      &Sysfs{Root: cfg.SysfsRoot},
		tlsConf:  tlsConf,
		pwHash:   sha256.Sum256([]byte(cfg.Password)),
		sessions: map[string]*session{},
		fails:    map[string]int{},
	}
}

func matchRule(rules []string, d *LocalDevice) bool {
	vp := fmt.Sprintf("%04x:%04x", d.IDVendor, d.IDProduct)
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" {
			continue
		}
		if r == vp || r == d.BusID || (strings.HasSuffix(r, ":*") && strings.HasPrefix(vp, strings.TrimSuffix(r, "*"))) {
			return true
		}
	}
	return false
}

// shareable mówi, czy urządzenie może być udostępnione wg konfiguracji.
func (s *Server) shareable(d *LocalDevice) bool {
	if d.DeviceClass == 0x09 && !s.cfg.ShareHubs {
		return false
	}
	if matchRule(s.cfg.Deny, d) {
		return false
	}
	if len(s.cfg.Allow) > 0 && !matchRule(s.cfg.Allow, d) {
		return false
	}
	return true
}

// Cleanup zwalnia urządzenia, które zostały na usbip-host po poprzednim uruchomieniu.
func (s *Server) Cleanup() {
	if !s.cfg.ReturnToHost {
		return
	}
	devs, _ := s.sys.List()
	for _, d := range devs {
		if d.Driver == stubDriver && d.UsbipStatus == sdevAvailable {
			log.Printf("zwalniam pozostawione urządzenie %s", d.BusID)
			_ = s.sys.Unbind(d.BusID)
		}
	}
}

func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) checkPassword(pw string) bool {
	h := sha256.Sum256([]byte(pw))
	return subtle.ConstantTimeCompare(h[:], s.pwHash[:]) == 1
}

func (s *Server) handle(raw net.Conn) {
	peer := raw.RemoteAddr().String()
	host, _, _ := net.SplitHostPort(peer)
	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}
	conn := tls.Server(raw, s.tlsConf)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := conn.Handshake(); err != nil {
		return
	}
	var hello proto.Hello
	if err := proto.ReadJSONUnbuffered(conn, &hello); err != nil {
		return
	}
	reply := proto.HelloReply{Server: s.cfg.Name, Version: version}
	if !s.checkPassword(hello.Password) {
		s.failMu.Lock()
		s.fails[host]++
		n := s.fails[host]
		s.failMu.Unlock()
		log.Printf("%s: błędne hasło (próba %d)", peer, n)
		time.Sleep(time.Duration(min(n, 10)) * time.Second)
		reply.Error = "błędne hasło"
		_ = proto.WriteLine(conn, reply)
		return
	}
	s.failMu.Lock()
	delete(s.fails, host)
	s.failMu.Unlock()
	if hello.V != proto.Version {
		reply.Error = fmt.Sprintf("niezgodna wersja protokołu (serwer %d, klient %d)", proto.Version, hello.V)
		_ = proto.WriteLine(conn, reply)
		return
	}
	client := hello.Client
	if client == "" {
		client = host
	} else {
		client = fmt.Sprintf("%s (%s)", client, host)
	}
	switch hello.Mode {
	case proto.ModeControl:
		reply.OK = true
		if proto.WriteLine(conn, reply) != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		s.control(conn, client)
	case proto.ModeUSBIP:
		reply.OK = true
		if proto.WriteLine(conn, reply) != nil {
			return
		}
		s.usbipSession(conn, peer, client)
	default:
		reply.Error = "nieznany tryb"
		_ = proto.WriteLine(conn, reply)
	}
}

func (s *Server) deviceInfos() ([]proto.DeviceInfo, error) {
	devs, err := s.sys.List()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []proto.DeviceInfo{}
	for i := range devs {
		d := &devs[i]
		if !s.shareable(d) {
			continue
		}
		info := proto.DeviceInfo{
			BusID:        d.BusID,
			VendorID:     fmt.Sprintf("%04x", d.IDVendor),
			ProductID:    fmt.Sprintf("%04x", d.IDProduct),
			Manufacturer: d.Manufacturer,
			Product:      d.Product,
			Serial:       d.Serial,
			Speed:        usbip.SpeedName(d.Speed),
			Class:        d.DeviceClass,
			State:        proto.StateAvailable,
		}
		for _, it := range d.Interfaces {
			info.IfClasses = append(info.IfClasses, int(it.Class))
		}
		if ss, ok := s.sessions[d.BusID]; ok {
			info.State = proto.StateInUse
			info.UsedBy = ss.client
			info.Since = ss.since.Unix()
		} else if d.UsbipStatus == sdevUsed {
			info.State = proto.StateInUse
			info.UsedBy = "inny program usbip"
		} else if d.UsbipStatus == sdevError {
			info.State = proto.StateError
			info.Note = "błąd sterownika usbip-host"
		}
		out = append(out, info)
	}
	return out, nil
}

func (s *Server) control(conn net.Conn, client string) {
	r := bufio.NewReaderSize(conn, 64*1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req proto.Request
		resp := proto.Response{}
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = "błędne żądanie"
		} else {
			switch req.Cmd {
			case "ping":
				resp.OK = true
			case "list":
				devs, err := s.deviceInfos()
				if err != nil {
					resp.Error = err.Error()
				} else {
					resp.OK, resp.Devices = true, devs
				}
			case "release":
				s.mu.Lock()
				ss, ok := s.sessions[req.BusID]
				s.mu.Unlock()
				if !ok {
					resp.Error = "urządzenie nie jest używane"
				} else {
					log.Printf("%s: wymuszone odłączenie %s od %s", client, req.BusID, ss.client)
					ss.conn.Close()
					resp.OK = true
				}
			default:
				resp.Error = "nieznane polecenie"
			}
		}
		if proto.WriteLine(conn, resp) != nil {
			return
		}
	}
}

func (s *Server) usbipSession(conn *tls.Conn, peer, client string) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	_, code, _, err := usbip.ReadHeader(conn)
	if err != nil {
		return
	}
	switch code {
	case usbip.OpReqDevlist:
		infos, _ := s.deviceInfos()
		var devs []usbip.Device
		for _, in := range infos {
			if in.State != proto.StateAvailable {
				continue
			}
			if d, err := s.sys.Get(in.BusID); err == nil {
				devs = append(devs, d.Device)
			}
		}
		_, _ = conn.Write(usbip.DevlistReply(devs))
	case usbip.OpReqImport:
		busid, err := usbip.ReadBusID(conn)
		if err != nil {
			return
		}
		s.importDevice(conn, peer, client, busid)
	default:
		_, _ = conn.Write(usbip.Header(code&0x00ff, usbip.StNA))
	}
}

func (s *Server) importDevice(conn *tls.Conn, peer, client, busid string) {
	fail := func(st uint32, msg string) {
		log.Printf("%s: import %s odrzucony: %s", client, busid, msg)
		_, _ = conn.Write(usbip.Header(usbip.OpRepImport, st))
	}
	d, err := s.sys.Get(busid)
	if err != nil || !s.shareable(&d) {
		fail(usbip.StNoDev, "brak urządzenia lub nie jest udostępniane")
		return
	}
	s.mu.Lock()
	if other, busy := s.sessions[busid]; busy {
		s.mu.Unlock()
		fail(usbip.StDevBusy, "używane przez "+other.client)
		return
	}
	ss := &session{busid: busid, peer: peer, client: client, since: time.Now(), conn: conn}
	s.sessions[busid] = ss
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, busid)
		s.mu.Unlock()
	}()

	if d.UsbipStatus == sdevUsed {
		fail(usbip.StDevBusy, "używane przez inny program usbip")
		return
	}
	if err := s.sys.Bind(busid); err != nil {
		fail(usbip.StDevErr, err.Error())
		return
	}
	if !s.sys.WaitStatus(busid, sdevAvailable, 3*time.Second) {
		fail(usbip.StDevErr, "usbip-host nie przejął urządzenia")
		s.release(busid)
		return
	}
	if d, err = s.sys.Get(busid); err != nil {
		fail(usbip.StNoDev, err.Error())
		return
	}

	kernelSide, mySide, err := loopbackPair()
	if err != nil {
		fail(usbip.StError, err.Error())
		s.release(busid)
		return
	}
	f, err := kernelSide.File() // dup deskryptora w trybie blokującym
	if err == nil {
		err = s.sys.Export(busid, f.Fd())
		f.Close()
	}
	kernelSide.Close() // jądro trzyma własną referencję do gniazda
	if err != nil {
		mySide.Close()
		fail(usbip.StDevErr, "przekazanie gniazda do jądra: "+err.Error())
		s.release(busid)
		return
	}
	if _, err := conn.Write(append(usbip.Header(usbip.OpRepImport, usbip.StOK), d.Marshal()...)); err != nil {
		mySide.Close()
		s.release(busid)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	s.mu.Lock()
	ss.ready = true
	s.mu.Unlock()
	log.Printf("%s: podłączono %s (%04x:%04x %s %s)", client, busid, d.IDVendor, d.IDProduct, d.Manufacturer, d.Product)

	pipe(conn, mySide)

	log.Printf("%s: rozłączono %s", client, busid)
	s.release(busid)
}

// release kończy eksport i (opcjonalnie) oddaje urządzenie systemowi serwera.
func (s *Server) release(busid string) {
	if s.sys.Status(busid) == sdevUsed {
		_ = s.sys.StopExport(busid)
		s.sys.WaitStatus(busid, sdevAvailable, 5*time.Second)
	}
	if s.cfg.ReturnToHost {
		if err := s.sys.Unbind(busid); err != nil {
			log.Printf("unbind %s: %v", busid, err)
		}
	}
}

// loopbackPair tworzy połączoną parę gniazd TCP na 127.0.0.1.
// Jądro wymaga gniazda SOCK_STREAM, a ruch po stronie sieci idzie przez TLS,
// więc pośredniczymy między TLS a lokalnym gniazdem jądra.
func loopbackPair() (kernel *net.TCPConn, mine *net.TCPConn, err error) {
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, nil, err
	}
	defer ln.Close()
	type res struct {
		c   *net.TCPConn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.AcceptTCP()
		ch <- res{c, err}
	}()
	mine, err = net.DialTCP("tcp4", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		return nil, nil, err
	}
	r := <-ch
	if r.err != nil {
		mine.Close()
		return nil, nil, r.err
	}
	_ = mine.SetNoDelay(true)
	_ = r.c.SetNoDelay(true)
	return r.c, mine, nil
}

// pipe przekazuje dane w obie strony, aż jedna strona się zamknie.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 256*1024)
		_, _ = io.CopyBuffer(dst, src, buf)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}
