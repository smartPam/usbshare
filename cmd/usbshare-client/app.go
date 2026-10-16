package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"usbshare/internal/proto"
	"usbshare/internal/tlsutil"
)

type LogEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

type ServerState struct {
	Status        string // "connecting", "online", "error", "fp_mismatch"
	Error         string
	ServerName    string
	ServerVersion string
	SeenFP        string
	Devices       []proto.DeviceInfo
	Updated       time.Time
}

type App struct {
	mu          sync.Mutex
	cfg         *Config
	cfgPath     string
	states      map[int]*ServerState
	ports       map[string]int
	portsErr    string
	busy        map[string]string
	autoBackoff map[string]time.Time
	logs        []LogEntry
	tm          *tunnelManager
	tunnelPort  int
	wake        chan struct{}
	autostart   bool
}

func newApp(cfg *Config, cfgPath string, tunnelPort int) *App {
	a := &App{
		cfg: cfg, cfgPath: cfgPath,
		states:      map[int]*ServerState{},
		ports:       map[string]int{},
		busy:        map[string]string{},
		autoBackoff: map[string]time.Time{},
		tunnelPort:  tunnelPort,
		wake:        make(chan struct{}, 1),
	}
	a.tm = newTunnelManager(a, tunnelPort)
	a.autostart = autostartEnabled()
	return a
}

func (a *App) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] %s", level, msg)
	a.mu.Lock()
	a.logs = append(a.logs, LogEntry{Time: time.Now().Format("15:04:05"), Level: level, Msg: msg})
	if len(a.logs) > 200 {
		a.logs = a.logs[len(a.logs)-200:]
	}
	a.mu.Unlock()
}

// saveLocked zapisuje konfigurację; wymaga a.mu.
func (a *App) saveLocked() {
	if err := saveConfig(a.cfgPath, a.cfg); err != nil {
		log.Printf("zapis konfiguracji: %v", err)
	}
}

func (a *App) kick() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *App) serverEntry(id int) (ServerEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.cfg.server(id); e != nil {
		return *e, true
	}
	return ServerEntry{}, false
}

func (a *App) usbipPath() string {
	a.mu.Lock()
	p := a.cfg.UsbipPath
	a.mu.Unlock()
	if p == "" {
		p = defaultUsbipPath()
	}
	return p
}

// rememberFingerprint – trust-on-first-use: zapamiętuje odcisk przy pierwszym połączeniu.
func (a *App) rememberFingerprint(id int, fp string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.cfg.server(id); e != nil && e.Fingerprint == "" {
		e.Fingerprint = fp
		a.saveLocked()
		go a.logf("info", "%s: zapamiętano odcisk certyfikatu %s", e.Name, fp)
	}
}

