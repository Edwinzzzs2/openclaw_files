package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var staticFiles embed.FS

func Handler() http.Handler {
	content, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requestPath == "" || requestPath == "." {
			requestPath = "index.html"
		}
		info, err := fs.Stat(content, requestPath)
		if err != nil || info.IsDir() {
			requestPath = "index.html"
		}
		cloned := r.Clone(r.Context())
		urlCopy := *r.URL
		urlCopy.Path = "/" + requestPath
		cloned.URL = &urlCopy
		fileServer.ServeHTTP(w, cloned)
	})
}
