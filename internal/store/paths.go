// Package store resolves on-disk locations and writes JSON atomically.
//
// Nothing the running program produces belongs in the source repository. State
// is what changes behaviour if lost (credit ledger, alert arming, watchlist);
// cache is re-derivable at credit cost, so deleting it must be harmless.
package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "lcw-dashboard"

type Paths struct {
	ConfigDir string
	StateDir  string
	CacheDir  string
}

func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return Paths{
		ConfigDir: filepath.Join(envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config")), appName),
		StateDir:  filepath.Join(envOr("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), appName),
		CacheDir:  filepath.Join(envOr("XDG_CACHE_HOME", filepath.Join(home, ".cache")), appName),
	}, nil
}

// envOr ignores a relative value: honouring one would scatter state relative to
// the working directory, which is usually the source repository.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); filepath.IsAbs(v) {
		return v
	}
	return fallback
}

func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.ConfigDir, p.StateDir, p.CacheDir, p.HistoryDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

func (p Paths) ConfigFile() string  { return filepath.Join(p.ConfigDir, "config.yaml") }
func (p Paths) EnvFile() string     { return filepath.Join(p.ConfigDir, ".env") }
func (p Paths) Ledger() string      { return filepath.Join(p.StateDir, "ledger.json") }
func (p Paths) AlertState() string  { return filepath.Join(p.StateDir, "alerts-state.json") }
func (p Paths) Watchlist() string   { return filepath.Join(p.StateDir, "watchlist.json") }
func (p Paths) LastGood() string    { return filepath.Join(p.StateDir, "lastgood.json") }
func (p Paths) Lock() string        { return filepath.Join(p.StateDir, "lock") }
func (p Paths) HistoryDir() string  { return filepath.Join(p.StateDir, "history") }
func (p Paths) SearchIndex() string { return filepath.Join(p.CacheDir, "search-index.json") }
func (p Paths) Fiats() string       { return filepath.Join(p.CacheDir, "fiats.json") }

// HistoryFile validates against an allowlist rather than rejecting bad shapes:
// a "not equal to filepath.Base" check accepts "." and "..".
func (p Paths) HistoryFile(code string) (string, error) {
	if !safeCodeForFilename(code) {
		return "", fmt.Errorf("coin code is not safe as a filename: %q", code)
	}
	return filepath.Join(p.HistoryDir(), code+".ring"), nil
}

const maxCodeLen = 32

func safeCodeForFilename(code string) bool {
	if code == "" || len(code) > maxCodeLen {
		return false
	}
	alnum := false
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			alnum = true
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return alnum
}
