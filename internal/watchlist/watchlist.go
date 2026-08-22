// Package watchlist owns the set of tracked coin codes.
//
// The list is server-owned because the server needs it to build a /coins/map
// request, and watchlist-scoped alert rules must keep firing with no browser
// open. The browser keeps a copy only to paint hearts before the first frame
// arrives.
package watchlist

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/store"
)

type persisted struct {
	Codes     []string  `json:"codes"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type List struct {
	mu        sync.RWMutex
	clk       clock.Clock
	path      string
	max       int
	chunkSize int
	codes     []string
	updatedAt time.Time
	// unknown are codes the API declined to return, kept so they surface in the
	// UI instead of silently vanishing from the table.
	unknown map[string]bool
}

func New(clk clock.Clock, path string, max, chunkSize int) *List {
	return &List{
		clk: clk, path: path, max: max, chunkSize: chunkSize,
		codes:   []string{},
		unknown: map[string]bool{},
	}
}

// Load reads the persisted list, seeding from initial on first run.
func (l *List) Load(initial []string) error {
	var p persisted
	found, err := store.ReadJSON(l.path, &p)
	if err != nil {
		if _, qerr := store.Quarantine(l.path); qerr != nil {
			return fmt.Errorf("%w (and quarantine failed: %v)", err, qerr)
		}
		found = false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if found && len(p.Codes) > 0 {
		l.codes = normalize(p.Codes, l.max)
		l.updatedAt = p.UpdatedAt
		return err
	}
	l.codes = normalize(initial, l.max)
	l.updatedAt = l.clk.Now()
	return err
}

func (l *List) Save() error {
	l.mu.RLock()
	p := persisted{Codes: append([]string(nil), l.codes...), UpdatedAt: l.updatedAt}
	l.mu.RUnlock()
	return store.WriteJSONAtomic(l.path, p)
}

// normalize upper-cases, trims, drops blanks and duplicates, sorts for a stable
// hash, and truncates to max.
func normalize(in []string, max int) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		code := lcw.NormalizeCode(raw)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Strings(out)
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

func (l *List) Codes() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string(nil), l.codes...)
}

func (l *List) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.codes)
}

func (l *List) Contains(code string) bool {
	code = lcw.NormalizeCode(code)
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, c := range l.codes {
		if c == code {
			return true
		}
	}
	return false
}

// Hash identifies the list contents, so a change to it becomes a new view key.
func (l *List) Hash() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return hashCodes(l.codes)
}

func hashCodes(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	h := sha256.Sum256([]byte(strings.Join(codes, ",")))
	return hex.EncodeToString(h[:8])
}

var ErrTooMany = fmt.Errorf("watchlist exceeds the configured maximum")

// Set replaces the list. It returns whether anything changed, so the caller only
// refetches when it must.
func (l *List) Set(codes []string) (changed bool, err error) {
	next := normalize(codes, 0)
	if l.max > 0 && len(next) > l.max {
		return false, fmt.Errorf("%w: %d codes, maximum is %d", ErrTooMany, len(next), l.max)
	}

	l.mu.Lock()
	same := len(next) == len(l.codes)
	if same {
		for i := range next {
			if next[i] != l.codes[i] {
				same = false
				break
			}
		}
	}
	if same {
		l.mu.Unlock()
		return false, nil
	}
	l.codes = next
	l.updatedAt = l.clk.Now()
	// Forget stale unknown markers for codes no longer tracked.
	keep := make(map[string]bool, len(l.unknown))
	for _, c := range next {
		if l.unknown[c] {
			keep[c] = true
		}
	}
	l.unknown = keep
	l.mu.Unlock()
	return true, nil
}

func (l *List) Toggle(code string) (added bool, err error) {
	code = lcw.NormalizeCode(code)
	if code == "" {
		return false, fmt.Errorf("empty coin code")
	}
	current := l.Codes()
	for i, c := range current {
		if c == code {
			_, err := l.Set(append(current[:i:i], current[i+1:]...))
			return false, err
		}
	}
	_, err = l.Set(append(current, code))
	return err == nil, err
}

// Chunks splits the list into requests of at most chunkSize codes.
//
// Chunk count is the credit cost of one refresh. The scheduler multiplies the
// poll interval by it, so a long watchlist costs latency rather than credits.
func (l *List) Chunks() [][]string {
	codes := l.Codes()
	size := l.chunkSize
	if size <= 0 || size > lcw.MaxListLimit {
		size = lcw.MaxListLimit
	}
	if len(codes) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(codes)+size-1)/size)
	for i := 0; i < len(codes); i += size {
		end := i + size
		if end > len(codes) {
			end = len(codes)
		}
		out = append(out, codes[i:end])
	}
	return out
}

// ChunkCount is the per-refresh credit cost.
func (l *List) ChunkCount() int {
	n := len(l.Chunks())
	if n == 0 {
		return 1
	}
	return n
}

// MarkUnknown records which requested codes the API did not return.
func (l *List) MarkUnknown(requested, returned []string) []string {
	got := make(map[string]bool, len(returned))
	for _, c := range returned {
		got[lcw.NormalizeCode(c)] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var missing []string
	for _, c := range requested {
		c = lcw.NormalizeCode(c)
		if !got[c] {
			l.unknown[c] = true
			missing = append(missing, c)
		} else {
			delete(l.unknown, c)
		}
	}
	sort.Strings(missing)
	return missing
}

func (l *List) Unknown() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.unknown))
	for c := range l.unknown {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (l *List) Snapshot() *snapshot.Watchlist {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return &snapshot.Watchlist{
		Codes:     append([]string(nil), l.codes...),
		Hash:      hashCodes(l.codes),
		UpdatedAt: l.updatedAt,
		Max:       l.max,
	}
}
