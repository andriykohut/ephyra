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
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "queued": true}, s.meta(false))
}
