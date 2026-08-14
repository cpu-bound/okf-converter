// Package httpx holds small cross-cutting HTTP helpers shared by handlers.
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as a JSON response body with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"message": message} with the given status code, matching
// the error body shape the frontend expects from every endpoint.
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"message": message})
}
