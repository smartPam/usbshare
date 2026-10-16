// usbshare-server – udostępnia urządzenia USB z Linuksa przez sieć (USB/IP w TLS).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"usbshare/internal/proto"
	"usbshare/internal/tlsutil"
)

var version = "dev"

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v, err := strconv.ParseBool(env(k, strconv.FormatBool(def)))
	if err != nil {
		return def
	}
	return v
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		out = append(out, p)
	}
	return out
}

func loadPassword(cfg *Config) {
	if cfg.Password != "" {
		return
	}
	pf := filepath.Join(cfg.DataDir, "password.txt")
	if b, err := os.ReadFile(pf); err == nil && strings.TrimSpace(string(b)) != "" {
		cfg.Password = strings.TrimSpace(string(b))
		log.Printf("hasło wczytane z %s", pf)
		return
	}
	buf := make([]byte, 9)
	_, _ = rand.Read(buf)
	cfg.Password = hex.EncodeToString(buf)
	_ = os.WriteFile(pf, []byte(cfg.Password+"\n"), 0o600)
	log.Printf("!!! Nie ustawiono USBSHARE_PASSWORD. Wygenerowano hasło: %s (zapisane w %s)", cfg.Password, pf)
}

func ensureModule(sys *Sysfs) {
	if sys.StubLoaded() {
		return
	}
	if p, err := exec.LookPath("modprobe"); err == nil {
		if out, err := exec.Command(p, "usbip-host").CombinedOutput(); err != nil {
			log.Printf("modprobe usbip-host: %v %s", err, strings.TrimSpace(string(out)))
		}
	}
	if !sys.StubLoaded() {
		log.Printf("UWAGA: moduł usbip-host nie jest załadowany. Na hoście wykonaj: sudo modprobe usbip-host")
	}
}

func main() {
	showVersion := flag.Bool("version", false, "pokaż wersję")
	flag.Parse()
	if *showVersion {
		println(version)
		return
	}
	log.SetFlags(log.LstdFlags)
	hostname, _ := os.Hostname()
	cfg := Config{
		Listen:       env("USBSHARE_LISTEN", ":"+strconv.Itoa(proto.DefaultPort)),
		Password:     env("USBSHARE_PASSWORD", ""),
		Name:         env("USBSHARE_NAME", hostname),
		DataDir:      env("USBSHARE_DATA", "/data"),
		SysfsRoot:    env("USBSHARE_SYSFS", "/sys"),
		Allow:        splitList(env("USBSHARE_ALLOW", "")),
		Deny:         splitList(env("USBSHARE_DENY", "")),
		ShareHubs:    envBool("USBSHARE_SHARE_HUBS", false),
		ReturnToHost: envBool("USBSHARE_RETURN_TO_HOST", true),
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("katalog danych: %v", err)
	}
	loadPassword(&cfg)
	cert, fp, err := tlsutil.LoadOrCreate(cfg.DataDir, cfg.Name)
	if err != nil {
		log.Fatalf("certyfikat TLS: %v", err)
	}
	srv := NewServer(cfg, tlsutil.ServerConfig(cert))
	ensureModule(srv.sys)
	srv.Cleanup()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("nasłuch %s: %v", cfg.Listen, err)
	}
	log.Printf("USBShare server %s „%s” nasłuchuje na %s", version, cfg.Name, cfg.Listen)
	log.Printf("Odcisk certyfikatu (porównaj w kliencie): %s", fp)
	if devs, err := srv.deviceInfos(); err == nil {
		for _, d := range devs {
			log.Printf("  urządzenie %s  %s:%s  %s %s", d.BusID, d.VendorID, d.ProductID, d.Manufacturer, d.Product)
		}
	} else {
		log.Printf("nie można odczytać listy USB: %v", err)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
		log.Fatal(err)
	}
}
