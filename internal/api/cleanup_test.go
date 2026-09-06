package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/store"
)

func seedCleanupAPI(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Big Never", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "c", Scope: "series", Name: "Stale Show", Library: "Shows", Bytes: 500, Episodes: 12,
			AddedAt: "2019-01-01T00:00:00Z", LastPlayedAt: "2023-06-01T00:00:00Z"},
	}
	if err := st.WriteLibraryAggregates(context.Background(),
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(context.Background(), store.RefreshMeta{
		Job: "library", LastRunAt: now.Add(-time.Minute), OK: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanup_JSON(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedCleanupAPI(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=never", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.CleanupResult `json:"data"`
		Meta Meta                `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Mode != "never" || env.Data.MatchCount != 1 || env.Data.ReclaimableBytes != 900 {
		t.Fatalf("bad data: %+v", env.Data)
	}
}

func TestCleanup_CSV(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedCleanupAPI(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=stale&format=csv", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("content-type %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != `attachment; filename="ephyra-cleanup-stale.csv"` {
		t.Fatalf("disposition %q", cd)
	}
	if !strings.Contains(rr.Body.String(), "Stale Show") {
		t.Fatalf("csv body:\n%s", rr.Body.String())
	}
}

func TestCleanup_LibraryParam(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Movie Never A", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "b", Scope: "movie", Name: "Movie Never B", Library: "Movies", Bytes: 100, AddedAt: "2024-01-01T00:00:00Z"},
		{ItemID: "e", Scope: "series", Name: "Show Never E", Library: "Shows", Bytes: 500, Episodes: 5, AddedAt: "2021-01-01T00:00:00Z"},
	}
	if err := st.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now, OK: true}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		q    string
		want int64
	}{
		{"", 3}, {"&library=all", 3}, {"&library=Movies", 2}, {"&library=Nonexistent", 0},
	} {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=never"+tc.q, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d body %s", tc.q, rr.Code, rr.Body)
		}
		var env struct {
			Data store.CleanupResult `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.Data.MatchCount != tc.want {
			t.Errorf("%s: match_count = %d, want %d", tc.q, env.Data.MatchCount, tc.want)
		}
	}

	// CSV output should also honor the filter.
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=never&format=csv&library=Movies", nil))
	if rr.Code != 200 {
		t.Fatalf("csv: status %d body %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "Show Never E") {
		t.Fatalf("csv library filter did not narrow output:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Movie Never A") {
		t.Fatalf("csv missing expected row:\n%s", rr.Body.String())
	}
}

func TestCleanup_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
}

func TestCleanup_BadMode(t *testing.T) {
	now := time.Now()
	s, st, _ := newTestServer(t, now)
	st.SetRefreshMeta(context.Background(), store.RefreshMeta{Job: "library", LastRunAt: now, OK: true})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=bogus", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}
