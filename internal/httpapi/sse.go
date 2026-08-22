package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
)

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "client_id required", "")
		return
	}
	q := r.URL.Query()
	view := snapshot.View(orDefault(q.Get("view"), s.cfg.Coins.DefaultView))
	currency := orDefault(q.Get("currency"), s.cfg.Currency.Default)
	visible := q.Get("visible") != "0"

	sortField := lcw.SortField(orDefault(q.Get("sort"), string(s.cfg.Coins.Sort)))
	if !sortField.Valid() {
		sortField = s.cfg.Coins.Sort
	}
	order := lcw.SortOrder(orDefault(q.Get("order"), string(s.cfg.Coins.Order)))
	if !order.Valid() {
		order = s.cfg.Coins.Order
	}

	rc := http.NewResponseController(w)
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("connection", "keep-alive")
	// Proxies buffer event streams by default, which would defeat the point.
	w.Header().Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	key := s.viewKey(view, currency, sortField, order)
	events, replay := s.hub.Register(clientID, key)
	defer s.hub.Unregister(clientID)

	s.ctrl.Presence(clientID, view, currency, sortField, order, visible)
	defer s.ctrl.Disconnect(clientID)

	// hello is written directly rather than queued, so it always precedes the
	// replayed data frames. The client needs the config before the first table.
	if body, err := json.Marshal(s.ctrl.Hello(clientID, s.version)); err == nil {
		ev := hub.Event{ID: 0, Type: hub.EventHello, Data: body}
		if _, err := w.Write(ev.Encode()); err != nil {
			return
		}
	} else {
		s.log.Warn("hello marshal failed", "err", err)
	}

	lastSeen := lastEventID(r)
	for _, ev := range replay {
		if ev.ID <= lastSeen {
			continue
		}
		if _, err := w.Write(ev.Encode()); err != nil {
			return
		}
	}
	if err := rc.Flush(); err != nil {
		return
	}

	// A heartbeat is mandatory: without it a dead-but-open connection is
	// indistinguishable from a quiet market, and EventSource never fires error.
	beat := s.clk.NewTimer(s.cfg.Poll.SSEHeartbeat.D())
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case ev, ok := <-events:
			if !ok {
				return
			}
			if _, err := w.Write(ev.Encode()); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			// Let coalesced events through now that the client has caught up.
			s.hub.Drain(clientID)

		case <-beat.C():
			if _, err := fmt.Fprint(w, ":hb\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			beat.Reset(s.cfg.Poll.SSEHeartbeat.D())
		}
	}
}

// viewKey must match scheduler.ViewKey.String exactly, or a client subscribes to
// a key the controller never publishes.
func (s *Server) viewKey(view snapshot.View, currency string,
	sortField lcw.SortField, order lcw.SortOrder) string {

	hash := ""
	if view == snapshot.ViewFavourites {
		hash = s.watch.Hash()
	}
	return string(view) + "|" + currency + "|" + hash +
		"|" + string(sortField) + "|" + string(order)
}

func lastEventID(r *http.Request) uint64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
