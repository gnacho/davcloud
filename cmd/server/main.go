// Command davcloud is a CalDAV/CardDAV server designed as a drop-in,
// Go-based replacement for the Radicale sidecar in an OpenCloud deployment.
//
// Contract with OpenCloud (same as Radicale):
//   - listens on :5232 (internal docker network only!)
//   - trusts the X-Remote-User header injected by the OpenCloud auth proxy
//   - stores data in the Radicale "multifilesystem" layout, so the existing
//     radicale-data volume can be mounted directly (zero migration)
//   - auto-creates "Personal Calendar" and "Personal Address Book" per user
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"davcloud/internal/auth"
	caldavbe "davcloud/internal/caldav"
	carddavbe "davcloud/internal/carddav"
	"davcloud/internal/store"
)

func main() {
	addr := flag.String("addr", ":5232", "listen address (bind to the internal docker network ONLY)")
	dataDir := flag.String("data-dir", "./data", "data directory (Radicale multifilesystem layout)")
	uiDir := flag.String("ui-dir", "web/dist", "directory with the built web UI (SPA)")
	devUser := flag.String("dev-user", "", "development only: force this user, ignore X-Remote-User")
	flag.Parse()

	st := store.New(*dataDir)
	mw := auth.Middleware(*devUser)

	// go-webdav v0.7 API: Handler{Backend, Prefix}. Prefix must match the
	// mount point so generated URLs are absolute.
	calHandler := &caldav.Handler{Backend: caldavbe.New(st), Prefix: "/caldav"}
	cardHandler := &carddav.Handler{Backend: carddavbe.New(st), Prefix: "/carddav"}

	mux := http.NewServeMux()
	// Same paths OpenCloud's proxy forwards: /caldav and /carddav
	mux.Handle("/caldav/", mw(http.StripPrefix("/caldav", calHandler)))
	mux.Handle("/carddav/", mw(http.StripPrefix("/carddav", cardHandler)))

	// Web UI (SPA with fallback to index.html)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean("/" + r.URL.Path)
		full := filepath.Join(*uiDir, clean)
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, full)
			return
		}
		http.ServeFile(w, r, filepath.Join(*uiDir, "index.html"))
	})

	log.Printf("davcloud listening on %s (data: %s)", *addr, *dataDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
