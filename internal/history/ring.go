// Package history records price samples the poll loop already receives, so it
// costs no API credits.
//
// Each tier is a fixed-size ring. The slot index comes from the timestamp, so
// the file reaches its size on creation and never grows. Appending instead would
// need a pruning job, and a pruning job that does not run fills a disk.
//
// A slot stores only rate, volume and cap: 16 bytes. Its timestamp is derived
// from the tier's newest epoch and the slot's distance behind it, which is why
// nothing per-slot needs storing. A zero rate marks a slot never written.
package history

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/config"
)

const (
	magic       = "LCWH"
	version     = 1
	SlotSize    = 16
	headerFixed = 8
	tierHeader  = 24
)

type Sample struct {
	At     time.Time
	Rate   float64
	Volume float64
	Cap    float64
}

type Point struct {
	Date   int64    `json:"date"`
	Rate   *float64 `json:"rate"`
	Volume *float64 `json:"volume"`
	Cap    *float64 `json:"cap"`
}

type tier struct {
	resolution time.Duration
	slots      int
	newest     int64
	rate       []float64
	volume     []float32
	cap        []float32
}

func (t *tier) retention() time.Duration { return t.resolution * time.Duration(t.slots) }

func (t *tier) index(epoch int64) int {
	i := int(epoch % int64(t.slots))
	if i < 0 {
		i += t.slots
	}
	return i
}

// epochAt derives a slot's epoch from its distance behind the newest write.
// A slot older than one full wrap is stale and reports false.
func (t *tier) epochAt(i int) (int64, bool) {
	if t.newest == 0 || t.rate[i] == 0 {
		return 0, false
	}
	back := (t.index(t.newest) - i + t.slots) % t.slots
	epoch := t.newest - int64(back)
	if epoch < 0 {
		return 0, false
	}
	return epoch, true
}

type Ring struct {
	mu    sync.Mutex
	code  string
	path  string
	tiers []*tier
	dirty bool
}

func New(code, path string, tiers []config.HistoryTier) (*Ring, error) {
	if len(tiers) == 0 {
		return nil, errors.New("history needs at least one tier")
	}
	r := &Ring{code: code, path: path}
	for i, t := range tiers {
		slots := t.Slots()
		if slots < 1 {
			return nil, fmt.Errorf("tier %d holds no slots", i)
		}
		r.tiers = append(r.tiers, &tier{
			resolution: t.Resolution.D(),
			slots:      slots,
			rate:       make([]float64, slots),
			volume:     make([]float32, slots),
			cap:        make([]float32, slots),
		})
	}
	return r, nil
}

func (r *Ring) Code() string { return r.code }
func (r *Ring) Path() string { return r.path }

// Bytes is the on-disk size, fixed for the ring's lifetime.
func (r *Ring) Bytes() int64 {
	n := int64(headerFixed + tierHeader*len(r.tiers))
	for _, t := range r.tiers {
		n += int64(t.slots) * SlotSize
	}
	return n
}

func (r *Ring) Dirty() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dirty
}

// Add records a sample in every tier. Two samples inside one bucket overwrite:
// the newest observation wins.
func (r *Ring) Add(s Sample) {
	if s.At.IsZero() || s.Rate == 0 || math.IsNaN(s.Rate) || math.IsInf(s.Rate, 0) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	ms := s.At.UTC().UnixMilli()
	for _, t := range r.tiers {
		epoch := ms / t.resolution.Milliseconds()
		if epoch < t.newest {
			// Out of order and older than the newest write; the derived-epoch
			// scheme cannot represent it.
			continue
		}
		i := t.index(epoch)
		t.rate[i] = s.Rate
		t.volume[i] = float32(s.Volume)
		t.cap[i] = float32(s.Cap)
		t.newest = epoch
	}
	r.dirty = true
}

