package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// handleNowPlayingStream is the SSE endpoint. It sends one snapshot frame, then
// relays hub events until the client goes away, with a heartbeat comment every
// 15s so proxies keep the connection open. The defer unsub() is what stops the
// hub poll loop once the last watcher leaves — no standing load on Jellyfin.
func (s *Server) handleNowPlayingStream(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)

	_, ch, unsub := s.live.Subscribe()
	defer unsub()

	if writeSSE(w, rc, "snapshot", s.live.Snapshot(r.Context())) != nil {
		return
	}

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
			if writeSSE(w, rc, ev.Kind, ev.Data) != nil {
				return
			}
		case <-hb.C:
			_ = rc.SetWriteDeadline(time.Now().Add(writeFrameTimeout))
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// writeFrameTimeout bounds one frame write. A client that finishes the handshake
// and then stops reading fills the TCP window and Write blocks forever — outside
// the select, so r.Context().Done() can't help. The handler would never return,
// defer unsub() would never run, and the poll loop would keep hitting Jellyfin
// for nobody. A dead client also sends no RST, so this deadline is the only way
// out.
const writeFrameTimeout = 10 * time.Second

// writeSSE marshals data and writes one "event: <name>" frame, then flushes so
// the client sees it now rather than whenever the buffer happens to fill. A
// non-nil return means the connection is done and the caller should give up on
// it; a frame that won't marshal is not the client's fault and is skipped.
func writeSSE(w io.Writer, rc *http.ResponseController, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	_ = rc.SetWriteDeadline(time.Now().Add(writeFrameTimeout))
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	_ = rc.Flush()
	return nil
}
