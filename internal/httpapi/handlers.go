package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
)

// Pointers throughout: a control message is a patch, so an absent field must
// leave the current value alone rather than reset it to a default.
type controlRequest struct {
	ClientID string  `json:"clientId"`
	Visible  *bool   `json:"visible"`
	View     *string `json:"view"`
	// No Currency field: there is one currency and the server owns it.
	// Sort and Order arrive only when the client asks for a market-wide page.
	Sort   *string `json:"sort"`
	Order  *string `json:"order"`
	Offset *int    `json:"offset"`
}

// handleControl is the single client-to-server channel: visibility, view and
// currency. The reply carries the revision so the UI knows the change landed
// rather than guessing.
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	var req controlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body", err.Error())
		return
	}
	if req.ClientID == "" {
		writeError(w, http.StatusBadRequest, "clientId required", "")
		return
	}
	// Start from what the client already has, so a heartbeat that carries only
	// visibility cannot discard the view the user chose.
	view, currency, sortField, order, offset := s.ctrl.ClientState(req.ClientID)
	if view == "" {
		view = snapshot.View(s.cfg.Coins.DefaultView)
	}
	if currency == "" {
		currency = s.cfg.Currency.Default
	}
	if !sortField.Valid() {
		sortField = s.cfg.Coins.Sort
	}
	if !order.Valid() {
		order = s.cfg.Coins.Order
	}
	if req.View != nil {
		view = snapshot.View(*req.View)
	}
	// One currency, owned by the server, so nothing the client sends can change
	// it.
	currency = s.cfg.Currency.Default
	if req.Offset != nil {
		offset = *req.Offset
		if offset < 0 {
			offset = 0
		}
	}
	if req.Sort != nil {
		next := lcw.SortField(*req.Sort)
		if !next.Valid() {
			// The API cannot sort by a percentage change, so this is the one
			// place that has to say no rather than pass it through.
			writeError(w, http.StatusBadRequest, "unsupported sort field", *req.Sort)
			return
		}
		sortField = next
	}
	if req.Order != nil {
		next := lcw.SortOrder(*req.Order)
		if !next.Valid() {
			writeError(w, http.StatusBadRequest, "unknown sort order", *req.Order)
			return
		}
		order = next
	}
	if view != snapshot.ViewTop && view != snapshot.ViewFavourites {
		writeError(w, http.StatusBadRequest, "unknown view", string(view))
		return
	}
	visible := true
	if req.Visible != nil {
		visible = *req.Visible
	}

	s.hub.SetViewKey(req.ClientID, s.viewKey(view, currency, sortField, order, offset))
	reply := s.ctrl.Presence(req.ClientID, view, currency, sortField, order, offset, visible)
	writeJSON(w, http.StatusOK, reply)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		What string `json:"what"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.What == "" {
		req.What = "coins"
	}
	reply := s.ctrl.Refresh(req.What)
	status := http.StatusOK
	if !reply.Accepted {
		status = http.StatusTooManyRequests
	}
	writeJSON(w, status, reply)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	world := s.world.Load()
	writeJSON(w, http.StatusOK, map[string]any{
		"coins":     world.Coins,
		"overview":  world.Overview,
		"status":    world.Status,
		"credits":   world.Credits,
		"watchlist": world.Watch,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.world.Load().Status
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"version":       s.version,
		"pollState":     st.PollState,
		"lastSuccessAt": st.LastSuccessAt,
		"budgetState":   string(s.guard.State()),
	})
}

// handleConfig serves the effective config. The key is never in it: Redacted
// clears the only field that holds it, and a test asserts it never appears.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"config":    s.cfg.Redacted(),
		"hasApiKey": s.cfg.HasAPIKey(),
		"spend":     s.cfg.ProjectSpend(),
		"history":   s.hist.Stats(),
	})
}

func (s *Server) handleUpstreamStatus(w http.ResponseWriter, r *http.Request) {
	// /status needs no key and costs no credit.
	err := s.client.Status(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"up":    err == nil,
		"error": errString(err),
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	st := s.index.Status()
	if !st.Ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"indexReady": false, "building": st.Building, "results": []any{},
		})
		return
	}
	results := s.index.Search(q)
	out := make([]map[string]any, 0, len(results))
	for _, res := range results {
		out = append(out, map[string]any{
			"code": res.Code, "name": res.Name, "symbol": res.Symbol,
			"rank": res.Rank, "png32": res.PNG32, "score": res.Score,
			"inWatchlist": s.watch.Contains(res.Code),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"indexReady": true, "indexCoins": st.Coins,
		"builtAt": st.BuiltAt, "results": out,
	})
}

func (s *Server) handleWatchlistGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.watch.Snapshot())
}

func (s *Server) handleWatchlistPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Codes []string `json:"codes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body", err.Error())
		return
	}
	changed, err := s.watch.Set(req.Codes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, watchlist.ErrTooMany) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, "watchlist rejected", err.Error())
		return
	}
	s.afterWatchlistChange(changed)
	writeJSON(w, http.StatusOK, s.watch.Snapshot())
}

func (s *Server) handleWatchlistToggle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body", err.Error())
		return
	}
	added, err := s.watch.Toggle(req.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, "toggle rejected", err.Error())
		return
	}
	s.afterWatchlistChange(true)
	writeJSON(w, http.StatusOK, map[string]any{
		"added": added, "watchlist": s.watch.Snapshot(),
	})
}

func (s *Server) afterWatchlistChange(changed bool) {
	if !changed {
		return
	}
	if err := s.watch.Save(); err != nil {
		s.log.Warn("saving watchlist failed", "err", err)
	}
	s.ctrl.WatchlistChanged()
}

func (s *Server) handleAlertsGet(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.cfg.Alerts.Enabled,
		"sinks":   s.cfg.Alerts.Sinks,
		"rules":   s.engine.Statuses(),
		"events":  s.engine.Events(),
	})
}

func (s *Server) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusNotFound, "alerts disabled", "")
		return
	}
	s.engine.Ack(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAlertEnabled(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusNotFound, "alerts disabled", "")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body", err.Error())
		return
	}
	s.engine.SetEnabled(r.PathValue("id"), req.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
