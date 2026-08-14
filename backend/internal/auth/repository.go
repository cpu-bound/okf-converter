package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("user not found")

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// UserRepository is implemented by PgUserRepository and swapped for a fake
// in handler tests so they don't need a live Postgres instance.
type UserRepository interface {
	EmailExists(ctx context.Context, email string) (bool, error)
	Create(ctx context.Context, name, email, passwordHash string) (User, error)
	FindByEmailWithPassword(ctx context.Context, email string) (User, string, error)
	FindByID(ctx context.Context, id string) (User, error)
}

type PgUserRepository struct {
	pool *pgxpool.Pool
}

func NewPgUserRepository(pool *pgxpool.Pool) *PgUserRepository {
	return &PgUserRepository{pool: pool}
}

func (r *PgUserRepository) EmailExists(ctx context.Context, email string) (bool, error) {
	var exists bool

	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`,
		email,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check email exists: %w", err)
	}

	return exists, nil
}

func (r *PgUserRepository) Create(ctx context.Context, name, email, passwordHash string) (User, error) {
	var u User

	err := r.pool.QueryRow(ctx,
		`
		INSERT INTO users (name, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, name, email
		`,
		name, email, passwordHash,
	).Scan(&u.ID, &u.Name, &u.Email)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}

	return u, nil
}

func (r *PgUserRepository) FindByEmailWithPassword(ctx context.Context, email string) (User, string, error) {
	var u User
	var passwordHash string

	err := r.pool.QueryRow(ctx,
		`SELECT id, name, email, password_hash FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Name, &u.Email, &passwordHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, "", ErrNotFound
		}
		return User{}, "", fmt.Errorf("find user by email: %w", err)
	}

	return u, passwordHash, nil
}

func (r *PgUserRepository) FindByID(ctx context.Context, id string) (User, error) {
	var u User

	err := r.pool.QueryRow(ctx,
		`SELECT id, name, email FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Name, &u.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("find user by id: %w", err)
	}

	return u, nil
}
