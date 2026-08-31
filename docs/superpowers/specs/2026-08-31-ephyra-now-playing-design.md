# Ephyra — Now Playing — Design

Status: approved for planning
Date: 2026-08-31
Parent spec: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`
Implements: parent §5.3, §8.5, §8.6, §9, §10 (`/now` page). This is **Plan 3 of 3**
and completes the v1 four-page surface.

---

## 1. Summary

The `/now` page is Ephyra's only live surface. A thin `jellyfin.Client` polls
`GET /Sessions` **only while a browser has the page open**; a `live.Hub` fans the
normalized snapshot out to connected browsers over Server-Sent Events. When
nobody is watching, Ephyra makes zero HTTP calls to Jellyfin.

Cards show what's playing — title, series/episode, user, client, a progress bar,
quality chips, poster and blurred backdrop art (proxied through Ephyra so the
API key never reaches the browser), and for transcoding sessions the
`hevc → h264` / `mkv → ts` detail plus Jellyfin's raw `TranscodeReasons`.

Plus a "finish" bundle: `POST /api/refresh` response shape, a one-line fix for
the growth chart's epoch bucket, and real Jellyfin-API-key setup steps in the
README.

Non-negotiables carried from the parent spec: never write to Jellyfin; never
serve 5xx for a page because upstream is down; the frontend ships in the binary;
no auth.

---

## 2. Goals & non-goals

### Goals

- `GET /api/now-playing` (one-shot) and `GET /api/now-playing/stream` (SSE) per
  §6, served from the hub.
- The poll loop runs **only** while ≥1 SSE client is connected; it stops within
  one interval of the last disconnect.
- Upstream failure degrades gracefully — last snapshot kept, one `degraded`
  event, capped backoff, never a panic or a torn-down hub.
- Poster + backdrop art via an Ephyra proxy; the key stays server-side.
- Ephyra starts even when Jellyfin's HTTP API is unreachable or the key is
  wrong; Now Playing shows its degraded state until a poll succeeds.

### Non-goals

- **No history.** Now Playing is instantaneous; bandwidth-over-time and
  recently-ended sessions are parent backlog #4.
- **No session control** — no pause/stop/seek commands to Jellyfin (would be a
  write). Read-only.
- **No music / audio sessions** in v1 — video only. Audio-session cards are
  backlog.
- **No `SOURCE=api`.** `jellyfin.Client` is a standalone client, not a `Source`
  implementation; folding it into the `Source` seam is parent backlog #1.
- No geo-IP / ISP lookup for remote sessions (backlog #5) — just a local/remote
  flag.

---

## 3. Task #0 findings — real `/Sessions` (10.11.11)

Probed 2026-08-31 against the operator's server (linuxserver
image). One **DirectPlay** session captured; **no live transcode sample** — the
`TranscodingInfo` shape below is from Jellyfin's OpenAPI and MUST be verified
against a real transcode in Task 1 (Task 1 Step 1). Goes into
`docs/schema-notes.md`.

### 3.1 Session object (fields Ephyra reads)

```jsonc
{
  "Id": "aaaa0000bbbb1111cccc2222dddd3333",   // session id
  "UserId": "11111111222222223333333344444444",   // dashless lowercase
  "UserName": "alice",                          // present — no /Users call needed
  "Client": "Jellyfin Media Player",
  "DeviceName": "Living Room Mac",
  "RemoteEndPoint": "192.168.1.50",            // bare IP string, no port, no brackets
  "LastActivityDate": "...", "LastPlaybackCheckIn": "...",
  "IsActive": true,
  "PlayState": {
    "PositionTicks": 25228120000,              // ÷ 1e7 = seconds
    "IsPaused": false,
    "PlayMethod": "DirectPlay",                // <-- on PlayState, NOT top-level. DirectPlay|DirectStream|Transcode
    "AudioStreamIndex": 1,
    "MediaSourceId": "abcdef0123456789abcdef0123456789"
  },
  "TranscodingInfo": { ... },                  // ABSENT when direct-playing; see 3.2
  "NowPlayingItem": {
    "Id": "abcdef0123456789abcdef0123456789",
    "Name": "The Long Retreat",
    "Type": "Episode",                          // Episode | Movie | (Audio — filtered out)
    "MediaType": "Video",                       // filter: keep only "Video"
    "RunTimeTicks": 33600640000,                // ÷ 1e7 = seconds
    "Container": "mkv",
    "SeriesName": "Northwind",                       // "" for movies
    "SeriesId": "fedcba9876543210fedcba9876543210",
    "IndexNumber": 7, "ParentIndexNumber": 1,   // -> "S1E7"
    "ProductionYear": 2019,
    "ImageTags": { "Primary": "5c5c5c5c6b6b6b6b7a7a7a7a89898989" },
    "ParentBackdropItemId": "fedcba9876543210fedcba9876543210",
    "ParentBackdropImageTags": ["0f0f0f0f1e1e1e1e2d2d2d2d3c3c3c3c"],
    "MediaStreams": [
      { "Type": "Video", "Index": 0, "Codec": "hevc", "Width": 1920, "Height": 1080,
        "BitRate": 4417693, "VideoRange": "SDR", "VideoRangeType": "SDR", "Profile": "Main 10" },
      { "Type": "Audio", "Index": 1, "Codec": "aac", "Channels": 6, "ChannelLayout": "5.1",
        "BitRate": 480014, "Language": "eng" },
      { "Type": "Subtitle", "Index": 3, "Codec": "PGSSUB" }
    ]
  }
}
```

- **No overall bitrate field** for direct-play — sum the playing video stream's
  `BitRate` + the selected audio stream's `BitRate`.
- `/Sessions` returns idle sessions too (app open, nothing playing). Filter to
  `NowPlayingItem != null && NowPlayingItem.MediaType == "Video"`.

### 3.2 `TranscodingInfo` (from OpenAPI — VERIFY in Task 1)

```jsonc
{
  "Bitrate": 9200000,
  "Container": "ts",
  "VideoCodec": "h264", "AudioCodec": "aac",
  "IsVideoDirect": false, "IsAudioDirect": true,
  "TranscodeReasons": ["VideoCodecNotSupported", "ContainerBitrateExceedsLimit"],
  "CompletionPercentage": 41.3,                 // how far ahead of the player the transcoder has buffered
  "HardwareAccelerationType": "qsv",            // "" / "none" when software
  "Width": 1280, "Height": 720, "AudioChannels": 2, "Framerate": 23.976
}
```

### 3.3 `/System/Info`

`{ "ServerName": "jellyfin.example.lan", "Version": "10.11.11", "Id": "...",
"ProductName": "Jellyfin Server", "HasPendingRestart": false,
"HasUpdateAvailable": false }`. Ephyra reads `ServerName` + `Version`.

### 3.4 `/Users`

Not needed for Now Playing — the session already carries `UserName`. (Watch
Stats reads users from the file, not this endpoint.) Dropped from parent §5.3's
"used by Now Playing" note.

### 3.5 Auth

`Authorization: MediaBrowser Token=<key>` header works against 10.11.11.
(`X-Emby-Token: <key>` also works; use the `Authorization` form.)

---

## 4. `internal/jellyfin` — the HTTP client

New package. A standalone client, **not** a `source.Source`.

```go
package jellyfin

