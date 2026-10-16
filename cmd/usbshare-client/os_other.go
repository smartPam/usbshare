//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

func hideWindow(cmd *exec.Cmd) {}

func openBrowser(url string) {
	for _, b := range []string{"xdg-open", "open"} {
		if p, err := exec.LookPath(b); err == nil {
			_ = exec.Command(p, url).Start()
			return
		}
	}
}

func autostartSupported() bool   { return false }
func autostartEnabled() bool     { return false }
func setAutostart(on bool) error { return errors.New("autostart dostępny tylko w Windows") }
