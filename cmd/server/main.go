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
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"davcloud/internal/auth"
	caldavbe "davcloud/internal/caldav"
	carddavbe "davcloud/internal/carddav"
	"davcloud/internal/dav"
	"davcloud/internal/store"
)

// newHandler wires the DAV endpoints and the SPA into one handler. It is a
// separate function so tests can exercise the real routing.
//
// The DAV endpoints are served by internal/dav, which implements the flat
// Radicale URL layout (/<user>/<collection>/<item>) and delegates REPORT,
// GET, HEAD and PUT to go-webdav. The mount prefix is deliberately NOT
// stripped: the library backend and our own handler both expect server paths
// ("/caldav/<user>/...") so that the hrefs they emit match Radicale's.
//
// With davRoot the DAV handler is additionally mounted at "/", which is how
// the OpenCloud proxy reaches davcloud: it forwards /caldav/ with the prefix
// stripped from the path and X-Script-Name set (verified against a live
// deployment's proxy.yaml). The SPA is not served in that mode, because every
// path belongs to DAV; that matches how Radicale runs behind the proxy.
func newHandler(st *store.Store, uiDir, devUser string, davRoot bool) http.Handler {
	mw := auth.Middleware(devUser)

	calHandler := &dav.Handler{
		Kind:   dav.KindCalendar,
		Store:  st,
		Prefix: "/caldav",
		Legacy: &caldav.Handler{Backend: caldavbe.New(st, "/caldav"), Prefix: "/caldav"},
	}
	cardHandler := &dav.Handler{
		Kind:   dav.KindAddressBook,
		Store:  st,
		Prefix: "/carddav",
		Legacy: &carddav.Handler{Backend: carddavbe.New(st, "/carddav"), Prefix: "/carddav"},
	}

	mux := http.NewServeMux()
	if davRoot {
		// Behind the OpenCloud proxy every path is DAV and the only signal
		// telling calendars from address books is X-Script-Name, which the
		// proxy sets to /caldav or /carddav.
		root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Script-Name") == "/carddav" {
				cardHandler.ServeHTTP(w, r)
				return
			}
			calHandler.ServeHTTP(w, r)
		})
		mux.Handle("/", mw(root))
		return mux
	}
	// Same paths OpenCloud's proxy forwards: /caldav and /carddav
	mux.Handle("/caldav/", mw(calHandler))
	mux.Handle("/carddav/", mw(cardHandler))
	// RFC 6764 service discovery: clients that are only given a hostname ask
	// here first and follow the redirect to their home set.
	mux.Handle("/.well-known/caldav", mw(wellKnownHandler("/caldav")))
	mux.Handle("/.well-known/carddav", mw(wellKnownHandler("/carddav")))
	mux.HandleFunc("/", spaHandler(uiDir))
	return mux
}

// wellKnownHandler redirects /.well-known/caldav|carddav to the user's home
// set, as RFC 6764 requires.
func wellKnownHandler(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.User(r.Context())
		http.Redirect(w, r, prefix+"/"+user+"/", http.StatusMovedPermanently)
	})
}

// spaHandler serves the built web UI, falling back to index.html so client
// side routes work on reload.
func spaHandler(uiDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Clean("/" + r.URL.Path)
		full := filepath.Join(uiDir, clean)
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, full)
			return
		}
		http.ServeFile(w, r, filepath.Join(uiDir, "index.html"))
	}
}

// envOr returns the value of the environment variable key, or def if unset.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Build information, stamped at build time:
//
//	go build -ldflags "-X main.version=v1.0.0 -X main.commit=<sha> -X main.date=<date>"
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	addr := flag.String("addr", envOr("DAVCLOUD_ADDR", ":5232"), "listen address (bind to the internal docker network ONLY)")
	dataDir := flag.String("data-dir", envOr("DAVCLOUD_DATA", "./data"), "data directory (Radicale multifilesystem layout)")
	uiDir := flag.String("ui-dir", envOr("DAVCLOUD_UI", "web/dist"), "directory with the built web UI (SPA)")
	devUser := flag.String("dev-user", envOr("DAVCLOUD_DEV_USER", ""), "development only: force this user, ignore X-Remote-User")
	davRoot := flag.Bool("dav-root", envOr("DAVCLOUD_DAV_ROOT", "") != "", "mount the DAV endpoints at / (mode used behind the OpenCloud proxy, which strips the /caldav and /carddav prefixes); the SPA is not served")
	flag.Parse()
	if *showVersion {
		fmt.Printf("davcloud %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	st := store.New(*dataDir)
	if *devUser != "" {
		log.Printf("davcloud: DEV MODE, every request is treated as user %q", *devUser)
	}
	if *davRoot {
		log.Printf("davcloud: DAV root mode (behind a prefix-stripping proxy), the SPA is not served")
	}

	log.Printf("davcloud %s listening on %s (data: %s, ui: %s)", version, *addr, *dataDir, *uiDir)
	if err := http.ListenAndServe(*addr, newHandler(st, *uiDir, *devUser, *davRoot)); err != nil {
		log.Fatal(err)
	}
}
