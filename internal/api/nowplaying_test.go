package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/jellyfin"
	"github.com/andriykohut/ephyra/internal/live"
	"github.com/andriykohut/ephyra/internal/store"
)

// stubClient satisfies the interface live.New wants.
type stubClient struct {
	sessions func() ([]jellyfin.RawSession, error)
	img      func() (io.ReadCloser, string, error)
}

func (s stubClient) Sessions(context.Context) ([]jellyfin.RawSession, error) { return s.sessions() }
func (s stubClient) SystemInfo(context.Context) (jellyfin.ServerInfo, error) {
	return jellyfin.ServerInfo{Name: "S", Version: "10.11.11"}, nil
}
func (s stubClient) Image(context.Context, string, jellyfin.ImageKind, string) (io.ReadCloser, string, error) {
	return s.img()
}

func nowServer(t *testing.T, sc stubClient) (*Server, *live.Hub) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := live.New(sc, 4*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(hub.Close)
	s := New(Deps{Store: st, Cfg: config.Config{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Live: hub})
	return s, hub
}

func oneRawSession() []jellyfin.RawSession {
	return []jellyfin.RawSession{{
		ID: "sess1", UserName: "orlyk", RemoteEndPoint: "192.168.1.5",
		PlayState: &jellyfin.RawPlayState{PositionTicks: 600 * 10_000_000, PlayMethod: "DirectPlay"},
		NowPlayingItem: &jellyfin.RawItem{
			ID: "item1", Name: "Movie X", Type: "Movie", MediaType: "Video", RunTimeTicks: 6000 * 10_000_000,
			MediaStreams: []jellyfin.RawStream{{Type: "Video", Codec: "h264", Width: 1920, BitRate: 5_000_000, VideoRangeType: "SDR"}},
		},
	}}
}

func TestNowPlaying_OneShot(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) { return oneRawSession(), nil }})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data live.Snapshot `json:"data"`
		Meta Meta          `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Sessions) != 1 || env.Data.Sessions[0].Title != "Movie X" || env.Data.Degraded {
		t.Fatalf("data: %+v", env.Data)
	}
}

func TestNowPlaying_DegradedNoCache(t *testing.T) {
	s, _ := nowServer(t, stubClient{sessions: func() ([]jellyfin.RawSession, error) {
		return nil, context.DeadlineExceeded
	}})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing", nil))
	if rr.Code != 200 {
		t.Fatalf("want 200 when upstream down, got %d", rr.Code)
	}
	var env struct {
		Data live.Snapshot `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Data.Degraded || len(env.Data.Sessions) != 0 {
		t.Fatalf("expected degraded empty snapshot: %+v", env.Data)
	}
}

func TestNowPlaying_Stream(t *testing.T) {
	sc := stubClient{sessions: func() ([]jellyfin.RawSession, error) { return oneRawSession(), nil }}
	s, hub := nowServer(t, sc)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/now-playing/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	// first frame is "event: snapshot"
	line, _ := br.ReadString('\n')
	if !strings.HasPrefix(line, "event: snapshot") {
		t.Fatalf("first frame = %q", line)
	}
	// hub publishes an update -> reaches the reader
	hub.Publish(live.Event{Kind: "update", Data: &live.Snapshot{Sessions: []live.Session{}}})
	deadline := time.Now().Add(2 * time.Second)
	var sawUpdate bool
	for time.Now().Before(deadline) {
		l, err := br.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(l, "event: update") {
			sawUpdate = true
			break
		}
	}
	if !sawUpdate {
		t.Fatal("did not receive the update frame")
	}

	// disconnect -> hub subscriber count drops to 0
	cancel()
	for i := 0; i < 200 && hub.SubscriberCount() != 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.SubscriberCount() != 0 {
		t.Fatalf("subscriber not removed on disconnect")
	}
}

func TestNowPlaying_ArtProxyAndCache(t *testing.T) {
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

	rr := do("/api/now-playing/art/8a5cc46f628a63cd9981105b4d50ccb7?kind=primary&tag=t1")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Body.String() != string(png) {
		t.Fatalf("art: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Fatal("missing Cache-Control")
	}
	// identical request -> cache hit, no second upstream call
	do("/api/now-playing/art/8a5cc46f628a63cd9981105b4d50ccb7?kind=primary&tag=t1")
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits)
	}

	if do("/api/now-playing/art/..%2Fetc?kind=primary").Code != 400 {
		t.Fatal("bad id should be 400")
	}
	if do("/api/now-playing/art/8a5cc46f628a63cd9981105b4d50ccb7?kind=logo").Code != 400 {
		t.Fatal("bad kind should be 400")
	}
}

func TestNowPlaying_ArtUpstreamError(t *testing.T) {
	sc := stubClient{
		sessions: func() ([]jellyfin.RawSession, error) { return nil, nil },
		img:      func() (io.ReadCloser, string, error) { return nil, "", errors.New("upstream 404") },
	}
	s, _ := nowServer(t, sc)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing/art/8a5cc46f628a63cd9981105b4d50ccb7", nil))
	if rr.Code != 502 {
		t.Fatalf("want 502, got %d", rr.Code)
	}
}
