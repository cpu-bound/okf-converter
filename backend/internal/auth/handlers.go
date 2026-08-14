package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"okf-converter/backend/internal/httpx"
)

type Handlers struct {
	repo         UserRepository
	loader       *UserLoader
	jwtSecret    string
	secureCookie bool
}

func NewHandlers(repo UserRepository, jwtSecret string, secureCookie bool) *Handlers {
	return &Handlers{
		repo:         repo,
		loader:       NewUserLoader(repo, jwtSecret),
		jwtSecret:    jwtSecret,
		secureCookie: secureCookie,
	}
}

// Loader exposes the shared UserLoader so it can be wired into
// middleware.RequireAuth for the /api/files routes.
func (h *Handlers) Loader() *UserLoader {
	return h.loader
}

type userResponse struct {
	User User `json:"user"`
}

func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Name == "" || body.Email == "" || body.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "Name, email and password are required.")
		return
	}

	name := strings.TrimSpace(body.Name)
	email := strings.ToLower(strings.TrimSpace(body.Email))

	if len(name) < 2 {
		httpx.Error(w, http.StatusBadRequest, "Name must contain at least 2 characters.")
		return
	}

	if len(body.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "Password must contain at least 8 characters.")
		return
	}

	ctx := r.Context()

	exists, err := h.repo.EmailExists(ctx, email)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	if exists {
		httpx.Error(w, http.StatusConflict, "An account with this email already exists.")
		return
	}

	passwordHash, err := HashPassword(body.Password)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	user, err := h.repo.Create(ctx, name, email, passwordHash)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	token, err := CreateToken(user, h.jwtSecret)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	SetAuthCookie(w, token, h.secureCookie)

	httpx.JSON(w, http.StatusCreated, userResponse{User: user})
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Email == "" || body.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "Email and password are required.")
		return
	}

	email := strings.ToLower(strings.TrimSpace(body.Email))

	user, passwordHash, err := h.repo.FindByEmailWithPassword(r.Context(), email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusUnauthorized, "Invalid email or password.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	valid, err := VerifyPassword(passwordHash, body.Password)
	if err != nil || !valid {
		httpx.Error(w, http.StatusUnauthorized, "Invalid email or password.")
		return
	}

	token, err := CreateToken(user, h.jwtSecret)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	SetAuthCookie(w, token, h.secureCookie)

	httpx.JSON(w, http.StatusOK, userResponse{User: user})
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.loader.UserFromRequest(r)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "Not authenticated.")
		return
	}

	httpx.JSON(w, http.StatusOK, userResponse{User: user})
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	ClearAuthCookie(w, h.secureCookie)

	httpx.JSON(w, http.StatusOK, map[string]string{"message": "Logged out successfully."})
}
