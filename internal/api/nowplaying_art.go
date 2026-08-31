package api

import (
	"container/list"
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

// artCache is a tiny LRU in front of Jellyfin's image endpoint. Art is
// immutable per tag, so a hit never needs revalidating.
type artCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List
	m   map[string]*list.Element
}

func newArtCache(capacity int) *artCache {
	return &artCache{cap: capacity, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *artCache) get(key string) (string, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*artEntry)
		return e.ct, e.body, true
	}
	return "", nil, false
}

func (c *artCache) put(key, ct string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		el.Value.(*artEntry).ct, el.Value.(*artEntry).body = ct, body
		return
	}
	el := c.ll.PushFront(&artEntry{key: key, ct: ct, body: body})
	c.m[key] = el
	for c.ll.Len() > c.cap {
		back := c.ll.Back()
		c.ll.Remove(back)
		delete(c.m, back.Value.(*artEntry).key)
	}
}

// handleNowPlayingArt proxies item art so the browser never needs the API key.
func (s *Server) handleNowPlayingArt(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "live subsystem not configured")
		return
	}
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
	key := itemID + "/" + string(kind) + "/" + tag

	if ct, body, ok := s.art.get(key); ok {
		s.serveArt(w, ct, body)
		return
	}
	rc, ct, err := s.live.Image(r.Context(), itemID, kind, tag)
	if err != nil {
		// err carries the Jellyfin base URL; that stays server-side.
		s.log.Warn("art fetch failed", "item", itemID, "kind", kind, "err", err)
		writeError(w, http.StatusBadGateway, "upstream", "art fetch failed")
		return
	}
	// bounded so one oversized upstream image can't sit in the LRU; 8 MB is far
	// above a resized poster or backdrop
	body, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	_ = rc.Close()
	if err != nil {
		s.log.Warn("art read failed", "item", itemID, "kind", kind, "err", err)
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
