package webserver

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Static serves the built web interface from files. Any path that isn't a
// file gets index.html, so the front end's own page addresses (/movies,
// /settings) work when opened directly or refreshed.
func Static(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(files, name); err != nil || info.IsDir() {
			serveIndex(w, r, files)
			return
		}
		if name == "index.html" {
			serveIndex(w, r, files)
			return
		}
		// app.js and app.css change on every build; the browser must check.
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, files fs.FS) {
	page, err := fs.ReadFile(files, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("The web interface isn't built. Run `make ui`, then rebuild the program.\n"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(page)
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	// Posters come from TMDB's image server; nothing else is loaded from
	// outside.
	h.Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data: https://image.tmdb.org https://static.tvmaze.com; "+
			"style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}
