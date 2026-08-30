package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/api"
	"github.com/andriykohut/ephyra/internal/config"
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
