package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads path over the defaults. A missing file is not an error: the
// defaults are a working configuration.
func Load(path string) (Config, error) {
	cfg := Default()

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	// KnownFields catches typos. In a config-driven scheduler a silently ignored
	// key means the program runs at a rate the user did not ask for.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, fs.ErrClosed) {
		if err.Error() == "EOF" {
			return cfg, nil
		}
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// ApplyEnv overlays environment variables. LCW_API_KEY is the only source for
// the key.
func (c *Config) ApplyEnv() {
	if v := os.Getenv("LCW_API_KEY"); v != "" {
		c.APIKey = strings.TrimSpace(v)
	}
	if v := os.Getenv("LCW_LISTEN"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("LCW_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
}

// Redacted returns a copy safe to serve over HTTP.
func (c Config) Redacted() Config {
	c.APIKey = ""
	return c
}

func (c Config) HasAPIKey() bool { return c.APIKey != "" }

// IsLoopback reports whether Listen binds only the local machine.
func (c Config) IsLoopback() bool {
	host, _, err := net.SplitHostPort(c.Server.Listen)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