type Client struct {
	base string          // JELLYFIN_URL, trailing slash trimmed
	key  string          // JELLYFIN_API_KEY
	http *http.Client    // Timeout: 3s
}

func New(cfg config.Config) *Client

// Sessions returns raw sessions with NowPlayingItem set (idle sessions filtered
// server-side via ?ActiveWithinSeconds and client-side by the caller).
func (c *Client) Sessions(ctx context.Context) ([]RawSession, error)

func (c *Client) SystemInfo(ctx context.Context) (ServerInfo, error) // {Name, Version}

// Image streams an item image; caller copies body to the response and closes it.
func (c *Client) Image(ctx context.Context, itemID, kind, tag string) (body io.ReadCloser, contentType string, err error)
```

- Every request sets the `Authorization: MediaBrowser Token=` header and a
  `ctx` with the 3s client timeout (callers may pass a shorter one).
- `RawSession` / `RawNowPlayingItem` / `RawMediaStream` / `RawTranscodingInfo`
  unmarshal **only** the §3 fields (`json:"-"`-free partial structs; unknown
  fields ignored by `encoding/json`).
- Non-2xx → `fmt.Errorf("jellyfin %s: %s", path, status)`. Network/timeout errors
  propagate as-is.
- `Image`: `GET {base}/Items/{itemID}/Images/{kind}?tag={tag}` (+ auth header).
  `kind` ∈ `Primary` | `Backdrop`. Returns the upstream body + `Content-Type`
  unread; the API handler streams and closes it.

---

## 5. `internal/live` — hub, poll loop, normalization

### 5.1 Types

```go
type Snapshot struct {
	Server   ServerInfo    `json:"server"`
	Degraded bool          `json:"degraded"`
	Summary  Summary       `json:"summary"`
	Sessions []Session     `json:"sessions"`
}
type Summary struct {
	Streams         int  `json:"streams"`
	Transcodes      int  `json:"transcodes"`
	OutboundBitrate int64 `json:"outbound_bitrate"`
	Capacity        *int `json:"capacity"` // STREAM_CAPACITY, nil when unset
}
type Event struct {
	Kind string   // "snapshot" | "update" | "degraded"
	Data any      // *Snapshot for snapshot/update; degradedPayload{Degraded bool `json:"degraded"`} for degraded
}
```

`Session` is the §7 DTO.

### 5.2 Hub

```go
type Hub struct {
	client   sessionsClient   // interface: Sessions(ctx) ([]jellyfin.RawSession, error); SystemInfo(ctx) (ServerInfo, error)
	interval time.Duration
	now      func() time.Time // default time.Now
	after    func(time.Duration) <-chan time.Time // default time.After — test seam for the poll wait
	log      *slog.Logger

	capacity  *int             // STREAM_CAPACITY, nil when unset

	mu        sync.Mutex
	subs      map[int64]chan Event
	nextID    int64
	snap      *Snapshot
	snapAt    time.Time
	server    ServerInfo
	serverAt  time.Time
	stop      chan struct{}    // non-nil while the poll loop runs
}

