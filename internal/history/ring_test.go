package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/config"
)

var base = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func tiers() []config.HistoryTier {
	return config.Default().History.Tiers
}

func smallTiers() []config.HistoryTier {
	return []config.HistoryTier{
		{Resolution: config.Duration(time.Minute), Retention: config.Duration(10 * time.Minute)},
		{Resolution: config.Duration(10 * time.Minute), Retention: config.Duration(100 * time.Minute)},
	}
}

func newRing(t *testing.T, ts []config.HistoryTier) *Ring {
	t.Helper()
	r, err := New("BTC", filepath.Join(t.TempDir(), "BTC.ring"), ts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSizeMatchesTheDocumentedFootprint(t *testing.T) {
	r := newRing(t, tiers())

	// 1440 + 2880 + 8760 slots at 16 bytes, plus an 80-byte header.
	const wantSlots = 13080
	wantBytes := int64(wantSlots*SlotSize + headerFixed + tierHeader*3)
	if got := r.Bytes(); got != wantBytes {
		t.Errorf("Bytes = %d, want %d", got, wantBytes)
	}
	if SlotSize != 16 {
		t.Errorf("SlotSize = %d, want 16 (float64 rate, float32 volume, float32 cap)", SlotSize)
	}
}

// The whole point of a ring: writing forever does not grow the file.
func TestFileSizeIsConstantAfterManyWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BTC.ring")
	r, err := New("BTC", path, smallTiers())
	if err != nil {
		t.Fatal(err)
	}

	at := base
	for i := 0; i < 100000; i++ {
		r.Add(Sample{At: at, Rate: float64(1000 + i%50), Volume: 1e9, Cap: 2e12})
		at = at.Add(30 * time.Second)
		if i%10000 == 0 {
			if err := r.Save(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != r.Bytes() {
		t.Errorf("file is %d bytes after 100k writes, want the preallocated %d",
			fi.Size(), r.Bytes())
	}
}

func TestRangeReturnsAscendingSamples(t *testing.T) {
	r := newRing(t, smallTiers())

	for i := 0; i < 5; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(100 + i)})
	}

	// Span must stay inside the fine tier's retention, or tier selection
	// correctly falls back to the coarse one.
	pts := r.Range(base, base.Add(5*time.Minute), 0)
	if len(pts) != 5 {
		t.Fatalf("got %d points, want 5: %+v", len(pts), pts)
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].Date <= pts[i-1].Date {
			t.Fatalf("points are not ascending at %d: %d then %d", i, pts[i-1].Date, pts[i].Date)
		}
	}
	if *pts[0].Rate != 100 || *pts[4].Rate != 104 {
		t.Errorf("rates = %v .. %v, want 100 .. 104", *pts[0].Rate, *pts[4].Rate)
	}
}

func TestRangeExcludesOutsideTheWindow(t *testing.T) {
	r := newRing(t, smallTiers())
	for i := 0; i < 10; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(i + 1)})
	}

	pts := r.Range(base.Add(3*time.Minute), base.Add(6*time.Minute), 0)
	for _, p := range pts {
		at := time.UnixMilli(p.Date).UTC()
		if at.Before(base.Add(3*time.Minute)) || at.After(base.Add(6*time.Minute)) {
			t.Errorf("point at %s is outside the requested window", at)
		}
	}
	if len(pts) == 0 {
		t.Error("want some points inside the window")
	}
}

// Once the ring wraps, slots older than one full lap must not resurface with a
// bogus timestamp.
func TestWrappedSlotsDoNotResurface(t *testing.T) {
	r := newRing(t, smallTiers()) // finest tier holds 10 one-minute slots

	for i := 0; i < 25; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(i + 1)})
	}

	// Ask for the first five minutes, long since overwritten.
	pts := r.Range(base, base.Add(5*time.Minute), 0)
	for _, p := range pts {
		if *p.Rate <= 5 {
			t.Errorf("stale sample resurfaced: rate %v at %s", *p.Rate,
				time.UnixMilli(p.Date).UTC())
		}
	}

	// The most recent ten minutes must still be there.
	recent := r.Range(base.Add(15*time.Minute), base.Add(25*time.Minute), 0)
	if len(recent) < 9 {
		t.Errorf("got %d recent points, want about 10", len(recent))
	}
}

func TestSameBucketOverwrites(t *testing.T) {
	r := newRing(t, smallTiers())
	r.Add(Sample{At: base, Rate: 100})
	r.Add(Sample{At: base.Add(10 * time.Second), Rate: 101})
	r.Add(Sample{At: base.Add(50 * time.Second), Rate: 102})

	pts := r.Range(base, base.Add(time.Minute), 0)
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1: three samples share a one-minute bucket", len(pts))
	}
	if *pts[0].Rate != 102 {
		t.Errorf("rate = %v, want 102 (newest wins)", *pts[0].Rate)
	}
}

