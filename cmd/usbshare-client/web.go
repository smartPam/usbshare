package main

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"usbshare/internal/proto"
)

//go:embed web/index.html
var indexHTML []byte

type apiDevice struct {
	proto.DeviceInfo
	AttachedPort int    `json:"attached_port"`
	Auto         bool   `json:"auto"`
	Busy         string `json:"busy,omitempty"`
}

type apiServer struct {
	ID            int         `json:"id"`
	Name          string      `json:"name"`
	Host          string      `json:"host"`
	Port          int         `json:"port"`
	Fingerprint   string      `json:"fingerprint"`
	SeenFP        string      `json:"seen_fp"`
	Status        string      `json:"status"`
	Error         string      `json:"error"`
	ServerName    string      `json:"server_name"`
	ServerVersion string      `json:"server_version"`
	Devices       []apiDevice `json:"devices"`
}

type apiState struct {
	Servers            []apiServer `json:"servers"`
	UsbipPath          string      `json:"usbip_path"`
	UsbipFound         bool        `json:"usbip_found"`
	PortsError         string      `json:"ports_error"`
	Autostart          bool        `json:"autostart"`
	AutostartSupported bool        `json:"autostart_supported"`
	Logs               []LogEntry  `json:"logs"`
	Client             string      `json:"client"`
	Version            string      `json:"version"`
	OS                 string      `json:"os"`
}

func (a *App) state() apiState {
	path := a.usbipPath()
	_, statErr := os.Stat(path)
	a.mu.Lock()
	defer a.mu.Unlock()
	s := apiState{
		UsbipPath: a.cfg.UsbipPath, UsbipFound: statErr == nil, PortsError: a.portsErr,
		AutostartSupported: autostartSupported(), Autostart: a.autostart,
		Logs:   append([]LogEntry(nil), a.logs...),
		Client: clientName, Version: version, OS: runtime.GOOS,
		Servers: []apiServer{},
	}
	if s.UsbipPath == "" {
		s.UsbipPath = path
	}
	for _, e := range a.cfg.Servers {
		as := apiServer{ID: e.ID, Name: e.Name, Host: e.Host, Port: e.Port, Fingerprint: e.Fingerprint,
			Status: "connecting", Devices: []apiDevice{}}
		if st := a.states[e.ID]; st != nil {
			as.Status, as.Error, as.SeenFP = st.Status, st.Error, st.SeenFP
			as.ServerName, as.ServerVersion = st.ServerName, st.ServerVersion
			for _, d := range st.Devices {
				key := portKey(loopbackIP(e.ID), d.BusID)
				ad := apiDevice{DeviceInfo: d, AttachedPort: -1, Auto: e.isAuto(d.BusID), Busy: a.busy[key]}
				if p, ok := a.ports[key]; ok {
					ad.AttachedPort = p
				}
				as.Devices = append(as.Devices, ad)
			}
		}
		s.Servers = append(s.Servers, as)
	}
	return s
}

type apiReq struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Password  string `json:"password"`
	BusID     string `json:"busid"`
	On        bool   `json:"on"`
	UsbipPath string `json:"usbip_path"`
	Autostart *bool  `json:"autostart"`
}

func (a *App) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/state" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(a.state())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"metoda"}`, http.StatusMethodNotAllowed)
		return
	}
	var req apiReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, errors.New("błędne dane"))
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/server/save":
		err = a.saveServer(req)
	case "/api/server/delete":
		a.mu.Lock()
		for i, s := range a.cfg.Servers {
			if s.ID == req.ID {
				a.cfg.Servers = append(a.cfg.Servers[:i], a.cfg.Servers[i+1:]...)
				delete(a.states, req.ID)
				break
			}
		}
		a.saveLocked()
		a.mu.Unlock()
		a.kick()
	case "/api/server/trust":
		a.mu.Lock()
		e, st := a.cfg.server(req.ID), a.states[req.ID]
		if e != nil && st != nil && st.SeenFP != "" {
			e.Fingerprint = st.SeenFP
			a.saveLocked()
		}
		a.mu.Unlock()
		a.kick()
	case "/api/attach":
		err = a.attach(req.ID, req.BusID, false)
	case "/api/detach":
		err = a.detach(req.ID, req.BusID)
	case "/api/release":
		err = a.release(req.ID, req.BusID)
	case "/api/auto":
		a.mu.Lock()
		if e := a.cfg.server(req.ID); e != nil {
			var keep []string
			for _, b := range e.AutoAttach {
				if b != req.BusID {
					keep = append(keep, b)
				}
			}
			if req.On {
				keep = append(keep, req.BusID)
				delete(a.autoBackoff, portKey(loopbackIP(req.ID), req.BusID))
			}
			e.AutoAttach = keep
			a.saveLocked()
		}
		a.mu.Unlock()
		a.kick()
	case "/api/settings":
		a.mu.Lock()
		a.cfg.UsbipPath = strings.TrimSpace(req.UsbipPath)
		a.saveLocked()
		a.mu.Unlock()
		if req.Autostart != nil && autostartSupported() {
			if err = setAutostart(*req.Autostart); err == nil {
				a.mu.Lock()
				a.autostart = *req.Autostart
				a.mu.Unlock()
			}
		}
		a.kick()
	case "/api/quit":
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		go func() { time.Sleep(300 * time.Millisecond); a.quit() }()
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func writeErr(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (a *App) saveServer(req apiReq) error {
	req.Host = strings.TrimSpace(req.Host)
	req.Name = strings.TrimSpace(req.Name)
	if req.Host == "" {
		return errors.New("podaj adres serwera")
	}
	if req.Port == 0 {
		req.Port = proto.DefaultPort
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("błędny port")
	}
	if req.Name == "" {
		req.Name = req.Host
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if req.ID == 0 {
		if len(a.cfg.Servers) >= 200 {
			return errors.New("za dużo serwerów")
		}
		a.cfg.Servers = append(a.cfg.Servers, ServerEntry{
			ID: a.cfg.NextID, Name: req.Name, Host: req.Host, Port: req.Port, Password: req.Password,
		})
		a.cfg.NextID++
	} else {
		e := a.cfg.server(req.ID)
		if e == nil {
			return errors.New("nieznany serwer")
		}
		if e.Host != req.Host || e.Port != req.Port {
			e.Fingerprint = ""
		}
		e.Name, e.Host, e.Port = req.Name, req.Host, req.Port
		if req.Password != "" {
			e.Password = req.Password
		}
		delete(a.states, e.ID)
	}
	a.saveLocked()
	a.kick()
	return nil
}

// handler zabezpiecza API: tylko z localhost (ochrona przed DNS rebinding)
// i z tokenem (ochrona przed CSRF z innych stron w przeglądarce).
func (a *App) handler(port int) http.Handler {
	allowedHosts := map[string]bool{
		"127.0.0.1:" + strconv.Itoa(port): true,
		"localhost:" + strconv.Itoa(port): true,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		tok := a.cfg.Token
		a.mu.Unlock()
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-USBShare-Token")), []byte(tok)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"token"}`))
			return
		}
		a.handleAPI(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func listenWeb(port int) (net.Listener, error) {
	return net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
}
