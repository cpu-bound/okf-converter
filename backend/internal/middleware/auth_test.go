package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"okf-converter/backend/internal/auth"
)

type fakeLoader struct {
	user auth.User
	err  error
}

func (f fakeLoader) UserFromRequest(r *http.Request) (auth.User, error) {
	return f.user, f.err
}

func TestRequireAuthRejectsWhenLoaderErrors(t *testing.T) {
	loader := fakeLoader{err: errors.New("not authenticated")}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	handler := RequireAuth(loader)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/files/upload-url", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("expected next handler not to be called")
	}
}

func TestRequireAuthInjectsUserIntoContext(t *testing.T) {
	want := auth.User{ID: "user-1", Name: "Ada", Email: "a@example.com"}
	loader := fakeLoader{user: want}

	var got auth.User
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := RequireAuth(loader)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/files/upload-url", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !ok {
		t.Fatal("expected user to be present in context")
	}
	if got != want {
		t.Errorf("UserFromContext() = %+v, want %+v", got, want)
	}
}
