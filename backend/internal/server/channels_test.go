package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreateChannelRequiresAuthentication(t *testing.T) {
	resp := newTestApp(t).browser().post("/api/v1/channels", map[string]any{"handle": "alice-tv", "name": "Alice TV"})
	expectStatus(t, resp, http.StatusUnauthorized)
}

func TestCreateAndFetchChannel(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	resp := b.post("/api/v1/channels", map[string]any{"handle": "Alice-TV", "name": "Alice TV", "description": "Clouds"})
	expectStatus(t, resp, http.StatusCreated)
	channel := resp.object(t)
	if str(channel, "handle") != "alice-tv" {
		t.Fatalf("handle should be lower-cased, got %v", channel["handle"])
	}

	fetched := b.get("/api/v1/channels/ALICE-tv")
	expectStatus(t, fetched, http.StatusOK)
	if str(fetched.object(t), "id") != str(channel, "id") {
		t.Fatal("fetched a different channel")
	}
}

func TestDuplicateHandleIsRejected(t *testing.T) {
	app := newTestApp(t)
	app.signedIn("alice@example.com").createChannel("alice-tv", "Alice TV")
	resp := app.signedIn("bob@example.com").post("/api/v1/channels", map[string]any{"handle": "alice-tv", "name": "Imposter"})
	expectStatus(t, resp, http.StatusConflict)
	if resp.errorCode(t) != "handle_taken" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestInvalidOrReservedHandlesAreRejected(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	for _, handle := range []string{"ab", "has space", "-leading", strings.Repeat("x", 31), "mine", "admin"} {
		resp := b.post("/api/v1/channels", map[string]any{"handle": handle, "name": "Name"})
		if resp.status != http.StatusUnprocessableEntity || !resp.hasDetail(t, "handle") {
			t.Errorf("handle %q: got %d %s", handle, resp.status, resp.body)
		}
	}
}

func TestListMyChannelsOnlyReturnsOwn(t *testing.T) {
	app := newTestApp(t)
	alice := app.signedIn("alice@example.com")
	alice.createChannel("alice-tv", "Alice TV")
	app.signedIn("bob@example.com").createChannel("bob-tv", "Bob TV")

	resp := alice.get("/api/v1/channels/mine")
	expectStatus(t, resp, http.StatusOK)
	if !strings.Contains(string(resp.body), `"alice-tv"`) || strings.Contains(string(resp.body), `"bob-tv"`) {
		t.Fatalf("unexpected channels: %s", resp.body)
	}
}

func TestUnknownChannelReturns404(t *testing.T) {
	resp := newTestApp(t).browser().get("/api/v1/channels/nobody-here")
	expectStatus(t, resp, http.StatusNotFound)
	if resp.errorCode(t) != "channel_not_found" {
		t.Fatalf("unexpected body: %s", resp.body)
	}
}

func TestOwnerCanUpdateChannel(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	b.createChannel("alice-tv", "Alice TV")
	resp := b.patch("/api/v1/channels/alice-tv", map[string]any{"name": "Alice Cloud TV"})
	expectStatus(t, resp, http.StatusOK)
	updated := resp.object(t)
	if str(updated, "name") != "Alice Cloud TV" || str(updated, "handle") != "alice-tv" {
		t.Fatalf("unexpected channel: %v", updated)
	}
}

func TestNonOwnerCannotUpdateChannel(t *testing.T) {
	app := newTestApp(t)
	app.signedIn("alice@example.com").createChannel("alice-tv", "Alice TV")
	resp := app.signedIn("bob@example.com").patch("/api/v1/channels/alice-tv", map[string]any{"name": "Mine now"})
	expectStatus(t, resp, http.StatusForbidden)
}

func TestChannelPatchRejectsExplicitNull(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	b.createChannel("alice-tv", "Alice TV")
	resp := b.patch("/api/v1/channels/alice-tv", map[string]any{"name": nil})
	expectStatus(t, resp, http.StatusUnprocessableEntity)
}
