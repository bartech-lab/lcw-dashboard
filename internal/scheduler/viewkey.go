package scheduler

import (
	"sort"
	"time"

	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

// ViewKey identifies one distinct thing to poll. Two tabs sharing a key share a
// fetch; two tabs with different keys share the poll loop by rotation, so the
// credit rate stays constant no matter how many tabs are open.
type ViewKey struct {
	View      snapshot.View
	Currency  string
	WatchHash string
}

func (k ViewKey) String() string {
	return string(k.View) + "|" + k.Currency + "|" + k.WatchHash
}

// Client is one browser tab, as the controller sees it.
type Client struct {
	ID       string
	View     snapshot.View
	Currency string
	Visible  bool
	LastSeen time.Time
	// ActivatedAt breaks ties for which key gets priority.
	ActivatedAt time.Time
}

// presence is controller-owned, so it needs no lock.
type presence struct {
	clients map[string]*Client
	ttl     time.Duration
}

func newPresence(ttl time.Duration) *presence {
	return &presence{clients: make(map[string]*Client), ttl: ttl}
}

func (p *presence) upsert(id string, view snapshot.View, currency string, visible bool, now time.Time) (changed bool) {
	c, ok := p.clients[id]
	if !ok {
		p.clients[id] = &Client{
			ID: id, View: view, Currency: currency, Visible: visible,
			LastSeen: now, ActivatedAt: now,
		}
		return true
	}
	if c.View != view || c.Currency != currency {
		c.ActivatedAt = now
		changed = true
	}
	if c.Visible != visible {
		if visible {
			c.ActivatedAt = now
		}
		changed = true
	}
	c.View, c.Currency, c.Visible, c.LastSeen = view, currency, visible, now
	return changed
}

func (p *presence) remove(id string) bool {
	_, ok := p.clients[id]
	delete(p.clients, id)
	return ok
}

// expire drops clients that stopped reporting. A frozen tab must never hold the
// server at the fast cadence, and a frozen tab cannot announce its own departure.
func (p *presence) expire(now time.Time) (dropped []string) {
	for id, c := range p.clients {
		if now.Sub(c.LastSeen) > p.ttl {
			dropped = append(dropped, id)
			delete(p.clients, id)
		}
	}
	sort.Strings(dropped)
	return dropped
}

func (p *presence) counts() (total, visible int) {
	for _, c := range p.clients {
		total++
		if c.Visible {
			visible++
		}
	}
	return total, visible
}

// keys returns the distinct view keys to poll, most recently activated first,
// capped at max.
//
// Visible clients decide the set. If none are visible, hidden clients still do:
// a backgrounded favourites tab must keep the server on favourites, just at the
// idle interval. Falling back to the config default here would poll a view
// nobody is looking at and leave the returning tab with nothing.
func (p *presence) keys(watchHash string, max int) []ViewKey {
	if keys := p.keysFor(watchHash, max, true); len(keys) > 0 {
		return keys
	}
	return p.keysFor(watchHash, max, false)
}

func (p *presence) keysFor(watchHash string, max int, visibleOnly bool) []ViewKey {
	type entry struct {
		key ViewKey
		at  time.Time
	}
	best := make(map[string]entry)
	for _, c := range p.clients {
		if visibleOnly && !c.Visible {
			continue
		}
		k := ViewKey{View: c.View, Currency: c.Currency}
		if c.View == snapshot.ViewFavourites {
			k.WatchHash = watchHash
		}
		s := k.String()
		if e, ok := best[s]; !ok || c.ActivatedAt.After(e.at) {
			best[s] = entry{key: k, at: c.ActivatedAt}
		}
	}
	out := make([]entry, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].at.Equal(out[j].at) {
			return out[i].at.After(out[j].at)
		}
		return out[i].key.String() < out[j].key.String()
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	keys := make([]ViewKey, 0, len(out))
	for _, e := range out {
		keys = append(keys, e.key)
	}
	return keys
}
