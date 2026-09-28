#!/bin/sh
# Smoke test that follows docs/api.md against a RUNNING stack. Needs curl, jq and sha256sum.
# Safe to repeat: every run uses unique names and soft-deletes what it creates.
#
#   docker compose --profile tools run --rm smoke                       # local stack, full run
#   API=https://calendar-api.example.org ADMIN_EMAIL=... ADMIN_PASSWORD=... \
#     SMOKE_WRITE=false sh scripts/smoke.sh                             # production: read-only checks
#
# Variables:
#   API            base URL (default http://localhost:8080)
#   KEY            public API key; if empty, the script issues a temporary key and revokes it at the end
#   ADMIN_EMAIL    super admin (default admin@example.com)
#   ADMIN_PASSWORD (default change-me-please-now, the development value)
#   SMOKE_WRITE    true (default) runs the write flows; false only reads (plus the temporary key)
#   CURL_INSECURE  true accepts self-signed TLS (local Caddy with DOMAIN=localhost)
set -eu

API=${API:-http://localhost:8080}
KEY=${KEY:-}
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@example.com}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-change-me-please-now}
SMOKE_WRITE=${SMOKE_WRITE:-true}
CURL_OPTS="-s"
[ "${CURL_INSECURE:-false}" = "true" ] && CURL_OPTS="-s -k"
RUN=$(date +%s)
PASS=0
TEMP_CLIENT=""

