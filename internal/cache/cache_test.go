package cache

import (
	"testing"
	"time"

	"github.com/bartech/lcw-dashboard/internal/clock"
)

var now = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

func TestMissAndHit(t *testing.T) {
	c := NewLRU[string](clock.NewFake(now), time.Minute, 10)

	if _, ok := c.Get("a"); ok {
		t.Error("empty cache should miss")
	}
	c.Put("a", "one")
	e, ok := c.Get("a")
	if !ok || e.Value != "one" || !e.Fresh {
		t.Errorf("got %+v, ok=%v", e, ok)
	}
}

// A stale hit still returns its value: showing old data beats showing nothing.
func TestStaleEntryStillReturnsItsValue(t *testing.T) {
	clk := clock.NewFake(now)
	c := NewLRU[string](clk, time.Minute, 10)
	c.Put("a", "one")

	clk.Advance(2 * time.Minute)
	e, ok := c.Get("a")
	if !ok {
		t.Fatal("a stale entry must still be found")
	}
	if e.Fresh {
		t.Error("Fresh should be false")
	}
	if e.Value != "one" {
		t.Errorf("Value = %q, want the stale value", e.Value)
	}
	if e.Age != 2*time.Minute {
		t.Errorf("Age = %s, want 2m", e.Age)
	}
}

func TestAgeExactlyAtTTLCountsAsStale(t *testing.T) {
	clk := clock.NewFake(now)
	c := NewLRU[int](clk, time.Minute, 10)
	c.Put("a", 1)

	clk.Advance(time.Minute)
	if e, _ := c.Get("a"); e.Fresh {
		t.Error("age == TTL should be stale, not fresh")
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewLRU[int](clock.NewFake(now), time.Hour, 2)
	c.Put("a", 1)
	c.Put("b", 2)
	// Touching a makes b the least recently used.
	c.Get("a")
	c.Put("c", 3)

	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was touched and should survive")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c was just added")
	}
	if got := c.Len(); got != 2 {
		t.Errorf("Len = %d, want 2", got)
	}
}

func TestPutReplacesAndRefreshes(t *testing.T) {
	clk := clock.NewFake(now)
	c := NewLRU[int](clk, time.Minute, 10)
	c.Put("a", 1)
	clk.Advance(2 * time.Minute)
	c.Put("a", 2)

	e, _ := c.Get("a")
	if e.Value != 2 {
		t.Errorf("Value = %d, want 2", e.Value)
	}
	if !e.Fresh {
		t.Error("a replaced entry should be fresh again")
	}
	if got := c.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}
}

func TestSizeOneIsUsable(t *testing.T) {
	c := NewLRU[int](clock.NewFake(now), time.Hour, 0)
	c.Put("a", 1)
	c.Put("b", 2)
	if got := c.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("the newest entry should be present")
	}
}

// The budget guard doubles TTLs when conserving, so a tighter budget reuses more.
func TestSetTTLAffectsFreshness(t *testing.T) {
	clk := clock.NewFake(now)
	c := NewLRU[int](clk, time.Minute, 10)
	c.Put("a", 1)
	clk.Advance(90 * time.Second)

	if e, _ := c.Get("a"); e.Fresh {
		t.Fatal("should be stale at 90s with a 1m TTL")
	}
	c.SetTTL(2 * time.Minute)
	if e, _ := c.Get("a"); !e.Fresh {
		t.Error("should be fresh again after the TTL doubled")
	}
}

func TestPurge(t *testing.T) {
	c := NewLRU[int](clock.NewFake(now), time.Hour, 10)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Purge()

	if got := c.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
	if _, ok := c.Get("a"); ok {
		t.Error("a should be gone")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := NewLRU[int](clock.Real{}, time.Minute, 50)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			c.Put(string(rune('a'+i%26)), i)
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		c.Get(string(rune('a' + i%26)))
		c.Len()
	}
	<-done
}
