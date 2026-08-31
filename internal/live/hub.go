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

// sessionsClient is the slice of *jellyfin.Client the hub needs: the /Sessions
// poll, /System/Info for the header, and the item-image passthrough for the art
// proxy. *jellyfin.Client satisfies it.
type sessionsClient interface {
	Sessions(ctx context.Context) ([]jellyfin.RawSession, error)
	SystemInfo(ctx context.Context) (jellyfin.ServerInfo, error)
	Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error)
}

// the real client is the one production implementation.
var _ sessionsClient = (*jellyfin.Client)(nil)

const (
	backoffMin = 4 * time.Second
	backoffMax = 30 * time.Second
	serverTTL  = 10 * time.Minute
)

// Hub owns a single poll loop that runs only while at least one SSE subscriber
// is connected. It fetches /Sessions, diffs against the last snapshot, and fans
// "update" / "degraded" events out to subscriber channels.
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

// Prime does a best-effort /System/Info at startup so the first frame has a
// server name. A failure here is not fatal; the loop retries later.
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

// Subscribe registers a buffered event channel and starts the poll loop if this
// is the first subscriber. The returned unsub is safe to call more than once;
// the last unsub stops the loop.
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

// Publish fans an event out to every subscriber. A subscriber whose buffer is
// full is dropped (channel closed, removed) — the SSE handler notices the close
// and unsubscribes.
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

// Image is a passthrough to the underlying client for the art proxy.
func (h *Hub) Image(ctx context.Context, itemID string, kind jellyfin.ImageKind, tag string) (io.ReadCloser, string, error) {
	return h.client.Image(ctx, itemID, kind, tag)
}

// Snapshot returns the cached snapshot if it is younger than one interval,
// otherwise it fetches directly. It never starts the poll loop.
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

// Close stops the loop and closes every subscriber channel. Safe to call more
// than once and after the last unsub.
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

// changed reports whether next is meaningfully different from prev: a new or
// gone session, a pause / play-method / remote flip, a transcode target change,
// or progress that moved at least a whole percent.
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
