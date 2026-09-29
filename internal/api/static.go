package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spa serves the statically exported Next.js app. Routes are exported as
// "<route>.html" (or "<route>/index.html"); unknown paths fall back to
// index.html. Hashed assets under _next/static are cached forever.
func spa(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	exists := func(p string) bool {
		fi, err := fs.Stat(files, p)
		return err == nil && !fi.IsDir()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exists("index.html") {
			http.Error(w, "The web UI is not embedded in this binary: build it with scripts/build.ps1 (or scripts/build.sh).", http.StatusNotFound)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case p == "" || p == ".":
			p = "index.html"
		case exists(p):
		case exists(p + ".html"):
			p += ".html"
		case exists(path.Join(p, "index.html")):
			p = path.Join(p, "index.html")
		default:
			p = "index.html"
		}
		if strings.HasPrefix(p, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "index.html" || strings.HasSuffix(p, "/index.html") {
			// Both FileServer and ServeFileFS redirect a request *path* ending
			// in /index.html; serving by name under the original path doesn't.
			http.ServeFileFS(w, r, files, p)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + p
		fileServer.ServeHTTP(w, r2)
	})
}
