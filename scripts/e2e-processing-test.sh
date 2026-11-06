#!/usr/bin/env bash
# End-to-end test of the whole Phase 3 pipeline against the running local stack:
#
#   upload (presigned PUT) -> MinIO -> queue (Redis) -> Go worker -> FFmpeg -> MinIO
#   -> PostgreSQL status READY -> HLS playlists/segments/thumbnail served by the API
#
# It then repeats the upload with a corrupt file to prove the failure path: the video
# must end FAILED with an explanation, after a single attempt (corrupt media is not retried).
#
# The test video is generated on the fly by FFmpeg inside the worker container from its
# built-in "testsrc2" picture and "sine" tone generators. There is no third-party media,
# so there is nothing to license, and every run is reproducible.
#
# Creates a throwaway user e2e-proc-<timestamp>@example.com and removes its videos (and
# their stored objects) at the end, unless KEEP=1.
#
# Usage (repository root, with `docker compose up -d` running and migrations applied):
#   bash scripts/e2e-processing-test.sh
#   KEEP=1 bash scripts/e2e-processing-test.sh     # keep the data to inspect or play it
#   TIMEOUT=300 bash scripts/e2e-processing-test.sh
#   CRASH_TEST=1 bash scripts/e2e-processing-test.sh   # also SIGKILL the worker mid-job and check recovery
#                                                      # (adds ~3-6 minutes: the job's lease must expire)
#
# Needs: curl, python3 (JSON parsing), docker compose.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:3000}"   # the Next.js origin, like a browser
ORIGIN="${ORIGIN:-http://localhost:3000}"
TIMEOUT="${TIMEOUT:-180}"                         # seconds to wait for each video to finish
cd "$(dirname "$0")/.."

command -v curl >/dev/null || { echo "curl is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required (used to parse JSON)" >&2; exit 1; }
[ -f .env ] || { echo ".env not found; run this from the repository root" >&2; exit 1; }

env_value() { grep -E "^$1=" .env | head -n1 | cut -d= -f2- || true; }   # a missing key is empty, not an error (set -e + pipefail)
S3_BUCKET="$(env_value S3_BUCKET)"; S3_BUCKET="${S3_BUCKET:-ryucloud-media}"
MINIO_PORT="$(env_value MINIO_API_HOST_PORT)"; MINIO_PORT="${MINIO_PORT:-9000}"

work="$(mktemp -d)"
jar="$work/cookies.txt"
trap 'rm -rf "$work"' EXIT
email="e2e-proc-$(date +%s)@example.com"
passed=0

step() { printf '\n==> %s\n' "$1"; }
ok() { passed=$((passed + 1)); printf '    ok: %s\n' "$1"; }
fail() { printf '    FAILED: %s\n' "$1" >&2; exit 1; }
expect_eq() { [ "$1" = "$2" ] && ok "$3" || fail "$3 (got '$1', want '$2')"; }

json() {
  python3 -c '
import json, sys
data = json.load(sys.stdin)
for key in sys.argv[1].split("."):
    data = data[int(key)] if isinstance(data, list) else data.get(key) if isinstance(data, dict) else None
    if data is None:
        break
print("" if data is None else (json.dumps(data) if isinstance(data, (dict, list)) else data))
' "$1"
}

api() { # api <method> <path> [json-body]: prints the body; the status goes to $work/status
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -o "$work/body.json" -w '%{http_code}' -X "$method" -b "$jar" -c "$jar" -H "Origin: $ORIGIN")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  curl "${args[@]}" "$BASE_URL/api/v1$path" >"$work/status"
  cat "$work/body.json"
}
status() { cat "$work/status"; }

