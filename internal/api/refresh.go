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
	// Trigger returns nothing, so "triggered" means "we asked the scheduler",
	// not "the job finished". Fan out over whichever jobs this call covers.
	data := map[string]map[string]bool{}
	for _, j := range []string{"library", "watch"} {
		if job == "all" || job == j {
			data[j] = map[string]bool{"triggered": true}
		}
	}
	writeJSON(w, http.StatusAccepted, data, s.meta(false))
}
