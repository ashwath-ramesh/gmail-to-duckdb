package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/auth"
	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
)

const (
	DefaultDB          = "mail.duckdb"
	DefaultCredentials = "credentials.json"
	DefaultPort        = "8080"
)

type Config struct {
	DB          string `json:"db"`
	Credentials string `json:"credentials"`
	OAuthPort   int    `json:"oauth_port"`
	Port        string `json:"port,omitempty"`
}

func Defaults() Config {
	return Config{
		DB:          DefaultDB,
		Credentials: DefaultCredentials,
		OAuthPort:   auth.DefaultOAuthPort,
		Port:        DefaultPort,
	}
}

func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "gmail-to-duckdb")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "gmail-to-duckdb")
	}
	return filepath.Join(home, ".config", "gmail-to-duckdb")
}

func DataDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "gmail-to-duckdb")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".local", "share", "gmail-to-duckdb")
	}
	return filepath.Join(home, ".local", "share", "gmail-to-duckdb")
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

// Load reads the config file. A missing file gives the defaults. An unreadable
// or invalid file is an error: a silent fallback would open a different mailbox.
func Load() (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return c, fmt.Errorf("read config %s: %w", Path(), err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return Defaults(), fmt.Errorf("config %s is not valid JSON (%v); fix it, or run gmail-to-duckdb init to write it again", Path(), err)
	}
	if c.DB == "" {
		c.DB = DefaultDB
	}
	if c.Credentials == "" {
		c.Credentials = DefaultCredentials
	}
	if c.OAuthPort <= 0 {
		c.OAuthPort = auth.DefaultOAuthPort
	}
	if c.Port == "" {
		c.Port = DefaultPort
	}
	c.DB = Expand(c.DB)
	c.Credentials = Expand(c.Credentials)
	return c, nil
}

func Write(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return privfile.Write(Path(), b)
}

func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
