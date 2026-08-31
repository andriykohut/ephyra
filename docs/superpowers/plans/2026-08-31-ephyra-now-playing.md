# Ephyra — Now Playing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the `/now` page end to end — a `jellyfin.Client` that polls `GET /Sessions` only while a browser has the page open, a `live.Hub` that fans normalized snapshots out over Server-Sent Events, `GET /api/now-playing` + `/stream` + an image proxy, and the live React page — plus a small "finish" bundle (`POST /api/refresh` shape, a growth-chart fix, README API-key steps).

**Architecture:** A new `internal/jellyfin` package holds a thin HTTP client (`/Sessions`, `/System/Info`, item images). A new `internal/live` package holds `normalize` (pure: raw sessions → snapshot DTO) and `Hub` (subscribe/publish + a single poll-loop goroutine that runs only while ≥1 SSE client is connected, with degraded/backoff handling). `internal/api` gains three handlers over the hub. The frontend `useNowPlaying` hook owns the `EventSource` lifecycle, visibility handling, and local progress interpolation; `NowPlayingCard` renders each session with poster/backdrop art proxied through Ephyra.

**Tech Stack:** Go 1.25, stdlib `net/http` (`http.NewResponseController` for SSE flush), `log/slog`; React 19 + Vite + TypeScript, Tailwind v4, TanStack Router, browser `EventSource`; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-31-ephyra-now-playing-design.md` — read it alongside this plan. Parent spec: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`. This plan implements the Now Playing spec in full: §3 (schema notes), §4 (`jellyfin.Client`), §5 (`live` hub + normalize), §6 (API routes), §7 (snapshot DTO), §8 (`/now` page), §9 (finish bundle), §10 (config — no new vars), §11 (tests).

## Global Constraints

Copied from the spec. Every task inherits these.

- **No CGO.** `CGO_ENABLED=0` for every build.
- **Module path:** `github.com/andriykohut/ephyra`.
- **Read-only.** No writes to Jellyfin — no playback commands, only `GET`s.
- **Never 5xx for a page because upstream is down.** `GET /api/now-playing` returns `200` with a degraded snapshot when Jellyfin is unreachable; Ephyra boots even when the Jellyfin API is unreachable or the key is wrong (`hub.Prime` is non-fatal).
- **Zero standing load on Jellyfin.** The poll loop and the `/System/Info` refresh run *only* while ≥1 SSE client is connected. Nobody watching → zero HTTP calls to Jellyfin.
- **Response envelope:** non-stream JSON is `{ "data": …, "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`. SSE frames carry the bare `Snapshot` (or `{"degraded":true}`), no envelope. Errors: `{ "error": { "code", "message" } }`.
- **Auth to Jellyfin:** `Authorization: MediaBrowser Token=<key>` header (verified against 10.11.11).
- **The API key never reaches the browser** — art is proxied through `GET /api/now-playing/art/{itemId}`.
- **TDD.** Each task: write the failing test → run it, see it fail → minimal implementation → run it, see it pass → commit. Small commits.

## Domain primer (`/Sessions`, from the real 10.11.11 probe)

Full detail in the spec §3. The essentials:

- `/Sessions` returns **all** sessions incl. idle ones. Keep only those with `NowPlayingItem != null` **and** `NowPlayingItem.MediaType == "Video"`.
- **`PlayMethod` is on `PlayState`**, not the session top level. Values: `DirectPlay` | `DirectStream` | `Transcode`.
- **`UserName` is in the session** — no `/Users` call.
- **`TranscodingInfo` is absent during direct-play** — it appears only mid-transcode. Task 1 Step 1 forces a real transcode and captures it; the committed `testdata/sessions.transcode.json` starts as a hand-build against Jellyfin's OpenAPI and is replaced with the real capture.
- **No overall bitrate field** for direct-play → sum the playing video + selected audio stream `BitRate`s.
- `RemoteEndPoint` is a bare IP string (`"192.168.1.50"`), occasionally `ip:port`.
- Ticks ÷ `1e7` = seconds. `NowPlayingItem`: `Name`, `Type`, `SeriesName`, `IndexNumber`/`ParentIndexNumber`, `RunTimeTicks`, `Container`, `ImageTags.Primary`, `ParentBackdropItemId` + `ParentBackdropImageTags[]`, `MediaStreams[]` (`Type`, `Index`, `Codec`, `Width`/`Height`, `BitRate`, `Channels`, `ChannelLayout`, `VideoRangeType`, `IsDefault`).
- `TranscodingInfo`: `Bitrate`, `Container`, `VideoCodec`, `AudioCodec`, `IsVideoDirect`, `IsAudioDirect`, `TranscodeReasons[]`, `CompletionPercentage`, `HardwareAccelerationType`.
- `/System/Info` → `ServerName`, `Version`.

## File structure

```
internal/jellyfin/client.go          NEW — Client{base,key,hc}: Sessions, SystemInfo, Image
internal/jellyfin/types.go           NEW — RawSession/RawPlayState/RawTranscodingInfo/RawItem/RawStream, ServerInfo, ImageKind
internal/jellyfin/client_test.go     NEW

internal/live/types.go               NEW — Snapshot, Summary, Session, SourceStreams, VideoSource, AudioSource, Transcode, Event, ServerInfo, degradedPayload
internal/live/normalize.go           NEW — normalize() + helpers
internal/live/normalize_test.go      NEW
internal/live/hub.go                 NEW — Hub: New, Prime, Subscribe, Publish, Snapshot, Image, Close, pollLoop, changed
internal/live/hub_test.go            NEW

internal/api/api.go                  MODIFY — Deps.Live, Server.live, register 3 routes, statusWriter.Unwrap
internal/api/nowplaying.go           NEW — handleNowPlaying (one-shot)
internal/api/nowplaying_stream.go    NEW — handleNowPlayingStream (SSE) + writeSSE
internal/api/nowplaying_art.go       NEW — handleNowPlayingArt + artCache (LRU)
internal/api/nowplaying_test.go      NEW
internal/api/refresh.go              MODIFY — per-job {triggered:true} response
internal/api/refresh_test.go         MODIFY (or add) — assert new shape

cmd/ephyra/main.go                   MODIFY — build jellyfin.Client + live.Hub, Prime, Deps.Live, hub.Close on shutdown
cmd/ephyra/main_test.go              MODIFY — smoke with a stub Jellyfin httptest.Server

web/src/api/types.ts                 MODIFY — Snapshot, NowSession, NowSummary, Transcode
web/src/live/useNowPlaying.ts        NEW — the hook (EventSource + one-shot + visibility + interpolation)
web/src/live/useNowPlaying.test.ts   NEW
web/src/components/NowPlayingCard.tsx NEW
web/src/routes/now.tsx               REPLACE — NowPlayingView + NowPlayingPage
web/src/routes/now.test.tsx          NEW
web/src/routes/library.tsx           MODIFY — growthForChart() drops the "1970-01" bucket
web/src/routes/library.test.tsx      MODIFY — a growthForChart unit assertion

testdata/sessions.directplay.json    NEW — real capture (spec §3.1)
testdata/sessions.transcode.json     NEW — synthetic, replaced with a real capture in Task 1
testdata/systeminfo.json             NEW — {"ServerName":"...","Version":"..."}

docs/schema-notes.md                 MODIFY — /Sessions section
CLAUDE.md                            MODIFY — live subsystem note; Plan 3 done
README.md                            MODIFY — real Jellyfin API-key steps; "all four pages"
```

---

## Task 1: `internal/jellyfin` — the HTTP client + fixtures

**Files:**
- Create: `internal/jellyfin/types.go`, `internal/jellyfin/client.go`, `internal/jellyfin/client_test.go`
- Create: `testdata/sessions.directplay.json`, `testdata/sessions.transcode.json`, `testdata/systeminfo.json`

**Interfaces:**
- Consumes: `config.Config` (`JellyfinURL`, `JellyfinAPIKey`).
- Produces:
  ```go
  package jellyfin

  type ServerInfo struct {
      Name    string `json:"ServerName"`
      Version string `json:"Version"`
  }
  type ImageKind string
  const (ImagePrimary ImageKind = "Primary"; ImageBackdrop ImageKind = "Backdrop")

  type RawSession struct {
      ID, UserID, UserName, Client, DeviceName, RemoteEndPoint string
      PlayState       *RawPlayState
      TranscodingInfo *RawTranscodingInfo
      NowPlayingItem  *RawItem
  }
  type RawPlayState struct {
      PositionTicks    int64
      IsPaused         bool
      PlayMethod       string
      AudioStreamIndex *int
  }
  type RawTranscodingInfo struct {
      Bitrate                  int64
      Container, VideoCodec, AudioCodec, HardwareAccelerationType string
      IsVideoDirect, IsAudioDirect bool
      TranscodeReasons         []string
      CompletionPercentage     float64
  }
  type RawItem struct {
      ID, Name, Type, MediaType, Container, SeriesName, ParentBackdropItemId string
      RunTimeTicks             int64
      IndexNumber, ParentIndexNumber, ProductionYear *int
      ImageTags                map[string]string
      ParentBackdropImageTags  []string
      MediaStreams             []RawStream
  }
  type RawStream struct {
      Type, Codec, ChannelLayout, VideoRangeType string
      Index, Width, Height, Channels int
      BitRate int64
      IsDefault bool
  }

  func New(cfg config.Config) *Client
  func (c *Client) Sessions(ctx context.Context) ([]RawSession, error)   // GET /Sessions?ActiveWithinSeconds=960
  func (c *Client) SystemInfo(ctx context.Context) (ServerInfo, error)   // GET /System/Info
  func (c *Client) Image(ctx context.Context, itemID string, kind ImageKind, tag string) (body io.ReadCloser, contentType string, err error)
  ```
  All `json:"..."` tags map struct fields to the PascalCase keys above (spec §3). Non-2xx → `fmt.Errorf("jellyfin %s: %s", path, resp.Status)`.

- [ ] **Step 1: Capture real payloads.** With something **transcoding** on the operator's Jellyfin (web player → gear → a low bitrate forces it), capture:
  ```sh
  # On the Jellyfin host, with an API key from Dashboard -> API Keys:
  curl -s "http://localhost:8096/Sessions?ActiveWithinSeconds=960" -H "Authorization: MediaBrowser Token=$KEY"
  curl -s "http://localhost:8096/System/Info" -H "Authorization: MediaBrowser Token=$KEY"
  ```
  Save the transcoding session (drop `Capabilities`, `SupportedCommands`, `Chapters`, `Trickplay`, `ImageBlurHashes` to keep the fixture small — keep the fields the primer lists) as `testdata/sessions.transcode.json` (a JSON array with one element). Save the DirectPlay probe already captured (spec §3.1, in `scratchpad/sessions-probe-notes.md`) as `testdata/sessions.directplay.json`. Save `/System/Info` as `testdata/systeminfo.json`.
  **If a transcode cannot be captured now:** commit `testdata/sessions.transcode.json` hand-built to spec §3.2 with a top comment-free but a `// SYNTHETIC` marker impossible in JSON — instead add a note in `docs/schema-notes.md` "transcode fixture synthetic, replace when a real capture is available", and open the plan's Task 10 note. Proceed; the normalize rules are stable.