func New(client sessionsClient, interval time.Duration, capacity *int, log *slog.Logger) *Hub
func (h *Hub) Prime(ctx context.Context)          // one non-fatal SystemInfo at startup
func (h *Hub) Subscribe() (id int64, ch <-chan Event, unsub func())
func (h *Hub) Publish(ev Event)
func (h *Hub) Snapshot(ctx context.Context) *Snapshot  // cached if fresh, else a direct fetch
func (h *Hub) Close()                             // stop the loop, close all sub channels
```

- **`Subscribe`**: makes a `chan Event` cap 4, adds it, and if `len(subs)`
  went 0 → 1, starts the poll loop (`h.stop = make(chan struct{})`, `go
  h.pollLoop()`). `unsub` removes the channel; if `len(subs)` hit 0, `close(h.stop)`
  and `h.stop = nil`.
- **`Publish`**: under the lock, non-blocking `select { case ch <- ev: default:
  close(ch); delete(subs, id) }` — a slow client whose buffer is full is dropped
  and left to `EventSource` auto-reconnect.
- **`pollLoop`**: waits via `h.after(d)` (not a fixed `Ticker`) so `d` can vary —
  `interval` after a normal tick, the current backoff after a failure.
  Each iteration:
  1. `SystemInfo` refresh if `serverAt` older than 10 min (cheap; only while
     someone is watching).
  2. `client.Sessions(ctx, 3s)`.
     - error → if not already degraded: `snap.Degraded = true`,
       `Publish({Kind:"degraded"})`; set the next wait to the current backoff
       (starts 4s, ×2 up to 30s); keep `snap`.
     - ok → `next := normalize(raw, server, capacity)`; `next.Degraded = false`;
       if `changed(h.snap, &next)` → store (`h.snap, h.snapAt`) + `Publish({Kind:"update", Data:&next})`;
       reset backoff and the next wait to `interval`. If we were degraded and now
       aren't, the `update` itself signals recovery (the frontend clears the
       ribbon on any non-degraded payload).
  3. `select { case <-h.after(wait): case <-h.stop: return }`.
- **`Snapshot(ctx)`**: returns `h.snap` if `now-h.snapAt < interval`; else a
  direct `client.Sessions` + `normalize` (does not start the loop). On fetch
  error with no cached snap → a `Snapshot{Server: h.server, Degraded: true}` with
  empty sessions.

### 5.3 `changed(prev, next *Snapshot) bool`

- `prev == nil` → true.
- `prev.Degraded != next.Degraded` → true.
- different set of `session_id`s → true.
- for each common session: any of `paused`, `play_method`, `is_remote`,
  `transcode.video`, `transcode.audio`, `transcode.container` differ → true;
  or `abs(next.progress_pct - prev.progress_pct) >= 1.0` → true.
- else false.

Steady playback thus emits ~1 event per (interval × ceil(1% of runtime /
per-tick delta)) — roughly every 15–25 s for a 30–60 min item at a 4 s tick. The
frontend interpolates the bar between events.

### 5.4 `normalize(raw []jellyfin.RawSession, server ServerInfo, capacity *int) Snapshot`

Pure. Rules per §7. `testdata/sessions.directplay.json` (real) + a hand-built
`testdata/sessions.transcode.json` drive golden tests.

---

## 6. HTTP API

`api.Deps` gains `Live *live.Hub`. Three routes registered in `Handler()`.

### 6.1 `GET /api/now-playing`

House envelope. `data` = `hub.Snapshot(ctx)` (§5.2). Always `200` — a degraded
snapshot with `sessions: []` when Jellyfin is unreachable and nothing is cached.
`meta.stale` is always `false` (this endpoint has no "refresh" concept).

### 6.2 `GET /api/now-playing/stream`

`Content-Type: text/event-stream`, `Cache-Control: no-cache`,
`X-Accel-Buffering: no`. Flush after every write (the existing `statusWriter`
delegates `Flush`).

1. `id, ch, unsub := hub.Subscribe()`; `defer unsub()`.
2. Write `event: snapshot\ndata: <json(hub.Snapshot(ctx))>\n\n`; flush.
3. Loop on `select`:
   - `<-ch` → `event: <ev.Kind>\ndata: <json(ev.Data)>\n\n`; flush.
   - `<-time.After(15s)` → `: heartbeat\n\n`; flush.
   - `<-r.Context().Done()` → return (defer runs `unsub`, which stops the poll
     loop if this was the last client).

No envelope on stream frames — `data:` is the bare `Snapshot` (or
`{"degraded":true}`).

### 6.3 `GET /api/now-playing/art/{itemId}`

Query: `kind` ∈ `primary` | `backdrop` (default `primary`), `tag` (optional
cache-buster from the DTO).

- `itemId` must match `^[0-9a-fA-F-]{8,64}$` → else `400`.
- `kind` not in the allowlist → `400`.
- Serve from a process-wide LRU (cap 32, keyed `itemId/kind/tag`, entries hold
  `contentType []byte`). Miss → `client.Image(ctx, itemId, Kind(kind), tag)`,
  read fully (images are small), store, serve. Upstream error → `502`.
- Response: upstream `Content-Type`, `Cache-Control: public, max-age=86400`.

### 6.4 `main.go` wiring

```go
jc := jellyfin.New(cfg)
var capacity *int
if cfg.StreamCapacity > 0 { capacity = &cfg.StreamCapacity }
hub := live.New(jc, cfg.LivePollInterval, capacity, log)
hub.Prime(ctx)                 // non-fatal; logs "jellyfin api ok" or WARN
defer hub.Close()
srv := api.New(api.Deps{ ..., Live: hub })
```

`hub.Close()` runs before `httpServer.Shutdown` in the graceful-shutdown path.

---

## 7. Snapshot DTO (`live.Session`) & normalization rules

```jsonc
{
  "session_id": "aaaa0000…",
  "user": "alice",
  "type": "Episode",
  "title": "The Long Retreat",
  "series": "Northwind",                 // "" for movies
  "season_episode": "S1E7",         // "" for movies / when numbers missing
  "item_id": "abcdef01…",           // for the art proxy
  "art": {
    "primary_tag": "5c5c5c5c…",       // ImageTags.Primary; "" if none
    "backdrop": { "item_id": "b6fc7b…", "tag": "0f0f0f0f…" }  // null if none
  },
  "play_method": "DirectPlay",      // PlayState.PlayMethod verbatim
  "paused": false,
  "position_sec": 2522,
  "runtime_sec": 3360,
  "progress_pct": 75.06,
  "is_remote": false,
  "client": "Jellyfin Media Player",
  "device": "Living Room Mac",
  "source": {
    "video": { "codec": "hevc", "width": 1920, "height": 1080, "range": "SDR", "bitrate": 4417693 },
    "audio": { "codec": "aac", "channels": 6, "layout": "5.1", "bitrate": 480014 }
  },
  "transcode": null
}
```

`transcode` (only when `play_method == "Transcode"` and `TranscodingInfo` present):

```jsonc
{
  "bitrate": 9200000,
  "container": "mkv→ts",        // "{src}→{tgt}" only when they differ, else just tgt
  "video": "hevc→h264",         // "" when IsVideoDirect
  "audio": "eac3→aac",          // "" when IsAudioDirect
  "hw": "qsv",                  // HardwareAccelerationType lowercased; "" for none/software
  "completion_pct": 41.3,       // TranscodingInfo.CompletionPercentage
  "reasons": ["VideoCodecNotSupported", "ContainerBitrateExceedsLimit"]  // raw enum strings
}
```

Rules:

| Field | From |
|---|---|
| filter | keep sessions with `NowPlayingItem != null` and `NowPlayingItem.MediaType == "Video"` |
| `title` | `NowPlayingItem.Name` |
| `series` / `season_episode` | `SeriesName`; `"S{ParentIndexNumber}E{IndexNumber}"` when both present |
| `item_id` | `NowPlayingItem.Id` |
| `art.primary_tag` | `NowPlayingItem.ImageTags.Primary` |
| `art.backdrop` | `{ParentBackdropItemId, ParentBackdropImageTags[0]}`; null if either missing |
| `play_method` | `PlayState.PlayMethod` |
| `position_sec` / `runtime_sec` | `PlayState.PositionTicks` / `NowPlayingItem.RunTimeTicks`, ÷ 1e7, integer |
| `progress_pct` | `runtime_sec > 0 ? position/runtime*100 : 0`, rounded to 2dp |
| `is_remote` | `net.ParseIP(RemoteEndPoint)`; remote = parsed AND not (`IsLoopback` \|\| `IsPrivate` \|\| `IsLinkLocalUnicast` \|\| `IsLinkLocalMulticast`). Unparseable → `false` |
| `source.video` | first `MediaStreams` entry with `Type=="Video"` |
| `source.audio` | entry with `Index == PlayState.AudioStreamIndex`; fallback first `Type=="Audio"` with `IsDefault`, then first `Type=="Audio"` |
| `source.video.range` | `VideoRangeType` → `SDR` \| `HDR10` \| `HLG` \| `DolbyVision` (raw pass-through; unknown → the raw string) |
| `transcode.video` | `""` if `IsVideoDirect`; else `"{source.video.codec}→{TranscodingInfo.VideoCodec}"` |
| `transcode.audio` | `""` if `IsAudioDirect`; else `"{source.audio.codec}→{TranscodingInfo.AudioCodec}"` |
| `transcode.container` | `NowPlayingItem.Container == TranscodingInfo.Container ? tgt : "{src}→{tgt}"` |
| `summary.streams` | `len(sessions)` |
| `summary.transcodes` | count where `play_method == "Transcode"` |
| `summary.outbound_bitrate` | Σ per session: `transcode.bitrate` if transcoding, else `source.video.bitrate + source.audio.bitrate` |
| `summary.capacity` | `*STREAM_CAPACITY` or null |

Sessions sorted by `session_id` for a stable diff/order.

---

## 8. Frontend — `/now`

Stack unchanged (React 19, TanStack Router/Query, Tailwind v4, hand-rolled
components). `/now` is a stub today.

### 8.1 `web/src/live/useNowPlaying.ts`

```ts
type ConnState = "connecting" | "live" | "reconnecting";
function useNowPlaying(): { snapshot: Snapshot | null; degraded: boolean; conn: ConnState }
```

- On mount: `fetchEnvelope<Snapshot>("/api/now-playing")` for first paint (plain
  `fetch`, not a TanStack query — the stream owns updates after this).
- Then `const es = new EventSource("/api/now-playing/stream")`.
  - `es.addEventListener("snapshot" | "update", e => setSnapshot(JSON.parse(e.data)); setConn("live"))`
  - `es.addEventListener("degraded", () => setDegraded(true))` — any subsequent
    `update` payload clears it.
  - `es.onerror` → `setConn("reconnecting")` (the browser reconnects on its own;
    a successful event flips it back to `"live"`).
- **Visibility**: `visibilitychange` → `document.hidden` closes `es`
  (`setConn("connecting")`); becoming visible reopens it and re-fetches the
  one-shot.
- **Interpolation**: the hook keeps the last server payload as a baseline
  (`baseSnapshot`, `baseAt = Date.now()`) and a `tick` state bumped by
  `setInterval(1000)`. The returned `snapshot` is derived each render: for every
  non-paused session, `position_sec = base + (Date.now() - baseAt)/1000`,
  clamped to `runtime_sec`, and `progress_pct` recomputed; paused sessions pass
  through unchanged. Every `snapshot`/`update` event replaces the baseline. The
  view stays pure.
- Cleanup on unmount: `es.close()`, `clearInterval`, remove the listener.

### 8.2 `web/src/routes/now.tsx`

`NowPlayingView({ snapshot, degraded, conn })` (pure) + `NowPlayingPage` (calls
the hook). Header: `ServerName` + `Version`, a `reconnecting` dot when
`conn === "reconnecting"`, and the summary strip
`{streams} streams · {transcodes} transcoding · {fmtBitrate(outbound)} out` with a
thin `streams / capacity` gauge bar when `summary.capacity != null`.

States: skeleton cards (no snapshot yet) · `"Nothing playing right now."`
(snapshot, empty `sessions`) · degraded ribbon (`"Can't reach Jellyfin — showing
the last known state."` with cards dimmed if `sessions` non-empty, else
`"…nothing to show."`).

