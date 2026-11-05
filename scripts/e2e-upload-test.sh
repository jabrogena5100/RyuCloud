#!/usr/bin/env bash
# End-to-end upload test against the running local stack (Phase 2).
#
# It does what the browser does, with curl: signs up, creates a channel, asks the API
# for an upload session (through the Next.js proxy), PUTs a file DIRECTLY to MinIO with
# the presigned URL, reports completion, and then proves the result in both places:
#   - PostgreSQL: the upload is "uploaded", the video is "processing", sizes match
#   - MinIO:      the object exists in the private bucket with the right size
# It also checks that tampered uploads, bad types, oversize files and anonymous
# requests are refused.
#
# Creates a throwaway user e2e-<timestamp>@example.com and removes the test video
# (and its stored object) at the end, unless KEEP=1.
#
# Usage (from the repository root, with `docker compose up -d` already running):
#   bash scripts/e2e-upload-test.sh
#   KEEP=1 bash scripts/e2e-upload-test.sh      # leave the video so you can inspect it
#
# Needs: curl, python3 (JSON parsing), docker compose. Reads .env for DB/bucket names.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:3000}"   # the Next.js origin, like a browser
ORIGIN="${ORIGIN:-http://localhost:3000}"
SIZE_BYTES="${SIZE_BYTES:-262144}"
cd "$(dirname "$0")/.."

command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required (used to parse JSON)" >&2; exit 1; }
[ -f .env ] || { echo ".env not found; run this from the repository root after setting up .env" >&2; exit 1; }

# Read only the non-secret settings we need (never print the .env file).
env_value() { grep -E "^$1=" .env | head -n1 | cut -d= -f2-; }
S3_BUCKET="$(env_value S3_BUCKET)"; S3_BUCKET="${S3_BUCKET:-ryucloud-media}"

work="$(mktemp -d)"
jar="$work/cookies.txt"
trap 'rm -rf "$work"' EXIT
email="e2e-$(date +%s)@example.com"
passed=0

step() { printf '\n==> %s\n' "$1"; }
ok() { passed=$((passed + 1)); printf '    ok: %s\n' "$1"; }
fail() { printf '    FAILED: %s\n' "$1" >&2; exit 1; }
expect_eq() { [ "$1" = "$2" ] && ok "$3" || fail "$3 (got '$1', want '$2')"; }

# json <path>: read a dotted path from JSON on stdin.
json() {
  python3 -c '
import json, sys
data = json.load(sys.stdin)
for key in sys.argv[1].split("."):
    data = data[key]
print(json.dumps(data) if isinstance(data, (dict, list)) else data)
' "$1"
}

# api <method> <path> [json-body]: prints the body; the HTTP status goes to $work/status.
api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -o "$work/body.json" -w '%{http_code}' -X "$method" -b "$jar" -c "$jar" -H "Origin: $ORIGIN")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  curl "${args[@]}" "$BASE_URL/api/v1$path" >"$work/status"
  cat "$work/body.json"
}
status() { cat "$work/status"; }

step "Create a throwaway user and channel"
api POST /auth/register "{\"email\":\"$email\",\"password\":\"e2e-test-password\",\"display_name\":\"E2E Test\"}" >/dev/null
expect_eq "$(status)" 201 "registered $email"
channel_id="$(api POST /channels '{"handle":"e2e-'"$(date +%s)"'","name":"E2E Channel"}' | json id)"
expect_eq "$(status)" 201 "created channel $channel_id"

step "Make a ${SIZE_BYTES}-byte sample file (random bytes; Phase 2 does not inspect media content)"
sample="$work/sample.mp4"
head -c "$SIZE_BYTES" /dev/urandom >"$sample"

start_upload() { # <filename> <content_type> <size> -> response body
  api POST /uploads "{\"channel_id\":\"$channel_id\",\"title\":\"E2E upload\",\"description\":\"created by e2e-upload-test.sh\",\"visibility\":\"private\",\"filename\":\"$1\",\"content_type\":\"$2\",\"size_bytes\":$3}"
}

step "Anonymous requests are refused"
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Origin: $ORIGIN" -H 'Content-Type: application/json' -d '{}' "$BASE_URL/api/v1/uploads")"
expect_eq "$code" 401 "anonymous POST /uploads -> 401"

step "Invalid requests are refused before any upload happens"
start_upload e2e.exe video/mp4 "$SIZE_BYTES" >/dev/null; expect_eq "$(status)" 415 "wrong extension for the declared type -> 415"
start_upload page.html text/html "$SIZE_BYTES" >/dev/null; expect_eq "$(status)" 415 "unsupported type -> 415"
start_upload huge.mp4 video/mp4 999999999999 >/dev/null; expect_eq "$(status)" 413 "oversized file -> 413"