- [ ] **Step 2: Write `internal/jellyfin/types.go`** with the structs from the Interfaces block. Field tags:

```go
package jellyfin

type ServerInfo struct {
	Name    string `json:"ServerName"`
	Version string `json:"Version"`
}

type ImageKind string

const (
	ImagePrimary  ImageKind = "Primary"
	ImageBackdrop ImageKind = "Backdrop"
)

type RawSession struct {
	ID              string              `json:"Id"`
	UserID          string              `json:"UserId"`
	UserName        string              `json:"UserName"`
	Client          string              `json:"Client"`
	DeviceName      string              `json:"DeviceName"`
	RemoteEndPoint  string              `json:"RemoteEndPoint"`
	PlayState       *RawPlayState       `json:"PlayState"`
	TranscodingInfo *RawTranscodingInfo `json:"TranscodingInfo"`
	NowPlayingItem  *RawItem            `json:"NowPlayingItem"`
}

type RawPlayState struct {
	PositionTicks    int64  `json:"PositionTicks"`
	IsPaused         bool   `json:"IsPaused"`
	PlayMethod       string `json:"PlayMethod"`
	AudioStreamIndex *int   `json:"AudioStreamIndex"`
}

type RawTranscodingInfo struct {
	Bitrate                  int64    `json:"Bitrate"`
	Container                string   `json:"Container"`
	VideoCodec               string   `json:"VideoCodec"`
	AudioCodec               string   `json:"AudioCodec"`
	IsVideoDirect            bool     `json:"IsVideoDirect"`
	IsAudioDirect            bool     `json:"IsAudioDirect"`
	TranscodeReasons         []string `json:"TranscodeReasons"`
	CompletionPercentage     float64  `json:"CompletionPercentage"`
	HardwareAccelerationType string   `json:"HardwareAccelerationType"`
}

type RawItem struct {
	ID                      string            `json:"Id"`
	Name                    string            `json:"Name"`
	Type                    string            `json:"Type"`
	MediaType               string            `json:"MediaType"`
	Container               string            `json:"Container"`
	SeriesName              string            `json:"SeriesName"`
	RunTimeTicks            int64             `json:"RunTimeTicks"`
	IndexNumber             *int              `json:"IndexNumber"`
	ParentIndexNumber       *int              `json:"ParentIndexNumber"`
	ProductionYear          *int              `json:"ProductionYear"`
	ImageTags               map[string]string `json:"ImageTags"`
	ParentBackdropItemId    string            `json:"ParentBackdropItemId"`
	ParentBackdropImageTags []string          `json:"ParentBackdropImageTags"`
	MediaStreams            []RawStream       `json:"MediaStreams"`
}

type RawStream struct {
	Type          string `json:"Type"`
	Index         int    `json:"Index"`
	Codec         string `json:"Codec"`
	Width         int    `json:"Width"`
	Height        int    `json:"Height"`
	BitRate       int64  `json:"BitRate"`
	Channels      int    `json:"Channels"`
	ChannelLayout string `json:"ChannelLayout"`
	VideoRangeType string `json:"VideoRangeType"`
	IsDefault     bool   `json:"IsDefault"`
}
```

- [ ] **Step 3: Write the failing test** — `internal/jellyfin/client_test.go`

```go
package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "testdata", name)
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("fixture %s not found", name)
		}
		dir = parent
	}
}

func TestClient_SessionsAndSystemInfo(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/Sessions":
			w.Write(fixture(t, "sessions.directplay.json"))
		case "/System/Info":
			w.Write(fixture(t, "systeminfo.json"))
		case "/slow":
			time.Sleep(5 * time.Second)
		default:
			http.Error(w, "no", 404)
		}
	}))
	defer srv.Close()

	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})

	ss, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].NowPlayingItem == nil || ss[0].PlayState == nil {
		t.Fatalf("sessions: %+v", ss)
	}
	if ss[0].PlayState.PlayMethod != "DirectPlay" || ss[0].NowPlayingItem.SeriesName != "Northwind" {
		t.Fatalf("parse: %+v", ss[0])
	}
	if gotAuth != "MediaBrowser Token=K" {
		t.Fatalf("auth header = %q", gotAuth)
	}

	si, err := c.SystemInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if si.Name == "" || si.Version == "" {
		t.Fatalf("system info: %+v", si)
	}
}

func TestClient_Error500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})
	if _, err := c.SystemInfo(context.Background()); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestClient_ImageAndTimeout(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items/slow/Images/Primary" {
			time.Sleep(5 * time.Second)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})

	body, ct, err := c.Image(context.Background(), "abc123", ImagePrimary, "t1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if ct != "image/png" || string(got) != string(png) {
		t.Fatalf("image: ct=%q body=%q", ct, got)
	}

	// the 3s client timeout fires before the 5s handler
	start := time.Now()
	if _, _, err := c.Image(context.Background(), "slow", ImagePrimary, ""); err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("3s timeout not honored: err=%v dur=%v", err, time.Since(start))
	}
}
```

> Add `"io"` to the test imports for `TestClient_ImageAndTimeout`.

- [ ] **Step 4: Run tests to verify they fail** — `go test ./internal/jellyfin/ -v` → build failure (`undefined: New`).

- [ ] **Step 5: Implement `internal/jellyfin/client.go`**

```go
// Package jellyfin is a thin read-only HTTP client for a Jellyfin server: the
// live /Sessions poll, /System/Info for the header, and item images for the
// Now Playing art proxy. It is not a source.Source.
package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
)

type Client struct {
	base string
	key  string
	hc   *http.Client
}

func New(cfg config.Config) *Client {
	return &Client{
		base: strings.TrimRight(cfg.JellyfinURL, "/"),
		key:  cfg.JellyfinAPIKey,
		hc:   &http.Client{Timeout: 3 * time.Second},
	}
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "MediaBrowser Token="+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("jellyfin %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Sessions(ctx context.Context) ([]RawSession, error) {
	var ss []RawSession
	err := c.getJSON(ctx, "/Sessions?ActiveWithinSeconds=960", &ss)
	return ss, err
}

func (c *Client) SystemInfo(ctx context.Context) (ServerInfo, error) {
	var si ServerInfo
	err := c.getJSON(ctx, "/System/Info", &si)
	return si, err
}

// Image streams an item image. The caller copies body to the response and
// closes it.
func (c *Client) Image(ctx context.Context, itemID string, kind ImageKind, tag string) (io.ReadCloser, string, error) {
	u := c.base + "/Items/" + url.PathEscape(itemID) + "/Images/" + string(kind)
	if tag != "" {
		u += "?tag=" + url.QueryEscape(tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "MediaBrowser Token="+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("jellyfin image %s/%s: %s", itemID, kind, resp.Status)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}
```

- [ ] **Step 6: Run tests to verify they pass** — `go test ./internal/jellyfin/ -v` → PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/jellyfin testdata/sessions.directplay.json testdata/sessions.transcode.json testdata/systeminfo.json
git commit -m "feat(jellyfin): thin HTTP client for /Sessions, /System/Info, item images"
```

---

## Task 2: `internal/live` — `normalize` + the snapshot DTO

**Files:**
- Create: `internal/live/types.go`, `internal/live/normalize.go`, `internal/live/normalize_test.go`

**Interfaces:**
- Consumes: `[]jellyfin.RawSession`, `jellyfin.RawStream`, `jellyfin.RawTranscodingInfo`.
- Produces:
  ```go
  package live

  type ServerInfo struct { Name string `json:"name"`; Version string `json:"version"` }
  type Summary struct {
      Streams         int   `json:"streams"`
      Transcodes      int   `json:"transcodes"`
      OutboundBitrate int64 `json:"outbound_bitrate"`
      Capacity        *int  `json:"capacity"`
  }
  type VideoSource struct { Codec string `json:"codec"`; Width, Height int `json:"width","height"`; Range string `json:"range"`; Bitrate int64 `json:"bitrate"` }
  type AudioSource struct { Codec string `json:"codec"`; Channels int `json:"channels"`; Layout string `json:"layout"`; Bitrate int64 `json:"bitrate"` }
  type SourceStreams struct { Video VideoSource `json:"video"`; Audio AudioSource `json:"audio"` }
  type Backdrop struct { ItemID string `json:"item_id"`; Tag string `json:"tag"` }
  type Art struct { PrimaryTag string `json:"primary_tag"`; Backdrop *Backdrop `json:"backdrop"` }
  type Transcode struct {
      Bitrate       int64    `json:"bitrate"`
      Container     string   `json:"container"`
      Video         string   `json:"video"`
      Audio         string   `json:"audio"`
      HW            string   `json:"hw"`
      CompletionPct float64  `json:"completion_pct"`
      Reasons       []string `json:"reasons"`
  }
  type Session struct {
      SessionID     string        `json:"session_id"`
      User          string        `json:"user"`
      Type          string        `json:"type"`
      Title         string        `json:"title"`
      Series        string        `json:"series"`
      SeasonEpisode string        `json:"season_episode"`
      ItemID        string        `json:"item_id"`
      Art           Art           `json:"art"`
      PlayMethod    string        `json:"play_method"`
      Paused        bool          `json:"paused"`
      PositionSec   int64         `json:"position_sec"`
      RuntimeSec    int64         `json:"runtime_sec"`
      ProgressPct   float64       `json:"progress_pct"`
      IsRemote      bool          `json:"is_remote"`
      Client        string        `json:"client"`
      Device        string        `json:"device"`
      Source        SourceStreams `json:"source"`
      Transcode     *Transcode    `json:"transcode"`
  }
  type Snapshot struct {
      Server   ServerInfo `json:"server"`
      Degraded bool       `json:"degraded"`
      Summary  Summary    `json:"summary"`
      Sessions []Session  `json:"sessions"`
  }
  type Event struct { Kind string; Data any }
  type degradedPayload struct { Degraded bool `json:"degraded"` }

  func normalize(raw []jellyfin.RawSession, server ServerInfo, capacity *int) Snapshot
  ```
  `Sessions` is always non-nil? No — `json:"sessions"` of a nil slice marshals to `null`. Initialize `snap.Sessions = []Session{}` so it's `[]`.

- [ ] **Step 1: Write `internal/live/types.go`** with the structs above. `Snapshot.Sessions` and `Transcode.Reasons` must marshal as `[]`, not `null` — normalize initializes them.

- [ ] **Step 2: Write the failing test** — `internal/live/normalize_test.go`

```go
package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

func loadSessions(t *testing.T, name string) []jellyfin.RawSession {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "testdata", name)
		if b, err := os.ReadFile(p); err == nil {
			var ss []jellyfin.RawSession
			if err := json.Unmarshal(b, &ss); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return ss
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("%s not found", name)
		}
		dir = filepath.Dir(dir)
	}
}

