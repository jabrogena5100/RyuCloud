#!/usr/bin/env bash
# End-to-end smoke test against a running local stack (through the Next.js proxy).
# Creates a throwaway user named smoke-<timestamp>@example.com in the dev database.
#
# Usage: bash scripts/smoke-test.sh            (defaults to http://localhost:3000)
#        BASE_URL=http://localhost:8000 bash scripts/smoke-test.sh   (backend directly)
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:3000}"
ORIGIN="${ORIGIN:-http://localhost:3000}"
jar="$(mktemp)"
trap 'rm -f "$jar"' EXIT
email="smoke-$(date +%s)@example.com"

step() { printf '\n==> %s\n' "$1"; }

step "Liveness"
curl -fsS "$BASE_URL/api/v1/health"

step "Readiness (database)"
curl -fsS "$BASE_URL/api/v1/health/ready"

step "Register $email"
curl -fsS -c "$jar" -H "Content-Type: application/json" -H "Origin: $ORIGIN" \
  -d "{\"email\":\"$email\",\"password\":\"smoke-test-password\",\"display_name\":\"Smoke Test\"}" \
  "$BASE_URL/api/v1/auth/register"

step "Current user via session cookie"
curl -fsS -b "$jar" "$BASE_URL/api/v1/auth/me"

step "Cross-origin POST is rejected (expect 403)"
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Origin: https://evil.example" -b "$jar" \
  "$BASE_URL/api/v1/auth/logout")"
echo "$code"
[ "$code" = "403" ]

step "Logout (expect 204)"
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Origin: $ORIGIN" -b "$jar" -c "$jar" \
  "$BASE_URL/api/v1/auth/logout")"
echo "$code"
[ "$code" = "204" ]

step "Current user after logout (expect 401)"
code="$(curl -s -o /dev/null -w '%{http_code}' -b "$jar" "$BASE_URL/api/v1/auth/me")"
echo "$code"
[ "$code" = "401" ]

printf '\nSmoke test passed.\n'