### 8.3 `web/src/components/NowPlayingCard.tsx`

- **Backdrop**: `<img src="/api/now-playing/art/{art.backdrop.item_id}?kind=backdrop&tag={art.backdrop.tag}">`
  absolutely positioned, `object-cover`, `blur-2xl opacity-25`, behind content;
  omitted when `art.backdrop == null`.
- **Poster**: `<img src="/api/now-playing/art/{item_id}?kind=primary&tag={art.primary_tag}">`,
  `w-24 aspect-[2/3] object-cover rounded-lg`. `onError` → hide the `<img>`,
  show a gradient tile with the title's first letter.
- **Body**: title (display font); `series · S1E7` or year for movies; `user`.
- **Progress**: a bar (`progress_pct`, interpolated) + `mm:ss / mm:ss`.
- **Pills**: method (`Direct play` cyan / `Remux` teal / `Transcode` violet),
  `local`/`remote`, `paused` (amber) when paused.
- **Quality chips**: `1080p` · `HEVC` · `SDR`/`HDR10`/`DV` (mote-colored for
  HDR/DV) · `AAC 5.1`.
- **Transcode block** (when `transcode != null`): rows
  `video  hevc → h264 · hw qsv` (hw part only when `hw != ""`),
  `audio  eac3 → aac`, `container  mkv → ts` (only rows whose value is non-empty),
  then `↑ {fmtBitrate(transcode.bitrate)} · buffered {completion_pct|0}%`, then
  `transcode.reasons` as small muted chips (raw strings).

