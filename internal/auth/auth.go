// Package auth implements the OpenCloud auth contract: the identity arrives
// via the X-Remote-User header, injected by the OpenCloud reverse proxy after
// it has authenticated the user (session cookie or app token).
//
// SECURITY: this server must NEVER be reachable from outside the internal
// docker network. Anyone able to send arbitrary X-Remote-User headers can
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
func Middleware(devUser string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := devUser
			if user == "" {
				user = r.Header.Get("X-Remote-User")
			}
			if user == "" {
				http.Error(w, "davcloud: missing identity (OpenCloud proxy must set X-Remote-User)", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
		})
	}
}
