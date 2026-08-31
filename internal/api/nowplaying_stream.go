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
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_ = rc.Flush()
		}
	}
}

// writeSSE marshals data and writes one "event: <name>" frame, then flushes so
// the client sees it now rather than whenever the buffer happens to fill.
func writeSSE(w io.Writer, rc *http.ResponseController, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	_ = rc.Flush()
}
