package history

import (
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/store"
)

// Store owns one Ring per coin, bounded by config.History.MaxCoins so a
// churning top 100 plus a long watchlist cannot fill a disk.
type Store struct {
	mu    sync.Mutex
	cfg   config.History
	paths store.Paths
	log   *slog.Logger
	rings map[string]*Ring
	// lastSeen drives eviction: the least recently updated coin is dropped when
	// the cap is reached.
	lastSeen map[string]time.Time
	pinned   map[string]bool
}

func NewStore(cfg config.History, paths store.Paths, log *slog.Logger) *Store {
	return &Store{
		cfg: cfg, paths: paths, log: log,
		rings:    make(map[string]*Ring),
		lastSeen: make(map[string]time.Time),
		pinned:   make(map[string]bool),
	}
}

func (s *Store) Enabled() bool { return s.cfg.Enabled && len(s.cfg.Tiers) > 0 }

// Pin marks watchlist coins so eviction never drops the ones being tracked.
func (s *Store) Pin(codes []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinned = make(map[string]bool, len(codes))
	for _, c := range codes {
		s.pinned[c] = true
	}
}

func (s *Store) ring(code string, now time.Time) *Ring {
	if r, ok := s.rings[code]; ok {
		s.lastSeen[code] = now
		return r
	}
	path, err := s.paths.HistoryFile(code)
	if err != nil {
		s.log.Warn("skipping history for coin with unusable code", "code", code, "err", err)
		return nil
	}
	r, err := Load(code, path, s.cfg.Tiers)
	if err != nil {
		// Geometry change or corruption: start fresh rather than refuse to record.
		s.log.Warn("starting history fresh", "code", code, "err", err)
	}
	if r == nil {
		return nil
	}
	s.evictIfNeeded(now)
	s.rings[code] = r
	s.lastSeen[code] = now
	return r
}

func (s *Store) evictIfNeeded(now time.Time) {
	if s.cfg.MaxCoins <= 0 || len(s.rings) < s.cfg.MaxCoins {
		return
	}
	var victim string
	var oldest time.Time
	for code := range s.rings {
		if s.pinned[code] {
			continue
		}
		seen := s.lastSeen[code]
		if victim == "" || seen.Before(oldest) {
			victim, oldest = code, seen
		}
	}
	if victim == "" {
		return
	}
	if r := s.rings[victim]; r != nil {
		if err := r.Save(); err != nil {
			s.log.Warn("saving evicted history failed", "code", victim, "err", err)
		}
	}
	delete(s.rings, victim)
	delete(s.lastSeen, victim)
}

// Record adds one sample per coin. Called after every successful poll, so it
// must be cheap and must never block on I/O.
func (s *Store) Record(samples map[string]Sample, now time.Time) {
	if !s.Enabled() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for code, sample := range samples {
		if r := s.ring(code, now); r != nil {
			r.Add(sample)
		}
	}
}

// Range serves the detail chart. An empty result means the caller should fall
// back to the API.
func (s *Store) Range(code string, start, end time.Time, maxPoints int) []Point {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	r := s.ring(code, end)
	s.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.Range(start, end, maxPoints)
}

func (s *Store) Coverage(code string) (oldest, newest time.Time, count int) {
	s.mu.Lock()
	r, ok := s.rings[code]
	s.mu.Unlock()
	if !ok {
		return time.Time{}, time.Time{}, 0
	}
	return r.Coverage()
}

// Flush writes every dirty ring. Errors are logged, not returned: losing a
// sample must not take down the dashboard.
func (s *Store) Flush() (written int) {
	if !s.Enabled() {
		return 0
	}
	s.mu.Lock()
	rings := make([]*Ring, 0, len(s.rings))
	for _, r := range s.rings {
		if r.Dirty() {
			rings = append(rings, r)
		}
	}
	s.mu.Unlock()

	sort.Slice(rings, func(i, j int) bool { return rings[i].code < rings[j].code })
	for _, r := range rings {
		if err := r.Save(); err != nil {
			s.log.Warn("history save failed", "code", r.Code(), "err", err)
			continue
		}
		written++
	}
	return written
}

// Stats describes the archive for the status payload and the README's claim
// that the footprint is constant.
type Stats struct {
	Coins        int   `json:"coins"`
	BytesOnDisk  int64 `json:"bytesOnDisk"`
	BytesPerCoin int64 `json:"bytesPerCoin"`
	MaxCoins     int   `json:"maxCoins"`
	MaxBytes     int64 `json:"maxBytes"`
}

func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{
		Coins:        len(s.rings),
		BytesPerCoin: s.cfg.BytesPerCoin(),
		MaxCoins:     s.cfg.MaxCoins,
		MaxBytes:     s.cfg.TotalBytes(),
	}
	if entries, err := os.ReadDir(s.paths.HistoryDir()); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				st.BytesOnDisk += info.Size()
			}
		}
	}
	return st
}
