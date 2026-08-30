package api

import (
	"net/http"

	"github.com/andriykohut/ephyra/internal/store"
)

var validRanges = map[string]bool{"30d": true, "90d": true, "1y": true, "all": true}

func (s *Server) handleWatchStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	rng := q.Get("range")
	if rng == "" {
		rng = "30d"
	}
	if !validRanges[rng] {
		writeError(w, http.StatusBadRequest, "bad_request", "range must be 30d|90d|1y|all")
		return
	}
	user := q.Get("user")
	if user == "all" {
		user = ""
	}

	_, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	data, err := s.st.ReadWatchStats(ctx, store.WatchStatsParams{Range: rng, User: user, Now: s.now()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	stale := false
	if wm, wok, _ := s.st.GetRefreshMeta(ctx, "watch"); wok && wm.PluginAvailable {
		stale = store.IsStale(wm, wok, s.cfg.RefreshWatch, s.now())
	}
	writeJSON(w, http.StatusOK, data, s.meta(stale))
}
