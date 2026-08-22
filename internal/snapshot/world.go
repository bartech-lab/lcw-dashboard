package snapshot

import (
	"sync/atomic"
)

// World is an immutable view of everything the HTTP layer serves. Readers load
// the pointer and read the struct without locking; the controller publishes a
// new World by copying and swapping the changed field.
type World struct {
	// Coins is keyed by view key, so switching back to a recently used view
	// paints instantly from cache with an honest age and costs no credits.
	Coins    map[string]*Coins
	Overview map[string]*Overview
	Status   *Status
	Credits  *Credits
	Watch    *Watchlist
	Fiats    *Fiats
}

// Holder publishes World values. It is safe for concurrent readers.
type Holder struct {
	p atomic.Pointer[World]
}

func NewHolder() *Holder {
	h := &Holder{}
	h.p.Store(&World{
		Coins:    map[string]*Coins{},
		Overview: map[string]*Overview{},
		Status:   &Status{PollState: PollInitializing},
		Credits:  &Credits{},
		Watch:    &Watchlist{Codes: []string{}},
		Fiats:    &Fiats{Fiats: []Fiat{}},
	})
	return h
}

func (h *Holder) Load() *World { return h.p.Load() }

// clone shallow-copies the maps so a published World is never mutated in place.
func (w *World) clone() *World {
	n := &World{
		Coins:    make(map[string]*Coins, len(w.Coins)),
		Overview: make(map[string]*Overview, len(w.Overview)),
		Status:   w.Status,
		Credits:  w.Credits,
		Watch:    w.Watch,
		Fiats:    w.Fiats,
	}
	for k, v := range w.Coins {
		n.Coins[k] = v
	}
	for k, v := range w.Overview {
		n.Overview[k] = v
	}
	return n
}

// Update applies mutate to a copy and publishes it. Only the controller
// goroutine calls this, so no compare-and-swap loop is needed.
func (h *Holder) Update(mutate func(*World)) *World {
	next := h.p.Load().clone()
	mutate(next)
	h.p.Store(next)
	return next
}

func (h *Holder) SetCoins(key string, c *Coins) *World {
	return h.Update(func(w *World) { w.Coins[key] = c })
}

func (h *Holder) SetOverview(currency string, o *Overview) *World {
	return h.Update(func(w *World) { w.Overview[currency] = o })
}

func (h *Holder) SetStatus(s *Status) *World {
	return h.Update(func(w *World) { w.Status = s })
}

func (h *Holder) SetCredits(c *Credits) *World {
	return h.Update(func(w *World) { w.Credits = c })
}

func (h *Holder) SetWatchlist(wl *Watchlist) *World {
	return h.Update(func(w *World) { w.Watch = wl })
}

func (h *Holder) SetFiats(f *Fiats) *World {
	return h.Update(func(w *World) { w.Fiats = f })
}