db_query() { docker compose exec -T db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atq'; }
mc_run() {
  docker compose run --rm --no-deps -T --entrypoint sh minio-init -c \
    'mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && '"$1"
}

step "Checking the stack"
curl -fsS "$BASE_URL/api/v1/health/ready" >/dev/null || fail "the API is not ready at $BASE_URL (is the stack up and migrated?)"
ok "API is ready"
docker compose ps --status running --services | grep -qx worker || fail "the worker service is not running (docker compose up -d worker)"
ok "worker container is running"
docker compose ps --status running --services | grep -qx redis || fail "the redis service is not running"
ok "redis container is running"

step "Create a throwaway user and channel"
api POST /auth/register "{\"email\":\"$email\",\"password\":\"e2e-test-password\",\"display_name\":\"E2E Proc\"}" >/dev/null
expect_eq "$(status)" 201 "registered $email"
channel_id="$(api POST /channels '{"handle":"e2e-proc-'"$(date +%s)"'","name":"E2E Processing"}' | json id)"
expect_eq "$(status)" 201 "created channel"

step "Generate a synthetic 1280x720 test video with FFmpeg (inside the worker container)"
fixture="$work/fixture.mp4"
docker compose run --rm --no-deps -T --entrypoint ffmpeg worker \
  -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1280x720:rate=24" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" -t 6 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac \
  -movflags frag_keyframe+empty_moov -f mp4 pipe:1 >"$fixture"
size="$(stat -c %s "$fixture")"
[ "$size" -gt 10000 ] || fail "fixture is suspiciously small ($size bytes)"
ok "fixture is $size bytes"

scrub() { # strip anything that looks like a signature, credential or token from text we print
  sed -E 's/(X-Amz-(Signature|Credential|Security-Token)=)[^&" <]*/\1<redacted>/g; s/(Authorization:)[^\r\n]*/\1 <redacted>/Ig'
}

# put_to_storage <file> <presigned-url>: the browser-equivalent direct PUT. On failure it prints a
# sanitized diagnosis to stderr (HTTP status, curl error, S3 error code/message, host, signed-header
# names) and returns 1. It never prints the URL's path or query, credentials or headers.
put_to_storage() {
  local file="$1" url="$2" out="$work/put.body" err="$work/put.err" code rc=0 hostport signed s3code s3msg curlmsg
  : >"$out"; : >"$err"
  code="$(curl -sS -o "$out" -w '%{http_code}' -X PUT -H 'Content-Type: video/mp4' \
    --data-binary "@$file" "$url" 2>"$err")" || rc=$?
  [ "$rc" -eq 0 ] && [ "$code" = 200 ] && return 0

  hostport="$(printf '%s' "$url" | sed -E 's#^(https?://[^/?]+).*#\1#')"
  signed="$(printf '%s' "$url" | sed -nE 's/.*[?&]X-Amz-SignedHeaders=([^&]*).*/\1/p' | sed 's/%3B/;/gI')"
  s3code="$(sed -nE 's:.*<Code>([^<]*)</Code>.*:\1:p' "$out" | head -n1 | cut -c1-80)"
  s3msg="$(sed -nE 's:.*<Message>([^<]*)</Message>.*:\1:p' "$out" | head -n1 | cut -c1-200 | scrub)"
  curlmsg="$(head -c 300 "$err" | tr -d '\r' | scrub)"
  curlmsg="${curlmsg//"$url"/<presigned-url>}"
  {
    printf '    PUT to %s failed\n' "$hostport"
    printf '      HTTP status : %s (curl exit %s)\n' "${code:-none}" "$rc"
    [ -n "$curlmsg" ] && printf '      curl said   : %s\n' "$curlmsg"
    [ -n "$s3code" ] && printf '      S3 error    : %s\n' "$s3code"
    [ -n "$s3msg" ] && printf '      S3 message  : %s\n' "$s3msg"
    [ -n "$signed" ] && printf '      signed hdrs : %s (curl sent Content-Type, Content-Length, Host)\n' "$signed"
    case "$s3code" in
      AccessDenied) printf '      hint        : the app user lacks bucket permissions; re-run: docker compose run --rm minio-init\n' ;;
      SignatureDoesNotMatch) printf '      hint        : signed host/headers differ from what was sent; check S3_PUBLIC_ENDPOINT matches %s\n' "$hostport" ;;
      InvalidAccessKeyId) printf '      hint        : the API'"'"'s S3_ACCESS_KEY_ID is unknown to MinIO; re-run: docker compose run --rm minio-init\n' ;;
    esac
    [ "$rc" -eq 7 ] && printf '      hint        : nothing is listening at %s from this machine\n' "$hostport"
  } >&2
  return 1
}

