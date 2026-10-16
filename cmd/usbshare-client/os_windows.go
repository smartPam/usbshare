//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func openBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	hideWindow(cmd)
	_ = cmd.Start()
}

const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

func autostartSupported() bool { return true }

func autostartEnabled() bool {
	cmd := exec.Command("reg", "query", runKey, "/v", "USBShare")
	hideWindow(cmd)
	return cmd.Run() == nil
}

func setAutostart(on bool) error {
	var cmd *exec.Cmd
	if on {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		val := `"` + strings.ReplaceAll(exe, `"`, ``) + `" -background`
		cmd = exec.Command("reg", "add", runKey, "/v", "USBShare", "/t", "REG_SZ", "/d", val, "/f")
	} else {
		cmd = exec.Command("reg", "delete", runKey, "/v", "USBShare", "/f")
	}
	hideWindow(cmd)
	return cmd.Run()
}