func (a *App) run() {
	for {
		a.pollOnce()
		select {
		case <-a.wake:
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *App) pollOnce() {
	a.mu.Lock()
	entries := append([]ServerEntry(nil), a.cfg.Servers...)
	a.mu.Unlock()
	ids := make([]int, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		ids[i] = e.ID
		wg.Add(1)
		go func(e ServerEntry) { defer wg.Done(); a.refreshServer(e) }(e)
	}
	a.tm.sync(ids)
	wg.Wait()
	a.refreshPorts()
	a.autoAttach(entries)
}

func (a *App) refreshServer(e ServerEntry) {
	res, resp, err := controlCall(e, proto.Request{Cmd: "list"})
	if res != nil && res.seenFP != "" && err == nil {
		a.rememberFingerprint(e.ID, res.seenFP)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.states[e.ID]
	if st == nil {
		st = &ServerState{}
		a.states[e.ID] = st
	}
	prev := st.Status
	st.Updated = time.Now()
	if res != nil {
		st.SeenFP = res.seenFP
	}
	var mm *tlsutil.ErrFingerprintMismatch
	switch {
	case errors.As(err, &mm):
		st.Status, st.Error, st.SeenFP, st.Devices = "fp_mismatch", err.Error(), mm.Got, nil
	case err != nil:
		st.Status, st.Error, st.Devices = "error", err.Error(), nil
	default:
		st.Status, st.Error = "online", ""
		st.ServerName, st.ServerVersion = res.reply.Server, res.reply.Version
		st.Devices = resp.Devices
	}
	if prev != st.Status {
		lvl := "info"
		if st.Status != "online" {
			lvl = "błąd"
		}
		msg := "połączono"
		if st.Status != "online" {
			msg = st.Error
		}
		go a.logf(lvl, "%s: %s", e.Name, msg)
	}
}

func (a *App) refreshPorts() {
	ports, err := usbipPorts(a.usbipPath(), a.tunnelPort)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.portsErr = err.Error()
		return
	}
	a.portsErr = ""
	a.ports = ports
}

func (a *App) autoAttach(entries []ServerEntry) {
	for _, e := range entries {
		if len(e.AutoAttach) == 0 {
			continue
		}
		a.mu.Lock()
		st := a.states[e.ID]
		var todo []string
		if st != nil && st.Status == "online" {
			for _, d := range st.Devices {
				key := portKey(loopbackIP(e.ID), d.BusID)
				_, attached := a.ports[key]
				if e.isAuto(d.BusID) && d.State == proto.StateAvailable && !attached &&
					a.busy[key] == "" && time.Now().After(a.autoBackoff[key]) {
					todo = append(todo, d.BusID)
				}
			}
		}
		a.mu.Unlock()
		for _, b := range todo {
			a.logf("info", "%s: automatyczne podłączanie %s", e.Name, b)
			a.attach(e.ID, b, true)
		}
	}
}

// attach podłącza urządzenie przez usbip; wait=true czeka na wynik.
func (a *App) attach(id int, busid string, wait bool) error {
	e, ok := a.serverEntry(id)
	if !ok {
		return errors.New("nieznany serwer")
	}
	ip := loopbackIP(id)
	key := portKey(ip, busid)
	a.mu.Lock()
	if a.busy[key] != "" {
		a.mu.Unlock()
		return errors.New("operacja już trwa")
	}
	a.busy[key] = "attaching"
	a.mu.Unlock()
	do := func() {
		port, err := usbipAttach(a.usbipPath(), a.tunnelPort, ip, busid)
		a.mu.Lock()
		delete(a.busy, key)
		if err != nil {
			a.autoBackoff[key] = time.Now().Add(30 * time.Second)
		}
		a.mu.Unlock()
		if err != nil {
			a.logf("błąd", "%s: nie udało się podłączyć %s: %v", e.Name, busid, err)
		} else if port >= 0 {
			a.logf("info", "%s: podłączono %s na porcie %d", e.Name, busid, port)
		} else {
			a.logf("info", "%s: podłączono %s", e.Name, busid)
		}
		a.kick()
	}
	if wait {
		do()
	} else {
		go do()
	}
	return nil
}

func (a *App) detach(id int, busid string) error {
	e, _ := a.serverEntry(id)
	key := portKey(loopbackIP(id), busid)
	a.mu.Lock()
	port, ok := a.ports[key]
	if ok && a.busy[key] == "" {
		a.busy[key] = "detaching"
		// wyłącz autopodłączanie, żeby urządzenie nie wróciło od razu
		a.autoBackoff[key] = time.Now().Add(24 * time.Hour)
	}
	a.mu.Unlock()
	if !ok {
		return errors.New("urządzenie nie jest podłączone na tym komputerze")
	}
	go func() {
		err := usbipDetach(a.usbipPath(), a.tunnelPort, port)
		a.mu.Lock()
		delete(a.busy, key)
		a.mu.Unlock()
		if err != nil {
			a.logf("błąd", "%s: odłączanie %s: %v", e.Name, busid, err)
		} else {
			a.logf("info", "%s: odłączono %s", e.Name, busid)
		}
		a.kick()
	}()
	return nil
}

// detachAll odłącza wszystkie urządzenia z naszych tuneli (przy zamykaniu).
func (a *App) detachAll() {
	a.mu.Lock()
	ports := map[string]int{}
	for k, v := range a.ports {
		ports[k] = v
	}
	a.mu.Unlock()
	for k, p := range ports {
		if len(k) > 7 && k[:7] == "127.42." {
			_ = usbipDetach(a.usbipPath(), a.tunnelPort, p)
		}
	}
}

func (a *App) release(id int, busid string) error {
	e, ok := a.serverEntry(id)
	if !ok {
		return errors.New("nieznany serwer")
	}
	_, _, err := controlCall(e, proto.Request{Cmd: "release", BusID: busid})
	if err == nil {
		a.logf("info", "%s: zwolniono %s na serwerze", e.Name, busid)
	}
	a.kick()
	return err
}

func (a *App) quit() {
	a.logf("info", "zamykanie klienta")
	a.detachAll()
	os.Exit(0)
}