func TestZeroAndInvalidSamplesAreIgnored(t *testing.T) {
	r := newRing(t, smallTiers())
	// A zero rate marks an unwritten slot, so it cannot be stored as data.
	r.Add(Sample{At: base, Rate: 0})
	r.Add(Sample{At: time.Time{}, Rate: 100})

	if _, _, count := r.Coverage(); count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestCoarserTierServesALongerSpan(t *testing.T) {
	r := newRing(t, smallTiers())
	// The fine tier retains 10 minutes; the coarse one 100.
	for i := 0; i < 60; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(i + 1)})
	}

	long := r.Range(base, base.Add(60*time.Minute), 0)
	if len(long) < 5 {
		t.Errorf("got %d points over an hour, want the coarse tier to serve it", len(long))
	}
	short := r.Range(base.Add(52*time.Minute), base.Add(60*time.Minute), 0)
	if len(short) < 5 {
		t.Errorf("got %d points over 8 minutes, want the fine tier", len(short))
	}
	// The fine tier must give more resolution over its own span.
	if len(short) <= 2 && len(long) <= 2 {
		t.Error("neither tier produced useful resolution")
	}
}

func TestMaxPointsDownsamplesAndKeepsTheEnds(t *testing.T) {
	r := newRing(t, smallTiers())
	for i := 0; i < 60; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(i + 1)})
	}

	all := r.Range(base, base.Add(60*time.Minute), 0)
	if len(all) < 6 {
		t.Fatalf("only %d points to thin", len(all))
	}
	thin := r.Range(base, base.Add(60*time.Minute), 5)
	if len(thin) != 5 {
		t.Fatalf("got %d points, want 5", len(thin))
	}
	if thin[0].Date != all[0].Date {
		t.Error("downsampling must keep the first point")
	}
	if thin[len(thin)-1].Date != all[len(all)-1].Date {
		t.Error("downsampling must keep the last point")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BTC.ring")

	r, err := New("BTC", path, smallTiers())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute),
			Rate: float64(100 + i), Volume: 1e9, Cap: 2e12})
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load("BTC", path, smallTiers())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	before := r.Range(base, base.Add(10*time.Minute), 0)
	after := loaded.Range(base, base.Add(10*time.Minute), 0)

	if len(before) != len(after) {
		t.Fatalf("got %d points after reload, want %d", len(after), len(before))
	}
	for i := range before {
		if before[i].Date != after[i].Date || *before[i].Rate != *after[i].Rate {
			t.Errorf("point %d differs: %+v vs %+v", i, before[i], after[i])
		}
	}
}

func TestSaveIsANoOpWhenClean(t *testing.T) {
	r := newRing(t, smallTiers())
	r.Add(Sample{At: base, Rate: 100})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if r.Dirty() {
		t.Error("should be clean after a save")
	}
}

func TestLoadMissingFileStartsEmpty(t *testing.T) {
	r, err := Load("BTC", filepath.Join(t.TempDir(), "absent.ring"), smallTiers())
	if err != nil {
		t.Fatalf("a missing file must not error: %v", err)
	}
	if _, _, count := r.Coverage(); count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

// Changed geometry is discarded, not migrated: the data is re-derivable by
// waiting, and a half-translated archive is worse than an empty one.
func TestGeometryChangeIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BTC.ring")

	r, _ := New("BTC", path, smallTiers())
	r.Add(Sample{At: base, Rate: 100})
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}

	changed := []config.HistoryTier{
		{Resolution: config.Duration(5 * time.Minute), Retention: config.Duration(time.Hour)},
		{Resolution: config.Duration(30 * time.Minute), Retention: config.Duration(24 * time.Hour)},
	}
	if _, err := Load("BTC", path, changed); err == nil {
		t.Error("differing geometry should be reported")
	}

	fewer := smallTiers()[:1]
	if _, err := Load("BTC", path, fewer); err == nil {
		t.Error("a differing tier count should be reported")
	}
}

func TestCorruptFileIsReportedButYieldsAUsableRing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BTC.ring")
	if err := os.WriteFile(path, []byte("not a ring file at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Load("BTC", path, smallTiers())
	if err == nil {
		t.Error("corruption should be reported")
	}
	if r == nil {
		t.Fatal("a usable empty ring should still be returned so recording continues")
	}
	r.Add(Sample{At: base, Rate: 100})
	if _, _, count := r.Coverage(); count == 0 {
		t.Error("the returned ring should be writable")
	}
}

func TestCoverageReportsTheSpanHeld(t *testing.T) {
	r := newRing(t, smallTiers())
	for i := 0; i < 5; i++ {
		r.Add(Sample{At: base.Add(time.Duration(i) * time.Minute), Rate: float64(i + 1)})
	}

	oldest, newest, count := r.Coverage()
	if count == 0 {
		t.Fatal("count = 0")
	}
	if oldest.After(base) {
		t.Errorf("oldest = %s, want at or before %s", oldest, base)
	}
	if newest.Before(base.Add(4 * time.Minute)) {
		t.Errorf("newest = %s, want at or after %s", newest, base.Add(4*time.Minute))
	}
}

func TestOutOfOrderOlderSampleIsDropped(t *testing.T) {
	r := newRing(t, smallTiers())
	r.Add(Sample{At: base.Add(5 * time.Minute), Rate: 200})
	r.Add(Sample{At: base, Rate: 100})

	pts := r.Range(base, base.Add(6*time.Minute), 0)
	for _, p := range pts {
		if *p.Rate == 100 {
			t.Error("a sample older than the newest write must not be stored: the " +
				"derived-epoch scheme cannot represent it")
		}
	}
}

func TestRangeRejectsAnInvertedWindow(t *testing.T) {
	r := newRing(t, smallTiers())
	r.Add(Sample{At: base, Rate: 100})
	if pts := r.Range(base, base.Add(-time.Hour), 0); pts != nil {
		t.Errorf("got %d points for an inverted window, want none", len(pts))
	}
}
