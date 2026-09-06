package api

import "net/http"

// handleLibraries serves the full set of known library names, sorted by item
// count desc then name. Backs the library filter dropdown on every
// library-aware page. No independent staleness concept: the library list is
// only as fresh as the last library refresh, which library/overview already
// surfaces.
func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	libs, err := s.st.ReadLibraries(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libs, s.meta(false))
}
