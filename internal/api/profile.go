package api

import (
	"context"
	"net/http"

	"github.com/andriykohut/ephyra/internal/store"
)

func (s *Server) handleProfileList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := s.st.ReadProfileList(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data, s.meta(s.profileStale(ctx)))
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "30d"
	}
	if !validRanges[rng] {
		writeError(w, http.StatusBadRequest, "bad_request", "range must be 30d|90d|1y|all")
		return
	}

	if _, ok, err := s.st.GetRefreshMeta(ctx, "library"); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	} else if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	// TODO(task-17): thread the real library query param through here.
	data, ok, err := s.st.ReadProfile(ctx, r.PathValue("userID"), rng, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	writeJSON(w, http.StatusOK, data, s.meta(s.profileStale(ctx)))
}

// profileStale mirrors the watch page: stale only when the plugin is meant to be
// there and the last watch run is old.
func (s *Server) profileStale(ctx context.Context) bool {
	wm, wok, _ := s.st.GetRefreshMeta(ctx, "watch")
	if !wok || !wm.PluginAvailable {
		return false
	}
	return store.IsStale(wm, wok, s.cfg.RefreshWatch, s.now())
}