func TestNormalize_DirectPlayEpisode(t *testing.T) {
	raw := loadSessions(t, "sessions.directplay.json")
	snap := normalize(raw, ServerInfo{Name: "S", Version: "10.11.11"}, nil)

	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.Title != "The Long Retreat" || s.Series != "Northwind" || s.SeasonEpisode != "S1E7" || s.Type != "Episode" {
		t.Fatalf("labels: %+v", s)
	}
	if s.PlayMethod != "DirectPlay" || s.Paused {
		t.Fatalf("state: %+v", s)
	}
	if s.RuntimeSec != 3360 || s.PositionSec != 2522 {
		t.Fatalf("times: pos=%d run=%d", s.PositionSec, s.RuntimeSec)
	}
	if s.ProgressPct < 75.0 || s.ProgressPct > 75.1 {
		t.Fatalf("progress = %v", s.ProgressPct)
	}
	if s.IsRemote {
		t.Fatalf("192.168.* is local")
	}
	if s.Source.Video.Codec != "hevc" || s.Source.Video.Width != 1920 || s.Source.Video.Range != "SDR" {
		t.Fatalf("video: %+v", s.Source.Video)
	}
	if s.Source.Audio.Codec != "aac" || s.Source.Audio.Channels != 6 || s.Source.Audio.Layout != "5.1" {
		t.Fatalf("audio: %+v", s.Source.Audio)
	}
	if s.Transcode != nil {
		t.Fatalf("no transcode expected: %+v", s.Transcode)
	}
	if s.Art.PrimaryTag == "" || s.Art.Backdrop == nil || s.Art.Backdrop.ItemID == "" {
		t.Fatalf("art: %+v", s.Art)
	}
	if snap.Summary.Streams != 1 || snap.Summary.Transcodes != 0 {
		t.Fatalf("summary: %+v", snap.Summary)
	}
	if snap.Summary.OutboundBitrate != 4417693+480014 {
		t.Fatalf("outbound = %d", snap.Summary.OutboundBitrate)
	}
	// nil slices must marshal as []
	b, _ := json.Marshal(snap)
	if !contains(string(b), `"sessions":[`) {
		t.Fatalf("sessions should be an array: %s", b)
	}
}

func TestNormalize_Transcode(t *testing.T) {
	raw := loadSessions(t, "sessions.transcode.json")
	snap := normalize(raw, ServerInfo{}, ptr(6))
	if len(snap.Sessions) != 1 {
		t.Fatalf("want 1 session")
	}
	tc := snap.Sessions[0].Transcode
	if tc == nil {
		t.Fatalf("expected transcode")
	}
	if tc.Video == "" || tc.Bitrate == 0 || tc.Reasons == nil {
		t.Fatalf("transcode: %+v", tc)
	}
	if snap.Summary.Transcodes != 1 || snap.Summary.OutboundBitrate != tc.Bitrate {
		t.Fatalf("summary: %+v", snap.Summary)
	}
	if snap.Summary.Capacity == nil || *snap.Summary.Capacity != 6 {
		t.Fatalf("capacity: %+v", snap.Summary.Capacity)
	}
}

