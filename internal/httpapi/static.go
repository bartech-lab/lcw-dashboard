package httpapi

import (
	"io/fs"
	"net/http"
	"strings"
)

// staticHandler serves the embedded bundle. Asset filenames carry no content
// hash, so the cache headers have to be right: a stale bundle.js against a fresh
// index.html is an infuriating bug class.
func (s *Server) staticHandler() http.Handler {
	if s.assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "frontend not built; run: go generate ./... && go build", http.StatusNotFound)
		})
	}
	files := http.FileServer(http.FS(s.assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(s.assets, clean); err != nil {
			// Unknown path: serve the SPA shell so client routing works on reload.
			serveIndex(w, r, s.assets)
			return
		}
		if clean == "index.html" {
			serveIndex(w, r, s.assets)
			return
		}
		w.Header().Set("cache-control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	body, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "index.html missing from the embedded bundle", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Write(body)
}
