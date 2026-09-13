package api

import (
	"net/http"

	"github.com/andriykohut/ephyra/internal/live"
	"github.com/andriykohut/ephyra/internal/store"
)

type overview struct {
	store.ProfileOverview
	NowPlaying *live.Session `json:"now_playing"`
}

func (s *Server) handleProfileOverview(w http.ResponseWriter, r *http.Request) {
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

	library := r.URL.Query().Get("library")
	if library == "all" {
		library = ""
	}

	userID := r.PathValue("userID")
	ov, ok, err := s.st.ReadProfileOverview(ctx, userID, rng, library, s.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	out := overview{ProfileOverview: ov}

	if s.live != nil {
		if snap := s.live.Snapshot(ctx); snap != nil {
			out.NowPlaying = sessionForUser(snap, userID)
		}
	}

	writeJSON(w, http.StatusOK, out, s.meta(s.profileStale(ctx)))
}

func sessionForUser(snap *live.Snapshot, userID string) *live.Session {
	if userID == "" {
		return nil
	}
	for i := range snap.Sessions {
		if snap.Sessions[i].UserID == userID {
			return &snap.Sessions[i]
		}
	}
	return nil
}
