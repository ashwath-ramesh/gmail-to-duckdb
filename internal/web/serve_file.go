package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type ServeInfo struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func ServePath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".serve.json"
}

func ValidateServeURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" {
		return fmt.Errorf("serve url must use http")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("serve url must be loopback")
	}
	return nil
}

func WriteServeFile(dbPath string, info ServeInfo) error {
	if err := ValidateServeURL(info.URL); err != nil {
		return err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	path := ServePath(dbPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func ReadServeFile(dbPath string) (ServeInfo, error) {
	var info ServeInfo
	b, err := os.ReadFile(ServePath(dbPath))
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(b, &info)
	return info, err
}

func RemoveServeFile(dbPath string) error {
	err := os.Remove(ServePath(dbPath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