upload_file() { # <file> <filename> <title> -> prints the video id (runs in a subshell, so it sets no variables)
  local file="$1" name="$2" title="$3" bytes session put_url upload_id
  bytes="$(stat -c %s "$file")"
  session="$(api POST /uploads "{\"channel_id\":\"$channel_id\",\"title\":\"$title\",\"description\":\"e2e-processing-test.sh\",\"visibility\":\"private\",\"filename\":\"$name\",\"content_type\":\"video/mp4\",\"size_bytes\":$bytes}")"
  [ "$(status)" = 201 ] || fail "POST /uploads -> $(status): $session"
  upload_id="$(printf '%s' "$session" | json upload.id)"
  put_url="$(printf '%s' "$session" | json target.url)"
  put_to_storage "$file" "$put_url" || fail "direct PUT to MinIO failed (details above)"
  api POST "/uploads/$upload_id/complete" >/dev/null
  [ "$(status)" = 200 ] || fail "complete -> $(status)"
  printf '%s' "$session" | json video.id
}

wait_for_video() { # <video-id> <ready|failed> -> prints the final video JSON; narrates progress
  local id="$1" want="$2" deadline=$((SECONDS + TIMEOUT)) last="" body state progress
  while [ "$SECONDS" -lt "$deadline" ]; do
    body="$(api GET "/videos/$id")"
    state="$(printf '%s' "$body" | json status)"
    progress="$(printf '%s' "$body" | json processing_progress)"
    if [ "$state/$progress" != "$last" ]; then
      printf '    status=%s progress=%s\n' "$state" "${progress:--}" >&2
      last="$state/$progress"
    fi
    case "$state" in
      "$want") printf '%s' "$body"; return 0 ;;
      ready|failed) printf '%s' "$body"; return 1 ;;
    esac
    sleep 1
  done
  printf '%s' "$body"
  return 2
}

step "1. Upload through the API and straight to MinIO, then report completion"
video_id="$(upload_file "$fixture" e2e.mp4 'E2E processed video')"
ok "uploaded and completed; video $video_id"
body="$(api GET "/videos/$video_id")"
case "$(printf '%s' "$body" | json status)" in queued|processing|ready) ok "video left the upload stage (now '$(printf '%s' "$body" | json status)')" ;; *) fail "unexpected status after upload: $body" ;; esac

step "2. Wait for queue -> worker -> FFmpeg -> MinIO -> READY (up to ${TIMEOUT}s)"
final="$(wait_for_video "$video_id" ready)" || fail "the video did not become ready: $final"
ok "video is READY"
expect_eq "$(printf '%s' "$final" | json playback_url)" "/api/v1/videos/$video_id/hls/master.m3u8" "playback_url points at the API media route"
expect_eq "$(printf '%s' "$final" | json thumbnail_url)" "/api/v1/videos/$video_id/thumbnail" "thumbnail_url is set"
duration="$(printf '%s' "$final" | json duration_seconds)"
[ "$duration" -ge 5 ] && [ "$duration" -le 7 ] && ok "duration is ${duration}s (source: 6s)" || fail "unexpected duration '$duration'"

step "3. PostgreSQL: job and renditions are recorded"
row="$(db_query <<SQL
SELECT j.state || '|' || j.attempts || '|' || j.progress || '|' || v.status || '|' || (v.hls_manifest_key = 'videos/$video_id/hls/master.m3u8') || '|' || (v.thumbnail_object_key IS NOT NULL)
FROM processing_jobs j JOIN videos v ON v.id = j.video_id WHERE v.id = '$video_id';
SQL
)"
expect_eq "$row" "succeeded|1|100|ready|true|true" "job succeeded on the first attempt (state|attempts|progress|video|manifest|thumbnail)"
renditions="$(db_query <<SQL
SELECT string_agg(name || ':' || width || 'x' || height, ',' ORDER BY height) FROM video_renditions WHERE video_id = '$video_id';
SQL
)"
expect_eq "$renditions" "360p:640x360,720p:1280x720" "renditions: 360p and 720p (no upscaling beyond the 720p source)"

step "4. MinIO: HLS output and thumbnail exist in the private bucket; the original is kept"
listing="$(mc_run "mc ls --recursive local/$S3_BUCKET/videos/$video_id/")"
for needle in master.m3u8 "360p/index.m3u8" "720p/index.m3u8" "360p/seg_00000.ts" "720p/seg_00000.ts" thumbnail.jpg; do
  echo "$listing" | grep -q "$needle" || fail "MinIO is missing $needle"
