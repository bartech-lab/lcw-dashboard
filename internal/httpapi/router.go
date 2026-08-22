// Package httpapi serves the SSE stream, the REST surface and the embedded UI.
package httpapi

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/cache"
	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/history"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/scheduler"
	"github.com/bartech/lcw-dashboard/internal/searchindex"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
)

type Server struct {
	cfg     config.Config
	clk     clock.Clock
	log     *slog.Logger
	hub     *hub.Hub
	world   *snapshot.Holder
	ctrl    *scheduler.Controller
	watch   *watchlist.List
	index   *searchindex.Index
	guard   *credits.Guard
	client  *lcw.Client
	hist    *history.Store
	engine  *alerts.Engine
	assets  fs.FS
	version string

	detail *cache.LRU[*snapshot.Detail]
}

type Deps struct {
	Cfg     config.Config
	Clk     clock.Clock
	Log     *slog.Logger
	Hub     *hub.Hub
	World   *snapshot.Holder
	Ctrl    *scheduler.Controller
	Watch   *watchlist.List
	Index   *searchindex.Index
	Guard   *credits.Guard
	Client  *lcw.Client
	Hist    *history.Store
	Engine  *alerts.Engine
	Assets  fs.FS
	Version string
}

func New(d Deps) *Server {
	return &Server{
		cfg: d.Cfg, clk: d.Clk, log: d.Log, hub: d.Hub, world: d.World,
		ctrl: d.Ctrl, watch: d.Watch, index: d.Index, guard: d.Guard,
		client: d.Client, hist: d.Hist, engine: d.Engine,
		assets: d.Assets, version: d.Version,
		detail: cache.NewLRU[*snapshot.Detail](d.Clk, d.Cfg.Cache.DetailTTL.D(), d.Cfg.Cache.DetailLRUSize),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/fiats", s.handleFiats)
	mux.HandleFunc("GET /api/coins/{code}", s.handleCoinDetail)
	mux.HandleFunc("GET /api/watchlist", s.handleWatchlistGet)
	mux.HandleFunc("GET /api/alerts", s.handleAlertsGet)
	mux.HandleFunc("GET /api/upstream/status", s.handleUpstreamStatus)

	mux.HandleFunc("POST /api/control", s.handleControl)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("PUT /api/watchlist", s.handleWatchlistPut)
	mux.HandleFunc("POST /api/watchlist/toggle", s.handleWatchlistToggle)
	mux.HandleFunc("POST /api/alerts/{id}/ack", s.handleAlertAck)
	mux.HandleFunc("POST /api/alerts/{id}/enabled", s.handleAlertEnabled)

	if s.cfg.Debug.Enabled && s.cfg.Debug.PProf {
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	}

	mux.Handle("GET /", s.staticHandler())

	return s.guardMiddleware(s.logMiddleware(mux))
}

// guardMiddleware is a cheap DNS-rebinding defence for a service that has no
// authentication: a page on another origin must not be able to drive it.
func (s *Server) guardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("handler panic", "path", r.URL.Path, "panic", v)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()

		if !s.cfg.Server.AllowNonLoopback && !hostIsLocal(r.Host) {
			http.Error(w, "host not permitted", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-site request rejected", http.StatusForbidden)
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !originIsLocal(origin) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hostIsLocal(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "localhost" || h == "" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

func originIsLocal(origin string) bool {
	origin = strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return hostIsLocal(origin)
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	if !s.cfg.Log.HTTPRequests {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clk.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path,
			"ms", s.clk.Since(start).Milliseconds())
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, reason, detail string) {
	writeJSON(w, status, map[string]string{"error": reason, "detail": detail})
}
