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

// Sessions runs the canned response closure while holding f.mu so that state the
// closure reads (see set) is synchronised against the test goroutine.
func (f *fakeClient) Sessions(context.Context) ([]jellyfin.RawSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.sessions()
}
func (f *fakeClient) SystemInfo(context.Context) (jellyfin.ServerInfo, error) {
	return jellyfin.ServerInfo{Name: "S", Version: "10.11.11"}, nil
}
func (f *fakeClient) Image(context.Context, string, jellyfin.ImageKind, string) (io.ReadCloser, string, error) {
	return io.NopCloser(nil), "image/png", nil
}

// set mutates test state the sessions closure captures, under the same lock the
// poll goroutine takes in Sessions. Pair it with waitCalls after a tick so the
// mutation lands after the poll that should see the old value.
func (f *fakeClient) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
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
	// Subscribe starts the loop; the first poll runs immediately and, because
	// changed(nil, …) is true, publishes an "update". Drain it.
	waitCalls(t, fc, 1)
	if k, _, _ := readEvent(t, ch1, time.Second); k != "update" {
		t.Fatalf("first event after subscribe should be update, got %q", k)
	}

	tick <- time.Now()
	waitCalls(t, fc, 2)

	_, ch2, unsub2 := h.Subscribe()
	_ = ch2
	if h.SubscriberCount() != 2 {
		t.Fatalf("subs = %d", h.SubscriberCount())
	}
	unsub1()
	unsub2()

	// Let the loop goroutine observe the now-closed stop channel and return
	// before we assert that nothing consumes ticks any more. Without this the
	// loop's select could still be racing the send below.
	time.Sleep(100 * time.Millisecond)

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
	paused := false
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", pos, paused), nil }}
	h, tick := newHub(t, fc)
	_, ch, unsub := h.Subscribe()
	defer unsub()
	waitCalls(t, fc, 1)           // initial poll on subscribe
	readEvent(t, ch, time.Second) // drain the initial update (changed(nil, …) is true)

	// +30s of a 6000s runtime = +0.5% -> no update
	fc.set(func() { pos = 130 })
	tick <- time.Now()
	waitCalls(t, fc, 2)
	assertNoEvent(t, ch)

	// +90s more -> crosses 1% -> update
	fc.set(func() { pos = 220 })
	tick <- time.Now()
	waitCalls(t, fc, 3)
	if k, _, _ := readEvent(t, ch, time.Second); k != "update" {
		t.Fatalf("want update, got %q", k)
	}

	// pause toggle -> update even without progress
	fc.set(func() { paused = true })
	tick <- time.Now()
	waitCalls(t, fc, 4)
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

	// initial poll failed with no prior snapshot -> exactly one degraded frame
	if k, d, _ := readEvent(t, ch, time.Second); k != "degraded" {
		t.Fatalf("want degraded, got %q %v", k, d)
	}
	// still failing -> still degraded, but no duplicate event
	tick <- time.Now()
	waitCalls(t, fc, 2)
	assertNoEvent(t, ch)

	fc.set(func() { fail = false })
	tick <- time.Now()
	waitCalls(t, fc, 3)
	if k, _, _ := readEvent(t, ch, time.Second); k != "update" {
		t.Fatalf("recovery should emit update, got %q", k)
	}
}

func TestHub_SlowSubscriberDropped(t *testing.T) {
	fc := &fakeClient{sessions: func() ([]jellyfin.RawSession, error) { return oneSession("a", 100, false), nil }}
	h, _ := newHub(t, fc)
	_, ch, _ := h.Subscribe()
	// never drain ch (cap 4). the loop's first poll plus 5 publishes overflow it.
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
