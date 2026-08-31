package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/api"
	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/jellyfin"
	"github.com/andriykohut/ephyra/internal/live"
	"github.com/andriykohut/ephyra/internal/scheduler"
	"github.com/andriykohut/ephyra/internal/source/file"
	"github.com/andriykohut/ephyra/internal/store"
	"github.com/andriykohut/ephyra/internal/testsupport"
)

// bootServer wires store + scheduler + api against dataDir and runs both jobs
// once, synchronously.
func bootServer(t *testing.T, dataDir string) http.Handler {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ephyra.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		JellyfinDataDir: dataDir,
		WorkDir:         t.TempDir(),
		RefreshLibrary:  30 * time.Minute,
		RefreshWatch:    10 * time.Minute,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	src := file.New(cfg, log)
	sc := scheduler.New(st, src, cfg, log)
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatalf("library refresh: %v", err)
	}
	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatalf("watch refresh: %v", err)
	}
	return api.New(api.Deps{Store: st, Cfg: cfg, Log: log, Trigger: sc}).Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestSmoke_WithPlugin(t *testing.T) {
	h := bootServer(t, testsupport.TwoDBLayout(t))

	for _, p := range []string{
		"/api/watch/stats",
		"/api/watch/stats?range=all",
		"/api/watch/stats?range=all&user=11111111222233334444555555555555",
		"/api/profile",
		"/api/profile/11111111222233334444555555555555?range=all",
		"/api/cleanup",
		"/api/cleanup?mode=stale",
	} {
		rr := get(t, h, p)
		if rr.Code != 200 {
			t.Fatalf("%s -> %d (%s)", p, rr.Code, rr.Body)
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: bad json: %v", p, err)
		}
		if _, ok := env["data"]; !ok {
			t.Fatalf("%s: no data key in %s", p, rr.Body)
		}
	}

	// watch stats over "all" sees the fixture's plugin history
	rr := get(t, h, "/api/watch/stats?range=all")
	var env struct {
		Data store.WatchStats `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Data.PluginAvailable || env.Data.Totals.Plays == 0 {
		t.Fatalf("expected plugin data over range=all: %+v", env.Data.Totals)
	}

	// CSV
	rr = get(t, h, "/api/cleanup?format=csv")
	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("csv content-type %q", ct)
	}
}

func TestSmoke_PluginAbsent(t *testing.T) {
	// jellyfin.db only: copy the built library fixture into <dir>/data/.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(testsupport.LibraryFixtureDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "jellyfin.db"), built, 0o644); err != nil {
		t.Fatal(err)
	}

	h := bootServer(t, dir)

	rr := get(t, h, "/api/watch/stats")
	if rr.Code != 200 {
		t.Fatalf("watch stats -> %d", rr.Code)
	}
	var env struct {
		Data struct {
			PluginAvailable bool `json:"plugin_available"`
		} `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Data.PluginAvailable {
		t.Fatal("plugin should be absent")
	}
	if get(t, h, "/api/cleanup").Code != 200 {
		t.Fatal("cleanup should be unaffected by the missing plugin")
	}
}

func TestSmoke_NowPlaying(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info":
			w.Write([]byte(`{"ServerName":"Stub","Version":"10.11.11"}`))
		case "/Sessions":
			b, _ := os.ReadFile(filepath.Join(repoRootForSmoke(t), "testdata", "sessions.directplay.json"))
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jf.Close()

	dataDir := testsupport.TwoDBLayout(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{JellyfinDataDir: dataDir, WorkDir: t.TempDir(), JellyfinURL: jf.URL, JellyfinAPIKey: "K", LivePollInterval: time.Second, RefreshLibrary: time.Hour, RefreshWatch: time.Hour}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := live.New(jellyfin.New(cfg), cfg.LivePollInterval, nil, log)
	t.Cleanup(hub.Close)
	hub.Prime(ctx)
	h := api.New(api.Deps{Store: st, Cfg: cfg, Log: log, Live: hub}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing", nil))
	if rr.Code != 200 {
		t.Fatalf("now-playing -> %d", rr.Code)
	}
	var env struct {
		Data live.Snapshot `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data.Sessions) != 1 || env.Data.Sessions[0].Title != "The Long Retreat" {
		t.Fatalf("snapshot: %+v", env.Data)
	}

	// the stream yields a snapshot frame
	srv := httptest.NewServer(h)
	defer srv.Close()
	// a deadline so an unflushed frame fails here instead of hanging to the
	// package timeout
	streamCtx, cancelStream := context.WithTimeout(ctx, 10*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, srv.URL+"/api/now-playing/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.HasPrefix(line, "event: snapshot") {
		t.Fatalf("first SSE frame = %q", line)
	}
}

func repoRootForSmoke(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("go.mod not found")
		}
		d = p
	}
}
