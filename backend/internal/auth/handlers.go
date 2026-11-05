package auth

import (
	"net/http"

	"github.com/jabrogena5100/RyuCloud/backend/internal/httpx"
)

// Routes registers the authentication endpoints.
func (s *Service) Routes(mux *http.ServeMux, prefix string) {
	mux.Handle("POST "+prefix+"/auth/register", httpx.Handler(s.handleRegister))
	mux.Handle("POST "+prefix+"/auth/login", httpx.Handler(s.handleLogin))
	mux.Handle("POST "+prefix+"/auth/logout", httpx.Handler(s.handleLogout))
	mux.Handle("GET "+prefix+"/auth/me", s.Required(s.handleMe))
}

// Required wraps a handler that needs a signed-in user; others get 401.
func (s *Service) Required(next func(http.ResponseWriter, *http.Request, User) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, err := s.currentUser(r)
		if err != nil {
			return err
		}
		if user == nil {
			return errNotAuthenticated
		}
		return next(w, r, *user)
	}
}

// Optional wraps a handler whose response depends on who (if anyone) is signed in.
func (s *Service) Optional(next func(http.ResponseWriter, *http.Request, *User) error) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		user, err := s.currentUser(r)
		if err != nil {
			return err
		}
		return next(w, r, user)
	}
}

func (s *Service) currentUser(r *http.Request) (*User, error) {
	c, err := r.Cookie(s.cookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return s.userForToken(r.Context(), c.Value)
}

func (s *Service) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     s.cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) error {
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	email := f.Email("email")
	password, _ := f.String("password", true, 8, 128)
	displayName, _ := f.TrimmedString("display_name", true, 1, 100)
	if err := f.Err(); err != nil {
		return err
	}

	user, token, err := s.register(r.Context(), email, password, displayName)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.cookie(token, int(s.ttl.Seconds())))
	httpx.WriteJSON(w, http.StatusCreated, user)
	return nil
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) error {
	f, err := httpx.DecodeObject(w, r)
	if err != nil {
		return err
	}
	email := f.Email("email")
	password, _ := f.String("password", true, 1, 128)
	if err := f.Err(); err != nil {
		return err
	}

	ctx := r.Context()
	user, ok, err := s.authenticate(ctx, email, password)
	if err != nil {
		return err
	}
	if !ok {
		return errInvalidCredentials
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1 AND expires_at <= now()`, user.ID); err != nil {
		return err
	}
	// Always issue a fresh token on login (no session fixation).
	token, err := s.createSession(ctx, s.db, user.ID)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.cookie(token, int(s.ttl.Seconds())))
	httpx.WriteJSON(w, http.StatusOK, user)
	return nil
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(s.cookieName); err == nil && c.Value != "" {
		if _, err := s.db.Exec(r.Context(), `DELETE FROM auth_sessions WHERE token_hash = $1`, hashSessionToken(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, s.cookie("", -1)) // MaxAge<0 sends Max-Age=0, deleting the cookie
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) handleMe(w http.ResponseWriter, _ *http.Request, user User) error {
	httpx.WriteJSON(w, http.StatusOK, user)
	return nil
}
