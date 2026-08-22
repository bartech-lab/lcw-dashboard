package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func drainAll(ch <-chan Event, d time.Duration) []Event {
	var out []Event
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			return out
		default:
			return out
		}
	}
}

func TestBroadcastReachesEveryClient(t *testing.T) {
	h := New()
	a, _ := h.Register("a", "")
	b, _ := h.Register("b", "")

	if err := h.Broadcast(EventStatus, "", map[string]string{"pollState": "active"}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]<-chan Event{"a": a, "b": b} {
		evs := drainAll(ch, time.Second)
		if len(evs) != 1 || evs[0].Type != EventStatus {
			t.Errorf("client %s got %+v", name, evs)
		}
	}
	if h.Clients() != 2 {
		t.Errorf("Clients = %d, want 2", h.Clients())
	}
}

// A client only receives the table it asked for, so several open views do not
// each pay for the others.
func TestViewKeyFiltersScopedEvents(t *testing.T) {
	h := New()
	top, _ := h.Register("top", "top|USD")
	fav, _ := h.Register("fav", "favourites|USD")

	if err := h.Broadcast(EventCoins, "top|USD", map[string]string{"view": "top"}); err != nil {
		t.Fatal(err)
	}
	if got := len(drainAll(top, time.Second)); got != 1 {
		t.Errorf("the matching client got %d events, want 1", got)
	}
	if got := len(drainAll(fav, time.Second)); got != 0 {
		t.Errorf("the other client got %d events, want 0", got)
	}

	// Unscoped events still reach everyone.
	if err := h.Broadcast(EventStatus, "", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if got := len(drainAll(fav, time.Second)); got != 1 {
		t.Errorf("unscoped event reached %d, want 1", got)
	}
}

func TestSetViewKeySwitchesSubscription(t *testing.T) {
	h := New()
	ch, _ := h.Register("c", "top|USD")

	h.SetViewKey("c", "favourites|USD")
	if err := h.Broadcast(EventCoins, "top|USD", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if got := len(drainAll(ch, time.Second)); got != 0 {
		t.Errorf("got %d events for the old key, want 0", got)
	}
	if err := h.Broadcast(EventCoins, "favourites|USD", map[string]int{"x": 2}); err != nil {
		t.Fatal(err)
	}
	if got := len(drainAll(ch, time.Second)); got != 1 {
		t.Errorf("got %d events for the new key, want 1", got)
	}
}

// A new connection is painted from cached state, which costs no credits.
func TestReplayGivesCurrentStateOnConnect(t *testing.T) {
	h := New()
	if err := h.Broadcast(EventStatus, "", map[string]string{"pollState": "active"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Broadcast(EventCoins, "top|USD", map[string]string{"view": "top"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Broadcast(EventCredits, "", map[string]int{"localSpend": 12}); err != nil {
		t.Fatal(err)
	}

	_, replay := h.Register("late", "top|USD")

	seen := map[EventType]bool{}
	for _, ev := range replay {
		seen[ev.Type] = true
	}
	for _, want := range []EventType{EventStatus, EventCoins, EventCredits} {
		if !seen[want] {
			t.Errorf("replay is missing %s: %+v", want, replay)
		}
	}
}

func TestReplayRespectsTheViewKey(t *testing.T) {
	h := New()
	h.Broadcast(EventCoins, "top|USD", map[string]int{"x": 1})
	h.Broadcast(EventCoins, "favourites|USD", map[string]int{"x": 2})

	_, replay := h.Register("late", "favourites|USD")
	for _, ev := range replay {
		if ev.Type == EventCoins && ev.Key != "favourites|USD" {
			t.Errorf("replayed a table for the wrong key: %s", ev.Key)
		}
	}
}

// Only the newest table matters to a client catching up, so coalescable events
// replace rather than queue.
func TestCoalescableEventsReplaceWhenTheBufferIsFull(t *testing.T) {
	h := New()
	ch, _ := h.Register("slow", "")

	for i := 0; i < clientBuffer+50; i++ {
		if err := h.Broadcast(EventCoins, "", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}

	// The client is still connected and its buffer is bounded.
	if h.Clients() != 1 {
		t.Fatalf("client was dropped; coalescable overflow should not disconnect")
	}
	evs := drainAll(ch, time.Second)
	if len(evs) > clientBuffer+1 {
		t.Errorf("buffered %d events, want at most %d", len(evs), clientBuffer+1)
	}

	// Whatever it does read, the last one is the newest.
	var last map[string]int
	if err := json.Unmarshal(evs[len(evs)-1].Data, &last); err != nil {
		t.Fatal(err)
	}
	if last["n"] == 0 {
		t.Error("the client is reading the oldest event; coalescing should favour the newest")
	}
}

// An alert must never be silently dropped, so overflow disconnects the client
// and lets the browser reconnect and replay.
func TestAlertOverflowDisconnectsRatherThanDropping(t *testing.T) {
	h := New()
	h.Register("slow", "")

	for i := 0; i < clientBuffer+maxHardDrops+5; i++ {
		if err := h.Broadcast(EventAlert, "", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if h.Clients() != 0 {
		t.Error("a client that cannot keep up with alerts should be disconnected")
	}
}

func TestAlertsAreReplayedAndBounded(t *testing.T) {
	h := New()
	for i := 0; i < replayHistory+10; i++ {
		if err := h.Broadcast(EventAlert, "", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	_, replay := h.Register("late", "")

	alerts := 0
	for _, ev := range replay {
		if ev.Type == EventAlert {
			alerts++
		}
	}
	if alerts != replayHistory {
		t.Errorf("replayed %d alerts, want the %d most recent", alerts, replayHistory)
	}
}

func TestUnregisterClosesTheChannelOnce(t *testing.T) {
	h := New()
	ch, _ := h.Register("c", "")

	h.Unregister("c")
	h.Unregister("c") // must not panic on a double close

	if _, open := <-ch; open {
		t.Error("channel should be closed")
	}
	if h.Clients() != 0 {
		t.Errorf("Clients = %d, want 0", h.Clients())
	}
}

func TestSendToTargetsOneClient(t *testing.T) {
	h := New()
	a, _ := h.Register("a", "")
	b, _ := h.Register("b", "")

	if err := h.SendTo("a", EventHello, map[string]string{"clientId": "a"}); err != nil {
		t.Fatal(err)
	}
	if got := len(drainAll(a, time.Second)); got != 1 {
		t.Errorf("target got %d events, want 1", got)
	}
	if got := len(drainAll(b, time.Second)); got != 0 {
		t.Errorf("other client got %d events, want 0", got)
	}
}

func TestSendToUnknownClientIsANoOp(t *testing.T) {
	h := New()
	if err := h.SendTo("ghost", EventHello, map[string]int{}); err != nil {
		t.Errorf("sending to a departed client should not error: %v", err)
	}
}

// hello and bye are per-connection, so caching them would replay a stale
// identity to the next client.
func TestHelloAndByeAreNotCachedForReplay(t *testing.T) {
	h := New()
	h.Broadcast(EventHello, "", map[string]string{"clientId": "old"})
	h.Broadcast(EventBye, "", map[string]string{"reason": "shutdown"})

	_, replay := h.Register("late", "")
	for _, ev := range replay {
		if ev.Type == EventHello || ev.Type == EventBye {
			t.Errorf("replayed a %s frame", ev.Type)
		}
	}
}

func TestEncodeProducesAValidSSEFrame(t *testing.T) {
	ev := Event{ID: 7, Type: EventCoins, Data: []byte(`{"view":"top"}`)}
	got := string(ev.Encode())

	for _, want := range []string{"id: 7\n", "event: coins\n", `data: {"view":"top"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("frame is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Error("an SSE frame must end with a blank line")
	}
}

func TestEventIDsIncrease(t *testing.T) {
	h := New()
	ch, _ := h.Register("c", "")
	for i := 0; i < 5; i++ {
		h.Broadcast(EventStatus, "", map[string]int{"n": i})
	}
	evs := drainAll(ch, time.Second)
	for i := 1; i < len(evs); i++ {
		if evs[i].ID <= evs[i-1].ID {
			t.Fatalf("ids not increasing: %d then %d", evs[i-1].ID, evs[i].ID)
		}
	}
}

func TestBroadcastReportsAMarshalFailure(t *testing.T) {
	h := New()
	// A channel cannot be marshalled; a broken payload is a bug worth surfacing.
	if err := h.Broadcast(EventStatus, "", make(chan int)); err == nil {
		t.Error("want a marshal error")
	}
}

func TestConcurrentBroadcastAndRegister(t *testing.T) {
	h := New()
	done := make(chan struct{})

	go func() {
		for i := 0; i < 200; i++ {
			h.Broadcast(EventStatus, "", map[string]int{"n": i})
		}
		close(done)
	}()
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("c%d", i)
		h.Register(id, "")
		h.Drain(id)
		h.Unregister(id)
	}
	<-done
}
