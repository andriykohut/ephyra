package api

import (
	"net/http"
	"strconv"

	"github.com/andriykohut/ephyra/internal/store"
)

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	mode := q.Get("mode")
	if mode == "" {
		mode = "never"
	}
	if mode != "never" && mode != "stale" {
		writeError(w, http.StatusBadRequest, "bad_request", "mode must be never|stale")
		return
	}
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "size"
	}
	if sortBy != "size" && sortBy != "added" {
		writeError(w, http.StatusBadRequest, "bad_request", "sort must be size|added")
		return
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1..1000")
			return
		}
		limit = n
	}

	m, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ephyra-cleanup-`+mode+`.csv"`)
		if err := s.st.StreamCleanupCSV(ctx, mode, "", s.now(), w); err != nil {
			s.log.Error("cleanup csv", "err", err)
		}
		return
	}

	res, err := s.st.ReadCleanup(ctx, store.CleanupParams{Mode: mode, Sort: sortBy, Limit: limit, Library: "", Now: s.now()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := store.IsStale(m, ok, s.cfg.RefreshLibrary, s.now())
	writeJSON(w, http.StatusOK, res, s.meta(stale))
}