done
ok "master + 2 media playlists + segments + thumbnail are stored"
mc_run "mc ls --recursive local/$S3_BUCKET/uploads/$video_id/" | grep -q source || fail "the original upload was not retained"
ok "original upload is retained"
anon="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$MINIO_PORT/$S3_BUCKET/videos/$video_id/hls/master.m3u8")"
{ [ "$anon" = 403 ] || [ "$anon" = 401 ]; } && ok "anonymous access to storage is denied ($anon)" || fail "storage is not private (anonymous GET -> $anon)"

step "5. HLS playback through the authorized API route (what the browser's player does)"
fetch() { curl -sS -b "$jar" -o "$1" -w '%{http_code} %{content_type}' "$BASE_URL/api/v1/videos/$video_id/$2"; }
out="$(fetch "$work/master.m3u8" hls/master.m3u8)"
expect_eq "$out" "200 application/vnd.apple.mpegurl" "master playlist is served with the HLS content type"
grep -q '^#EXTM3U' "$work/master.m3u8" || fail "master playlist does not start with #EXTM3U"
grep -q 'RESOLUTION=640x360' "$work/master.m3u8" && grep -q 'RESOLUTION=1280x720' "$work/master.m3u8" || fail "master playlist is missing a rendition"
ok "master lists both renditions"
out="$(fetch "$work/720p.m3u8" hls/720p/index.m3u8)"
expect_eq "$out" "200 application/vnd.apple.mpegurl" "media playlist is served"
grep -q '#EXT-X-ENDLIST' "$work/720p.m3u8" || fail "media playlist is not a complete VOD playlist"
segment="$(grep -v '^#' "$work/720p.m3u8" | head -n1)"
out="$(fetch "$work/seg.ts" "hls/720p/$segment")"
expect_eq "$out" "200 video/mp2t" "first segment ($segment) is served"
[ "$(head -c1 "$work/seg.ts" | od -An -tx1 | tr -d ' ')" = 47 ] && ok "segment starts with the MPEG-TS sync byte" || fail "segment is not MPEG-TS"
out="$(fetch "$work/thumb.jpg" thumbnail)"
expect_eq "$out" "200 image/jpeg" "thumbnail is served"
[ "$(head -c2 "$work/thumb.jpg" | od -An -tx1 | tr -d ' ')" = ffd8 ] && ok "thumbnail is a JPEG" || fail "thumbnail is not a JPEG"
anon_code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL/api/v1/videos/$video_id/hls/master.m3u8")"
expect_eq "$anon_code" 404 "a signed-out visitor cannot fetch a private video's media"

step "6. Failure path: a corrupt file must end FAILED, with an explanation, after one attempt"
printf 'this is not a video\n%.0s' $(seq 1 400) >"$work/corrupt.mp4"
bad_id="$(upload_file "$work/corrupt.mp4" corrupt.mp4 'E2E corrupt video')"
ok "uploaded a corrupt file as video $bad_id"
bad="$(wait_for_video "$bad_id" failed)" || fail "the corrupt video did not end as failed: $bad"
ok "video is FAILED"
message="$(printf '%s' "$bad" | json processing_error)"
[ -n "$message" ] && ok "owner-facing explanation: $message" || fail "a failed video must explain why"
expect_eq "$(printf '%s' "$bad" | json playback_url)" "" "a failed video has no playback URL"
row="$(db_query <<SQL
SELECT state || '|' || attempts FROM processing_jobs WHERE video_id = '$bad_id';
SQL
)"
expect_eq "$row" "failed|1" "corrupt media failed permanently on the first attempt (no pointless retries)"
[ -z "$(mc_run "mc ls --recursive local/$S3_BUCKET/videos/$bad_id/" 2>/dev/null)" ] || fail "output was stored for a failed video"
ok "no output was stored for the failed video"

