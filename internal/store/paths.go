// Package store resolves on-disk locations and writes JSON atomically.
//
// Nothing the running program produces belongs in the source repository. State
// is what changes behaviour if lost (credit ledger, alert arming, watchlist);
// cache is re-derivable at credit cost, so deleting it must be harmless.
package store

import (
	"crypto/sha256"
	"encoding/hex"
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

// HistoryFile maps a coin code to a ring path.
//
// The character allowlist is the safety check: a "not equal to filepath.Base"
// test accepts "." and "..", and enumerating traversal tricks is a losing game.
//
// Length is handled by shortening, not rejection. Live Coin Watch pads
// duplicated tickers with underscores, and real codes reach 45 characters
// (_________________________________________BULL) and will get longer. Rejecting
// on length silently denied those coins any history at all.
func (p Paths) HistoryFile(code string) (string, error) {
	if !safeCodeChars(code) {
		return "", fmt.Errorf("coin code is not safe as a filename: %q", code)
	}
	return filepath.Join(p.HistoryDir(), shortenCode(code)+".ring"), nil
}

// maxCodeLen bounds the filename. Beyond it the code is truncated and given a
// hash suffix, so every code still maps to exactly one file.
const maxCodeLen = 48

func shortenCode(code string) string {
	if len(code) <= maxCodeLen {
		return code
	}
	sum := sha256.Sum256([]byte(code))
	return code[:maxCodeLen-9] + "-" + hex.EncodeToString(sum[:4])
}

func safeCodeChars(code string) bool {
	if code == "" {
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
