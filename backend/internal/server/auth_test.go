package server_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRegisterCreatesUserAndSessionCookie(t *testing.T) {
	b := newTestApp(t).browser()
	resp := b.register("alice@example.com")
	expectStatus(t, resp, http.StatusCreated)

	user := resp.object(t)
	if str(user, "email") != "alice@example.com" || str(user, "display_name") != "alice" {
		t.Fatalf("unexpected user: %v", user)
	}
	for _, field := range []string{"password", "password_hash"} {
		if _, leaked := user[field]; leaked {
			t.Fatalf("response leaked %s", field)
		}
	}

	setCookie := strings.Join(resp.header.Values("Set-Cookie"), "; ")
	for _, want := range []string{cookieName + "=", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(setCookie, want) {
			t.Errorf("Set-Cookie %q is missing %q", setCookie, want)
		}
	}

	me := b.get("/api/v1/auth/me")
	expectStatus(t, me, http.StatusOK)
	if str(me.object(t), "id") != str(user, "id") {
		t.Fatal("/auth/me returned a different user")
	}
}

func TestPasswordIsStoredAsArgon2Hash(t *testing.T) {
	newTestApp(t).browser().register("alice@example.com")
	var hash string
	if err := testPool.QueryRow(context.Background(), `SELECT password_hash FROM users`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, defaultPassword) {
		t.Fatalf("unexpected stored hash: %s", hash)
	}
}

func TestSessionTokenIsStoredHashed(t *testing.T) {
	b := newTestApp(t).browser()
	b.register("alice@example.com")
	token := b.sessionToken()
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT token_hash FROM auth_sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if token == "" || stored == token || len(stored) != 64 {
		t.Fatalf("token %q stored as %q", token, stored)
	}
}

func TestDuplicateEmailIsRejectedCaseInsensitively(t *testing.T) {
	app := newTestApp(t)
	expectStatus(t, app.browser().register("alice@example.com"), http.StatusCreated)
	resp := app.browser().register("ALICE@Example.com")
	expectStatus(t, resp, http.StatusConflict)
	if resp.errorCode(t) != "email_taken" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestRegisterValidation(t *testing.T) {
	b := newTestApp(t).browser()

	short := b.post("/api/v1/auth/register", map[string]any{
		"email": "alice@example.com", "password": "tiny7", "display_name": "Alice",
	})
	expectStatus(t, short, http.StatusUnprocessableEntity)
	if short.errorCode(t) != "validation_error" || !short.hasDetail(t, "password") {
		t.Fatalf("unexpected body: %s", short.body)
	}
	if strings.Contains(string(short.body), "tiny7") {
		t.Fatal("validation error echoed the submitted password")
	}

	badEmail := b.post("/api/v1/auth/register", map[string]any{
		"email": "not-an-email", "password": defaultPassword, "display_name": "Alice",
	})
	expectStatus(t, badEmail, http.StatusUnprocessableEntity)
	if !badEmail.hasDetail(t, "email") {
		t.Fatalf("unexpected body: %s", badEmail.body)
	}

	missing := b.post("/api/v1/auth/register", map[string]any{})
	expectStatus(t, missing, http.StatusUnprocessableEntity)
	for _, field := range []string{"email", "password", "display_name"} {
		if !missing.hasDetail(t, field) {
			t.Errorf("missing detail for %s: %s", field, missing.body)
		}
	}
}

func TestLoginWithValidCredentials(t *testing.T) {
	app := newTestApp(t)
	app.browser().register("alice@example.com")
	b := app.browser()
	expectStatus(t, b.login("Alice@example.com", defaultPassword, nil), http.StatusOK)
	expectStatus(t, b.get("/api/v1/auth/me"), http.StatusOK)
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	app := newTestApp(t)
	app.browser().register("alice@example.com")
	b := app.browser()

	wrongPassword := b.login("alice@example.com", "wrong-password", nil)
	unknownEmail := b.login("nobody@example.com", "wrong-password", nil)
	expectStatus(t, wrongPassword, http.StatusUnauthorized)
	expectStatus(t, unknownEmail, http.StatusUnauthorized)
	for _, key := range []string{"code", "message"} {
		if wrongPassword.errorObject(t)[key] != unknownEmail.errorObject(t)[key] {
			t.Fatalf("responses differ in %s: %s vs %s", key, wrongPassword.body, unknownEmail.body)
		}
	}
	if b.sessionToken() != "" {
		t.Fatal("a failed login must not set a session cookie")
	}
}

func TestMeRequiresAuthentication(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/auth/me")
	expectStatus(t, resp, http.StatusUnauthorized)
	if resp.errorCode(t) != "not_authenticated" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestLogoutRevokesSessionServerSide(t *testing.T) {
	app := newTestApp(t)
	b := app.signedIn("alice@example.com")
	token := b.sessionToken()

	expectStatus(t, b.post("/api/v1/auth/logout", nil), http.StatusNoContent)
	expectStatus(t, b.get("/api/v1/auth/me"), http.StatusUnauthorized)

	// Replaying the old cookie from another client must also fail.
	replay := app.browser().do(http.MethodGet, "/api/v1/auth/me", nil, map[string]string{"Cookie": cookieName + "=" + token})
	expectStatus(t, replay, http.StatusUnauthorized)
}

func TestExpiredSessionIsRejected(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	execSQL(t, `UPDATE auth_sessions SET expires_at = now() - interval '1 minute'`)
	expectStatus(t, b.get("/api/v1/auth/me"), http.StatusUnauthorized)
}

func TestInactiveUserCannotUseSessionOrLogIn(t *testing.T) {
	app := newTestApp(t)
	b := app.signedIn("alice@example.com")
	execSQL(t, `UPDATE users SET is_active = false`)
	expectStatus(t, b.get("/api/v1/auth/me"), http.StatusUnauthorized)
	expectStatus(t, app.browser().login("alice@example.com", defaultPassword, nil), http.StatusUnauthorized)
}

func TestCrossOriginPostIsRejected(t *testing.T) {
	resp := newTestApp(t).browser().login("alice@example.com", defaultPassword, map[string]string{"Origin": "https://evil.example"})
	expectStatus(t, resp, http.StatusForbidden)
	if resp.errorCode(t) != "untrusted_origin" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestTrustedOriginPostIsAllowed(t *testing.T) {
	app := newTestApp(t)
	app.browser().register("alice@example.com")
	resp := app.browser().login("alice@example.com", defaultPassword, map[string]string{"Origin": trustedOrigin})
	expectStatus(t, resp, http.StatusOK)
}
