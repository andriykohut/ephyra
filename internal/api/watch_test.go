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

func TestWatchStats_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 before any library refresh, got %d", rr.Code)
	}
}

func TestWatchStats_PluginAbsentStillHasUsersAndCore(t *testing.T) {
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()

	if err := st.WriteLibraryAggregates(ctx, map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}},
		nil,
		[]aggregate.UserRow{{ID: "u1", Name: "alice"}},
		[]aggregate.CorePlayRow{{UserID: "u1", Scope: "movie", ItemID: "m9", Name: "Fav", PlayCount: 9}},
	); err != nil {
		t.Fatal(err)
	}
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-time.Minute), OK: true})

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats?range=30d", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.WatchStats `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.PluginAvailable {
		t.Fatal("plugin should be reported absent")
	}
	if len(env.Data.Users) != 1 || len(env.Data.MostPlayedCore) != 1 {
		t.Fatalf("users/core missing when plugin absent: %+v", env.Data)
	}
	if len(env.Data.TopMovies) != 0 {
		t.Fatalf("plugin panels should be empty: %+v", env.Data.TopMovies)
	}

	// Empty lists must serialize as [] not null — the SPA iterates them straight
	// off the response and a null blows up the render.
	var raw struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"users", "top_movies", "top_series", "top_episodes", "active_users",
		"trend", "heatmap", "play_method_weekly", "most_played_core",
	} {
		if string(raw.Data[k]) == "null" {
			t.Errorf("data.%s serialized as null; want []", k)
		}
	}
}

func TestWatchStats_BadRange(t *testing.T) {
	now := time.Now()
	s, st, _ := newTestServer(t, now)
	st.SetRefreshMeta(context.Background(), store.RefreshMeta{Job: "library", LastRunAt: now, OK: true})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats?range=lolyear", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}
