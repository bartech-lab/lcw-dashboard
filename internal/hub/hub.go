// Package hub fans server-sent events out to connected browsers.
package hub

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

type EventType string

const (
	EventHello     EventType = "hello"
	EventCoins     EventType = "coins"
	EventOverview  EventType = "overview"
	EventStatus    EventType = "status"
	EventCredits   EventType = "credits"
	EventAlert     EventType = "alert"
	EventWatchlist EventType = "watchlist"
	EventFiats     EventType = "fiats"
	EventBye       EventType = "bye"
)

// coalescable events are last-write-wins: a slow client only needs the newest
// price table, not every one it missed. Alerts are never coalesced.
func (t EventType) coalescable() bool {
	switch t {
	case EventCoins, EventOverview, EventStatus, EventCredits, EventWatchlist, EventFiats:
		return true
	}
	return false
}

type Event struct {
	ID   uint64
	Type EventType
	Data []byte
	// Key scopes an event to one view key so a client only receives the table it
	// asked for. Empty means broadcast to everyone.
	Key string
}

// Encode writes the SSE frame.
func (e Event) Encode() []byte {
	return []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, e.Data))
}

const (
	clientBuffer  = 32
	maxHardDrops  = 3
	replayHistory = 20
)

type client struct {
	id  string
	ch  chan Event
	mu  sync.Mutex
	key string
	// pending holds the newest coalescable event per type, so a stalled client
	// costs bounded memory instead of an unbounded queue.
	pending   map[EventType]Event
	hardDrops int
	closed    bool
}

type Hub struct {
	mu      sync.RWMutex
	clients map[string]*client
	nextID  atomic.Uint64

	// latest is replayed to a new connection at zero upstream cost.
	latest map[EventType]map[string]Event
	alerts []Event
}

func New() *Hub {
	return &Hub{
		clients: make(map[string]*client),
		latest:  make(map[EventType]map[string]Event),
	}
}

// Register returns the channel a connection reads, plus the replay backlog.
func (h *Hub) Register(id, viewKey string) (<-chan Event, []Event) {
	c := &client{
		id:      id,
		ch:      make(chan Event, clientBuffer),
		key:     viewKey,
		pending: make(map[EventType]Event),
	}
	h.mu.Lock()
	h.clients[id] = c
	replay := h.backlogLocked(viewKey)
	h.mu.Unlock()
	return c.ch, replay
}

// backlogLocked collects current state plus recent alerts.
func (h *Hub) backlogLocked(viewKey string) []Event {
	order := []EventType{EventStatus, EventCredits, EventWatchlist, EventFiats, EventOverview, EventCoins}
	out := make([]Event, 0, len(order)+len(h.alerts))
	for _, t := range order {
		byKey, ok := h.latest[t]
		if !ok {
			continue
		}
		for key, ev := range byKey {
			if key != "" && viewKey != "" && key != viewKey {
				continue
			}
			out = append(out, ev)
		}
	}
	out = append(out, h.alerts...)
	return out
}

// SetViewKey changes which scoped events a client receives.
func (h *Hub) SetViewKey(id, key string) {
	h.mu.RLock()
	c := h.clients[id]
	h.mu.RUnlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	c.key = key
	c.mu.Unlock()
}

func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	c := h.clients[id]
	delete(h.clients, id)
	h.mu.Unlock()

	if c != nil {
		c.mu.Lock()
		if !c.closed {
			c.closed = true
			close(c.ch)
		}
		c.mu.Unlock()
	}
}

func (h *Hub) Clients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast marshals v and sends it. A marshal failure is returned rather than
// silently dropped, because a broken payload is a bug worth logging.
func (h *Hub) Broadcast(t EventType, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", t, err)
	}
	ev := Event{ID: h.nextID.Add(1), Type: t, Data: data, Key: key}

	h.mu.Lock()
	if t == EventAlert {
		h.alerts = append(h.alerts, ev)
		if len(h.alerts) > replayHistory {
			h.alerts = h.alerts[len(h.alerts)-replayHistory:]
		}
	} else if t != EventHello && t != EventBye {
		if h.latest[t] == nil {
			h.latest[t] = make(map[string]Event)
		}
		h.latest[t][key] = ev
	}
	targets := make([]*client, 0, len(h.clients))
	for _, c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	for _, c := range targets {
		h.send(c, ev)
	}
	return nil
}

// SendTo delivers to one client, used for the hello frame.
func (h *Hub) SendTo(id string, t EventType, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", t, err)
	}
	h.mu.RLock()
	c := h.clients[id]
	h.mu.RUnlock()
	if c == nil {
		return nil
	}
	h.send(c, Event{ID: h.nextID.Add(1), Type: t, Data: data})
	return nil
}

// send holds the client lock across the channel send. The send must not happen
// outside it: Unregister closes the channel under the same lock, and sending on
// a closed channel panics. The send is non-blocking, so holding the lock cannot
// deadlock.
func (h *Hub) send(c *client, ev Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if ev.Key != "" && c.key != "" && ev.Key != c.key {
		c.mu.Unlock()
		return
	}

	select {
	case c.ch <- ev:
		c.mu.Unlock()
		return
	default:
	}

	// Buffer full. A coalescable event replaces the client's pending copy; an
	// alert is never dropped, so a full buffer counts as a hard drop and three
	// of those close the connection. The browser reconnects and replays.
	if ev.Type.coalescable() {
		c.pending[ev.Type] = ev
		c.mu.Unlock()
		h.drainPending(c)
		return
	}
	c.hardDrops++
	drops := c.hardDrops
	c.mu.Unlock()

	if drops >= maxHardDrops {
		h.Unregister(c.id)
	}
}

// drainPending moves coalesced events into the channel as space appears.
func (h *Hub) drainPending(c *client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	for t, ev := range c.pending {
		select {
		case c.ch <- ev:
			delete(c.pending, t)
		default:
			return
		}
	}
}

// Drain is called by the connection goroutine after each write, so coalesced
// events reach a client that has caught up.
func (h *Hub) Drain(id string) {
	h.mu.RLock()
	c := h.clients[id]
	h.mu.RUnlock()
	if c != nil {
		h.drainPending(c)
	}
}
