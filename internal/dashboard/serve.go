// Package dashboard serves the embedded React build as static files.
package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded React app.
// It must be mounted at /dashboard/ in the router.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// embed.FS.Sub only fails if the prefix doesn't exist, which is a
		// programming error caught at startup.
		panic("dashboard: dist directory not embedded: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All dashboard subroutes serve index.html for client-side routing.
		// Static assets (JS/CSS with content hashes) are served directly.
		fileServer.ServeHTTP(w, r)
	})
}