// Range returns ascending samples between start and end from the finest tier
// that covers the span. maxPoints downsamples, so callers never decimate and the
// browser never receives thousands of points.
func (r *Ring) Range(start, end time.Time, maxPoints int) []Point {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !end.After(start) {
		return nil
	}
	t := r.tierFor(end.Sub(start))
	if t == nil {
		return nil
	}

	res := t.resolution.Milliseconds()
	startEpoch := start.UTC().UnixMilli() / res
	endEpoch := end.UTC().UnixMilli() / res

	out := make([]Point, 0, t.slots)
	// Walk oldest to newest so the result is already sorted.
	newestIdx := t.index(t.newest)
	for back := t.slots - 1; back >= 0; back-- {
		i := (newestIdx - back + t.slots) % t.slots
		epoch, ok := t.epochAt(i)
		if !ok || epoch < startEpoch || epoch > endEpoch {
			continue
		}
		rate := t.rate[i]
		p := Point{Date: epoch * res, Rate: &rate}
		if v := float64(t.volume[i]); v != 0 {
			p.Volume = &v
		}
		if c := float64(t.cap[i]); c != 0 {
			p.Cap = &c
		}
		out = append(out, p)
	}

	if maxPoints > 0 && len(out) > maxPoints {
		step := float64(len(out)-1) / float64(maxPoints-1)
		thinned := make([]Point, 0, maxPoints)
		for i := 0; i < maxPoints; i++ {
			thinned = append(thinned, out[int(float64(i)*step+0.5)])
		}
		out = thinned
	}
	return out
}

func (r *Ring) tierFor(span time.Duration) *tier {
	for _, t := range r.tiers {
		if t.retention() >= span {
			return t
		}
	}
	if len(r.tiers) == 0 {
		return nil
	}
	return r.tiers[len(r.tiers)-1]
}

// Coverage reports the span held, so a caller knows when to fall back to the API.
func (r *Ring) Coverage() (oldest, newest time.Time, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tiers {
		res := t.resolution.Milliseconds()
		for i := range t.rate {
			epoch, ok := t.epochAt(i)
			if !ok {
				continue
			}
			at := time.UnixMilli(epoch * res).UTC()
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
			if at.After(newest) {
				newest = at
			}
			count++
		}
	}
	return oldest, newest, count
}

func (r *Ring) Save() error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	buf := r.encode()
	r.dirty = false
	r.mu.Unlock()

	tmp := r.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", tmp, err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename onto %s: %w", r.path, err)
	}
	return nil
}

func (r *Ring) encode() []byte {
	buf := make([]byte, 0, r.Bytes())
	buf = append(buf, magic...)
	buf = binary.LittleEndian.AppendUint16(buf, version)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(r.tiers)))

	for _, t := range r.tiers {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(t.resolution.Milliseconds()))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(t.slots))
		buf = binary.LittleEndian.AppendUint64(buf, uint64(t.newest))
	}
	for _, t := range r.tiers {
		for i := range t.rate {
			buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(t.rate[i]))
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(t.volume[i]))
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(t.cap[i]))
		}
	}
	return buf
}

var ErrGeometryChanged = errors.New("stored tier geometry differs from the configured tiers")

// Load reads a ring. Geometry that no longer matches the config is discarded
// rather than migrated: the data is re-derivable by waiting, and a
// half-translated archive is worse than an empty one.
func Load(code, path string, tiers []config.HistoryTier) (*Ring, error) {
	r, err := New(code, path, tiers)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := r.decode(raw); err != nil {
		return r, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func (r *Ring) decode(raw []byte) error {
	if len(raw) < headerFixed {
		return io.ErrUnexpectedEOF
	}
	if string(raw[:4]) != magic {
		return errors.New("not an lcw history file")
	}
	if v := binary.LittleEndian.Uint16(raw[4:6]); v != version {
		return fmt.Errorf("unsupported file version %d", v)
	}
	if n := int(binary.LittleEndian.Uint16(raw[6:8])); n != len(r.tiers) {
		return ErrGeometryChanged
	}

	off := headerFixed
	newest := make([]int64, len(r.tiers))
	for i, t := range r.tiers {
		if len(raw) < off+tierHeader {
			return io.ErrUnexpectedEOF
		}
		res := int64(binary.LittleEndian.Uint64(raw[off : off+8]))
		slots := int(binary.LittleEndian.Uint64(raw[off+8 : off+16]))
		if res != t.resolution.Milliseconds() || slots != t.slots {
			return ErrGeometryChanged
		}
		newest[i] = int64(binary.LittleEndian.Uint64(raw[off+16 : off+24]))
		off += tierHeader
	}
	for ti, t := range r.tiers {
		for i := range t.rate {
			if len(raw) < off+SlotSize {
				return io.ErrUnexpectedEOF
			}
			t.rate[i] = math.Float64frombits(binary.LittleEndian.Uint64(raw[off : off+8]))
			t.volume[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+8 : off+12]))
			t.cap[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[off+12 : off+16]))
			off += SlotSize
		}
		t.newest = newest[ti]
	}
	return nil
}
