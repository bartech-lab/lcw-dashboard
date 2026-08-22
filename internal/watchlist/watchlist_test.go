package watchlist

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
)

var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func newList(t *testing.T, max, chunk int) *List {
	t.Helper()
	l := New(clock.NewFake(now), filepath.Join(t.TempDir(), "watchlist.json"), max, chunk)
	if err := l.Load(nil); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNormalizeUpperCasesDedupesAndSorts(t *testing.T) {
	l := newList(t, 300, 100)
	if _, err := l.Set([]string{"eth", " BTC ", "btc", "", "sol"}); err != nil {
		t.Fatal(err)
	}
	got := l.Codes()
	want := []string{"BTC", "ETH", "SOL"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A lower-case code returns no data from the API rather than an error, which
// looks like an empty watchlist.
func TestContainsIsCaseInsensitive(t *testing.T) {
	l := newList(t, 300, 100)
	l.Set([]string{"BTC"})
	if !l.Contains("btc") {
		t.Error("Contains should normalize its argument")
	}
}

func TestSetReportsWhetherAnythingChanged(t *testing.T) {
	l := newList(t, 300, 100)

	changed, err := l.Set([]string{"BTC", "ETH"})
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	// Same contents in a different order is not a change; the caller must not
	// spend a credit refetching.
	changed, err = l.Set([]string{"ETH", "btc"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("reordering the same codes should not count as a change")
	}
}

func TestSetRejectsTooMany(t *testing.T) {
	l := newList(t, 3, 100)
	if _, err := l.Set([]string{"A", "B", "C", "D"}); err == nil {
		t.Error("want an error above the maximum")
	}
	if got := l.Len(); got != 0 {
		t.Errorf("Len = %d, want the list unchanged after a rejected Set", got)
	}
}

func TestToggle(t *testing.T) {
	l := newList(t, 300, 100)

	added, err := l.Toggle("btc")
	if err != nil || !added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	if !l.Contains("BTC") {
		t.Fatal("BTC should be present")
	}

	added, err = l.Toggle("BTC")
	if err != nil || added {
		t.Fatalf("added = %v, err = %v", added, err)
	}
	if l.Contains("BTC") {
		t.Error("BTC should have been removed")
	}
	if got := l.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
}

func TestToggleRejectsEmpty(t *testing.T) {
	l := newList(t, 300, 100)
	if _, err := l.Toggle("  "); err == nil {
		t.Error("want an error for an empty code")
	}
}

// Chunk count is the per-refresh credit cost, so it must be exact.
func TestChunksBoundTheCreditCost(t *testing.T) {
	l := newList(t, 300, 100)

	if got := l.ChunkCount(); got != 1 {
		t.Errorf("an empty watchlist should still report 1, got %d", got)
	}

	codes := make([]string, 100)
	for i := range codes {
		codes[i] = string(rune('A'+i/26)) + string(rune('A'+i%26))
	}
	if _, err := l.Set(codes); err != nil {
		t.Fatal(err)
	}
	if got := l.ChunkCount(); got != 1 {
		t.Errorf("100 codes = %d chunks, want 1", got)
	}

	more := append(codes, "ZZZ")
	if _, err := l.Set(more); err != nil {
		t.Fatal(err)
	}
	if got := l.ChunkCount(); got != 2 {
		t.Errorf("101 codes = %d chunks, want 2", got)
	}
	for i, ch := range l.Chunks() {
		if len(ch) > 100 {
			t.Errorf("chunk %d holds %d codes, above the API maximum", i, len(ch))
		}
	}
}

func TestChunkSizeIsCappedAtTheAPIMaximum(t *testing.T) {
	l := newList(t, 500, 1000)
	codes := make([]string, 150)
	for i := range codes {
		codes[i] = string(rune('A'+i/26)) + string(rune('A'+i%26)) + string(rune('a'+i%7))
	}
	l.Set(codes)
	for _, ch := range l.Chunks() {
		if len(ch) > 100 {
			t.Fatalf("chunk of %d exceeds the API limit even though chunk_size was 1000", len(ch))
		}
	}
}

// A code the API declines must surface rather than silently vanish.
func TestMarkUnknownTracksMissingCodes(t *testing.T) {
	l := newList(t, 300, 100)
	l.Set([]string{"BTC", "ETH", "NOPE"})

	missing := l.MarkUnknown([]string{"BTC", "ETH", "NOPE"}, []string{"BTC", "ETH"})
	if len(missing) != 1 || missing[0] != "NOPE" {
		t.Fatalf("missing = %v, want [NOPE]", missing)
	}
	if got := l.Unknown(); len(got) != 1 || got[0] != "NOPE" {
		t.Errorf("Unknown = %v", got)
	}

	// It reappears later; the marker must clear.
	l.MarkUnknown([]string{"NOPE"}, []string{"NOPE"})
	if got := l.Unknown(); len(got) != 0 {
		t.Errorf("Unknown = %v, want empty", got)
	}
}

func TestRemovingACodeClearsItsUnknownMarker(t *testing.T) {
	l := newList(t, 300, 100)
	l.Set([]string{"BTC", "NOPE"})
	l.MarkUnknown([]string{"BTC", "NOPE"}, []string{"BTC"})

	l.Set([]string{"BTC"})
	if got := l.Unknown(); len(got) != 0 {
		t.Errorf("Unknown = %v, want empty after the code was removed", got)
	}
}

func TestHashChangesWithContentsOnly(t *testing.T) {
	l := newList(t, 300, 100)
	l.Set([]string{"BTC", "ETH"})
	first := l.Hash()

	l.Set([]string{"ETH", "BTC"})
	if l.Hash() != first {
		t.Error("hash should ignore input order")
	}
	l.Set([]string{"BTC", "ETH", "SOL"})
	if l.Hash() == first {
		t.Error("hash should change when contents change")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watchlist.json")
	clk := clock.NewFake(now)

	a := New(clk, path, 300, 100)
	if err := a.Load([]string{"BTC", "ETH"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Set([]string{"BTC", "ETH", "HYPE"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}

	b := New(clk, path, 300, 100)
	if err := b.Load([]string{"SOL"}); err != nil {
		t.Fatal(err)
	}
	if got := b.Codes(); len(got) != 3 {
		t.Fatalf("got %v, want the persisted three", got)
	}
	if !b.Contains("HYPE") {
		t.Error("HYPE should have survived the reload")
	}
}

func TestLoadSeedsFromInitialOnFirstRun(t *testing.T) {
	l := New(clock.NewFake(now), filepath.Join(t.TempDir(), "w.json"), 300, 100)
	if err := l.Load([]string{"btc", "eth"}); err != nil {
		t.Fatal(err)
	}
	if got := l.Codes(); len(got) != 2 || got[0] != "BTC" {
		t.Errorf("got %v, want [BTC ETH]", got)
	}
}

func TestSnapshotCarriesHashAndMax(t *testing.T) {
	l := newList(t, 300, 100)
	l.Set([]string{"BTC"})
	s := l.Snapshot()

	if len(s.Codes) != 1 || s.Codes[0] != "BTC" {
		t.Errorf("Codes = %v", s.Codes)
	}
	if s.Hash == "" {
		t.Error("Hash should be set")
	}
	if s.Max != 300 {
		t.Errorf("Max = %d, want 300", s.Max)
	}
}
