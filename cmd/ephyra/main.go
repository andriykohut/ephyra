package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andriykohut/ephyra/internal/api"
	"github.com/andriykohut/ephyra/internal/buildinfo"
	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/jellyfin"
	"github.com/andriykohut/ephyra/internal/live"
	"github.com/andriykohut/ephyra/internal/scheduler"
	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/source/file"
	"github.com/andriykohut/ephyra/internal/store"
	"github.com/andriykohut/ephyra/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("config", "err", err)
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("starting", "version", buildinfo.Version(), "source", cfg.Source)

	if err := os.MkdirAll(filepath.Dir(cfg.StorePath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.StorePath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	src, err := buildSource(cfg, log)
	if err != nil {
		return err
	}

	sched := scheduler.New(st, src, cfg, log)
	go sched.Run(ctx)

	jc := jellyfin.New(cfg)
	var capacity *int
	if cfg.StreamCapacity > 0 {
		capacity = &cfg.StreamCapacity
	}
	hub := live.New(jc, cfg.LivePollInterval, capacity, log)
	defer hub.Close()
	hub.Prime(ctx)

	dist, err := web.DistFS()
	if err != nil {
		return err
	}
	srv := api.New(api.Deps{
		Store:   st,
		Cfg:     cfg,
		Log:     log,
		Trigger: sched,
		Static:  api.NewStaticHandler(dist),
		Live:    hub,
	})
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler()}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func buildSource(cfg config.Config, log *slog.Logger) (source.Source, error) {
	if cfg.Source == "api" {
		return nil, errors.New("SOURCE=api is not implemented in this build; use the file source")
	}
	fsrc := file.New(cfg, log)
	mt, err := fsrc.SourceMTime("library")
	if err != nil {
		return nil, fmt.Errorf("stat Jellyfin library DB: %w", err)
	}
	if mt.IsZero() {
		return nil, fmt.Errorf(
			"no Jellyfin item DB under %s/data/ or %s/data/data/ (looked for jellyfin.db, library.db) — check JELLYFIN_DATA_DIR and that the mount is present",
			cfg.JellyfinDataDir, cfg.JellyfinDataDir)
	}
	return fsrc, nil
}