step "1. Ask the API for an upload session"
session="$(start_upload e2e.mp4 video/mp4 "$SIZE_BYTES")"
expect_eq "$(status)" 201 "POST /uploads -> 201"
upload_id="$(printf '%s' "$session" | json upload.id)"
video_id="$(printf '%s' "$session" | json video.id)"
put_url="$(printf '%s' "$session" | json target.url)"
echo "    upload $upload_id, video $video_id"
echo "    presigned URL host: $(printf '%s' "$put_url" | sed -E 's#^(https?://[^/]+)/.*#\1#')"
case "$put_url" in http://minio:*) fail "presigned URL points at the Docker-internal hostname; browsers cannot reach it" ;; esac
ok "presigned URL uses a browser-reachable host"

step "Completing before the file is uploaded is refused (and can be retried)"
api POST "/uploads/$upload_id/complete" >/dev/null
expect_eq "$(status)" 409 "complete before upload -> 409 upload_incomplete"

step "2. Upload DIRECTLY to MinIO with the presigned URL (the API never sees these bytes)"
code="$(curl -sS -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: video/mp4' --data-binary "@$sample" "$put_url")"
expect_eq "$code" 200 "PUT to object storage -> 200"

step "A tampered request is rejected by storage itself"
tampered="$(start_upload tamper.mp4 video/mp4 "$SIZE_BYTES")"
tamper_id="$(printf '%s' "$tampered" | json upload.id)"
tamper_url="$(printf '%s' "$tampered" | json target.url)"
code="$(curl -sS -o /dev/null -w '%{http_code}' -X PUT -H 'Content-Type: text/html' --data-binary "@$sample" "$tamper_url")"
[ "$code" -ge 400 ] && ok "PUT with a different Content-Type was rejected ($code)" || fail "tampered PUT was accepted ($code)"
api DELETE "/uploads/$tamper_id" >/dev/null
expect_eq "$(status)" 204 "cancelled the tampered session"

step "3. Report completion; the API verifies the object in storage"
done_body="$(api POST "/uploads/$upload_id/complete")"
expect_eq "$(status)" 200 "POST /uploads/{id}/complete -> 200"
expect_eq "$(printf '%s' "$done_body" | json status)" uploaded "upload status is 'uploaded'"
completed_at="$(printf '%s' "$done_body" | json completed_at)"
api POST "/uploads/$upload_id/complete" >/dev/null
expect_eq "$(status)" 200 "completing again is idempotent -> 200"

step "4. The video is processing-pending, not playable, and hidden from the public"
expect_eq "$(api GET "/videos/$video_id" | json status)" processing "owner sees status 'processing'"
code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/videos/$video_id")"
expect_eq "$code" 404 "anonymous GET /videos/{id} -> 404"
feed_count="$(curl -s "$BASE_URL/api/v1/videos?limit=50" | python3 -c 'import json,sys; print(sum(1 for v in json.load(sys.stdin)["items"] if v["id"]==sys.argv[1]))' "$video_id")"
expect_eq "$feed_count" 0 "video is not in the public feed"

step "PostgreSQL has the verified record"
# SQL is read from stdin, so the database name/user come from the db container's own environment.
db_query() { docker compose exec -T db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atq'; }
row="$(db_query <<SQL
SELECT u.status || '|' || v.status || '|' || u.declared_size_bytes || '|' || u.verified_size_bytes || '|' || (v.source_object_key = u.object_key)
FROM video_uploads u JOIN videos v ON v.id = u.video_id
WHERE u.id = '$upload_id';
SQL
)"
expect_eq "$row" "uploaded|processing|${SIZE_BYTES}|${SIZE_BYTES}|true" "video_uploads/videos row (upload|video|declared|verified|key linked)"
object_key="$(db_query <<SQL
SELECT object_key FROM video_uploads WHERE id = '$upload_id';
SQL
)"

step "MinIO has the object, in a private bucket"
# Uses the minio-init service's mc image and root credentials (set in its environment).
mc_run() {
  docker compose run --rm --no-deps -T --entrypoint sh minio-init -c \
    'mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && '"$1"
}
stat_out="$(mc_run "mc stat local/$S3_BUCKET/$object_key")"
echo "$stat_out" | sed 's/^/    /'
echo "$stat_out" | grep -q "Size" || fail "mc stat did not report the object"
ok "object exists at $object_key"
anon_code="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:${MINIO_API_HOST_PORT:-9000}/$S3_BUCKET/$object_key")"
[ "$anon_code" = 403 ] || [ "$anon_code" = 401 ] && ok "anonymous GET of the object is denied ($anon_code)" || fail "bucket is not private (anonymous GET -> $anon_code)"

if [ "${KEEP:-0}" = 1 ]; then
  step "KEEP=1: leaving the test data in place"
  echo "    sign in at $BASE_URL as $email (password: e2e-test-password) and open /studio"
else
  step "Cleanup: deleting the video also deletes the stored object"
  api DELETE "/videos/$video_id" >/dev/null
  expect_eq "$(status)" 204 "DELETE /videos/{id} -> 204"
  if mc_run "mc stat local/$S3_BUCKET/$object_key" >/dev/null 2>&1; then fail "object still exists in MinIO after the video was deleted"; else ok "object removed from MinIO"; fi
fi

printf '\nEnd-to-end upload test passed (%d checks).\n' "$passed"
