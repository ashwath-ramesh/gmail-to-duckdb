package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

type ServeInfo struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
	// DB is the absolute database path. Two databases with the same base
	// name (mail.duckdb and mail.db) share one serve file name.
	DB string `json:"db,omitempty"`
}

type otherDatabaseError struct{ serveDB, want string }

func (e otherDatabaseError) Error() string {
	return fmt.Sprintf("the serve file belongs to %s, not %s", e.serveDB, e.want)
}

// IsOtherDatabase reports whether a Dial error means that the serve file
// belongs to a different database.
func IsOtherDatabase(err error) bool {
	var e otherDatabaseError
	return errors.As(err, &e)
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
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return err
	}
	info.DB = abs
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return privfile.Write(ServePath(dbPath), b)
}

func ReadServeFile(dbPath string) (ServeInfo, error) {
	var info ServeInfo
	b, err := privfile.Read(ServePath(dbPath))
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
