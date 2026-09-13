package api

import (
	"container/list"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

var artIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

type artEntry struct {
	key  string
	ct   string
	body []byte
}

// entryOverhead is charged per cache entry regardless of body size, so a miss
// (nil body) still counts toward the byte budget. Half the people in a real
// library have no image, and every one of those misses is keyed on an
// unvalidated, unauthenticated name param -- without this, a cache of nothing
// but misses never evicts.
const entryOverhead = 64

func cost(e *artEntry) int {
	return len(e.key) + len(e.body) + entryOverhead
}

// artCache is a byte-budgeted LRU in front of Jellyfin's image endpoints. Art
// is immutable per tag, so a hit never needs revalidating. A nil body marks a
// cached 404 -- about half the people in a real library have no image, so
// caching the miss is what keeps a profile page from re-asking Jellyfin for
// the same 404 on every render.
type artCache struct {
	mu    sync.Mutex
	cap   int
	bytes int
	ll    *list.List
	m     map[string]*list.Element
}

func newArtCache(capBytes int) *artCache {
	return &artCache{cap: capBytes, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *artCache) get(key string) (string, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return "", nil, false
	}
	c.ll.MoveToFront(el)
	e := el.Value.(*artEntry)
	return e.ct, e.body, true
}

func (c *artCache) put(key, ct string, body []byte) {
	c.store(&artEntry{key: key, ct: ct, body: body})
}

func (c *artCache) putMiss(key string) {
	c.store(&artEntry{key: key})
}

func (c *artCache) store(e *artEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[e.key]; ok {
		c.bytes -= cost(el.Value.(*artEntry))
		el.Value = e
		c.ll.MoveToFront(el)
	} else {
		c.m[e.key] = c.ll.PushFront(e)
	}
	c.bytes += cost(e)
	for c.bytes > c.cap && c.ll.Len() > 0 {
		back := c.ll.Remove(c.ll.Back()).(*artEntry)
		delete(c.m, back.key)
		c.bytes -= cost(back)
	}
}

// handleArtItem proxies item art so the browser never needs the API key.
func (s *Server) handleArtItem(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("itemId")
	if !artIDRe.MatchString(itemID) {
		writeError(w, http.StatusBadRequest, "bad_request", "bad item id")
		return
	}
	var kind jellyfin.ImageKind
	switch r.URL.Query().Get("kind") {
	case "", "primary":
		kind = jellyfin.ImagePrimary
	case "backdrop":
		kind = jellyfin.ImageBackdrop
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be primary|backdrop")
		return
	}
	tag := r.URL.Query().Get("tag")
	key := "item/" + itemID + "/" + string(kind) + "/" + tag
	s.serveArtFrom(w, r, key, func() (io.ReadCloser, string, error) {
		return s.live.Image(r.Context(), itemID, kind, tag)
	})
}

// handleArtPerson proxies a person's image, looked up by name. There is no
// person id in jellyfin.db that means anything to the Jellyfin API.
func (s *Server) handleArtPerson(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	key := "person/" + name
	s.serveArtFrom(w, r, key, func() (io.ReadCloser, string, error) {
		return s.live.PersonImage(r.Context(), name)
	})
}

func (s *Server) handleArtUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")
	if !artIDRe.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "bad_request", "bad user id")
		return
	}
	key := "user/" + userID
	s.serveArtFrom(w, r, key, func() (io.ReadCloser, string, error) {
		return s.live.UserImage(r.Context(), userID)
	})
}

func (s *Server) serveArtFrom(w http.ResponseWriter, r *http.Request, key string, fetch func() (io.ReadCloser, string, error)) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
	if ct, body, ok := s.art.get(key); ok {
		if body == nil {
			serveArtMiss(w)
			return
		}
		s.serveArt(w, ct, body)
		return
	}
	rc, ct, err := fetch()
	if err != nil {
		if errors.Is(err, jellyfin.ErrNotFound) {
			s.art.putMiss(key)
			serveArtMiss(w)
			return
		}
		// err carries the Jellyfin base URL; that stays server-side.
		s.log.Warn("art fetch failed", "key", key, "err", err)
		writeError(w, http.StatusBadGateway, "upstream", "art fetch failed")
		return
	}
	// bounded so one oversized upstream image can't sit in the cache; 8 MB is
	// far above a resized poster, backdrop, or face
	body, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	_ = rc.Close()
	if err != nil {
		s.log.Warn("art read failed", "key", key, "err", err)
		writeError(w, http.StatusBadGateway, "upstream", "art fetch failed")
		return
	}
	s.art.put(key, ct, body)
	s.serveArt(w, ct, body)
}

func (s *Server) serveArt(w http.ResponseWriter, ct string, body []byte) {
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(body)
}

func serveArtMiss(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusNotFound)
}
