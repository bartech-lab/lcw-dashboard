// Package store resolves on-disk locations and writes JSON atomically.
//
// Nothing the running program produces belongs in the source repository, so
// every path here resolves outside it. Config, state and cache are kept
// separate on purpose:
//
//	config — user-edited input
//	state  — losing it changes behaviour (credit ledger, alert arming, watchlist)
//	cache  — merely re-derivable at credit cost; deleting it must be harmless
//
// macOS uses the same XDG variables and the same ~/.local fallbacks as Linux, so
// both platforms behave identically and a config directory can be synced.
package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "lcw-dashboard"

// Paths holds every resolved directory and the files inside them.
type Paths struct {
	ConfigDir string
	StateDir  string
	CacheDir  string
}

// Resolve computes the three base directories. It performs no I/O.
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

// envOr returns the environment variable if it is set to an absolute path.
// A relative value is ignored: the XDG spec requires absolute paths, and
// honouring a relative one would scatter state relative to the working
// directory, which for this program is usually the source repository.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); filepath.IsAbs(v) {
		return v
	}
	return fallback
}

// EnsureDirs creates every directory, including the history subdirectory.
// Directories are 0700: the config directory holds the API key.
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

// HistoryFile returns the ring-buffer path for a coin code.
//
// A coin code arrives from the API, so it is untrusted input that becomes a
// filename. This validates against a positive allowlist rather than trying to
// reject bad shapes: a "not equal to filepath.Base" check accepts "." and "..",
// and enumerating traversal tricks is a losing game.
//
// A code outside the allowlist is an error, not a panic. The caller skips
// history for that coin and keeps serving it in the table.
func (p Paths) HistoryFile(code string) (string, error) {
	if !safeCodeForFilename(code) {
		return "", fmt.Errorf("coin code is not safe as a filename: %q", code)
	}
	return filepath.Join(p.HistoryDir(), code+".ring"), nil
}

// maxCodeLen bounds the filename. Real codes are a handful of characters; a
// pathological one should not produce a 4KB path.
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
			// Allowed, but cannot be the whole code.
		default:
			return false
		}
	}
	return alnum
}
