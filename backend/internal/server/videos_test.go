package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// publish simulates what the processing pipeline will do in a later phase.
func publish(t *testing.T, videoID string) {
	t.Helper()
	execSQL(t, `UPDATE videos SET status = 'ready', visibility = 'public', published_at = now() WHERE id = $1`, videoID)
}

func items(t *testing.T, resp response) []any {
	t.Helper()
	list, _ := resp.object(t)["items"].([]any)
	return list
}

// aliceWithChannel returns a signed-in browser and the ID of its channel "alice-tv".
func aliceWithChannel(app *testApp) (*browser, string) {
	b := app.signedIn("alice@example.com")
	return b, str(b.createChannel("alice-tv", "Alice TV"), "id")
}

func TestCreateVideoDefaultsToPrivateDraft(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	video := alice.createVideo(channelID, nil)
	if str(video, "status") != "draft" || str(video, "visibility") != "private" {
		t.Fatalf("unexpected video: %v", video)
	}
	channel, _ := video["channel"].(map[string]any)
	if str(channel, "handle") != "alice-tv" {
		t.Errorf("unexpected channel summary: %v", video["channel"])
	}
	if video["playback_url"] != nil || video["thumbnail_url"] != nil {
		t.Errorf("URLs should be null in Phase 1: %v", video)
	}
}

func TestCreateVideoRequiresAuthentication(t *testing.T) {
	resp := newTestApp(t).browser().post("/api/v1/videos", map[string]any{"channel_id": missingUUID, "title": "x"})
	expectStatus(t, resp, http.StatusUnauthorized)
}

func TestCannotCreateVideoInSomeoneElsesChannel(t *testing.T) {
	app := newTestApp(t)
	_, channelID := aliceWithChannel(app)
	resp := app.signedIn("bob@example.com").post("/api/v1/videos", map[string]any{"channel_id": channelID, "title": "Sneaky"})
	expectStatus(t, resp, http.StatusForbidden)
}

func TestCreateVideoInMissingChannel(t *testing.T) {
	b := newTestApp(t).signedIn("alice@example.com")
	resp := b.post("/api/v1/videos", map[string]any{"channel_id": missingUUID, "title": "x"})
	expectStatus(t, resp, http.StatusNotFound)
}

func TestVideoTitleIsValidated(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	for _, title := range []string{"", strings.Repeat("x", 101)} {
		resp := alice.post("/api/v1/videos", map[string]any{"channel_id": channelID, "title": title})
		if resp.status != http.StatusUnprocessableEntity || !resp.hasDetail(t, "title") {
			t.Errorf("title of length %d: got %d %s", len(title), resp.status, resp.body)
		}
	}
}

func TestFeedOnlyListsPublicReadyVideos(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	alice.createVideo(channelID, map[string]any{"title": "Draft", "visibility": "public"})
	published := alice.createVideo(channelID, map[string]any{"title": "Published"})
	publish(t, str(published, "id"))

	feed := app.browser().get("/api/v1/videos")
	expectStatus(t, feed, http.StatusOK)
	list := items(t, feed)
	if len(list) != 1 || str(list[0].(map[string]any), "id") != str(published, "id") {
		t.Fatalf("unexpected feed: %s", feed.body)
	}
}

func TestFeedPaginationIsValidated(t *testing.T) {
	b := newTestApp(t).browser()
	for _, query := range []string{"limit=0", "limit=51", "offset=-1", "limit=abc"} {
		if resp := b.get("/api/v1/videos?" + query); resp.status != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d", query, resp.status)
		}
	}
}

func TestPrivateVideoHiddenFromOthersButVisibleToOwner(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	path := "/api/v1/videos/" + str(alice.createVideo(channelID, nil), "id")
	expectStatus(t, alice.get(path), http.StatusOK)
	expectStatus(t, app.browser().get(path), http.StatusNotFound)
}

func TestChannelVideosShowDraftsOnlyToOwner(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	alice.createVideo(channelID, nil)
	if n := len(items(t, alice.get("/api/v1/channels/alice-tv/videos"))); n != 1 {
		t.Errorf("owner sees %d videos, want 1", n)
	}
	if n := len(items(t, app.browser().get("/api/v1/channels/alice-tv/videos"))); n != 0 {
		t.Errorf("visitor sees %d videos, want 0", n)
	}
}

func TestOwnerCanUpdateVideo(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	path := "/api/v1/videos/" + str(alice.createVideo(channelID, nil), "id")
	resp := alice.patch(path, map[string]any{"title": "Intro to S3 (updated)", "visibility": "unlisted"})
	expectStatus(t, resp, http.StatusOK)
	updated := resp.object(t)
	if str(updated, "title") != "Intro to S3 (updated)" || str(updated, "visibility") != "unlisted" {
		t.Fatalf("unexpected video: %v", updated)
	}
}

func TestVideoUpdateRejectsNullAndBadEnum(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	path := "/api/v1/videos/" + str(alice.createVideo(channelID, nil), "id")
	expectStatus(t, alice.patch(path, map[string]any{"title": nil}), http.StatusUnprocessableEntity)
	expectStatus(t, alice.patch(path, map[string]any{"visibility": "secret"}), http.StatusUnprocessableEntity)
}

func TestNonOwnerCannotModifyPublicVideo(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	id := str(alice.createVideo(channelID, nil), "id")
	publish(t, id)
	bob := app.signedIn("bob@example.com")
	expectStatus(t, bob.patch("/api/v1/videos/"+id, map[string]any{"title": "Hacked"}), http.StatusForbidden)
	expectStatus(t, bob.del("/api/v1/videos/"+id), http.StatusForbidden)
}

func TestNonOwnerGets404ForPrivateVideoMutations(t *testing.T) {
	app := newTestApp(t)
	alice, channelID := aliceWithChannel(app)
	id := str(alice.createVideo(channelID, nil), "id")
	expectStatus(t, app.signedIn("bob@example.com").del("/api/v1/videos/"+id), http.StatusNotFound)
}

func TestOwnerCanDeleteVideo(t *testing.T) {
	alice, channelID := aliceWithChannel(newTestApp(t))
	path := "/api/v1/videos/" + str(alice.createVideo(channelID, nil), "id")
	expectStatus(t, alice.del(path), http.StatusNoContent)
	expectStatus(t, alice.get(path), http.StatusNotFound)
}

func TestMalformedVideoIDIsAValidationError(t *testing.T) {
	expectStatus(t, newTestApp(t).browser().get("/api/v1/videos/not-a-uuid"), http.StatusUnprocessableEntity)
}
