package openurl

import (
	"os/exec"
	"runtime"
)

func Command(rawURL string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL)
	case "windows":
		return exec.Command("cmd", "/c", "start", "", rawURL)
	default:
		return exec.Command("xdg-open", rawURL)
	}
}

func Open(rawURL string) error {
	return Command(rawURL).Start()
}
