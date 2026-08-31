// Package api serves Ephyra's JSON API and, in production, the embedded SPA.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/live"
	"github.com/andriykohut/ephyra/internal/store"
)

// Triggerer is the slice of the scheduler the API needs.
type Triggerer interface{ Trigger(job string) }

type Deps struct {
	Store   *store.Store
	Cfg     config.Config
	Log     *slog.Logger
	Trigger Triggerer
	Static  http.Handler     // SPA handler from Task 10; nil in tests
	Now     func() time.Time // injectable clock; defaults to time.Now
	Live    *live.Hub
}

type Server struct {
	st      *store.Store
	cfg     config.Config
	log     *slog.Logger
	trigger Triggerer
	static  http.Handler
	now     func() time.Time
	live    *live.Hub
}

func New(d Deps) *Server {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Server{st: d.Store, cfg: d.Cfg, log: d.Log, trigger: d.Trigger, static: d.Static, now: now, live: d.Live}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/library/overview", s.handleLibraryOverview)
	mux.HandleFunc("GET /api/watch/stats", s.handleWatchStats)
	mux.HandleFunc("GET /api/cleanup", s.handleCleanup)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("GET /api/now-playing", s.handleNowPlaying)
	mux.HandleFunc("GET /api/now-playing/stream", s.handleNowPlayingStream)
	mux.HandleFunc("/", s.handleRoot)
	return withLogging(s.log, mux)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	// unknown /api/* paths are 404 JSON, never the SPA shell
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
		return
	}
	if s.static != nil && r.Method == http.MethodGet {
		s.static.ServeHTTP(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "no such route")
}

type Meta struct {
	GeneratedAt string `json:"generated_at"`
	Stale       bool   `json:"stale"`
}

func (s *Server) meta(stale bool) Meta {
	return Meta{GeneratedAt: s.now().UTC().Format(time.RFC3339), Stale: stale}
}

func writeJSON(w http.ResponseWriter, status int, data any, meta Meta) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			log.Debug("http",
				"method", r.Method, "path", r.URL.Path,
				"status", sw.status, "dur_ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
