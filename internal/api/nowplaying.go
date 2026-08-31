package api

import "net/http"

func (s *Server) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.live.Snapshot(r.Context()), s.meta(false))
}
