package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type fakeUserRepository struct {
	byEmail map[string]User
	hashes  map[string]string // email -> password hash
	byID    map[string]User
	nextID  int
}

func newFakeUserRepository() *fakeUserRepository {
	return &fakeUserRepository{
		byEmail: map[string]User{},
		hashes:  map[string]string{},
		byID:    map[string]User{},
	}
}

func (f *fakeUserRepository) EmailExists(ctx context.Context, email string) (bool, error) {
	_, ok := f.byEmail[email]
	return ok, nil
}

func (f *fakeUserRepository) Create(ctx context.Context, name, email, passwordHash string) (User, error) {
	f.nextID++
	user := User{ID: "id-" + strconv.Itoa(f.nextID), Name: name, Email: email}
	f.byEmail[email] = user
	f.hashes[email] = passwordHash
	f.byID[user.ID] = user
	return user, nil
}

func (f *fakeUserRepository) FindByEmailWithPassword(ctx context.Context, email string) (User, string, error) {
	user, ok := f.byEmail[email]
	if !ok {
		return User{}, "", ErrNotFound
	}
	return user, f.hashes[email], nil
}

func (f *fakeUserRepository) FindByID(ctx context.Context, id string) (User, error) {
	user, ok := f.byID[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}

func TestRegisterHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		seedEmail  string
		wantStatus int
	}{
		{"valid registration", `{"name":"Ada Lovelace","email":"Ada@Example.com","password":"password123"}`, "", http.StatusCreated},
		{"missing fields", `{"name":"Ada"}`, "", http.StatusBadRequest},
		{"short name", `{"name":"A","email":"a@example.com","password":"password123"}`, "", http.StatusBadRequest},
		{"short password", `{"name":"Ada","email":"a@example.com","password":"short"}`, "", http.StatusBadRequest},
		{"duplicate email", `{"name":"Ada","email":"a@example.com","password":"password123"}`, "a@example.com", http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeUserRepository()
			if tt.seedEmail != "" {
				repo.byEmail[tt.seedEmail] = User{ID: "existing", Email: tt.seedEmail}
			}

			h := NewHandlers(repo, "test-secret", false)

			req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			h.Register(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantStatus == http.StatusCreated {
				var resp userResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.User.Email != "ada@example.com" {
					t.Errorf("user.Email = %q, want normalized lowercase email", resp.User.Email)
				}

				cookies := rec.Result().Cookies()
				if len(cookies) != 1 || cookies[0].Name != "auth_token" {
					t.Errorf("expected auth_token cookie to be set, got %v", cookies)
				}
			}
		})
	}
}

func TestLoginHandler(t *testing.T) {
	repo := newFakeUserRepository()
	hash, err := HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	repo.byEmail["a@example.com"] = User{ID: "user-1", Name: "Ada", Email: "a@example.com"}
	repo.hashes["a@example.com"] = hash

	h := NewHandlers(repo, "test-secret", false)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid login", `{"email":"a@example.com","password":"password123"}`, http.StatusOK},
		{"wrong password", `{"email":"a@example.com","password":"wrong"}`, http.StatusUnauthorized},
		{"unknown email", `{"email":"nope@example.com","password":"password123"}`, http.StatusUnauthorized},
		{"missing fields", `{"email":"a@example.com"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			h.Login(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestMeHandler(t *testing.T) {
	repo := newFakeUserRepository()
	repo.byID["user-1"] = User{ID: "user-1", Name: "Ada", Email: "a@example.com"}

	h := NewHandlers(repo, "test-secret", false)

	t.Run("no cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		rec := httptest.NewRecorder()

		h.Me(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})

	t.Run("valid cookie", func(t *testing.T) {
		token, err := CreateToken(User{ID: "user-1"}, "test-secret")
		if err != nil {
			t.Fatalf("CreateToken: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		rec := httptest.NewRecorder()

		h.Me(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp userResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.User.ID != "user-1" {
			t.Errorf("user.ID = %q, want %q", resp.User.ID, "user-1")
		}
	})
}

func TestLogoutHandler(t *testing.T) {
	repo := newFakeUserRepository()
	h := NewHandlers(repo, "test-secret", false)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	rec := httptest.NewRecorder()

	h.Logout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Errorf("expected auth_token cookie to be cleared (negative MaxAge), got %v", cookies)
	}
}