### 8.4 API types

`web/src/api/types.ts` gains `Snapshot`, `NowSession`, `Summary`, mirroring §7
JSON tags. No `queries.ts` factory — the hook owns fetching.

---

## 9. The "finish" bundle

- **`POST /api/refresh`** — response becomes
  `202 { "data": { "<job>": {"triggered": true}, ... } }` for the requested job
  (`all` → both `library` and `watch`). Drops the parent spec's
  `queued`/`running` distinction (the scheduler's fire-and-forget `Trigger` can't
  report it without new state). `internal/api/refresh.go` + its test.
- **Growth chart epoch bucket** — `web/src/routes/library.tsx` `growthOption`:
  filter `g.filter(p => p.month !== "1970-01")` before building the series. The
  API still returns the row (the "show source data as-is" principle). One line +
  a comment.
- **README** — replace the "not exercised in this build, still required" note
  with real steps: create a key (Dashboard → API Keys → + → name `ephyra`), set
  `EPHYRA_JELLYFIN_API_KEY`; note it powers Now Playing and the header's server
  name/version, and that a wrong/missing key degrades Now Playing without
  stopping Ephyra. Flip the feature list / pre-alpha line to "all four pages
  work".

**Not** in this plan: `CHANGELOG` and a `v0.1.0` tag — a separate step the
operator triggers when satisfied.

