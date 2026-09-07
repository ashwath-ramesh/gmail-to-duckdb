package openurl

import (
	"runtime"
	"testing"
)

func TestCommand(t *testing.T) {
	cmd := Command("http://127.0.0.1:8080/")
	if cmd == nil {
		t.Fatal("nil command")
	}
	switch runtime.GOOS {
	case "darwin":
		if cmd.Args[0] != "open" || cmd.Args[1] != "http://127.0.0.1:8080/" {
			t.Fatalf("args %#v", cmd.Args)
		}
	case "windows":
		if len(cmd.Args) < 5 || cmd.Args[0] != "cmd" || cmd.Args[3] != "" {
			t.Fatalf("args %#v", cmd.Args)
		}
	default:
		if cmd.Args[0] != "xdg-open" || cmd.Args[1] != "http://127.0.0.1:8080/" {
			t.Fatalf("args %#v", cmd.Args)
		}
	}
}
