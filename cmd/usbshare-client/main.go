// usbshare-client – podłącza zdalne urządzenia USB z serwera USBShare jako lokalne.
// Interfejs: przeglądarka pod http://127.0.0.1:7601
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

var version = "dev"

func main() {
	background := flag.Bool("background", false, "nie otwieraj przeglądarki przy starcie")
	webPort := flag.Int("port", 0, "port interfejsu WWW (domyślnie 7601)")
	showVersion := flag.Bool("version", false, "pokaż wersję")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	dir := configDir()
	_ = os.MkdirAll(dir, 0o700)
	if f, err := os.OpenFile(filepath.Join(dir, "client.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		if st, _ := f.Stat(); st != nil && st.Size() > 5<<20 {
			_ = f.Truncate(0)
		}
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	cfgPath := filepath.Join(dir, "config.json")
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("konfiguracja %s: %v", cfgPath, err)
	}
	if *webPort != 0 {
		cfg.WebPort = *webPort
	}
	_ = saveConfig(cfgPath, cfg)
	url := fmt.Sprintf("http://127.0.0.1:%d/#t=%s", cfg.WebPort, cfg.Token)

	ln, err := listenWeb(cfg.WebPort)
	if err != nil {
		// prawdopodobnie klient już działa – pokaż jego okno
		log.Printf("port %d zajęty (%v) – otwieram istniejące okno", cfg.WebPort, err)
		openBrowser(url)
		return
	}

	tunnelPort := usbipPort
	if p, err := strconv.Atoi(os.Getenv("USBSHARE_TUNNEL_PORT")); err == nil && p > 0 {
		tunnelPort = p
	}
	app := newApp(cfg, cfgPath, tunnelPort)
	app.logf("info", "USBShare klient %s uruchomiony (%s)", version, clientName)
	go app.run()
	if !*background {
		openBrowser(url)
	}
	log.Printf("interfejs: %s", url)
	if err := http.Serve(ln, app.handler(cfg.WebPort)); err != nil {
		log.Fatal(err)
	}
}
