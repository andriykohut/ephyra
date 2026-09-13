package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

func TestArtCacheEvictsOnBytesNotCount(t *testing.T) {
	c := newArtCache(1000) // 1000-byte budget
	c.put("a", "image/jpeg", make([]byte, 600))
	c.put("b", "image/jpeg", make([]byte, 600))
	if _, _, ok := c.get("a"); ok {
		t.Error("a should have been evicted; two 600-byte entries exceed the budget")
	}
	if _, _, ok := c.get("b"); !ok {
		t.Error("b should still be cached")
	}
}

func TestArtCacheServesACachedMissWithoutABody(t *testing.T) {
	c := newArtCache(1000)
	c.putMiss("nobody")
	ct, body, ok := c.get("nobody")
	if !ok {
		t.Fatal("cached miss should still be a cache hit")
	}
	if body != nil || ct != "" {
		t.Fatalf("miss entry should carry no content: ct=%q body=%v", ct, body)
	}
}

func TestArtCacheEvictsMisses(t *testing.T) {
	c := newArtCache(1000)
	for i := 0; i < 100; i++ {
		c.putMiss(fmt.Sprintf("nobody-%d", i))
	}
	if c.bytes > c.cap {
		t.Fatalf("bytes=%d exceeds cap=%d after 100 misses -- a miss-only cache never evicted", c.bytes, c.cap)
	}
	if _, _, ok := c.get("nobody-0"); ok {
		t.Fatal("earliest miss should have been evicted once later misses filled the budget")
	}
}

func TestArtProxyItemServesAndCachesAHit(t *testing.T) {
	png := []byte("\x89PNGdata")
	var hits int32
	sc := stubClient{
		sessions: func() ([]jellyfin.RawSession, error) { return nil, nil },
		img: func() (io.ReadCloser, string, error) {
			atomic.AddInt32(&hits, 1)
			return io.NopCloser(bytes.NewReader(png)), "image/png", nil
		},
	}
	s, _ := nowServer(t, sc)

	do := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	rr := do("/api/art/item/8a5cc46f628a63cd9981105b4d50ccb7?kind=primary&tag=t1")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Body.String() != string(png) {
		t.Fatalf("art: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Fatal("missing Cache-Control")
	}
	do("/api/art/item/8a5cc46f628a63cd9981105b4d50ccb7?kind=primary&tag=t1")
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits)
	}
}

func TestArtProxyItemRejectsBadIDOrKind(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) { return nil, nil }})
	do := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}
	if do("/api/art/item/..%2Fetc?kind=primary").Code != 400 {
		t.Fatal("bad id should be 400")
	}
	if do("/api/art/item/8a5cc46f628a63cd9981105b4d50ccb7?kind=logo").Code != 400 {
		t.Fatal("bad kind should be 400")
	}
}

func TestArtProxyItemUpstreamErrorHidesTheBaseURL(t *testing.T) {
	sc := stubClient{
		sessions: func() ([]jellyfin.RawSession, error) { return nil, nil },
		img: func() (io.ReadCloser, string, error) {
			return nil, "", errors.New(`Get "http://jellyfin.internal:8096/Items/x/Images/Primary": dial tcp: refused`)
		},
	}
	s, _ := nowServer(t, sc)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/item/8a5cc46f628a63cd9981105b4d50ccb7", nil))
	if rr.Code != 502 {
		t.Fatalf("want 502, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "jellyfin.internal") {
		t.Fatalf("502 body leaked the upstream URL: %s", rr.Body.String())
	}
}

func TestArtProxyCachesMisses(t *testing.T) {
	var upstream int32
	sc := stubClient{
		sessions: func() ([]jellyfin.RawSession, error) { return nil, nil },
		personImg: func() (io.ReadCloser, string, error) {
			atomic.AddInt32(&upstream, 1)
			return nil, "", fmt.Errorf("jellyfin /Persons/Nobody/Images/Primary: %w", jellyfin.ErrNotFound)
		},
	}
	s, _ := nowServer(t, sc)

	do := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/person?name=Nobody", nil))
		return rr
	}
	first := do()
	if first.Code != 404 {
		t.Fatalf("want 404 on a miss, got %d", first.Code)
	}
	if first.Header().Get("Cache-Control") == "" {
		t.Fatal("a cached miss should still carry Cache-Control")
	}
	second := do()
	if second.Code != 404 {
		t.Fatalf("want 404 on the cached miss too, got %d", second.Code)
	}
	if atomic.LoadInt32(&upstream) != 1 {
		t.Fatalf("upstream hits = %d, want 1: over half the people in a real library "+
			"have no image, and re-asking on every render is the whole cost", upstream)
	}
}

func TestArtProxyPersonRequiresAName(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) { return nil, nil }})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/person", nil))
	if rr.Code != 400 {
		t.Fatalf("want 400 with no name, got %d", rr.Code)
	}
}

func TestArtProxyUserServesAHit(t *testing.T) {
	jpg := []byte("\xff\xd8jpegdata")
	sc := stubClient{
		sessions: func() ([]jellyfin.RawSession, error) { return nil, nil },
		userImg: func() (io.ReadCloser, string, error) {
			return io.NopCloser(bytes.NewReader(jpg)), "image/jpeg", nil
		},
	}
	s, _ := nowServer(t, sc)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/user/8a5cc46f628a63cd9981105b4d50ccb7", nil))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/jpeg" || rr.Body.String() != string(jpg) {
		t.Fatalf("art: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
}

func TestArtProxyUserRejectsBadID(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) { return nil, nil }})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/user/..%2Fetc", nil))
	if rr.Code != 400 {
		t.Fatal("bad id should be 400")
	}
}

func TestArtProxyRemovesTheOldNowPlayingRoute(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) { return nil, nil }})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing/art/8a5cc46f628a63cd9981105b4d50ccb7", nil))
	if rr.Code != 404 {
		t.Fatalf("old route should be gone, got %d", rr.Code)
	}
}

func TestArtProxyNotReadyWithoutLive(t *testing.T) {
	s := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/art/item/8a5cc46f628a63cd9981105b4d50ccb7", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 with no live subsystem, got %d", rr.Code)
	}
}
