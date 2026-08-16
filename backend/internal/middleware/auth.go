// Package middleware holds cross-cutting net/http middleware.
package middleware

import (
	"context"
	"net/http"

	"okf-converter/backend/internal/auth"
	"okf-converter/backend/internal/httpx"
)

type ctxKey int

const userCtxKey ctxKey = iota

// UserLoader is satisfied by *auth.UserLoader. Declaring it here (rather
// than depending on the concrete type) keeps RequireAuth testable with a
// fake loader that doesn't touch a database.
type UserLoader interface {
	UserFromRequest(r *http.Request) (auth.User, error)
}

// RequireAuth is the idiomatic-Go replacement for Express's requireAuth
// middleware mutating req.user: the authenticated user is attached to the
// request's context instead, retrievable downstream via UserFromContext.
func RequireAuth(loader UserLoader) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := loader.UserFromRequest(r)
			if err != nil {
				httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
				return
			}

			ctx := context.WithValue(r.Context(), userCtxKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext retrieves the user attached by RequireAuth.
func UserFromContext(ctx context.Context) (auth.User, bool) {
	user, ok := ctx.Value(userCtxKey).(auth.User)
	return user, ok
}
