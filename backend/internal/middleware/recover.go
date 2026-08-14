package middleware

import (
	"log"
	"net/http"

	"okf-converter/backend/internal/httpx"
)

// Recover catches panics in downstream handlers and responds with the same
// generic {"message": ...} shape the previous Express error handler used,
// instead of letting the connection die with no body.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic handling %s %s: %v", r.Method, r.URL.Path, rec)
				httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
			}
		}()

		next.ServeHTTP(w, r)
	})
}