---

## 10. Config

No new env vars. All already parsed, now load-bearing:

| Var | Role in Plan 3 |
|---|---|
| `JELLYFIN_URL` | `jellyfin.Client` base |
| `JELLYFIN_API_KEY` | `Authorization: MediaBrowser Token=` |
| `LIVE_POLL_INTERVAL` | poll-loop tick (default 4s) |
| `STREAM_CAPACITY` | `summary.capacity`; gauge renders only when set |

---

## 11. Testing

### 11.1 Fixtures

- `testdata/sessions.directplay.json` — the real capture from §3.
- `testdata/sessions.transcode.json` — hand-built to §3.2; **Task 1 Step 1
  replaces it with a real capture** (force a transcode via the web player,
  re-probe).
- `testdata/systeminfo.json` — `{ServerName, Version}`.

### 11.2 Go

- **`internal/jellyfin`** — `httptest.Server` serving the fixtures:
  `Sessions`/`SystemInfo` parse the right fields; the `Authorization` header is
  sent; a 500 → error; a slow handler → context-deadline error at 3s;
  `Image` returns the body + content-type unread.
- **`live.normalize`** — golden `Snapshot` from both session fixtures. Cases:
  episode vs movie (`series`/`season_episode` empty for movie); `is_remote` for
  `8.8.8.8` / `192.168.1.5` / `10.0.0.9` / `172.20.0.1` / `::1` / `fe80::1` /
  `garbage`; a music session (`MediaType != "Video"`) dropped; `runtime_sec == 0`
  → `progress_pct == 0`; audio picked by `AudioStreamIndex` vs fallback;
  transcode with `IsVideoDirect: true` → `transcode.video == ""`;
  `container` same src/tgt → no arrow; `summary.outbound_bitrate` sums transcode
  vs source; `capacity` nil vs `&6`.
