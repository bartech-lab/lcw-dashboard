// Command lcwd serves the Live Coin Watch dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/bartech/lcw-dashboard/internal/alerts"
	"github.com/bartech/lcw-dashboard/internal/clock"
	"github.com/bartech/lcw-dashboard/internal/config"
	"github.com/bartech/lcw-dashboard/internal/credits"
	"github.com/bartech/lcw-dashboard/internal/history"
	"github.com/bartech/lcw-dashboard/internal/httpapi"
	"github.com/bartech/lcw-dashboard/internal/hub"
	"github.com/bartech/lcw-dashboard/internal/lcw"
	"github.com/bartech/lcw-dashboard/internal/logging"
	"github.com/bartech/lcw-dashboard/internal/notify"
	"github.com/bartech/lcw-dashboard/internal/scheduler"
	"github.com/bartech/lcw-dashboard/internal/searchindex"
	"github.com/bartech/lcw-dashboard/internal/snapshot"
	"github.com/bartech/lcw-dashboard/internal/store"
	"github.com/bartech/lcw-dashboard/internal/watchlist"
	"github.com/bartech/lcw-dashboard/web"
)

var version = "0.1.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath  = flag.String("config", "", "path to config.yaml (default: $XDG_CONFIG_HOME/lcw-dashboard/config.yaml)")
		listen      = flag.String("listen", "", "override server.listen")
		logLevel    = flag.String("log-level", "", "override log.level")
		checkConfig = flag.Bool("check-config", false, "validate the config and exit")
		printConfig = flag.Bool("print-config", false, "print the effective config and exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("lcw-dashboard %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return nil
	}

	paths, err := store.Resolve()
	if err != nil {
		return err
	}
	if err := paths.EnsureDirs(); err != nil {
		return err
	}

	// A .env beside the config comes first; a local one is a development
	// convenience. Neither overrides an already-set environment variable.
	_ = godotenv.Load(paths.EnvFile())
	_ = godotenv.Load(".env")

	cfgPath := *configPath
	if cfgPath == "" {
		if v := os.Getenv("LCW_CONFIG"); v != "" {
			cfgPath = v
		} else {
			cfgPath = paths.ConfigFile()
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnv()
	if *listen != "" {
		cfg.Server.Listen = *listen
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration in %s:\n%w", cfgPath, err)
	}
	if *checkConfig {
		fmt.Printf("%s is valid\n", cfgPath)
		return nil
	}
	if *printConfig {
		return printEffective(cfg, cfgPath, paths)
	}

	log := logging.Setup(cfg.Log, cfg.APIKey)
	clk := clock.Real{}

	log.Info("starting", "version", version,
		"config", cfgPath, "state", paths.StateDir, "cache", paths.CacheDir)

	// Making the projection loud is the point: a too-fast interval is visible on
	// the first run rather than discovered as an afternoon slowdown.
	spend := cfg.ProjectSpend()
	logSpend(log, cfg, spend)

	// Bind before any network call, so the UI can explain a missing key or a
	// dead upstream instead of the process refusing to start.
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}

	client := lcw.New(cfg.APIKey,
		lcw.WithBaseURL(cfg.API.BaseURL),
		lcw.WithUserAgent(cfg.API.UserAgent+"/"+version),
		lcw.WithTimeout(cfg.API.Timeout.D()))

	ledger := credits.NewLedger(clk, cfg.Credits.APIDailyLimit)
	restoreLedger(log, paths, ledger)
	limiter := credits.NewLimiter(cfg.Credits, clk)
	guard := credits.NewGuard(cfg.Credits, clk, ledger, limiter, cfg.HasAPIKey())

	wl := watchlist.New(clk, paths.Watchlist(), cfg.Coins.WatchlistMax, cfg.Coins.ChunkSize)
	if err := wl.Load(cfg.Watchlist.Initial); err != nil {
		log.Warn("watchlist load had problems, continuing", "err", err)
	}

	hist := history.NewStore(cfg.History, paths, log)
	hist.Pin(wl.Codes())

	engine := alerts.NewEngine(clk, cfg.Alerts.Rules,
		cfg.Alerts.DefaultCooldown.D(), cfg.Alerts.RestartGrace.D(), cfg.Alerts.MaxEventsKept)
	restoreAlerts(log, paths, engine)

	index := searchindex.New(cfg.SearchIndex, clk, paths.SearchIndex(), log)
	index.Load()

	sinks := buildSinks(cfg, log)
	fan := notify.NewFanout(log, sinks...)
	if cfg.Alerts.Enabled {
		log.Info("alert sinks", "active", fan.Names())
	}

	world := snapshot.NewHolder()
	events := hub.New()

	ctrl := scheduler.New(scheduler.Deps{
		Cfg: cfg, Clk: clk, Log: log, Client: client, Guard: guard,
		Hub: events, World: world, Watch: wl, Hist: hist,
		Engine: engine, Index: index, Notify: fan,
	})
	ctrl.SetEnvPath(paths.EnvFile())
	restoreLastGood(log, cfg, paths, ctrl)

	api := httpapi.New(httpapi.Deps{
		Cfg: cfg, Clk: clk, Log: log, Hub: events, World: world, Ctrl: ctrl,
		Watch: wl, Index: index, Guard: guard, Client: client, Hist: hist,
		Engine: engine, Assets: web.Assets(), Version: version,
	})

	srv := &http.Server{
		Handler:     api.Handler(),
		ReadTimeout: cfg.Server.ReadTimeout.D(),
		// No WriteTimeout: it would kill every SSE stream on a fixed schedule.
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go ctrl.Run(ctx)
	go ctrl.RunReconcile(ctx)
	go ctrl.RunIndex(ctx)
	go ctrl.RunHistoryFlush(ctx)
	go ctrl.Bootstrap(ctx)

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	url := "http://" + cfg.Server.Listen
	log.Info("ready", "url", url)
	if cfg.Server.OpenBrowser {
		openBrowser(log, url)
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Tell clients to back off before closing, or they all reconnect at once.
	if err := events.Broadcast(hub.EventBye, "", map[string]string{"reason": "shutdown"}); err != nil {
		log.Debug("bye broadcast failed", "err", err)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.D())
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Warn("graceful shutdown timed out", "err", err)
	}

	stop()
	persistOnExit(log, cfg, paths, ledger, engine, wl, hist, ctrl)

	r := ledger.Report()
	log.Info("stopped", "creditsSpentToday", r.Spend, "byKind", r.ByKind)
	return nil
}

func logSpend(log *slog.Logger, cfg config.Config, s config.Spend) {
	args := []any{
		"total", s.Total, "coins", s.Coins, "overview", s.Overview,
		"searchIndex", s.SearchIndex, "reconcile", s.Reconcile,
		"ceiling", s.Ceiling, "apiLimit", s.APILimit,
	}
	switch {
	case s.OverAPILimit():
		log.Error("projected daily credits exceed the API limit; the dashboard will "+
			"throttle itself every day. Raise poll.active_interval or "+
			"overview.interval", args...)
	case s.OverCeiling():
		log.Warn("projected daily credits exceed credits.daily_ceiling; expect the "+
			"budget guard to engage", args...)
	default:
		log.Info("projected daily credit spend", args...)
	}
}

func buildSinks(cfg config.Config, log *slog.Logger) []notify.Sink {
	if !cfg.Alerts.Enabled {
		return nil
	}
	var out []notify.Sink
	for _, name := range cfg.Alerts.Sinks {
		switch name {
		case config.SinkNative:
			out = append(out, notify.NewNative())
		case config.SinkLog:
			out = append(out, notify.LogSink{Log: log})
		case config.SinkBrowser:
			// Delivered over SSE by the controller, not through a Sink.
		}
	}
	return out
}

func restoreLedger(log *slog.Logger, paths store.Paths, ledger *credits.Ledger) {
	target := credits.NewSnapshotTarget()
	found, err := store.ReadJSON(paths.Ledger(), target)
	if err != nil {
		log.Warn("credit ledger unreadable, starting fresh", "err", err)
		if p, qerr := store.Quarantine(paths.Ledger()); qerr == nil {
			log.Warn("quarantined", "path", p)
		}
		return
	}
	if found {
		ledger.Restore(target)
	}
}

func restoreAlerts(log *slog.Logger, paths store.Paths, engine *alerts.Engine) {
	target := alerts.NewSnapshotTarget()
	found, err := store.ReadJSON(paths.AlertState(), target)
	if err != nil {
		log.Warn("alert state unreadable, starting fresh", "err", err)
		return
	}
	if found {
		engine.Restore(target)
	}
}

func restoreLastGood(log *slog.Logger, cfg config.Config, paths store.Paths, ctrl *scheduler.Controller) {
	if !cfg.Cache.PersistLastGood {
		return
	}
	var lg scheduler.LastGood
	found, err := store.ReadJSON(paths.LastGood(), &lg)
	if err != nil {
		log.Debug("last-good snapshot unreadable", "err", err)
		return
	}
	if !found {
		return
	}
	ctrl.Warm(lg.Coins, lg.Overview)
}

func persistOnExit(log *slog.Logger, cfg config.Config, paths store.Paths,
	ledger *credits.Ledger, engine *alerts.Engine, wl *watchlist.List,
	hist *history.Store, ctrl *scheduler.Controller) {

	if snap, dirty := ledger.Snapshot(); dirty {
		if err := store.WriteJSONAtomic(paths.Ledger(), snap); err != nil {
			log.Warn("saving credit ledger failed", "err", err)
		}
	}
	if err := store.WriteJSONAtomic(paths.AlertState(), engine.Snapshot()); err != nil {
		log.Warn("saving alert state failed", "err", err)
	}
	if err := wl.Save(); err != nil {
		log.Warn("saving watchlist failed", "err", err)
	}
	if n := hist.Flush(); n > 0 {
		log.Info("flushed history", "rings", n)
	}
	if cfg.Cache.PersistLastGood {
		if err := store.WriteJSONAtomic(paths.LastGood(), ctrl.LastGood()); err != nil {
			log.Warn("saving last-good snapshot failed", "err", err)
		}
	}
}

func printEffective(cfg config.Config, cfgPath string, paths store.Paths) error {
	s := cfg.ProjectSpend()
	fmt.Printf("config:      %s\n", cfgPath)
	fmt.Printf("env file:    %s\n", paths.EnvFile())
	fmt.Printf("state dir:   %s\n", paths.StateDir)
	fmt.Printf("cache dir:   %s\n", paths.CacheDir)
	fmt.Printf("api key:     %s\n", presence(cfg.HasAPIKey()))
	fmt.Printf("listen:      %s\n", cfg.Server.Listen)
	fmt.Printf("intervals:   active %s, hidden %s, no clients %s\n",
		cfg.Poll.ActiveInterval, cfg.Poll.IdleIntervalHidden, cfg.Poll.IdleIntervalNoClients)
	fmt.Printf("overview:    %v every %s\n", cfg.Overview.Enabled, cfg.Overview.Interval)
	fmt.Printf("credits/day: %d projected (coins %d, overview %d, index %d, reconcile %d)\n",
		s.Total, s.Coins, s.Overview, s.SearchIndex, s.Reconcile)
	fmt.Printf("             ceiling %d, api limit %d\n", s.Ceiling, s.APILimit)
	if s.OverAPILimit() {
		fmt.Printf("             WARNING: exceeds the API limit\n")
	} else if s.OverCeiling() {
		fmt.Printf("             WARNING: exceeds the configured ceiling\n")
	}
	fmt.Printf("history:     %v, %d bytes/coin, %d MB max\n",
		cfg.History.Enabled, cfg.History.BytesPerCoin(), cfg.History.TotalBytes()/(1<<20))
	fmt.Printf("alerts:      %v, sinks %v, %d rules\n",
		cfg.Alerts.Enabled, cfg.Alerts.Sinks, len(cfg.Alerts.Rules))
	return nil
}

func presence(ok bool) string {
	if ok {
		return "set"
	}
	return "MISSING"
}

func openBrowser(log *slog.Logger, url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		log.Debug("cannot open a browser automatically", "err", err)
		return
	}
	if err := exec.Command(path, append(args, url)...).Start(); err != nil {
		log.Debug("opening a browser failed", "err", err)
	}
}
