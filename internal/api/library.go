package api

import (
	"net/http"

	"github.com/andrii/ephyra/internal/store"
)

func (s *Server) handleLibraryOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first refresh has not completed")
		return
	}
	ov, err := s.st.ReadLibraryOverview(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := store.IsStale(m, ok, s.cfg.RefreshLibrary, s.now())
	writeJSON(w, http.StatusOK, ov, s.meta(stale))
}