- **`live.Hub`** — fake client (injectable `SessionsFn`); the loop's wait is an
  injected `after func(time.Duration) <-chan time.Time` (defaults to `time.After`)
  so tests drive it with a hand-fed channel; `now func()` for the 10-min server
  refresh and snapshot age:
  - poll loop goroutine starts on the first `Subscribe`, and a `sync.WaitGroup`
    (or a `done` chan the loop closes) confirms it exits after the last `unsub`.
  - a subscriber that never drains: fill its 4-slot buffer, one more `Publish`
    closes its channel and removes it.
  - `changed`: same sessions + `progress_pct` +0.5 → no `update`; +1.5 or
    `paused` flip → `update`.
  - `Sessions` returns an error → exactly one `degraded` event, `h.snap`
    unchanged, backoff grows 4→8→16→30 (assert via the `now`/sleep seam); next
    ok tick → `update` and backoff reset.
  - `Snapshot(ctx)` fresh vs stale (direct fetch) vs error-with-no-cache
    (`Degraded: true`, empty sessions).
- **`internal/api`** — fake hub:
  - `GET /api/now-playing` → `200`, envelope, session shape; degraded-no-cache →
    `200` `degraded:true`, `sessions: []`.
  - `GET /api/now-playing/stream` via `httptest.NewServer` + a real client
    reading the body: first frame is `event: snapshot`; a `hub.Publish` of an
    `update` reaches the reader; cancelling the request context drops the hub
    subscriber count to 0 (assert through the fake hub).
  - `GET /api/now-playing/art/{id}` — proxies bytes + `Content-Type` from a stub
    upstream; `id="../etc"` → `400`; `kind=logo` → `400`; upstream `404` → `502`;
    a second identical request increments the stub's hit counter by `0` (LRU
    hit).
