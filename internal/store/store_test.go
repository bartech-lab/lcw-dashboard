package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sample struct {
	Day   string `json:"day"`
	Spend int    `json:"spend"`
}

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	want := sample{Day: "2026-08-21", Spend: 1204}

	if err := WriteJSONAtomic(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	var got sample
	found, err := ReadJSON(path, &got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !found {
		t.Fatal("found = false for a file we just wrote")
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestMissingFileIsNotAnError covers first run: nothing persisted yet is normal,
// not a failure that should stop startup.
func TestMissingFileIsNotAnError(t *testing.T) {
	var got sample
	found, err := ReadJSON(filepath.Join(t.TempDir(), "absent.json"), &got)
	if err != nil {
		t.Fatalf("a missing file should not error: %v", err)
	}
	if found {
		t.Error("found = true for a missing file")
	}
}

func TestWritePermissionsAre0600(t *testing.T) {
	// State files sit beside the API key's directory; they must not be
	// world-readable.
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteJSONAtomic(path, sample{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// TestWriteLeavesNoTempFiles verifies the temp file is always cleaned up, so a
// long-running process does not litter the state directory.
func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	for i := 0; i < 5; i++ {
		if err := WriteJSONAtomic(path, sample{Spend: i}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1", len(entries))
	}
}

// TestOverwriteIsAtomic checks the guarantee that matters: the target is either
// the old content or the new content, never a truncated mix.
func TestOverwriteIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	if err := WriteJSONAtomic(path, sample{Day: "old", Spend: 1}); err != nil {
		t.Fatal(err)
	}
	big := sample{Day: strings.Repeat("y", 100000), Spend: 2}
	if err := WriteJSONAtomic(path, big); err != nil {
		t.Fatal(err)
	}
	var got sample
	if _, err := ReadJSON(path, &got); err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	if got.Spend != 2 || len(got.Day) != 100000 {
		t.Errorf("got Spend=%d len(Day)=%d, want 2 and 100000", got.Spend, len(got.Day))
	}
}

func TestCorruptFileErrorsAndCanBeQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"data":{"day":`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got sample
	found, err := ReadJSON(path, &got)
	if !found {
		t.Error("found should be true: the file exists, it is just unreadable")
	}
	if err == nil {
		t.Fatal("truncated JSON should error")
	}

	newPath, err := Quarantine(path)
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if !strings.HasSuffix(newPath, ".corrupt") {
		t.Errorf("quarantined to %s", newPath)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("original should have been moved aside")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("quarantined file missing: %v", err)
	}
}

// TestSchemaTooNewIsRecoverable covers a rolled-back binary meeting a file from
// a newer one. It must not be fatal.
func TestSchemaTooNewIsRecoverable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	body, _ := json.Marshal(map[string]any{
		"schemaVersion": SchemaVersion + 1,
		"data":          sample{Day: "tomorrow"},
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var got sample
	_, err := ReadJSON(path, &got)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("error = %v, want ErrSchemaTooNew", err)
	}
}

func TestEnvelopeWithoutDataIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got sample
	if _, err := ReadJSON(path, &got); err == nil {
		t.Error("an envelope with no data should error rather than yield a zero value")
	}
}

func TestPathsAreOutsideTheRepository(t *testing.T) {
	// Everything the running program writes must land outside the source tree.
	cfg := t.TempDir()
	state := t.TempDir()
	cache := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CACHE_HOME", cache)

	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigDir != filepath.Join(cfg, appName) {
		t.Errorf("ConfigDir = %s", p.ConfigDir)
	}
	if p.StateDir != filepath.Join(state, appName) {
		t.Errorf("StateDir = %s", p.StateDir)
	}
	if p.CacheDir != filepath.Join(cache, appName) {
		t.Errorf("CacheDir = %s", p.CacheDir)
	}

	if err := p.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, d := range []string{p.ConfigDir, p.StateDir, p.CacheDir, p.HistoryDir()} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Errorf("%s: %v", d, err)
			continue
		}
		if perm := fi.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %o, want 700 (the config dir holds the API key)", d, perm)
		}
	}
}

// TestRelativeXDGIsIgnored guards against scattering state relative to the
// working directory, which for this program is usually the source repository.
func TestRelativeXDGIsIgnored(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative/path")
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.StateDir, "relative/path") {
		t.Errorf("StateDir = %s, want the ~/.local/state fallback", p.StateDir)
	}
	if !filepath.IsAbs(p.StateDir) {
		t.Errorf("StateDir = %s, want an absolute path", p.StateDir)
	}
}

func TestHistoryFileRejectsUnsafeCodes(t *testing.T) {
	p := Paths{StateDir: "/tmp/state"}

	good, err := p.HistoryFile("BTC")
	if err != nil {
		t.Fatalf("BTC: %v", err)
	}
	if filepath.Base(good) != "BTC.ring" {
		t.Errorf("got %s", good)
	}

	// A coin code arrives from the API, so treat it as untrusted input.
	for _, bad := range []string{"", "../../etc/passwd", "a/b", ".", "..", "___", "BTC/../ETH", "BTC\x00"} {
		if _, err := p.HistoryFile(bad); err == nil {
			t.Errorf("HistoryFile(%q) should be rejected", bad)
		}
	}
}

func TestHistoryFileAcceptsRealisticCodes(t *testing.T) {
	p := Paths{StateDir: "/tmp/state"}
	// Codes seen in the wild, including digits and separators.
	for _, code := range []string{"BTC", "ETH", "HYPE", "PAXG", "1INCH", "USDT", "A-B", "A_B"} {
		if _, err := p.HistoryFile(code); err != nil {
			t.Errorf("HistoryFile(%q) rejected a legitimate code: %v", code, err)
		}
	}
}

// Live Coin Watch pads duplicated tickers with underscores, and real codes reach
// 45 characters. Rejecting them on length denied those coins any history.
func TestLongCodesAreShortenedNotRejected(t *testing.T) {
	p := Paths{StateDir: "/tmp/state"}

	long := "_________________________________________BULL"
	path, err := p.HistoryFile(long)
	if err != nil {
		t.Fatalf("a 45-character code must be accepted: %v", err)
	}
	name := filepath.Base(path)
	if len(name) > maxCodeLen+len(".ring") {
		t.Errorf("filename %q is %d chars, want it bounded", name, len(name))
	}

	// Distinct long codes must not collide.
	other := "_________________________________________BEAR"
	otherPath, err := p.HistoryFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if otherPath == path {
		t.Error("two different long codes produced the same file")
	}

	// And the mapping is stable across calls.
	again, _ := p.HistoryFile(long)
	if again != path {
		t.Error("the same code produced two different files")
	}
}

func TestRealWorldCodesAllMapToAFile(t *testing.T) {
	p := Paths{StateDir: "/tmp/state"}
	// Observed in the live top 2000.
	for _, code := range []string{
		"BTC", "______HYPE", "____TAO", "__LAYER",
		"_______________________________TRUMP",
		"_________________________________META",
		"__________________________________________X",
		"_________________________________________BULL",
	} {
		if _, err := p.HistoryFile(code); err != nil {
			t.Errorf("HistoryFile(%q) rejected a real code: %v", code, err)
		}
	}
}
