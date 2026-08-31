package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/store"
)

type fakeTrigger struct{ got []string }

func (f *fakeTrigger) Trigger(job string) { f.got = append(f.got, job) }

func newTestServer(t *testing.T, now time.Time) (*Server, *store.Store, *fakeTrigger) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ft := &fakeTrigger{}
	s := New(Deps{
		Store:   st,
		Cfg:     config.Config{RefreshLibrary: 30 * time.Minute},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Trigger: ft,
		Now:     func() time.Time { return now },
	})
	return s, st, ft
}

func TestHealthz(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestLibraryOverview_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (%s)", rr.Code, rr.Body)
	}
	var env struct {
		Error struct{ Code string } `json:"error"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "not_ready" {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestLibraryOverview_OKAndStaleFlag(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()

	if err := st.WriteLibraryAggregates(ctx, sampleAgg(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-10 * time.Minute), OK: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data struct {
			Totals struct {
				Items int64 `json:"items"`
			} `json:"totals"`
		} `json:"data"`
		Meta Meta `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Totals.Items != 6 || env.Meta.Stale || env.Meta.GeneratedAt == "" {
		t.Fatalf("bad envelope: %+v", env)
	}

	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-2 * time.Hour), OK: true})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Meta.Stale {
		t.Fatal("expected stale=true")
	}
}

func TestUnknownAPIPathIs404JSON(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want json, got %q", ct)
	}
}

func TestRefreshEndpoint(t *testing.T) {
	s, _, ft := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=library", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rr.Code)
	}
	var env struct {
		Data map[string]map[string]bool `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Data["library"]["triggered"] {
		t.Fatalf("library not triggered: %+v", env.Data)
	}
	if _, ok := env.Data["watch"]; ok {
		t.Fatalf("watch should not appear for job=library: %+v", env.Data)
	}
	if len(ft.got) != 1 || ft.got[0] != "library" {
		t.Fatalf("trigger not called: %v", ft.got)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=all", nil))
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Data["library"]["triggered"] || !env.Data["watch"]["triggered"] {
		t.Fatalf("job=all should trigger both: %+v", env.Data)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=bogus", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad job should be 400, got %d", rr.Code)
	}
}
