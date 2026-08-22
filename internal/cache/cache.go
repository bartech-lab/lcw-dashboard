// Package cache holds TTL and LRU caches for on-demand data.
package cache

import (
	"container/list"
	"sync"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
)

// Entry reports its own age so callers can serve stale data with an honest
// timestamp rather than blanking the screen.
type Entry[T any] struct {
	Value    T
	StoredAt time.Time
	Fresh    bool
	Age      time.Duration
}

// LRU is a bounded cache with a TTL. A stale hit still returns its value with
// Fresh false, because showing old data beats showing nothing.
type LRU[T any] struct {
	mu    sync.Mutex
	clk   clock.Clock
	ttl   time.Duration
	max   int
	ll    *list.List
	items map[string]*list.Element
}

type node[T any] struct {
	key      string
	value    T
	storedAt time.Time
}

func NewLRU[T any](clk clock.Clock, ttl time.Duration, max int) *LRU[T] {
	if max < 1 {
		max = 1
	}
	return &LRU[T]{
		clk: clk, ttl: ttl, max: max,
		ll:    list.New(),
		items: make(map[string]*list.Element, max),
	}
}

func (c *LRU[T]) Get(key string) (Entry[T], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return Entry[T]{}, false
	}
	c.ll.MoveToFront(el)
	n := el.Value.(*node[T])
	age := c.clk.Since(n.storedAt)
	return Entry[T]{
		Value:    n.value,
		StoredAt: n.storedAt,
		// Age exactly equal to the TTL counts as stale.
		Fresh: age < c.ttl,
		Age:   age,
	}, true
}

func (c *LRU[T]) Put(key string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		n := el.Value.(*node[T])
		n.value = v
		n.storedAt = c.clk.Now()
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&node[T]{key: key, value: v, storedAt: c.clk.Now()})
	c.items[key] = el

	for c.ll.Len() > c.max {
		oldest := c.ll.Back()
		if oldest == nil {
			break
		}
		c.ll.Remove(oldest)
		delete(c.items, oldest.Value.(*node[T]).key)
	}
}

func (c *LRU[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

func (c *LRU[T]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[string]*list.Element, c.max)
}

// SetTTL doubles cache lifetimes when the budget tightens, so a conserving
// server reuses more.
func (c *LRU[T]) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}
