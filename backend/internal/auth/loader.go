package auth

import (
	"errors"
	"net/http"
)

var ErrNotAuthenticated = errors.New("not authenticated")

// UserLoader resolves the authenticated User (if any) from a request's
// auth_token cookie. It's shared by the GET /me handler (which 401s inline,
// same as the previous Express route) and by middleware.RequireAuth.
type UserLoader struct {
	repo   UserRepository
	secret string
}

func NewUserLoader(repo UserRepository, secret string) *UserLoader {
	return &UserLoader{repo: repo, secret: secret}
}

func (l *UserLoader) UserFromRequest(r *http.Request) (User, error) {
	token := tokenFromRequest(r)
	if token == "" {
		return User{}, ErrNotAuthenticated
	}

	userID, err := ParseUserID(token, l.secret)
	if err != nil {
		return User{}, ErrNotAuthenticated
	}

	user, err := l.repo.FindByID(r.Context(), userID)
	if err != nil {
		return User{}, ErrNotAuthenticated
	}

	return user, nil
}
