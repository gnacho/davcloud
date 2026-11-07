// Package auth implements the OpenCloud auth contract: the identity arrives
// via the X-Remote-User header, injected by the OpenCloud reverse proxy after
// it has authenticated the user (session cookie or app token).
//
// SECURITY: this server must NEVER be reachable from outside the internal
// internal network. Anyone able to send arbitrary X-Remote-User headers can
// impersonate any user.
package auth

import (
	"context"
	"net/http"
)

type ctxKey struct{}

// User returns the authenticated username stored in the context.
func User(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

// Middleware extracts the identity. In dev mode (devUser != "") the header is
// ignored and the given user is forced.
//
// OPTIONS is the one exception: it is capability discovery, answered with the
// compliance headers and an Allow list that are identical for every user and
// touch no data, while some proxies (OpenCloud's) do not authenticate that
// method and therefore never set X-Remote-User. Rejecting it made every
// capability probe look like an authentication failure. Every method that
// reads or writes data still requires the identity.
func Middleware(devUser string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := devUser
			if user == "" {
				user = r.Header.Get("X-Remote-User")
			}
			if user == "" && r.Method != http.MethodOptions {
				http.Error(w, "davcloud: missing identity (OpenCloud proxy must set X-Remote-User)", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
		})
	}
}
