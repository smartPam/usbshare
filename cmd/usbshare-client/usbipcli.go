package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Obsługa narzędzia usbip: usbip-win2 (Windows) lub usbip z pakietu linux-tools.
// Obie wersje mają te same polecenia: attach -r <host> -b <busid>, detach -p <port>, port.

func defaultUsbipPath() string {
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), `C:\Program Files`} {
			if base == "" {
				continue
			}
			for _, sub := range []string{"USBip", "usbip-win2", "USBip-win2"} {
				p := filepath.Join(base, sub, "usbip.exe")
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
		if p, err := exec.LookPath("usbip.exe"); err == nil {
			return p
		}
		return `C:\Program Files\USBip\usbip.exe`
	}
	if p, err := exec.LookPath("usbip"); err == nil {
		return p
	}
	return "/usr/sbin/usbip"
}

func runUsbip(path string, tcpPort int, args ...string) (string, error) {
	if path == "" {
		return "", errors.New("nie ustawiono ścieżki do usbip")
	}
	if _, err := os.Stat(path); err != nil {
		if _, err2 := exec.LookPath(path); err2 != nil {
			return "", fmt.Errorf("nie znaleziono %s – zainstaluj usbip-win2 lub ustaw ścieżkę w Ustawieniach", path)
		}
	}
	if tcpPort != usbipPort && runtime.GOOS != "windows" {
		args = append([]string{"--tcp-port", strconv.Itoa(tcpPort)}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		low := strings.ToLower(text)
		if strings.Contains(low, "access is denied") || strings.Contains(low, "odmowa dostępu") || strings.Contains(low, "permission denied") {
			text += " – uruchom klienta jako administrator"
		}
		return text, errors.New(text)
	}
	return text, nil
}

var (
	rePortLine = regexp.MustCompile(`(?i)^\s*port\s+(\d+)`)
	reURL      = regexp.MustCompile(`usbip://([^\s/]+)/(\S+)`)
	reAttached = regexp.MustCompile(`(?i)port\s+(\d+)`)
)

func portKey(ip, busid string) string { return ip + "|" + busid }

// parsePorts zamienia wynik `usbip port` na mapę "ip|busid" -> numer portu.
func parsePorts(out string) map[string]int {
	res := map[string]int{}
	cur := -1
	for _, line := range strings.Split(out, "\n") {
		if m := rePortLine.FindStringSubmatch(line); m != nil {
			cur, _ = strconv.Atoi(m[1])
		}
		if m := reURL.FindStringSubmatch(line); m != nil && cur >= 0 {
			host := m[1]
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			res[portKey(host, m[2])] = cur
		}
	}
	return res
}

func usbipPorts(path string, tcpPort int) (map[string]int, error) {
	out, err := runUsbip(path, tcpPort, "port")
	if err != nil {
		// brak podłączonych urządzeń bywa zgłaszany jako błąd
		if strings.Contains(strings.ToLower(out), "no ") || out == "" {
			return map[string]int{}, nil
		}
		return nil, err
	}
	return parsePorts(out), nil
}

func usbipAttach(path string, tcpPort int, ip, busid string) (int, error) {
	out, err := runUsbip(path, tcpPort, "attach", "-r", ip, "-b", busid)
	if err != nil {
		return -1, err
	}
	if m := reAttached.FindStringSubmatch(out); m != nil {
		p, _ := strconv.Atoi(m[1])
		return p, nil
	}
	return -1, nil
}

func usbipDetach(path string, tcpPort int, port int) error {
	_, err := runUsbip(path, tcpPort, "detach", "-p", strconv.Itoa(port))
	return err
}
