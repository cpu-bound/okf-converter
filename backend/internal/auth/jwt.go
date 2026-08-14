package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const tokenTTL = time.Hour

var ErrInvalidToken = errors.New("invalid token")

type claims struct {
	Email string `json:"email"`
	jwt.RegisteredClaims
}

// CreateToken mirrors the previous jsonwebtoken payload: {sub: user.id, email},
// HS256, 1h expiry.
func CreateToken(user User, secret string) (string, error) {
	now := time.Now()

	c := claims{
		Email: user.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)

	return token.SignedString([]byte(secret))
}

// ParseUserID extracts and validates the subject (user id) from a signed token.
func ParseUserID(tokenString, secret string) (string, error) {
	c := &claims{}

	token, err := jwt.ParseWithClaims(tokenString, c, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return "", ErrInvalidToken
	}

	if c.Subject == "" {
		return "", ErrInvalidToken
	}

	return c.Subject, nil
}
