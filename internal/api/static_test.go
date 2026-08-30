package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticHandler_ServesAssetsAndSPAFallback(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>Ephyra</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	h := NewStaticHandler(dist)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rr.Code != 200 || rr.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library", nil))
	if rr.Code != 200 || rr.Body.Len() == 0 || rr.Body.String()[0] != '<' {
		t.Fatalf("spa fallback: %d %q", rr.Code, rr.Body.String())
	}
}

func TestStaticHandler_NoBuild(t *testing.T) {
	h := NewStaticHandler(fstest.MapFS{".gitkeep": {Data: []byte{}}})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when unbuilt, got %d", rr.Code)
	}
}
