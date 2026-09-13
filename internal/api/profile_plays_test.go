package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlaysHandlerRejectsBadLimit(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/plays?limit=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPlaysHandlerRejectsBeforeIDWithoutBefore(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/plays?before_id=5", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPlaysHandlerSerialisesEmptyListNotNull(t *testing.T) {
	srv, _, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/nobody/plays", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"plays":[]`) {
		t.Fatalf("body = %s, want an empty array", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("body = %s, want a null cursor", rec.Body.String())
	}
}
