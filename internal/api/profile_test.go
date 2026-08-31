package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/store"
)

// seedForProfile gets a library refresh_meta + a dim_user row + some
// agg_profile_* rows in place so the profile endpoints have something to serve.
func seedForProfile(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := st.WriteLibraryAggregates(ctx,
		aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil,
		[]aggregate.UserRow{{ID: "u1", Name: "alice"}}, nil,
	); err != nil {
		t.Fatal(err)
	}
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-time.Minute), OK: true})
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "watch", LastRunAt: now.Add(-time.Minute), OK: true, PluginAvailable: true})

	if err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{
			{UserID: "u1", Range: "all", WatchSec: 9000, Plays: 12, RewatchPct: 0.25, FirstPlay: "2025-01-01", LastPlay: "2025-05-01"},
			{UserID: "u1", Range: "30d", WatchSec: 3000, Plays: 4, FirstPlay: "2025-04-10", LastPlay: "2025-05-01"},
		},
		Completion: []aggregate.ProfileCompletionRow{
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "finished", Count: 2},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandleProfileList_OK(t *testing.T) {
	now := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedForProfile(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.ProfileList `json:"data"`
		Meta map[string]any    `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Data.PluginAvailable || len(env.Data.Users) != 1 || env.Data.Users[0].Name != "alice" {
		t.Fatalf("list body: %+v", env.Data)
	}
	if env.Data.Users[0].TotalPlays != 12 {
		t.Fatalf("headline numbers not joined: %+v", env.Data.Users[0])
	}
}

func TestHandleProfile_OK(t *testing.T) {
	now := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedForProfile(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile/u1?range=30d", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.Profile `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Range != "30d" || env.Data.User.Name != "alice" || env.Data.Summary.Plays != 4 {
		t.Fatalf("profile body: %+v", env.Data)
	}
	if env.Data.Summary.RewatchPct != 0.25 {
		t.Fatalf("lifetime rewatch_pct should be filled from the 'all' row: %+v", env.Data.Summary)
	}
	if env.Data.Completion == nil {
		t.Fatal("completion should serialize as [] not null")
	}
}

func TestHandleProfile_BadRange(t *testing.T) {
	now := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedForProfile(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile/u1?range=weekly", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestHandleProfile_UnknownUser(t *testing.T) {
	now := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedForProfile(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile/ghost?range=all", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Error map[string]string `json:"error"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error["code"] != "not_found" {
		t.Fatalf("want not_found error, got %v", env.Error)
	}
}

func TestHandleProfile_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile/u1", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 before any library refresh, got %d", rr.Code)
	}
}