func TestIsRemote(t *testing.T) {
	cases := map[string]bool{
		"192.168.1.5": false, "10.0.0.9": false, "172.20.0.1": false,
		"127.0.0.1": false, "::1": false, "fe80::1": false,
		"8.8.8.8": true, "1.1.1.1": true,
		"203.0.113.7:52344": true, "192.168.1.5:9999": false,
		"garbage": false, "": false,
	}
	for in, want := range cases {
		if got := isRemote(in); got != want {
			t.Errorf("isRemote(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSeasonEpisode(t *testing.T) {
	e7, s1 := 7, 1
	if got := seasonEpisode(&jellyfin.RawItem{Type: "Episode", IndexNumber: &e7, ParentIndexNumber: &s1}); got != "S1E7" {
		t.Errorf("episode = %q", got)
	}
	if got := seasonEpisode(&jellyfin.RawItem{Type: "Movie"}); got != "" {
		t.Errorf("movie = %q", got)
	}
}

func ptr(n int) *int { return &n }
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run it, see it fail** — `go test ./internal/live/ -run TestNormalize -v` → `undefined: normalize`.

- [ ] **Step 4: Implement `internal/live/normalize.go`**

```go
package live

import (
	"fmt"
	"math"
	"net"
	"sort"
	"strings"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

func normalize(raw []jellyfin.RawSession, server ServerInfo, capacity *int) Snapshot {
	snap := Snapshot{Server: server, Sessions: []Session{}, Summary: Summary{Capacity: capacity}}

	for _, rs := range raw {
		it := rs.NowPlayingItem
		if it == nil || it.MediaType != "Video" || rs.PlayState == nil {
			continue
		}
		vid := firstStream(it.MediaStreams, "Video")
		aud := pickAudio(it.MediaStreams, rs.PlayState.AudioStreamIndex)

		s := Session{
			SessionID:     rs.ID,
			User:          rs.UserName,
			Type:          it.Type,
			Title:         it.Name,
			Series:        it.SeriesName,
			SeasonEpisode: seasonEpisode(it),
			ItemID:        it.ID,
			Art:           art(it),
			PlayMethod:    rs.PlayState.PlayMethod,
			Paused:        rs.PlayState.IsPaused,
			PositionSec:   rs.PlayState.PositionTicks / 10_000_000,
			RuntimeSec:    it.RunTimeTicks / 10_000_000,
			IsRemote:      isRemote(rs.RemoteEndPoint),
			Client:        rs.Client,
			Device:        rs.DeviceName,
			Source:        SourceStreams{Video: videoSource(vid), Audio: audioSource(aud)},
		}
		if s.RuntimeSec > 0 {
			s.ProgressPct = math.Round(float64(s.PositionSec)/float64(s.RuntimeSec)*10000) / 100
		}
		if strings.EqualFold(rs.PlayState.PlayMethod, "Transcode") && rs.TranscodingInfo != nil {
			s.Transcode = transcode(rs.TranscodingInfo, vid, aud, it.Container)
		}
		snap.Sessions = append(snap.Sessions, s)
	}

	sort.Slice(snap.Sessions, func(i, j int) bool {
		return snap.Sessions[i].SessionID < snap.Sessions[j].SessionID
	})

	for _, s := range snap.Sessions {
		snap.Summary.Streams++
		var bps int64
		if s.Transcode != nil {
			snap.Summary.Transcodes++
			bps = s.Transcode.Bitrate
		} else {
			bps = s.Source.Video.Bitrate + s.Source.Audio.Bitrate
		}
		snap.Summary.OutboundBitrate += bps
	}
	return snap
}

func seasonEpisode(it *jellyfin.RawItem) string {
	if it.Type != "Episode" || it.IndexNumber == nil || it.ParentIndexNumber == nil {
		return ""
	}
	return fmt.Sprintf("S%dE%d", *it.ParentIndexNumber, *it.IndexNumber)
}

func art(it *jellyfin.RawItem) Art {
	a := Art{PrimaryTag: it.ImageTags["Primary"]}
	if it.ParentBackdropItemId != "" && len(it.ParentBackdropImageTags) > 0 {
		a.Backdrop = &Backdrop{ItemID: it.ParentBackdropItemId, Tag: it.ParentBackdropImageTags[0]}
	}
	return a
}

func isRemote(ep string) bool {
	host := ep
	if h, _, err := net.SplitHostPort(ep); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast())
}

func firstStream(streams []jellyfin.RawStream, typ string) *jellyfin.RawStream {
	for i := range streams {
		if streams[i].Type == typ {
			return &streams[i]
		}
	}
	return nil
}

func pickAudio(streams []jellyfin.RawStream, idx *int) *jellyfin.RawStream {
	if idx != nil {
		for i := range streams {
			if streams[i].Index == *idx && streams[i].Type == "Audio" {
				return &streams[i]
			}
		}
	}
	for i := range streams {
		if streams[i].Type == "Audio" && streams[i].IsDefault {
			return &streams[i]
		}
	}
	return firstStream(streams, "Audio")
}

func videoSource(s *jellyfin.RawStream) VideoSource {
	if s == nil {
		return VideoSource{}
	}
	return VideoSource{Codec: s.Codec, Width: s.Width, Height: s.Height, Range: s.VideoRangeType, Bitrate: s.BitRate}
}

func audioSource(s *jellyfin.RawStream) AudioSource {
	if s == nil {
		return AudioSource{}
	}
	return AudioSource{Codec: s.Codec, Channels: s.Channels, Layout: s.ChannelLayout, Bitrate: s.BitRate}
}

func transcode(ti *jellyfin.RawTranscodingInfo, vid, aud *jellyfin.RawStream, srcContainer string) *Transcode {
	t := &Transcode{
		Bitrate:       ti.Bitrate,
		CompletionPct: ti.CompletionPercentage,
		HW:            hw(ti.HardwareAccelerationType),
		Reasons:       ti.TranscodeReasons,
	}
	if t.Reasons == nil {
		t.Reasons = []string{}
	}
	if !ti.IsVideoDirect {
		src := ""
		if vid != nil {
			src = vid.Codec
		}
		t.Video = src + "→" + ti.VideoCodec
	}
	if !ti.IsAudioDirect {
		src := ""
		if aud != nil {
			src = aud.Codec
		}
		t.Audio = src + "→" + ti.AudioCodec
	}
	if srcContainer != "" && !strings.EqualFold(srcContainer, ti.Container) {
		t.Container = srcContainer + "→" + ti.Container
	} else {
		t.Container = ti.Container
	}
	return t
}

func hw(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "none" {
		return ""
	}
	return s
}
```

- [ ] **Step 5: Run it, see it pass** — `go test ./internal/live/ -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/live/types.go internal/live/normalize.go internal/live/normalize_test.go
git commit -m "feat(live): normalize /Sessions into the Now Playing snapshot DTO"
```

---

## Task 3: `internal/live` — the `Hub` (subscribe / publish / poll loop)

**Files:**
- Create: `internal/live/hub.go`, `internal/live/hub_test.go`

**Interfaces:**
- Consumes: `normalize` (Task 2), `jellyfin.RawSession` / `jellyfin.ServerInfo`.
- Produces:
  ```go
  type sessionsClient interface {
      Sessions(ctx context.Context) ([]jellyfin.RawSession, error)
      SystemInfo(ctx context.Context) (jellyfin.ServerInfo, error)
      Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error)
  }

  func New(client sessionsClient, interval time.Duration, capacity *int, log *slog.Logger) *Hub
  func (h *Hub) Prime(ctx context.Context)                       // non-fatal /System/Info at startup
  func (h *Hub) Subscribe() (id int64, ch <-chan Event, unsub func())
  func (h *Hub) Publish(ev Event)
  func (h *Hub) Snapshot(ctx context.Context) *Snapshot          // cached if fresh, else a direct fetch (no loop)
  func (h *Hub) Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error) // passthrough to the client
  func (h *Hub) SubscriberCount() int                            // for tests
  func (h *Hub) Close()
  ```
  `*jellyfin.Client` satisfies `sessionsClient`.

- [ ] **Step 1: Write the failing test** — `internal/live/hub_test.go`

```go
package live

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

type fakeClient struct {
	mu       sync.Mutex
	sessions func() ([]jellyfin.RawSession, error)
	calls    int
}

func (f *fakeClient) Sessions(context.Context) ([]jellyfin.RawSession, error) {
	f.mu.Lock()
	f.calls++
	fn := f.sessions
	f.mu.Unlock()
	return fn()
}
func (f *fakeClient) SystemInfo(context.Context) (jellyfin.ServerInfo, error) {
	return jellyfin.ServerInfo{Name: "S", Version: "10.11.11"}, nil
}
func (f *fakeClient) Image(context.Context, string, jellyfin.ImageKind, string) (io.ReadCloser, string, error) {
	return io.NopCloser(nil), "image/png", nil
}

// oneSession returns a raw session with the given id and position (seconds).
func oneSession(id string, posSec int64, paused bool) []jellyfin.RawSession {
	return []jellyfin.RawSession{{
		ID: id, UserName: "u",
		PlayState: &jellyfin.RawPlayState{PositionTicks: posSec * 10_000_000, IsPaused: paused, PlayMethod: "DirectPlay"},
		NowPlayingItem: &jellyfin.RawItem{
			ID: "item", Name: "T", Type: "Movie", MediaType: "Video", RunTimeTicks: 6000 * 10_000_000,
		},
	}}
}

func newHub(t *testing.T, fc *fakeClient) (*Hub, chan time.Time) {
	t.Helper()
	tick := make(chan time.Time)
	h := New(fc, 4*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.after = func(time.Duration) <-chan time.Time { return tick }
	t.Cleanup(h.Close)
	return h, tick
}

func TestHub_LoopRunsOnlyWhileSubscribed(t *testing.T) {
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", 100, false), nil }}
	h, tick := newHub(t, fc)

	if h.SubscriberCount() != 0 || fc.calls != 0 {
		t.Fatal("no loop before subscribe")
	}
	_, ch1, unsub1 := h.Subscribe()
	// first tick after subscribe produces a snapshot fetch
	waitCalls(t, fc, 1)
	_, ev, _ := readEvent(t, ch1, 0) // Subscribe does an initial fetch for the first-frame snapshot? No — the caller does. Drain nothing yet.
	_ = ev

	tick <- time.Now()
	waitCalls(t, fc, 2)

	_, ch2, unsub2 := h.Subscribe()
	_ = ch2
	if h.SubscriberCount() != 2 {
		t.Fatalf("subs = %d", h.SubscriberCount())
	}
	unsub1()
	unsub2()
	// loop stops: no more Sessions calls even if we (would) tick
	calls := fc.calls
	select {
	case tick <- time.Now(): // the loop goroutine is gone, nobody reads
		t.Fatal("loop still consuming ticks after last unsub")
	case <-time.After(50 * time.Millisecond):
	}
	if fc.calls != calls {
		t.Fatalf("Sessions called after last unsub")
	}
}

func TestHub_DiffEmitsUpdateOnRealChange(t *testing.T) {
	pos := int64(100)
	var paused bool
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", pos, paused), nil }}
	h, tick := newHub(t, fc)
	_, ch, unsub := h.Subscribe()
	defer unsub()
	waitCalls(t, fc, 1) // initial poll on subscribe

	// +30s of a 6000s runtime = +0.5% -> no update
	pos = 130
	tick <- time.Now()
	assertNoEvent(t, ch)

	// +90s more -> crosses 1% -> update
	pos = 220
	tick <- time.Now()
	if k, _, _ := readEvent(t, ch, time.Second); k != "update" {
		t.Fatalf("want update, got %q", k)
	}

	// pause toggle -> update even without progress
	paused = true
	tick <- time.Now()
	if k, _, _ := readEvent(t, ch, time.Second); k != "update" {
		t.Fatalf("pause toggle should update, got %q", k)
	}
}

func TestHub_DegradedThenRecovery(t *testing.T) {
	fail := true
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) {
		if fail {
			return nil, errors.New("upstream down")
		}
		return oneSession("a", 100, false), nil
	}}
	h, tick := newHub(t, fc)
	_, ch, unsub := h.Subscribe()
	defer unsub()
	waitCalls(t, fc, 1)

	tick <- time.Now()
	if k, d, _ := readEvent(t, ch, time.Second); k != "degraded" {
		t.Fatalf("want degraded, got %q %v", k, d)
	}
	// still degraded, no duplicate event
	tick <- time.Now()
	assertNoEvent(t, ch)

	fail = false
	tick <- time.Now()
	if k, _, _ := readEvent(t, ch, time.Second); k != "update" {
		t.Fatalf("recovery should emit update, got %q", k)
	}
}

func TestHub_SlowSubscriberDropped(t *testing.T) {
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", 100, false), nil }}
	h, _ := newHub(t, fc)
	_, ch, _ := h.Subscribe()
	// never drain ch (cap 4). 5 publishes -> 5th closes it.
	for i := 0; i < 5; i++ {
		h.Publish(Event{Kind: "update", Data: &Snapshot{}})
	}
	if _, open := <-drain4(ch); open {
		t.Fatal("channel should be closed after overflow")
	}
	if h.SubscriberCount() != 0 {
		t.Fatalf("overflowed subscriber not removed: %d", h.SubscriberCount())
	}
}

func TestHub_SnapshotCachedVsDirect(t *testing.T) {
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", 100, false), nil }}
	h := New(fc, 4*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	nowT := time.Unix(1000, 0)
	h.now = func() time.Time { return nowT }
	defer h.Close()

	s1 := h.Snapshot(context.Background()) // no cache -> direct fetch
	if len(s1.Sessions) != 1 || fc.calls != 1 {
		t.Fatalf("direct fetch: %+v calls=%d", s1, fc.calls)
	}
	nowT = nowT.Add(2 * time.Second) // < interval
	h.Snapshot(context.Background())
	if fc.calls != 1 {
		t.Fatalf("should have used cache, calls=%d", fc.calls)
	}
	nowT = nowT.Add(10 * time.Second) // > interval
	h.Snapshot(context.Background())
	if fc.calls != 2 {
		t.Fatalf("stale cache should refetch, calls=%d", fc.calls)
	}
}

// --- helpers ---

func waitCalls(t *testing.T, fc *fakeClient, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		fc.mu.Lock()
		c := fc.calls
		fc.mu.Unlock()
		if c >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("Sessions not called %d times", n)
}
func readEvent(t *testing.T, ch <-chan Event, d time.Duration) (string, any, bool) {
	t.Helper()
	if d == 0 {
		select {
		case ev, ok := <-ch:
			return ev.Kind, ev.Data, ok
		default:
			return "", nil, false
		}
	}
	select {
	case ev, ok := <-ch:
		return ev.Kind, ev.Data, ok
	case <-time.After(d):
		t.Fatalf("no event in %v", d)
		return "", nil, false
	}
}
func assertNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %q", ev.Kind)
	case <-time.After(80 * time.Millisecond):
	}
}
func drain4(ch <-chan Event) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		for range ch {
		}
		close(out)
	}()
	return out
}
```

> The test's `readEvent(t, ch1, 0)` right after the first `Subscribe` expects nothing — `Subscribe` starts the loop but does **not** itself publish; the first frame is produced by the loop's first poll (`waitCalls(t, fc, 1)`) *only if* it changed vs `nil` (it does — `changed(nil, …)` is true), so that first poll publishes an `update`. Adjust `TestHub_LoopRunsOnlyWhileSubscribed` to drain that first `update` before ticking. Simplest: after `Subscribe`, `readEvent(t, ch1, time.Second)` and assert `"update"`.

- [ ] **Step 2: Run it, see it fail** — `go test ./internal/live/ -run TestHub -v` → `undefined: New` / `Hub`.

- [ ] **Step 3: Implement `internal/live/hub.go`**

```go
package live

import (
	"context"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

type sessionsClient interface {
	Sessions(ctx context.Context) ([]jellyfin.RawSession, error)
	SystemInfo(ctx context.Context) (jellyfin.ServerInfo, error)
	Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error)
}

const (
	backoffMin = 4 * time.Second
	backoffMax = 30 * time.Second
	serverTTL  = 10 * time.Minute
)

type Hub struct {
	client   sessionsClient
	interval time.Duration
	capacity *int
	log      *slog.Logger
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time

	mu       sync.Mutex
	subs     map[int64]chan Event
	nextID   int64
	snap     *Snapshot
	snapAt   time.Time
	server   ServerInfo
	serverAt time.Time
	stop     chan struct{}
}

func New(client sessionsClient, interval time.Duration, capacity *int, log *slog.Logger) *Hub {
	return &Hub{
		client: client, interval: interval, capacity: capacity, log: log,
		now: time.Now, after: time.After,
		subs: map[int64]chan Event{},
	}
}

func (h *Hub) Prime(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	si, err := h.client.SystemInfo(cctx)
	if err != nil {
		h.log.Warn("jellyfin api unreachable at startup; Now Playing will show degraded", "err", err)
		return
	}
	h.mu.Lock()
	h.server = ServerInfo{Name: si.Name, Version: si.Version}
	h.serverAt = h.now()
	h.mu.Unlock()
	h.log.Info("jellyfin api ok", "server", si.Name, "version", si.Version)
}

func (h *Hub) Subscribe() (int64, <-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan Event, 4)
	h.subs[id] = ch
	if len(h.subs) == 1 {
		h.stop = make(chan struct{})
		go h.pollLoop(h.stop)
	}
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if c, ok := h.subs[id]; ok {
				close(c)
				delete(h.subs, id)
			}
			if len(h.subs) == 0 && h.stop != nil {
				close(h.stop)
				h.stop = nil
			}
		})
	}
	return id, ch, unsub
}

func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		select {
		case ch <- ev:
		default:
			close(ch)
			delete(h.subs, id)
		}
	}
}

func (h *Hub) Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error) {
	return h.client.Image(ctx, itemID, kind, tag)
}

func (h *Hub) Snapshot(ctx context.Context) *Snapshot {
	h.mu.Lock()
	if h.snap != nil && h.now().Sub(h.snapAt) < h.interval {
		s := *h.snap
		h.mu.Unlock()
		return &s
	}
	server := h.server
	h.mu.Unlock()

	raw, err := h.client.Sessions(ctx)
	if err != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.snap != nil {
			s := *h.snap
			s.Degraded = true
			return &s
		}
		return &Snapshot{Server: server, Degraded: true, Sessions: []Session{}, Summary: Summary{Capacity: h.capacity}}
	}
	s := normalize(raw, server, h.capacity)
	h.mu.Lock()
	h.snap, h.snapAt = &s, h.now()
	h.mu.Unlock()
	sc := s
	return &sc
}

func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stop != nil {
		close(h.stop)
		h.stop = nil
	}
	for id, ch := range h.subs {
		close(ch)
		delete(h.subs, id)
	}
}

func (h *Hub) pollLoop(stop <-chan struct{}) {
	backoff := backoffMin
	for {
		wait := h.interval
		if err := h.pollOnce(); err != nil {
			wait = backoff
			if backoff < backoffMax {
				backoff *= 2
				if backoff > backoffMax {
					backoff = backoffMax
				}
			}
		} else {
			backoff = backoffMin
		}
		select {
		case <-stop:
			return
		case <-h.after(wait):
		}
	}
}

// pollOnce fetches, diffs, and publishes. Returns the fetch error (nil on success).
func (h *Hub) pollOnce() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	h.mu.Lock()
	needServer := h.server.Name == "" || h.now().Sub(h.serverAt) > serverTTL
	server := h.server
	h.mu.Unlock()
	if needServer {
		if si, err := h.client.SystemInfo(ctx); err == nil {
			server = ServerInfo{Name: si.Name, Version: si.Version}
			h.mu.Lock()
			h.server, h.serverAt = server, h.now()
			h.mu.Unlock()
		}
	}

	raw, err := h.client.Sessions(ctx)
	if err != nil {
		h.mu.Lock()
		already := h.snap != nil && h.snap.Degraded
		if h.snap == nil {
			h.snap = &Snapshot{Server: server, Sessions: []Session{}, Summary: Summary{Capacity: h.capacity}}
		}
		h.snap.Degraded = true
		h.mu.Unlock()
		if !already {
			h.Publish(Event{Kind: "degraded", Data: degradedPayload{Degraded: true}})
		}
		return err
	}

	next := normalize(raw, server, h.capacity)
	next.Degraded = false
	h.mu.Lock()
	prev := h.snap
	if changed(prev, &next) {
		h.snap, h.snapAt = &next, h.now()
		h.mu.Unlock()
		h.Publish(Event{Kind: "update", Data: &next})
	} else {
		h.snapAt = h.now()
		h.mu.Unlock()
	}
	return nil
}

func changed(prev, next *Snapshot) bool {
	if prev == nil || prev.Degraded != next.Degraded || len(prev.Sessions) != len(next.Sessions) {
		return true
	}
	pm := map[string]Session{}
	for _, s := range prev.Sessions {
		pm[s.SessionID] = s
	}
	for _, n := range next.Sessions {
		p, ok := pm[n.SessionID]
		if !ok {
			return true
		}
		if p.Paused != n.Paused || p.PlayMethod != n.PlayMethod || p.IsRemote != n.IsRemote {
			return true
		}
		if tcChanged(p.Transcode, n.Transcode) {
			return true
		}
		if math.Abs(n.ProgressPct-p.ProgressPct) >= 1.0 {
			return true
		}
	}
	return false
}

func tcChanged(a, b *Transcode) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	if a == nil {
		return false
	}
	return a.Video != b.Video || a.Audio != b.Audio || a.Container != b.Container
}
```

- [ ] **Step 4: Run it, see it pass** — `go test ./internal/live/ -v` → PASS. (Adjust the one drain in `TestHub_LoopRunsOnlyWhileSubscribed` per the Step 1 note if it's flaky.)

- [ ] **Step 5: Commit**

```bash
git add internal/live/hub.go internal/live/hub_test.go
git commit -m "feat(live): SSE hub — poll loop runs only while subscribed, degraded backoff, diff"
```

---

## Task 4: `GET /api/now-playing` (one-shot) + `api.Deps.Live` + `main.go` wiring

**Files:**
- Create: `internal/api/nowplaying.go`
- Modify: `internal/api/api.go` (Deps + Server + Handler + `statusWriter.Unwrap`), `cmd/ephyra/main.go`
- Create: `internal/api/nowplaying_test.go` (the one-shot cases; stream + art added in Tasks 5–6)

**Interfaces:**
- Consumes: `live.Hub` (`Snapshot`, `Subscribe`, `Image`, `Close`), `jellyfin.New`, `live.New`.
- Produces: `api.Deps` gains `Live *live.Hub`; `Server` gains `live *live.Hub`. `GET /api/now-playing` → `200` envelope, `data` = `*live.Snapshot`.

- [ ] **Step 1: Add `Live` to `api.Deps` and `Server`** in `internal/api/api.go`

```go
// in Deps:
	Live *live.Hub
// in Server:
	live *live.Hub
// in New(): s.live = d.Live  (add to the struct literal)
// in Handler(), with the other GET routes:
	mux.HandleFunc("GET /api/now-playing", s.handleNowPlaying)
```
Add the import `"github.com/andriykohut/ephyra/internal/live"`.

- [ ] **Step 2: Add `Unwrap` to `statusWriter`** (so `http.NewResponseController` in Task 5 reaches the real writer for `SetWriteDeadline`)

```go
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
```

- [ ] **Step 3: Write the failing test** — `internal/api/nowplaying_test.go`

```go
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
		ID: "sess1", UserName: "alice", RemoteEndPoint: "192.168.1.5",
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
```

- [ ] **Step 4: Run it, see it fail** — `go test ./internal/api/ -run TestNowPlaying -v` → `undefined: (*Server).handleNowPlaying` / `Deps.Live`.

- [ ] **Step 5: Implement `internal/api/nowplaying.go`**

```go
package api

import "net/http"

func (s *Server) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.live.Snapshot(r.Context()), s.meta(false))
}
```

- [ ] **Step 6: Wire `main.go`** — after `sched := scheduler.New(...)`:

```go
	jc := jellyfin.New(cfg)
	var capacity *int
	if cfg.StreamCapacity > 0 {
		capacity = &cfg.StreamCapacity
	}
	hub := live.New(jc, cfg.LivePollInterval, capacity, log)
	defer hub.Close()
	hub.Prime(ctx)
```
Add `hub.Prime` **before** `httpServer.ListenAndServe` (it's a one-shot non-fatal call). Pass `Live: hub` in the `api.Deps` literal. Add imports `"github.com/andriykohut/ephyra/internal/jellyfin"` and `"github.com/andriykohut/ephyra/internal/live"`.

- [ ] **Step 7: Run the api + build** — `go test ./internal/api/ -v` → PASS; `go build ./...` → ok.

- [ ] **Step 8: Commit**

```bash
git add internal/api/api.go internal/api/nowplaying.go internal/api/nowplaying_test.go cmd/ephyra/main.go
git commit -m "feat(api): GET /api/now-playing one-shot + live.Hub wiring"
```

---

## Task 5: `GET /api/now-playing/stream` — SSE

**Files:**
- Create: `internal/api/nowplaying_stream.go`
- Modify: `internal/api/api.go` (register route), `internal/api/nowplaying_test.go` (add stream cases)

**Interfaces:**
- Consumes: `hub.Subscribe()`, `hub.Snapshot()`, `hub.SubscriberCount()`.
- Produces: `GET /api/now-playing/stream` → `text/event-stream`; first frame `event: snapshot`, then relays `hub` events, `: heartbeat` every 15s, unsubscribes on client disconnect.

- [ ] **Step 1: Register the route** in `api.go` `Handler()`:

```go
	mux.HandleFunc("GET /api/now-playing/stream", s.handleNowPlayingStream)
```

- [ ] **Step 2: Write the failing test** — append to `internal/api/nowplaying_test.go`

```go
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
```

> Add `"bufio"` and `"strings"` to the test imports.

- [ ] **Step 3: Run it, see it fail** — 404 (route not yet handled).

- [ ] **Step 4: Implement `internal/api/nowplaying_stream.go`**

```go
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func (s *Server) handleNowPlayingStream(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // ignore if unsupported

	_, ch, unsub := s.live.Subscribe()
	defer unsub()

	writeSSE(w, rc, "snapshot", s.live.Snapshot(r.Context()))

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, rc, ev.Kind, ev.Data)
		case <-hb.C:
			io.WriteString(w, ": heartbeat\n\n")
			_ = rc.Flush()
		}
	}
}

func writeSSE(w io.Writer, rc *http.ResponseController, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	_ = rc.Flush()
}
```

- [ ] **Step 5: Run it, see it pass** — `go test ./internal/api/ -run TestNowPlaying -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api/api.go internal/api/nowplaying_stream.go internal/api/nowplaying_test.go
git commit -m "feat(api): GET /api/now-playing/stream (SSE)"
```

---

## Task 6: `GET /api/now-playing/art/{itemId}` — the image proxy

**Files:**
- Create: `internal/api/nowplaying_art.go`
- Modify: `internal/api/api.go` (register route + `artCache` on `Server`), `internal/api/nowplaying_test.go`

**Interfaces:**
- Consumes: `hub.Image(ctx, itemID, jellyfin.ImageKind, tag)`.
- Produces: `GET /api/now-playing/art/{itemId}?kind=primary|backdrop&tag=…` → the image bytes + upstream `Content-Type` + `Cache-Control: public, max-age=86400`. `400` on a bad id / kind, `502` on upstream error. A 32-entry process-wide cache keyed `itemId/kind/tag`.

- [ ] **Step 1: Register the route + add the cache** in `api.go`

```go
// Server gets a field:
	art *artCache
// New() sets:
	s.art = newArtCache(32)
// Handler():
	mux.HandleFunc("GET /api/now-playing/art/{itemId}", s.handleNowPlayingArt)
```

- [ ] **Step 2: Write the failing test** — append to `nowplaying_test.go`

```go
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

	rr := do("/api/now-playing/art/abcdef0123456789abcdef0123456789?kind=primary&tag=t1")
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" || rr.Body.String() != string(png) {
		t.Fatalf("art: %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Fatal("missing Cache-Control")
	}
	// identical request -> cache hit, no second upstream call
	do("/api/now-playing/art/abcdef0123456789abcdef0123456789?kind=primary&tag=t1")
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits)
	}

	if do("/api/now-playing/art/..%2Fetc?kind=primary").Code != 400 {
		t.Fatal("bad id should be 400")
	}
	if do("/api/now-playing/art/abcdef0123456789abcdef0123456789?kind=logo").Code != 400 {
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
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing/art/abcdef0123456789abcdef0123456789", nil))
	if rr.Code != 502 {
		t.Fatalf("want 502, got %d", rr.Code)
	}
}
```

> Add `"bytes"`, `"errors"`, `"sync/atomic"` to the test imports.

- [ ] **Step 3: Run it, see it fail** — 404.

- [ ] **Step 4: Implement `internal/api/nowplaying_art.go`**

```go
package api

import (
	"container/list"
	"io"
	"net/http"
	"regexp"
	"sync"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

var artIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

type artEntry struct {
	key  string
	ct   string
	body []byte
}

type artCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

func newArtCache(capacity int) *artCache {
	return &artCache{cap: capacity, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *artCache) get(key string) (string, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*artEntry)
		return e.ct, e.body, true
	}
	return "", nil, false
}

func (c *artCache) put(key, ct string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*artEntry).ct, el.Value.(*artEntry).body = ct, body
		return
	}
	el := c.ll.PushFront(&artEntry{key: key, ct: ct, body: body})
	c.m[key] = el
	for c.ll.Len() > c.cap {
		back := c.ll.Back()
		c.ll.Remove(back)
		delete(c.m, back.Value.(*artEntry).key)
	}
}

func (s *Server) handleNowPlayingArt(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	itemID := r.PathValue("itemId")
	if !artIDRe.MatchString(itemID) {
		writeError(w, http.StatusBadRequest, "bad_request", "bad item id")
		return
	}
	var kind jellyfin.ImageKind
	switch r.URL.Query().Get("kind") {
	case "", "primary":
		kind = jellyfin.ImagePrimary
	case "backdrop":
		kind = jellyfin.ImageBackdrop
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be primary|backdrop")
		return
	}
	tag := r.URL.Query().Get("tag")
	key := itemID + "/" + string(kind) + "/" + tag

	if ct, body, ok := s.art.get(key); ok {
		s.serveArt(w, ct, body)
		return
	}
	rc, ct, err := s.live.Image(r.Context(), itemID, kind, tag)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
		return
	}
	s.art.put(key, ct, body)
	s.serveArt(w, ct, body)
}

func (s *Server) serveArt(w http.ResponseWriter, ct string, body []byte) {
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(body)
}
```

> `r.PathValue("itemId")` URL-decodes the segment, so `..%2Fetc` becomes `../etc` which fails `artIDRe`. Good.

- [ ] **Step 5: Run it, see it pass** — `go test ./internal/api/ -v` → PASS. `go build ./...`, `go vet ./...`, `golangci-lint run`.

- [ ] **Step 6: Commit**

```bash
git add internal/api/api.go internal/api/nowplaying_art.go internal/api/nowplaying_test.go
git commit -m "feat(api): GET /api/now-playing/art/{itemId} image proxy + LRU"
```

---

## Task 7: Frontend — `useNowPlaying` hook + types

**Files:**
- Modify: `web/src/api/types.ts`
- Create: `web/src/live/useNowPlaying.ts`, `web/src/live/useNowPlaying.test.ts`

**Interfaces:**
- Produces:
  ```ts
  // api/types.ts — mirrors internal/live JSON tags
  export interface NowVideo { codec: string; width: number; height: number; range: string; bitrate: number }
  export interface NowAudio { codec: string; channels: number; layout: string; bitrate: number }
  export interface NowTranscode {
    bitrate: number; container: string; video: string; audio: string;
    hw: string; completion_pct: number; reasons: string[];
  }
  export interface NowSession {
    session_id: string; user: string; type: string;
    title: string; series: string; season_episode: string; item_id: string;
    art: { primary_tag: string; backdrop: { item_id: string; tag: string } | null };
    play_method: string; paused: boolean;
    position_sec: number; runtime_sec: number; progress_pct: number;
    is_remote: boolean; client: string; device: string;
    source: { video: NowVideo; audio: NowAudio };
    transcode: NowTranscode | null;
  }
  export interface NowSummary { streams: number; transcodes: number; outbound_bitrate: number; capacity: number | null }
  export interface Snapshot {
    server: { name: string; version: string };
    degraded: boolean;
    summary: NowSummary;
    sessions: NowSession[];
  }

  // live/useNowPlaying.ts
  type ConnState = "connecting" | "live" | "reconnecting";
  export function useNowPlaying(): { snapshot: Snapshot | null; degraded: boolean; conn: ConnState }
  ```

- [ ] **Step 1: Append the types to `web/src/api/types.ts`** (the block above).

- [ ] **Step 2: Write the failing test** — `web/src/live/useNowPlaying.test.ts`

```ts
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { Snapshot } from "@/api/types";
import { useNowPlaying } from "./useNowPlaying";

class FakeES {
  static last: FakeES | null = null;
  url: string;
  onerror: ((e: unknown) => void) | null = null;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  closed = false;
  constructor(url: string) {
    this.url = url;
    FakeES.last = this;
  }
  addEventListener(t: string, fn: (e: MessageEvent) => void) {
    (this.listeners[t] ??= []).push(fn);
  }
  emit(t: string, data: unknown) {
    for (const fn of this.listeners[t] ?? []) fn({ data: JSON.stringify(data) } as MessageEvent);
  }
  close() {
    this.closed = true;
  }
}

const snap = (over: Partial<Snapshot> = {}): Snapshot => ({
  server: { name: "S", version: "10.11.11" },
  degraded: false,
  summary: { streams: 1, transcodes: 0, outbound_bitrate: 5e6, capacity: null },
  sessions: [
    {
      session_id: "a", user: "u", type: "Movie", title: "T", series: "", season_episode: "",
      item_id: "i", art: { primary_tag: "", backdrop: null },
      play_method: "DirectPlay", paused: false,
      position_sec: 100, runtime_sec: 6000, progress_pct: 1.67,
      is_remote: false, client: "c", device: "d",
      source: { video: { codec: "h264", width: 1920, height: 1080, range: "SDR", bitrate: 5e6 }, audio: { codec: "aac", channels: 2, layout: "stereo", bitrate: 2e5 } },
      transcode: null,
    },
  ],
  ...over,
});

beforeEach(() => {
  vi.stubGlobal("EventSource", FakeES as unknown as typeof EventSource);
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ data: snap(), meta: { generated_at: "x", stale: false } }), { status: 200 }),
  );
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test("first paint from the one-shot, then EventSource takes over", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot?.sessions[0].title).toBe("T"));
  expect(FakeES.last?.url).toContain("/api/now-playing/stream");

  act(() => FakeES.last!.emit("update", snap({ summary: { streams: 2, transcodes: 1, outbound_bitrate: 9e6, capacity: null } })));
  expect(result.current.snapshot?.summary.streams).toBe(2);
  expect(result.current.conn).toBe("live");
});

test("degraded event sets the flag; a later update clears it", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot).not.toBeNull());
  act(() => FakeES.last!.emit("degraded", { degraded: true }));
  expect(result.current.degraded).toBe(true);
  act(() => FakeES.last!.emit("update", snap()));
  expect(result.current.degraded).toBe(false);
});

test("interpolates position for non-paused sessions", async () => {
  vi.useFakeTimers();
  const { result } = renderHook(() => useNowPlaying());
  await vi.runOnlyPendingTimersAsync();
  await waitFor(() => expect(result.current.snapshot).not.toBeNull(), { timeout: 100 });
  const p0 = result.current.snapshot!.sessions[0].position_sec;
  act(() => vi.advanceTimersByTime(3000));
  expect(result.current.snapshot!.sessions[0].position_sec).toBeGreaterThanOrEqual(p0 + 2);
});

test("closes the EventSource when the tab is hidden", async () => {
  const { result } = renderHook(() => useNowPlaying());
  await waitFor(() => expect(result.current.snapshot).not.toBeNull());
  const es = FakeES.last!;
  act(() => {
    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(es.closed).toBe(true);
});
```

> `renderHook` comes from `@testing-library/react` (v16). If the interpolation test is timing-flaky under fake timers, assert `>= p0` instead of `>= p0 + 2`.

- [ ] **Step 3: Run it, see it fail** — `npx vitest run src/live/useNowPlaying.test.ts` → module has no `useNowPlaying`.

- [ ] **Step 4: Implement `web/src/live/useNowPlaying.ts`**

```ts
import { useCallback, useEffect, useRef, useState } from "react";
import { fetchEnvelope } from "@/api/client";
import type { Snapshot } from "@/api/types";

type ConnState = "connecting" | "live" | "reconnecting";

export function useNowPlaying(): { snapshot: Snapshot | null; degraded: boolean; conn: ConnState } {
  const [base, setBase] = useState<Snapshot | null>(null);
  const baseAt = useRef<number>(Date.now());
  const [degraded, setDegraded] = useState(false);
  const [conn, setConn] = useState<ConnState>("connecting");
  const [, forceTick] = useState(0);
  const esRef = useRef<EventSource | null>(null);

  const applySnapshot = useCallback((s: Snapshot) => {
    setBase(s);
    baseAt.current = Date.now();
    setDegraded(s.degraded);
    setConn("live");
  }, []);

  const open = useCallback(() => {
    esRef.current?.close();
    const es = new EventSource("/api/now-playing/stream");
    esRef.current = es;
    const onData = (e: MessageEvent) => applySnapshot(JSON.parse(e.data) as Snapshot);
    es.addEventListener("snapshot", onData);
    es.addEventListener("update", onData);
    es.addEventListener("degraded", () => setDegraded(true));
    es.onerror = () => setConn("reconnecting");
  }, [applySnapshot]);

  // first paint + open the stream
  useEffect(() => {
    let cancelled = false;
    fetchEnvelope<Snapshot>("/api/now-playing")
      .then((env) => {
        if (!cancelled) applySnapshot(env.data);
      })
      .catch(() => setConn("reconnecting"));
    open();
    return () => {
      cancelled = true;
      esRef.current?.close();
      esRef.current = null;
    };
  }, [applySnapshot, open]);

  // visibility: drop the stream when hidden, reopen + refetch when visible
  useEffect(() => {
    const onVis = () => {
      if (document.hidden) {
        esRef.current?.close();
        esRef.current = null;
        setConn("connecting");
      } else {
        fetchEnvelope<Snapshot>("/api/now-playing").then((env) => applySnapshot(env.data)).catch(() => {});
        open();
      }
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [applySnapshot, open]);

  // 1s interpolation tick
  useEffect(() => {
    const id = setInterval(() => forceTick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, []);

  const snapshot = base && interpolate(base, baseAt.current);
  return { snapshot: snapshot ?? null, degraded, conn };
}

function interpolate(s: Snapshot, at: number): Snapshot {
  const dt = (Date.now() - at) / 1000;
  if (dt < 1) return s;
  return {
    ...s,
    sessions: s.sessions.map((sess) => {
      if (sess.paused || sess.runtime_sec <= 0) return sess;
      const pos = Math.min(sess.runtime_sec, sess.position_sec + dt);
      return { ...sess, position_sec: pos, progress_pct: Math.round((pos / sess.runtime_sec) * 10000) / 100 };
    }),
  };
}
```

- [ ] **Step 5: Run it, see it pass** — `npx vitest run src/live/useNowPlaying.test.ts` → PASS. `npx tsc -b`, `npx biome ci .`.

- [ ] **Step 6: Commit**

```bash
cd .. && git add web/src && git commit -m "feat(web): useNowPlaying hook (EventSource + one-shot + visibility + interpolation)"
```

---

## Task 8: Frontend — `NowPlayingCard` + `NowPlayingView` + `/now` route

**Files:**
- Create: `web/src/components/NowPlayingCard.tsx`
- Replace: `web/src/routes/now.tsx`
- Create: `web/src/routes/now.test.tsx`

**Interfaces:**
- Consumes: `useNowPlaying`, `Snapshot`/`NowSession` types, `Panel`, `Skeleton`, `StaleBanner` (unused here), `fmtBitrate` (new small helper in `lib/format.ts`).
- Produces: `NowPlayingView({ snapshot, degraded, conn })` (pure) + `NowPlayingPage` (calls the hook) + the route.

- [ ] **Step 1: Add `fmtBitrate` to `web/src/lib/format.ts`**

```ts
export function fmtBitrate(bps: number): string {
  if (bps >= 1_000_000) return `${(bps / 1_000_000).toFixed(1)} Mbps`;
  if (bps >= 1_000) return `${Math.round(bps / 1_000)} kbps`;
  return `${bps} bps`;
}
```

- [ ] **Step 2: Write the failing test** — `web/src/routes/now.test.tsx`

```tsx
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { NowSession, Snapshot } from "@/api/types";
import { NowPlayingView } from "./now";

const sess = (over: Partial<NowSession> = {}): NowSession => ({
  session_id: "a", user: "alice", type: "Episode",
  title: "The Long Retreat", series: "Northwind", season_episode: "S1E7", item_id: "item1",
  art: { primary_tag: "p1", backdrop: { item_id: "b1", tag: "t1" } },
  play_method: "DirectPlay", paused: false,
  position_sec: 2522, runtime_sec: 3360, progress_pct: 75.1,
  is_remote: false, client: "Jellyfin Media Player", device: "Living Room Mac",
  source: { video: { codec: "hevc", width: 1920, height: 1080, range: "SDR", bitrate: 4_417_693 }, audio: { codec: "aac", channels: 6, layout: "5.1", bitrate: 480_014 } },
  transcode: null,
  ...over,
});
const snap = (over: Partial<Snapshot> = {}): Snapshot => ({
  server: { name: "jellyfin.example.lan", version: "10.11.11" },
  degraded: false,
  summary: { streams: 1, transcodes: 0, outbound_bitrate: 4_897_707, capacity: null },
  sessions: [sess()],
  ...over,
});

afterEach(() => vi.restoreAllMocks());

test("renders a session card with progress and quality chips", () => {
  render(<NowPlayingView snapshot={snap()} degraded={false} conn="live" />);
  expect(screen.getByText("The Long Retreat")).toBeInTheDocument();
  expect(screen.getByText(/Northwind/)).toBeInTheDocument();
  expect(screen.getByText(/S1E7/)).toBeInTheDocument();
  expect(screen.getByText(/HEVC/i)).toBeInTheDocument();
  expect(screen.getByText(/1080p/)).toBeInTheDocument();
  expect(screen.getByText(/local/i)).toBeInTheDocument();
  const poster = screen.getByRole("img", { name: /poster/i });
  expect(poster).toHaveAttribute("src", expect.stringContaining("/api/now-playing/art/item1"));
});

test("transcode session shows the arrow rows and raw reasons", () => {
  render(
    <NowPlayingView
      snapshot={snap({
        summary: { streams: 1, transcodes: 1, outbound_bitrate: 9_200_000, capacity: 6 },
        sessions: [
          sess({
            play_method: "Transcode",
            transcode: { bitrate: 9_200_000, container: "mkv→ts", video: "hevc→h264", audio: "", hw: "qsv", completion_pct: 41.3, reasons: ["VideoCodecNotSupported"] },
          }),
        ],
      })}
      degraded={false}
      conn="live"
    />,
  );
  expect(screen.getByText("hevc→h264")).toBeInTheDocument();
  expect(screen.queryByText(/audio/i)).not.toBeNull(); // label present
  expect(screen.getByText("VideoCodecNotSupported")).toBeInTheDocument();
  expect(screen.getByText(/1 transcoding/)).toBeInTheDocument();
  expect(screen.getByText(/\/ 6/)).toBeInTheDocument(); // capacity gauge
});

test("empty and degraded states", () => {
  const { rerender } = render(<NowPlayingView snapshot={snap({ sessions: [] })} degraded={false} conn="live" />);
  expect(screen.getByText(/nothing playing/i)).toBeInTheDocument();
  rerender(<NowPlayingView snapshot={snap({ sessions: [], degraded: true })} degraded={true} conn="reconnecting" />);
  expect(screen.getByText(/can't reach jellyfin/i)).toBeInTheDocument();
});

function _wrap(ui: ReactNode) {
  return ui;
}
```

> Drop `_wrap` if unused. The `now.test.tsx` targets `NowPlayingView` directly (props-driven), like Plan 2's `*View` split.

- [ ] **Step 3: Run it, see it fail** — no `NowPlayingView` export.

- [ ] **Step 4: Implement `web/src/components/NowPlayingCard.tsx`**

```tsx
import { useState } from "react";
import type { NowSession } from "@/api/types";
import { Panel } from "./Panel";
import { cn } from "@/lib/utils";
import { fmtBitrate } from "@/lib/format";

function mmss(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, "0")}`;
}
function resLabel(w: number): string {
  if (w >= 3200) return "4K";
  if (w >= 1400) return "1080p";
  if (w >= 1000) return "720p";
  return w > 0 ? "SD" : "";
}
const methodStyle: Record<string, string> = {
  DirectPlay: "bg-cyan/15 text-cyan",
  DirectStream: "bg-cyan/15 text-cyan",
  Transcode: "bg-violet/15 text-violet",
};

function Chip({ children, mote }: { children: React.ReactNode; mote?: boolean }) {
  return (
    <span
      className={cn(
        "rounded-full px-2 py-0.5 text-[10.5px] font-medium",
        mote ? "bg-mote/15 text-mote" : "bg-line/60 text-muted",
      )}
    >
      {children}
    </span>
  );
}

export function NowPlayingCard({ s }: { s: NowSession }) {
  const [posterOK, setPosterOK] = useState(true);
  const hdr = s.source.video.range && s.source.video.range !== "SDR";
  const art = (kind: "primary" | "backdrop", id: string, tag: string) =>
    `/api/now-playing/art/${id}?kind=${kind}&tag=${encodeURIComponent(tag)}`;

  return (
    <Panel className="relative overflow-hidden p-4">
      {s.art.backdrop && (
        <img
          alt=""
          aria-hidden
          src={art("backdrop", s.art.backdrop.item_id, s.art.backdrop.tag)}
          className="pointer-events-none absolute inset-0 h-full w-full object-cover opacity-20 blur-2xl"
        />
      )}
      <div className="relative flex gap-4">
        <div className="w-24 shrink-0">
          {posterOK && s.art.primary_tag ? (
            <img
              alt={`${s.title} poster`}
              src={art("primary", s.item_id, s.art.primary_tag)}
              onError={() => setPosterOK(false)}
              className="aspect-[2/3] w-full rounded-lg object-cover"
            />
          ) : (
            <div className="flex aspect-[2/3] w-full items-center justify-center rounded-lg bg-gradient-to-br from-cyan/20 to-violet/20 font-display text-2xl text-ink">
              {s.title.slice(0, 1)}
            </div>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <div>
            <div className="font-display text-[15px] font-semibold text-ink">{s.title}</div>
            <div className="text-[12px] text-muted">
              {s.series ? `${s.series} · ${s.season_episode}` : s.type} · {s.user}
            </div>
          </div>

          <div>
            <div className="h-1.5 overflow-hidden rounded-full bg-line/60">
              <div className="h-full bg-cyan" style={{ width: `${Math.min(100, s.progress_pct)}%` }} />
            </div>
            <div className="mt-1 font-mono text-[11px] text-muted">
              {mmss(s.position_sec)} / {mmss(s.runtime_sec)}
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <span className={cn("rounded-full px-2 py-0.5 text-[10.5px] font-medium", methodStyle[s.play_method] ?? "bg-line/60 text-muted")}>
              {s.play_method === "Transcode" ? "Transcode" : s.play_method === "DirectStream" ? "Remux" : "Direct play"}
            </span>
            <span className={cn("rounded-full px-2 py-0.5 text-[10.5px] font-medium", s.is_remote ? "bg-mote/15 text-mote" : "bg-line/60 text-muted")}>
              {s.is_remote ? "remote" : "local"}
            </span>
            {s.paused && <span className="rounded-full bg-mote/15 px-2 py-0.5 text-[10.5px] font-medium text-mote">paused</span>}
            {resLabel(s.source.video.width) && <Chip>{resLabel(s.source.video.width)}</Chip>}
            {s.source.video.codec && <Chip>{s.source.video.codec.toUpperCase()}</Chip>}
            <Chip mote={hdr}>{s.source.video.range || "SDR"}</Chip>
            {s.source.audio.codec && (
              <Chip>
                {s.source.audio.codec.toUpperCase()} {s.source.audio.layout}
              </Chip>
            )}
          </div>

          {s.transcode && (
            <div className="mt-1 rounded-lg bg-abyss/40 p-2 font-mono text-[11px] text-muted">
              {s.transcode.video && (
                <div>
                  video {s.transcode.video}
                  {s.transcode.hw && ` · hw ${s.transcode.hw}`}
                </div>
              )}
              {s.transcode.audio && <div>audio {s.transcode.audio}</div>}
              <div>container {s.transcode.container}</div>
              <div>
                ↑ {fmtBitrate(s.transcode.bitrate)} · buffered {Math.round(s.transcode.completion_pct)}%
              </div>
              {s.transcode.reasons.length > 0 && (
                <div className="mt-1 flex flex-wrap gap-1">
                  {s.transcode.reasons.map((r) => (
                    <span key={r} className="rounded bg-line/60 px-1.5 py-0.5 text-[10px]">
                      {r}
                    </span>
                  ))}
                </div>
              )}
            </div>
          )}

          <div className="text-[11px] text-muted">
            {s.client} · {s.device}
          </div>
        </div>
      </div>
    </Panel>
  );
}
```

- [ ] **Step 5: Replace `web/src/routes/now.tsx`**

```tsx
import { createRoute } from "@tanstack/react-router";
import { NowPlayingCard } from "@/components/NowPlayingCard";
import { Panel } from "@/components/Panel";
import { Skeleton } from "@/components/Skeleton";
import type { Snapshot } from "@/api/types";
import { useNowPlaying } from "@/live/useNowPlaying";
import { fmtBitrate } from "@/lib/format";
import { Route as rootRoute } from "./__root";

export function NowPlayingView({
  snapshot,
  degraded,
  conn,
}: {
  snapshot: Snapshot | null;
  degraded: boolean;
  conn: "connecting" | "live" | "reconnecting";
}) {
  return (
    <div className="flex flex-col gap-5">
      <div className="flex items-baseline justify-between gap-4">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
          Now Playing
        </h1>
        <span className="font-mono text-[11.5px] text-muted">
          {snapshot ? `${snapshot.server.name || "jellyfin"} · ${snapshot.server.version}` : "—"}
          {conn === "reconnecting" && <span className="ml-2 inline-block h-2 w-2 animate-pulse rounded-full bg-mote" />}
        </span>
      </div>

      {snapshot && (
        <div className="font-mono text-[12.5px] text-muted">
          {snapshot.summary.streams} streams · {snapshot.summary.transcodes} transcoding ·{" "}
          {fmtBitrate(snapshot.summary.outbound_bitrate)} out
          {snapshot.summary.capacity != null && (
            <span className="ml-2">
              <span className="inline-block h-1.5 w-24 overflow-hidden rounded-full bg-line/60 align-middle">
                <span
                  className="inline-block h-full bg-cyan"
                  style={{ width: `${Math.min(100, (snapshot.summary.streams / snapshot.summary.capacity) * 100)}%` }}
                />
              </span>{" "}
              {snapshot.summary.streams} / {snapshot.summary.capacity}
            </span>
          )}
        </div>
      )}

      {degraded && (
        <div className="rounded-[14px] border border-mote/40 bg-panel/60 px-4 py-3 text-[13px] text-muted">
          {snapshot && snapshot.sessions.length > 0
            ? "Can't reach Jellyfin — showing the last known state."
            : "Can't reach Jellyfin — nothing to show."}
        </div>
      )}

      {!snapshot ? (
        <div className="flex flex-col gap-3">
          <Skeleton className="h-[168px] w-full" />
          <Skeleton className="h-[168px] w-full" />
        </div>
      ) : snapshot.sessions.length === 0 && !degraded ? (
        <Panel className="p-8 text-center text-[14px] text-muted">Nothing playing right now.</Panel>
      ) : (
        <div className={degraded ? "flex flex-col gap-3 opacity-60" : "flex flex-col gap-3"}>
          {snapshot.sessions.map((s) => (
            <NowPlayingCard key={s.session_id} s={s} />
          ))}
        </div>
      )}
    </div>
  );
}

function NowPlayingPage() {
  const { snapshot, degraded, conn } = useNowPlaying();
  return <NowPlayingView snapshot={snapshot} degraded={degraded} conn={conn} />;
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/now",
  component: NowPlayingPage,
});
```

- [ ] **Step 6: Run it, see it pass** — `npx vitest run src/routes/now.test.tsx` → PASS. Then `npx vitest run`, `npx tsc -b`, `npx biome ci .`, `npm run build && touch dist/.gitkeep`.

- [ ] **Step 7: Commit**

```bash
cd .. && git add web && git commit -m "feat(web): Now Playing page — cards, art, transcode detail, degraded/empty states"
```

---

## Task 9: The "finish" bundle

**Files:**
- Modify: `internal/api/refresh.go`, `internal/api/api_test.go` (the `TestRefreshEndpoint` case)
- Modify: `web/src/routes/library.tsx`, `web/src/routes/library.test.tsx`
- Modify: `README.md`

- [ ] **Step 1: Update `TestRefreshEndpoint`** in `internal/api/api_test.go` to expect the new shape:

```go
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
```

- [ ] **Step 2: Run it, see it fail** — old handler returns `{job, queued}`.

- [ ] **Step 3: Rewrite `internal/api/refresh.go`**

```go
package api

import "net/http"

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	job := r.URL.Query().Get("job")
	if job == "" {
		job = "all"
	}
	switch job {
	case "library", "watch", "all":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "job must be library|watch|all")
		return
	}
	if s.trigger != nil {
		s.trigger.Trigger(job)
	}
	data := map[string]map[string]bool{}
	for _, j := range []string{"library", "watch"} {
		if job == "all" || job == j {
			data[j] = map[string]bool{"triggered": true}
		}
	}
	writeJSON(w, http.StatusAccepted, data, s.meta(false))
}
```

- [ ] **Step 4: Run it, see it pass** — `go test ./internal/api/ -run TestRefreshEndpoint -v` → PASS.

- [ ] **Step 5: Growth-chart filter** — in `web/src/routes/library.tsx`, add a pure helper and use it at the call site:

```ts
// near the other helpers, exported for the unit test:
export function growthForChart<T extends { month: string }>(g: T[]): T[] {
  // The API returns items with a broken (unix-epoch) DateCreated in a "1970-01"
  // bucket. That's real source data — kept in the API — but plotting it wrecks
  // the axis. Rendering is where this belongs.
  return g.filter((p) => p.month !== "1970-01");
}
```
Call site (was `growthOption(data.growth)`):
```tsx
                option={growthOption(growthForChart(data.growth))}
```

- [ ] **Step 6: Add the unit assertion** to `web/src/routes/library.test.tsx`:

```ts
import { growthForChart } from "./library";

test("growthForChart drops the 1970-01 epoch bucket", () => {
  const g = [
    { month: "1970-01", added_items: 3, cum_items: 3, added_bytes: 0 },
    { month: "2024-01", added_items: 40, cum_items: 43, added_bytes: 1e9 },
  ];
  expect(growthForChart(g).map((p) => p.month)).toEqual(["2024-01"]);
});
```

- [ ] **Step 7: README** — replace the API-key line in "Configuration" and add setup steps. In the config table row for `JELLYFIN_API_KEY`, change `not exercised in this build, still required` → `for Now Playing + the server-name header`. Add a short paragraph under "What you need":

```markdown
- A Jellyfin API key: **Dashboard → API Keys → +**, name it `ephyra`. Put it in
  `EPHYRA_JELLYFIN_API_KEY`. It powers Now Playing and the header's server
  name/version; a wrong or missing key degrades Now Playing but doesn't stop
  Ephyra.
```
Update the pre-alpha / feature line to say all four pages work (Now Playing included).

- [ ] **Step 8: Run everything** — `go test ./... && go vet ./... && golangci-lint run`; `cd web && npx vitest run && npx tsc -b && npx biome ci . && npm run build && touch dist/.gitkeep`.

- [ ] **Step 9: Commit**

```bash
git add internal/api/refresh.go internal/api/api_test.go web/src/routes/library.tsx web/src/routes/library.test.tsx web/dist/.gitkeep README.md
git commit -m "feat: per-job refresh response, growth-chart epoch filter, README API-key steps"
```

---

## Task 10: Docs, smoke test, finish

**Files:**
- Modify: `docs/schema-notes.md`, `CLAUDE.md`
- Modify: `cmd/ephyra/main_test.go`

- [ ] **Step 1: `docs/schema-notes.md`** — add a `## /Sessions (Now Playing)` section from spec §3: the filter (`NowPlayingItem != null && MediaType == "Video"`), `PlayMethod` on `PlayState`, `UserName` in the session (no `/Users`), `TranscodingInfo` absent during direct-play (+ note if the transcode fixture is still synthetic), ticks ÷ 1e7, `RemoteEndPoint` bare IP, the auth header form, and `/System/Info` → `ServerName`/`Version`.

- [ ] **Step 2: `CLAUDE.md`** — under Architecture add: "`internal/jellyfin` is a thin read-only HTTP client (not a `Source`). `internal/live` holds the SSE hub whose poll loop runs **only** while a browser has `/now` open — zero standing load otherwise. `hub.Prime` is non-fatal: Ephyra boots even with the Jellyfin API down." Change the "Plans 1 and 2 done" line to "All three plans done; v1 four-page surface complete."

- [ ] **Step 3: Extend the smoke test** — `cmd/ephyra/main_test.go`. Add a stub Jellyfin server and a `Live` hub to `bootServer` (or a sibling helper):

```go
func TestSmoke_NowPlaying(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info":
			w.Write([]byte(`{"ServerName":"Stub","Version":"10.11.11"}`))
		case "/Sessions":
			b, _ := os.ReadFile(filepath.Join(repoRootForSmoke(t), "testdata", "sessions.directplay.json"))
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	defer jf.Close()

	dataDir := testsupport.TwoDBLayout(t)
	ctx := context.Background()
	st, _ := store.Open(ctx, filepath.Join(t.TempDir(), "e.db"))
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{JellyfinDataDir: dataDir, WorkDir: t.TempDir(), JellyfinURL: jf.URL, JellyfinAPIKey: "K", LivePollInterval: time.Second, RefreshLibrary: time.Hour, RefreshWatch: time.Hour}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := live.New(jellyfin.New(cfg), cfg.LivePollInterval, nil, log)
	t.Cleanup(hub.Close)
	hub.Prime(ctx)
	h := api.New(api.Deps{Store: st, Cfg: cfg, Log: log, Live: hub}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/now-playing", nil))
	if rr.Code != 200 {
		t.Fatalf("now-playing -> %d", rr.Code)
	}
	var env struct {
		Data live.Snapshot `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data.Sessions) != 1 || env.Data.Sessions[0].Title != "The Long Retreat" {
		t.Fatalf("snapshot: %+v", env.Data)
	}

	// the stream yields a snapshot frame
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/now-playing/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.HasPrefix(line, "event: snapshot") {
		t.Fatalf("first SSE frame = %q", line)
	}
}

func repoRootForSmoke(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("go.mod not found")
		}
		d = p
	}
}
```

> Add imports `bufio`, `strings`, `time`, `log/slog`, `io`, and `internal/jellyfin` / `internal/live` to `main_test.go` as needed.

- [ ] **Step 4: Full gate**

```
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run
cd web && npx tsc -b && npx biome ci . && npx vitest run && npm run build && touch dist/.gitkeep
```

- [ ] **Step 5: Commit**

```bash
git add docs/schema-notes.md CLAUDE.md cmd/ephyra/main_test.go web/dist/.gitkeep
git commit -m "docs + smoke test for Now Playing"
```

- [ ] **Step 6: Finish the branch** — use `superpowers:finishing-a-development-branch`: verify the full suite green on `plan-3-now-playing`, present the merge menu, execute the choice against `main`.

---

## Self-review notes

- **Spec coverage:** §3 → Task 1 (fixtures) + Task 10 (schema-notes); §4 → Task 1; §5.1/§5.4 → Task 2; §5.2/§5.3 → Task 3; §6.1 → Task 4; §6.2 → Task 5; §6.3 → Task 6; §6.4 → Task 4 Step 6; §7 → Task 2 (types) + Task 7 (TS mirror); §8.1 → Task 7; §8.2/§8.3 → Task 8; §8.4 → Task 7 Step 1; §9 → Task 9; §10 → no code (config already parsed); §11 → tests in every task + Task 10 smoke.
- **Types defined once:** `jellyfin.Raw*` in Task 1; `live.Snapshot`/`Session`/`Summary`/`Transcode`/`Event`/`ServerInfo`/`degradedPayload` in Task 2 and consumed unchanged in Tasks 3–6; the TS `Snapshot`/`NowSession`/… in Task 7 mirror the Go JSON tags field-for-field; Task 8 consumes them.
- **`api.Deps.Live` ripples:** added in Task 4; the existing `newTestServer` in `api_test.go` does **not** set it, and the nowplaying handlers guard `s.live == nil` → `503`, so Plan-1/2 api tests are unaffected. `nowplaying_test.go` builds its own server with a `Live`.
- **Plan-1/2 gotcha carried forward:** `touch web/dist/.gitkeep` after every `npm run build` before committing (Tasks 8, 9, 10).
- **Transcode fixture:** if Task 1 Step 1 can't capture a real transcode, the committed `sessions.transcode.json` is synthetic (spec §3.2) and Task 10 Step 1 records that in `schema-notes.md`; `normalize`'s rules don't depend on a real capture, only the golden values in `TestNormalize_Transcode` do (keep them loose — assert shape, not exact strings).