crash_id=""
if [ "${CRASH_TEST:-0}" = 1 ]; then
  step "7. Crash recovery: SIGKILL the worker mid-job; the lease must expire and another attempt must finish it"
  CRASH_SECONDS="${CRASH_SECONDS:-240}"        # length of the long fixture, so the job is still running when killed
  CRASH_TIMEOUT="${CRASH_TIMEOUT:-600}"        # lease (WORKER_LEASE_SECONDS, default 120) + sweep + a full re-run
  lease="$(env_value WORKER_LEASE_SECONDS)"; lease="${lease:-120}"
  long="$work/long.mp4"
  docker compose run --rm --no-deps -T --entrypoint ffmpeg worker \
    -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1280x720:rate=24" \
    -f lavfi -i "sine=frequency=440:sample_rate=48000" -t "$CRASH_SECONDS" \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac \
    -movflags frag_keyframe+empty_moov -f mp4 pipe:1 >"$long"
  ok "generated a ${CRASH_SECONDS}s fixture ($(stat -c %s "$long") bytes)"
  crash_id="$(upload_file "$long" long.mp4 'E2E crash recovery')"
  ok "uploaded; video $crash_id"

  # Wait until a worker is really transcoding (processing with some progress), then kill it hard.
  deadline=$((SECONDS + TIMEOUT)); killed=0
  while [ "$SECONDS" -lt "$deadline" ]; do
    body="$(api GET "/videos/$crash_id")"
    state="$(printf '%s' "$body" | json status)"; progress="$(printf '%s' "$body" | json processing_progress)"
    [ "$state" = ready ] && fail "the job finished before it could be killed; rerun with a larger CRASH_SECONDS (now $CRASH_SECONDS)"
    [ "$state" = failed ] && fail "the video failed before the crash: $body"
    if [ "$state" = processing ] && [ "${progress:-0}" -gt 0 ]; then
      docker compose kill -s KILL worker >/dev/null 2>&1 || fail "could not kill the worker container"
      killed=1; break
    fi
    sleep 1
  done
  [ "$killed" = 1 ] || fail "the video never reached 'processing' with progress within ${TIMEOUT}s"
  ok "worker SIGKILLed at ${progress}% (status=$state)"
  docker compose up -d worker >/dev/null 2>&1 || fail "could not restart the worker"
  ok "worker restarted; waiting for the lease (${lease}s) to expire and the sweeper to re-send the job"

  crash_final="$(TIMEOUT="$CRASH_TIMEOUT" wait_for_video "$crash_id" ready)" \
    || fail "the crashed job was not recovered: $crash_final"
  ok "video is READY after the crash"
  row="$(db_query <<SQL
SELECT j.state || '|' || j.attempts || '|' || v.status FROM processing_jobs j JOIN videos v ON v.id = j.video_id WHERE v.id = '$crash_id';
SQL
)"
  expect_eq "$row" "succeeded|2|ready" "recovered on a second attempt (state|attempts|video)"
  crash_files="$(mc_run "mc ls --recursive local/$S3_BUCKET/videos/$crash_id/")"
  for needle in master.m3u8 "360p/index.m3u8" "720p/index.m3u8" thumbnail.jpg; do
    echo "$crash_files" | grep -q "$needle" || fail "recovered video is missing $needle"
  done
  ok "recovered output is complete in MinIO"
fi

if [ "${KEEP:-0}" = 1 ]; then
  step "KEEP=1: leaving the test data in place"
  echo "    sign in at $BASE_URL as $email (password: e2e-test-password), open /studio and watch '$video_id'"
else
  step "Cleanup: deleting the videos removes their stored objects"
  api DELETE "/videos/$video_id" >/dev/null; expect_eq "$(status)" 204 "deleted the processed video"
  api DELETE "/videos/$bad_id" >/dev/null; expect_eq "$(status)" 204 "deleted the failed video"
  if [ -n "$crash_id" ]; then api DELETE "/videos/$crash_id" >/dev/null; expect_eq "$(status)" 204 "deleted the crash-recovery video"; fi
  [ -z "$(mc_run "mc ls --recursive local/$S3_BUCKET/videos/$video_id/" 2>/dev/null)" ] && ok "processed output removed from MinIO" || fail "processed output remains in MinIO"
  mc_run "mc ls --recursive local/$S3_BUCKET/uploads/$video_id/" 2>/dev/null | grep -q source && fail "the original remains in MinIO" || ok "original removed from MinIO"
fi

printf '\nEnd-to-end processing test passed (%d checks).\n' "$passed"
