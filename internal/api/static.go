package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// NewStaticHandler serves files from dist and falls back to index.html for
// anything that isn't a real file (client-side routes). No index.html means the
// frontend wasn't built; it says so with a 503.
func NewStaticHandler(dist fs.FS) http.Handler {
	_, hasIndex := statFile(dist, "index.html")
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasIndex {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("frontend not built (run `make web`)"))
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if _, ok := statFile(dist, p); ok {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// root, /index.html, or an unknown client route: serve index.html.
		// (http.FileServer would 301 /index.html -> /, so do it by hand.)
		serveIndex(w, dist)
	})
}

func serveIndex(w http.ResponseWriter, dist fs.FS) {
	f, err := dist.Open("index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}

func statFile(fsys fs.FS, name string) (fs.FileInfo, bool) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return nil, false
	}
	return fi, true
}
