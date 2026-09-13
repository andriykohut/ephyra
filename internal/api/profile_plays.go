package api

import (
	"net/http"
	"strconv"

	"github.com/andriykohut/ephyra/internal/store"
)

func (s *Server) handleProfilePlays(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1..200")
			return
		}
		limit = n
	}

	var cur *store.PlayCursor
	if at := q.Get("before"); at != "" {
		id, err := strconv.ParseInt(q.Get("before_id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "before_id must accompany before")
			return
		}
		cur = &store.PlayCursor{At: at, RowID: id}
	} else if q.Get("before_id") != "" {
		writeError(w, http.StatusBadRequest, "bad_request", "before must accompany before_id")
		return
	}

	library := q.Get("library")
	if library == "all" {
		library = ""
	}

	rows, next, err := s.st.ReadPlays(ctx, r.PathValue("userID"), library, cur, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plays": rows, "next_cursor": next,
	}, s.meta(s.profileStale(ctx)))
}
