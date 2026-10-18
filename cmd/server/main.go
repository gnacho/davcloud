// Command davcloud is a CalDAV/CardDAV server designed as a drop-in,
// Go-based replacement for the Radicale sidecar in an OpenCloud deployment.
//
// Contract with OpenCloud (same as Radicale):
//   - listens on 127.0.0.1:5232 (internal network only!)
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
	"strconv"
	"time"

	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"davcloud/internal/auth"
	caldavbe "davcloud/internal/caldav"
	carddavbe "davcloud/internal/carddav"
	"davcloud/internal/dav"
	"davcloud/internal/store"
)

// limits holds the request limits applied by the server. They exist because a
// DAV client (or anyone who can reach the port and set X-Remote-User) must not
// be able to fill the disk or the memory of the host: Radicale bounds the same
// things with max_content_length, max_resource_size, max_expand_occurrence,
// max_connections and a socket timeout.
type limits struct {
	// RequestSize caps any request body, in bytes.
	RequestSize int64
	// ItemSize caps a single calendar object or vCard, in bytes.
	ItemSize int64
	// Expand caps the recurrence instances one object may produce while a
	// time-range query is matched.
	Expand int
}

// Defaults, mirroring Radicale's configuration so a drop-in deployment keeps
// the same expectations: max_content_length (100 MB), max_resource_size
// (10 MB) and max_expand_occurrence (10000).
const (
	defaultMaxRequestSize = 100 << 20 // 100 MiB
	defaultMaxItemSize    = 10 << 20  // 10 MiB
	defaultMaxExpand      = 10000
)

// defaultLimits are the limits used when a caller passes none.
func defaultLimits() limits {
	return limits{RequestSize: defaultMaxRequestSize, ItemSize: defaultMaxItemSize, Expand: defaultMaxExpand}
}

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
func newHandler(st *store.Store, uiDir, devUser string, davRoot bool, lim limits) http.Handler {
	mw := auth.Middleware(devUser)

	calBackend := caldavbe.New(st, "/caldav")
	calBackend.MaxExpandedInstances = lim.Expand
	cardBackend := carddavbe.New(st, "/carddav")

	calHandler := &dav.Handler{
		Kind:        dav.KindCalendar,
		Store:       st,
		Prefix:      "/caldav",
		MaxItemSize: lim.ItemSize,
		Legacy:      &caldav.Handler{Backend: calBackend, Prefix: "/caldav"},
	}
	cardHandler := &dav.Handler{
		Kind:        dav.KindAddressBook,
		Store:       st,
		Prefix:      "/carddav",
		MaxItemSize: lim.ItemSize,
		Legacy:      &carddav.Handler{Backend: cardBackend, Prefix: "/carddav"},
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
		return limitRequestBody(mux, lim.RequestSize)
	}
	// Same paths OpenCloud's proxy forwards: /caldav and /carddav
	mux.Handle("/caldav/", mw(calHandler))
	mux.Handle("/carddav/", mw(cardHandler))
	// RFC 6764 service discovery: clients that are only given a hostname ask
	// here first and follow the redirect to their home set.
	mux.Handle("/.well-known/caldav", mw(wellKnownHandler("/caldav")))
	mux.Handle("/.well-known/carddav", mw(wellKnownHandler("/carddav")))
	mux.HandleFunc("/", spaHandler(uiDir))
	return limitRequestBody(mux, lim.RequestSize)
}

// limitRequestBody caps every request body before any handler sees it, so no
// handler can be made to buffer an unbounded amount of data. A handler that
// wants a tighter cap (a single calendar object, say) wraps the body again.
func limitRequestBody(next http.Handler, max int64) http.Handler {
	if max <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}

// newServer builds the HTTP server with explicit timeouts. Radicale sets a
// 30 second socket timeout and caps concurrent connections; without any
// timeout here a slow client could hold connections open forever.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10, // 64 KiB of request headers
	}
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

// envInt64 returns the integer value of the environment variable key, or def
// when it is unset or unparseable.
func envInt64(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		log.Printf("davcloud: ignoring unparseable %s=%q", key, v)
	}
	return def
}

// Build information, stamped at build time:
//
//	go build -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	addr := flag.String("addr", envOr("DAVCLOUD_ADDR", ":5232"), "listen address (bind to the internal network ONLY)")
	dataDir := flag.String("data-dir", envOr("DAVCLOUD_DATA", "./data"), "data directory (Radicale multifilesystem layout)")
	uiDir := flag.String("ui-dir", envOr("DAVCLOUD_UI", "web/dist"), "directory with the built web UI (SPA)")
	devUser := flag.String("dev-user", envOr("DAVCLOUD_DEV_USER", ""), "development only: force this user, ignore X-Remote-User")
	davRoot := flag.Bool("dav-root", envOr("DAVCLOUD_DAV_ROOT", "") != "", "mount the DAV endpoints at / (mode used behind the OpenCloud proxy, which strips the /caldav and /carddav prefixes); the SPA is not served")
	maxRequestSize := flag.Int64("max-request-size", envInt64("DAVCLOUD_MAX_REQUEST_SIZE", defaultMaxRequestSize), "maximum request body size in bytes")
	maxItemSize := flag.Int64("max-item-size", envInt64("DAVCLOUD_MAX_ITEM_SIZE", defaultMaxItemSize), "maximum size of a single calendar object or vCard in bytes")
	maxExpand := flag.Int("max-expand", int(envInt64("DAVCLOUD_MAX_EXPAND", int64(defaultMaxExpand))), "maximum recurrence instances one object may expand to while matching a time-range query")
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
	log.Printf("davcloud: limits: request %d bytes, object %d bytes, %d expanded instances", *maxRequestSize, *maxItemSize, *maxExpand)
	lim := limits{RequestSize: *maxRequestSize, ItemSize: *maxItemSize, Expand: *maxExpand}
	if err := newServer(*addr, newHandler(st, *uiDir, *devUser, *davRoot, lim)).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