- **`cmd/ephyra` smoke** — extend with a stub Jellyfin `httptest.Server`
  (`/System/Info` + `/Sessions` from fixtures); boot with `JELLYFIN_URL` at it;
  `GET /api/now-playing` returns one session; `GET /api/now-playing/stream`
  yields a `snapshot` frame then EOF on cancel.

### 11.3 Frontend (Vitest + RTL)

- A small `class FakeEventSource` stub (records `close()`, exposes
  `emit(type, data)`), installed as `globalThis.EventSource`.
- **`useNowPlaying`** — first paint from the mocked `fetch`; a `snapshot` emit
  sets state and `conn === "live"`; a `degraded` emit sets the flag and a later
  `update` clears it; `document.hidden` + `visibilitychange` calls
  `close()`; a fake timer advances `position_sec` on a non-paused session and
  leaves a paused one alone.
- **`NowPlayingView`** — skeleton / empty / data / degraded; a transcode card
  renders the `→` rows (and omits an empty `audio` row) + raw reason chips; the
  capacity gauge appears only when `summary.capacity != null`; forcing the
  poster `<img>` `onError` shows the letter placeholder.
- **`now.test.tsx`** — the page mounts through the hook with the fake
  EventSource + fetch and renders a card.

### 11.4 CI

Commands unchanged.

---

## 12. Build sequence (coarse; the plan expands this)

1. **Verify `TranscodingInfo`** — force a real transcode on the operator's
   server, re-probe `/Sessions`, replace `testdata/sessions.transcode.json`,
   adjust §7 rules if reality differs. `docs/schema-notes.md` updated.
2. `internal/jellyfin` — `Client` + raw structs + tests against `httptest`.
3. `internal/live` — `normalize` + `Snapshot`/`Session` types + golden tests.
4. `internal/live` — `Hub` (subscribe/publish/poll-loop/backoff/`changed`) +
   tests with the fake client and manual tick.
5. `GET /api/now-playing` + `GET /api/now-playing/stream` + `api.Deps.Live` +
   `main.go` wiring + `hub.Prime`/`Close`.
6. `GET /api/now-playing/art/{itemId}` + the LRU + tests.
7. Frontend `useNowPlaying` hook + `FakeEventSource` test harness.
8. Frontend `NowPlayingCard` + `NowPlayingView` + `/now` route + tests.
9. The "finish" bundle: `POST /api/refresh` shape, the growth-chart filter, the
   README API-key section.
10. `docs/schema-notes.md` (Now Playing section), `CLAUDE.md` (live subsystem
    note), smoke-test extension.

---

## 13. Explicitly deferred (parent backlog, not this plan)

- Ephyra-owned session history — bandwidth-over-time, recently-ended sessions
  (#4).
- Session control (pause/stop/seek) — would be a write to Jellyfin.
- Audio / music session cards.
- Geo-IP / ISP for remote sessions (#5).
- `jellyfin.Client` as a `source.Source` implementation / `SOURCE=api` (#1).
- `/metrics` (SSE client gauge, upstream error counter) (#8).
- CHANGELOG + `v0.1.0` tag — operator-triggered, post-plan.
