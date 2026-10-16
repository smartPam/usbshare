package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

type ServerEntry struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Password    string   `json:"password"`
	Fingerprint string   `json:"fingerprint"`
	AutoAttach  []string `json:"auto_attach"` // busid urządzeń podłączanych automatycznie
}

type Config struct {
	Servers   []ServerEntry `json:"servers"`
	UsbipPath string        `json:"usbip_path"`
	WebPort   int           `json:"web_port"`
	Token     string        `json:"token"`
	NextID    int           `json:"next_id"`
}

func configDir() string {
	if d := os.Getenv("USBSHARE_CLIENT_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "USBShare")
}

func loadConfig(path string) (*Config, error) {
	cfg := &Config{WebPort: 7601, NextID: 1}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if cfg.Token == "" {
		buf := make([]byte, 16)
		_, _ = rand.Read(buf)
		cfg.Token = hex.EncodeToString(buf)
	}
	if cfg.WebPort == 0 {
		cfg.WebPort = 7601
	}
	for _, s := range cfg.Servers {
		if s.ID >= cfg.NextID {
			cfg.NextID = s.ID + 1
		}
	}
	return cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) server(id int) *ServerEntry {
	for i := range c.Servers {
		if c.Servers[i].ID == id {
			return &c.Servers[i]
		}
	}
	return nil
}

func (s *ServerEntry) isAuto(busid string) bool {
	for _, b := range s.AutoAttach {
		if b == busid {
			return true
		}
	}
	return false
}
