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

	"github.com/andriykohut/ephyra/internal/aggregate"
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

func TestSPAServesHEAD(t *testing.T) {
	// Uptime monitors and reverse-proxy health checks often probe with HEAD.
	s, _, _ := newTestServer(t, time.Now())
	s.static = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html>"))
	})
	for _, path := range []string{"/", "/library"} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodHead, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("HEAD %s: got %d, want 200", path, rr.Code)
		}
	}
	// HEAD on an unknown /api path still 404s like GET does.
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodHead, "/api/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("HEAD /api/nope: got %d, want 404", rr.Code)
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

func TestLibraryOverview_EmptyListsSerializeAsArrays(t *testing.T) {
	now := time.Now()
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()
	if err := st.WriteLibraryAggregates(ctx,
		aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now, OK: true})

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var totals struct {
		ItemsByLibrary json.RawMessage `json:"items_by_library"`
	}
	json.Unmarshal(raw.Data["totals"], &totals)
	if string(totals.ItemsByLibrary) == "null" {
		t.Error("data.totals.items_by_library serialized as null; want []")
	}
	for _, k := range []string{
		"disk_by_resolution", "disk_by_codec", "disk_by_container", "disk_by_library",
		"genres_top", "by_decade", "growth",
	} {
		if string(raw.Data[k]) == "null" {
			t.Errorf("data.%s serialized as null; want []", k)
		}
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
	// decoded fresh each time: reusing one env would let json.Unmarshal merge
	// into the already-populated map and hide a job the handler dropped.
	type refreshEnv struct {
		Data map[string]map[string]bool `json:"data"`
	}
	decode := func(t *testing.T, rr *httptest.ResponseRecorder) refreshEnv {
		t.Helper()
		var env refreshEnv
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %q: %v", rr.Body.String(), err)
		}
		return env
	}

	env := decode(t, rr)
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
	env = decode(t, rr)
	if !env.Data["library"]["triggered"] || !env.Data["watch"]["triggered"] {
		t.Fatalf("job=all should trigger both: %+v", env.Data)
	}
	if len(env.Data) != 2 {
		t.Fatalf("job=all should name exactly library+watch: %+v", env.Data)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=bogus", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad job should be 400, got %d", rr.Code)
	}
}
