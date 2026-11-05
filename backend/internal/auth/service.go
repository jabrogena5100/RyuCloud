// Package auth implements accounts, password login and server-side sessions.
//
// On login or registration the API generates a random 256-bit token, stores
// only its SHA-256 hash, and sends the token in an HttpOnly, SameSite=Lax cookie.
// JavaScript never sees it, and logout or revocation takes effect immediately.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

// User is the public view of an account. Password hashes never leave this package.
type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

var (
	errNotAuthenticated   = httpx.NewError(http.StatusUnauthorized, "not_authenticated", "Authentication required.")
	errInvalidCredentials = httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "Invalid email or password.")
	errEmailTaken         = httpx.NewError(http.StatusConflict, "email_taken", "An account with this email already exists.")
)

// Service handles authentication and exposes middleware for other modules.
type Service struct {
	db         *pgxpool.Pool
	cookieName string
	ttl        time.Duration
	secure     bool
}

// NewService returns an auth service. secureCookie must be true whenever the
// site is served over HTTPS.
func NewService(db *pgxpool.Pool, cookieName string, ttl time.Duration, secureCookie bool) *Service {
	return &Service{db: db, cookieName: cookieName, ttl: ttl, secure: secureCookie}
}

// execer is satisfied by both *pgxpool.Pool and pgx.Tx.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// register creates the account and its first session in one transaction.
func (s *Service) register(ctx context.Context, email, password, displayName string) (User, string, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	var u User
	err = tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, display_name) VALUES ($1, $2, $3)
		 RETURNING id, email, display_name, created_at`,
		email, hash, displayName,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)
	if database.IsUniqueViolation(err, "uq_users_email") {
		return User{}, "", errEmailTaken
	}
	if err != nil {
		return User{}, "", err
	}

	token, err := s.createSession(ctx, tx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

// authenticate checks credentials. It returns ok=false for unknown emails, wrong
// passwords and inactive accounts alike.
func (s *Service) authenticate(ctx context.Context, email, password string) (user User, ok bool, err error) {
	var (
		hash   string
		active bool
	)
	err = s.db.QueryRow(ctx,
		`SELECT id, email, display_name, created_at, password_hash, is_active FROM users WHERE email = $1`,
		email,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.CreatedAt, &hash, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(dummyHash(), password)
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}

	matched, needsRehash := VerifyPassword(hash, password)
	if !matched || !active {
		return User{}, false, nil
	}
	if needsRehash {
		if newHash, err := HashPassword(password); err == nil {
			_, _ = s.db.Exec(ctx, `UPDATE users SET password_hash = $1, updated_at = now() WHERE id = $2`, newHash, user.ID)
		}
	}
	return user, true, nil
}

func (s *Service) createSession(ctx context.Context, db execer, userID string) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(ctx,
		`INSERT INTO auth_sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hashSessionToken(token), time.Now().Add(s.ttl),
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// userForToken returns the active user owning an unexpired session, or nil.
func (s *Service) userForToken(ctx context.Context, token string) (*User, error) {
	var u User
	err := s.db.QueryRow(ctx,
		`SELECT u.id, u.email, u.display_name, u.created_at
		 FROM auth_sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = $1 AND s.expires_at > now() AND u.is_active`,
		hashSessionToken(token),
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// newSessionToken returns 256 bits of randomness, URL-safe encoded.
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashSessionToken returns the value stored in the database. A fast hash is
// fine because tokens are high-entropy; it means a database leak exposes no
// usable sessions.
func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
