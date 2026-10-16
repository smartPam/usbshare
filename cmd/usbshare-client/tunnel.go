package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
)

// Każdy serwer dostaje własny adres pętli zwrotnej 127.42.x.y:3240.
// usbip.exe (usbip-win2) łączy się z tym adresem zwykłym USB/IP,
// a tunel przekazuje ruch do serwera przez TLS z hasłem.
const usbipPort = 3240

func loopbackIP(id int) string {
	return fmt.Sprintf("127.42.%d.%d", (id/250)%256, id%250+1)
}

type tunnel struct {
	id int
	ln net.Listener
}

type tunnelManager struct {
	mu      sync.Mutex
	tunnels map[int]*tunnel
	app     *App
	port    int
}

func newTunnelManager(app *App, port int) *tunnelManager {
	return &tunnelManager{tunnels: map[int]*tunnel{}, app: app, port: port}
}

// sync uruchamia tunele dla bieżącej listy serwerów i zamyka zbędne.
func (m *tunnelManager) sync(ids []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
		if _, ok := m.tunnels[id]; ok {
			continue
		}
		addr := net.JoinHostPort(loopbackIP(id), strconv.Itoa(m.port))
		ln, err := net.Listen("tcp4", addr)
		if err != nil {
			m.app.logf("błąd", "nie można otworzyć tunelu %s: %v", addr, err)
			continue
		}
		t := &tunnel{id: id, ln: ln}
		m.tunnels[id] = t
		go m.serve(t)
	}
	for id, t := range m.tunnels {
		if !want[id] {
			t.ln.Close()
			delete(m.tunnels, id)
		}
	}
}

func (m *tunnelManager) serve(t *tunnel) {
	for {
		c, err := t.ln.Accept()
		if err != nil {
			return
		}
		go m.forward(t.id, c)
	}
}

func (m *tunnelManager) forward(id int, local net.Conn) {
	defer local.Close()
	if tc, ok := local.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	e, ok := m.app.serverEntry(id)
	if !ok {
		return
	}
	res, err := dialServer(e, "usbip")
	if res != nil && res.seenFP != "" {
		m.app.rememberFingerprint(id, res.seenFP)
	}
	if err != nil {
		m.app.logf("błąd", "%s: tunel USB/IP: %v", e.Name, err)
		return
	}
	remote := res.conn
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.CopyBuffer(remote, local, make([]byte, 256*1024)); done <- struct{}{} }()
	go func() { _, _ = io.CopyBuffer(local, remote, make([]byte, 256*1024)); done <- struct{}{} }()
	<-done
}
