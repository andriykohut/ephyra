package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/jellyfin"
	"github.com/andriykohut/ephyra/internal/store"
)

// markLibraryReady satisfies the overview handler's not_ready guard, which the
// helpers below don't set up on their own.
func markLibraryReady(t *testing.T, srv *Server, now time.Time) {
	t.Helper()
	if err := srv.st.SetRefreshMeta(context.Background(),
		store.RefreshMeta{Job: "library", LastRunAt: now, OK: true},
	); err != nil {
		t.Fatal(err)
	}
}

// insertOverviewUser satisfies the overview handler's not_found guard: without
// a dim_user row, userID reads as unknown rather than a known user with an
// empty spine.
func insertOverviewUser(t *testing.T, srv *Server, userID string) {
	t.Helper()
	if _, err := srv.st.DB().ExecContext(context.Background(),
		`INSERT INTO dim_user (id, name) VALUES (?, ?)`, userID, userID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestOverviewRejectsBadRange(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview?range=7d", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestOverviewNotReadyBeforeFirstLibraryRefresh(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Now()) // no refresh_meta row for "library" at all
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 before the first library refresh: %s", rec.Code, rec.Body)
	}
}

func TestOverview404sForAnUnknownUser(t *testing.T) {
	now := time.Now()
	srv, _, _ := newTestServer(t, now)
	markLibraryReady(t, srv, now) // no dim_user row for "ghost"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/ghost/overview", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown user: %s", rec.Code, rec.Body)
	}
}

func TestOverviewWithEmptySpineStillServes200(t *testing.T) {
	now := time.Now()
	srv, _, _ := newTestServer(t, now) // no playback events at all
	markLibraryReady(t, srv, now)
	insertOverviewUser(t, srv, "u1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an empty spine is a page state, not an error", rec.Code)
	}
	for _, field := range []string{`"people":[]`, `"items":[]`, `"genres":[]`, `"activity":[]`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("body missing %s: %s", field, rec.Body.String())
		}
	}
}

func TestOverviewNowPlayingNullWhenLiveUnconfigured(t *testing.T) {
	now := time.Now()
	srv, _, _ := newTestServer(t, now) // Deps.Live == nil
	markLibraryReady(t, srv, now)
	insertOverviewUser(t, srv, "u1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview", nil))
	if !strings.Contains(rec.Body.String(), `"now_playing":null`) {
		t.Fatalf("body = %s, want a null now_playing", rec.Body.String())
	}
}

func rawSessionForUser(userID string) []jellyfin.RawSession {
	sessions := oneRawSession()
	sessions[0].UserID = userID
	return sessions
}

func TestOverviewNowPlayingJoinsOnRequestedUserID(t *testing.T) {
	sc := stubClient{sessions: func() ([]jellyfin.RawSession, error) {
		return rawSessionForUser("ABC-123"), nil // dashed/uppercase, like a raw Jellyfin id
	}}
	srv, _ := nowServer(t, sc)
	markLibraryReady(t, srv, time.Now())
	insertOverviewUser(t, srv, "abc123")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/abc123/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"session_id":"sess1"`) {
		t.Fatalf("body = %s, want the live session for abc123 joined in", rec.Body.String())
	}
}

func TestOverviewNowPlayingNullForDifferentUser(t *testing.T) {
	sc := stubClient{sessions: func() ([]jellyfin.RawSession, error) {
		return rawSessionForUser("ABC-123"), nil
	}}
	srv, _ := nowServer(t, sc)
	markLibraryReady(t, srv, time.Now())
	insertOverviewUser(t, srv, "someoneelse")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/someoneelse/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"now_playing":null`) {
		t.Fatalf("body = %s, want now_playing null for a user with no session", rec.Body.String())
	}
}
