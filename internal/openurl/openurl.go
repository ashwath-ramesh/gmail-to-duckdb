// Package openurl opens a web page or a local file in the default browser.
package openurl

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Command returns the command that opens target. It never runs a shell:
// on Windows, "cmd /c start" splits a URL at "&" and runs the rest.
func Command(target string) *exec.Cmd {
	return command(runtime.GOOS, target)
}

func command(goos, target string) *exec.Cmd {
	switch goos {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}

// Open opens an https URL, a loopback http URL, or an existing local file.
// It refuses all other targets.
func Open(target string) error {
	if err := Allowed(target); err != nil {
		return err
	}
	cmd := Command(target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Allowed reports whether Open accepts target.
func Allowed(target string) error {
	if filepath.IsAbs(target) {
		if fi, err := os.Lstat(target); err == nil && fi.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("open: %s is not a regular file", target)
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("open: refusing target")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("open: refusing %s URL to %s", u.Scheme, u.Hostname())
}
