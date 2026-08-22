package snapshot

import (
	"fmt"
	"sync"
	"testing"
)

// Bootstrap and the on-demand handlers publish from their own goroutines, so a
// plain load-clone-store loses whichever write landed second.
func TestConcurrentUpdatesDoNotLoseWrites(t *testing.T) {
	h := NewHolder()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			h.SetStatus(&Status{PollState: PollActive, Revision: uint64(i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			h.SetCoins(fmt.Sprintf("key%d", i), &Coins{View: ViewTop})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			h.SetFiats(&Fiats{Fiats: []Fiat{{Code: "USD"}, {Code: "EUR"}}})
		}
	}()
	wg.Wait()

	w := h.Load()
	if w.Fiats == nil || len(w.Fiats.Fiats) != 2 {
		t.Errorf("fiats lost: %+v", w.Fiats)
	}
	if w.Status == nil || w.Status.PollState != PollActive {
		t.Errorf("status lost: %+v", w.Status)
	}
	if len(w.Coins) != n {
		t.Errorf("kept %d coin tables, want %d", len(w.Coins), n)
	}
}

func TestUpdateIsCopyOnWrite(t *testing.T) {
	h := NewHolder()
	h.SetCoins("a", &Coins{View: ViewTop})
	before := h.Load()

	h.SetCoins("b", &Coins{View: ViewFavourites})

	if _, ok := before.Coins["b"]; ok {
		t.Error("a published World was mutated in place")
	}
	if _, ok := h.Load().Coins["a"]; !ok {
		t.Error("the earlier entry was dropped")
	}
}