ok()   { PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
fail() { printf '  \033[31m✗ %s\033[0m\n' "$1"; exit 1; }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# call METHOD PATH [BODY] [extra curl args...] → sets $STATUS and $BODY
call() {
  method=$1; path=$2; shift 2
  data=""
  if [ $# -gt 0 ] && [ "${1#-}" = "$1" ]; then data=$1; shift; fi
  out=$(mktemp)
  if [ -n "$data" ]; then
    # shellcheck disable=SC2086
    STATUS=$(curl $CURL_OPTS -o "$out" -w '%{http_code}' -X "$method" "$API$path" -H 'Content-Type: application/json' -d "$data" "$@")
  else
    # shellcheck disable=SC2086
    STATUS=$(curl $CURL_OPTS -o "$out" -w '%{http_code}' -X "$method" "$API$path" "$@")
  fi
  BODY=$(cat "$out"); rm -f "$out"
}
expect() { [ "$STATUS" = "$1" ] || fail "$2: HTTP $STATUS, want $1: $BODY"; ok "$2"; }
field()  { printf '%s' "$BODY" | jq -r "$1"; }
login()  { call POST /v1/admin/auth/login "{\"email\":\"$1\",\"password\":\"$2\"}"; expect 200 "login $1" >&2; field .accessToken; }
manifest_field() {
  # shellcheck disable=SC2086
  curl $CURL_OPTS -H "X-Api-Key: $KEY" "$API/v1/manifest" | jq -r "$1"
}

step "0. Waiting for the API ($API)"
i=0
# shellcheck disable=SC2086
until curl $CURL_OPTS -f "$API/readyz" >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -gt 90 ] && fail "API at $API not ready after 90 s"; sleep 1
done
ok "ready"

step "1. Operations"
call GET /healthz;      expect 200 "healthz"
call GET /readyz;       expect 200 "readyz"
call GET /openapi.yaml; expect 200 "openapi.yaml served"
call GET /v1/manifest;  expect 401 "public routes require an API key"

step "2. Admin sign-in (docs/api.md §3.2)"
TOKEN=$(login "$ADMIN_EMAIL" "$ADMIN_PASSWORD")
call GET /v1/admin/me -H "Authorization: Bearer $TOKEN"; expect 200 "me"
if [ -z "$KEY" ]; then
  call POST /v1/admin/api-clients "{\"name\":\"smoke test $RUN\",\"kind\":\"public\"}" -H "Authorization: Bearer $TOKEN"
  expect 201 "issue a temporary public API key"
  KEY=$(field .key); TEMP_CLIENT=$(field .id)
fi

step "3. Client sync (§6.1)"
call GET "/v1/manifest?app=web&clientVersion=1.0.0" -H "X-Api-Key: $KEY"; expect 200 "manifest"
DATA=$(field .links.data); CONF=$(field .links.config); CUR=$(field .currentBsYear)
V=$(field ".eventBuckets[\"$CUR\"] // .defaultBucketVersion")
call GET "$DATA" -H "X-Api-Key: $KEY"; expect 200 "year table $DATA"
SUM=$(printf '%s' "$BODY" | jq -j '.years[] | "\(.y):\(.start):\(.days|map(tostring)|join(",")):\(.status)\n"' | sha256sum | cut -d' ' -f1)
[ "$SUM" = "$(field .sha256)" ] && ok "year table checksum verifies (canonical text form)" || fail "checksum mismatch: $SUM"
call GET "$CONF" -H "X-Api-Key: $KEY"; expect 200 "ui config $CONF"
call GET "/v1/events/buckets/$CUR/$V" -H "X-Api-Key: $KEY"; expect 200 "bucket $CUR v$V"

step "4. Dates (§6.2)"
call GET "/v1/convert?ad=2026-09-24" -H "X-Api-Key: $KEY"; expect 200 "convert AD → BS"
[ "$(field .bs.date)" = "2083-06-08" ] && ok "2026-09-24 = BS 2083-06-08" || fail "conversion: $BODY"
call GET "/v1/months/BS/$CUR/1?include=events" -H "X-Api-Key: $KEY"; expect 200 "month grid"
[ "$(field '.cells|length')" = "42" ] && ok "grid has 42 cells" || fail "grid size"

if [ "$SMOKE_WRITE" = "true" ]; then
  step "5. Publish an event (§6.3)"
  call POST /v1/admin/events "{\"category\":\"event\",\"title\":{\"en\":\"Smoke test $RUN\"},\"basis\":\"BS\",\"start\":\"$CUR-06-20\"}" -H "Authorization: Bearer $TOKEN"
  expect 201 "create draft"
  ID=$(field .id)
  call PATCH "/v1/admin/events/$ID" '{"description":{"en":"patched"}}' -H "Authorization: Bearer $TOKEN" -H 'If-Match: "v9"'
  expect 412 "stale If-Match is rejected"
  call PATCH "/v1/admin/events/$ID" '{"description":{"en":"patched"}}' -H "Authorization: Bearer $TOKEN" -H 'If-Match: "v1"'
  expect 200 "patch with If-Match"
  BEFORE=$(manifest_field ".eventBuckets[\"$CUR\"] // .defaultBucketVersion")
  call POST "/v1/admin/events/$ID/publish" -H "Authorization: Bearer $TOKEN"; expect 200 "publish"
  AFTER=$(manifest_field ".eventBuckets[\"$CUR\"] // .defaultBucketVersion")
  [ "$AFTER" -gt "$BEFORE" ] && ok "bucket version bumped ($BEFORE → $AFTER)" || fail "bucket version did not change"
  call GET "/v1/events/buckets/$CUR/$AFTER" -H "X-Api-Key: $KEY"; expect 200 "new bucket"
  printf '%s' "$BODY" | jq -e --arg id "$ID" '.events | any(.eventId == $id)' >/dev/null && ok "event is in the bucket" || fail "event missing"

  step "6. Year table four-eyes (§6.5)"
  APPROVER_EMAIL="approver-$RUN@example.com"
  call POST /v1/admin/users "{\"email\":\"$APPROVER_EMAIL\",\"role\":\"calendar_admin\",\"password\":\"approver-password-1\"}" -H "Authorization: Bearer $TOKEN"
  expect 201 "create second calendar admin"
  APPROVER=$(login "$APPROVER_EMAIL" approver-password-1)
  call GET /v1/admin/years -H "Authorization: Bearer $TOKEN"; expect 200 "list years"
  Y=$(field "[.items[] | select(.status == \"projected\")][0].bsYear")
  DAYS=$(field "[.items[] | select(.bsYear == $Y)][0].days | tostring")
  # A calendar admin proposes; super admins may approve their own drafts, calendar admins may not.
  call POST /v1/admin/years/drafts "{\"changes\":[{\"bsYear\":$Y,\"days\":$DAYS,\"status\":\"projected\",\"source\":\"smoke test $RUN (no change)\"}]}" -H "Authorization: Bearer $APPROVER"
  expect 201 "propose draft for BS $Y"
  DRAFT=$(field .id)
  call POST "/v1/admin/years/drafts/$DRAFT/approve" -H "Authorization: Bearer $APPROVER"; expect 403 "author cannot approve (four-eyes)"
  call POST "/v1/admin/years/drafts/$DRAFT/reject" '{"reason":"smoke test"}' -H "Authorization: Bearer $TOKEN"; expect 200 "another admin rejects"

  step "7. UI config review and rollout (§6.6)"
  APP="smoke-$RUN"
  for n in 1 2; do
    call POST /v1/admin/users "{\"email\":\"designer$n-$RUN@example.com\",\"role\":\"designer\",\"password\":\"designer-password-1\"}" -H "Authorization: Bearer $TOKEN"
    expect 201 "create designer $n"
  done
  D1=$(login "designer1-$RUN@example.com" designer-password-1)
  D2=$(login "designer2-$RUN@example.com" designer-password-1)
  # shellcheck disable=SC2086
  CFG=$(curl $CURL_OPTS -H "X-Api-Key: $KEY" "$API/v1/ui-config/$APP/0" | jq -c '.theme.light.primary = "#1E40AF"')
  BAD=$(printf '%s' "$CFG" | jq -c '.theme.dark.text = "#0B0F17"')
  call POST /v1/admin/ui-configs/validate "$BAD" -H "Authorization: Bearer $D1"; expect 200 "validate"
  [ "$(field .valid)" = "false" ] && ok "contrast gate rejects unreadable dark text" || fail "contrast gate"
  call POST "/v1/admin/ui-configs/$APP" "{\"config\":$CFG,\"note\":\"smoke\"}" -H "Authorization: Bearer $D1"; expect 201 "draft"
  call POST "/v1/admin/ui-configs/$APP/1/submit" -H "Authorization: Bearer $D1"; expect 200 "submit"
  call POST "/v1/admin/ui-configs/$APP/1/approve" -H "Authorization: Bearer $D1"; expect 403 "author cannot approve"
  call POST "/v1/admin/ui-configs/$APP/1/approve" '{"rolloutPercent":20}' -H "Authorization: Bearer $D2"; expect 200 "approve at 20 %"
  call GET "/v1/manifest?app=$APP" -H "X-Api-Key: $KEY"; expect 200 "manifest"
  [ "$(field .config.candidate.percent)" = "20" ] && ok "manifest advertises the 20 % candidate" || fail "candidate: $BODY"
  call POST "/v1/admin/ui-configs/$APP/rollout" '{"percent":100}' -H "Authorization: Bearer $D2"; expect 200 "promote to 100 %"

  step "8. Clean up writes"
  call DELETE "/v1/admin/events/$ID" -H "Authorization: Bearer $TOKEN" -H "If-Match: \"v3\""; expect 200 "soft-delete the smoke event"
else
  step "5-8. Write flows skipped (SMOKE_WRITE=false)"
fi

step "9. Finish"
if [ -n "$TEMP_CLIENT" ]; then
  call DELETE "/v1/admin/api-clients/$TEMP_CLIENT" -H "Authorization: Bearer $TOKEN"; expect 204 "revoke the temporary API key"
fi
call POST /v1/admin/auth/logout -H "Authorization: Bearer $TOKEN"; expect 204 "logout"
call GET /v1/admin/me -H "Authorization: Bearer $TOKEN"; expect 401 "token rejected after logout"

printf '\n\033[32mAll %d checks passed.\033[0m\n' "$PASS"
