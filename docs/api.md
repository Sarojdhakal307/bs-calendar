# BS/AD Calendar API — Developer Guide

> **Status:** v1, implemented in `services/calendar-api` · **Last updated:** 2026-09-24
> **Contract:** [`api/openapi.yaml`](../api/openapi.yaml) (browse it at `http://localhost:8080/docs`, try it at `/docs/try`)
> **Related:** [architecture.md](architecture.md) · [flow.md](flow.md) · [reliable.md](reliable.md) · [API changelog](../api/CHANGELOG.md)

This guide explains how to **use** the API (every flow, with runnable commands) and how the API is **managed** (how it changes without breaking apps). The OpenAPI file is the source of truth for exact fields; this guide explains how the pieces fit together.

**Contents**

1. [Quick start](#1-quick-start)
2. [How the API is organised](#2-how-the-api-is-organised)
3. [Authentication](#3-authentication)
4. [Conventions](#4-conventions)
5. [Caching and versioning model](#5-caching-and-versioning-model)
6. [Flows, step by step](#6-flows-step-by-step)
7. [Webhooks](#7-webhooks)
8. [Error codes](#8-error-codes)
9. [Limits](#9-limits)
10. [Operations: health, metrics, logs](#10-operations-health-metrics-logs)
11. [Managing the API](#11-managing-the-api)

---

## 1. Quick start

Requirements: Docker Desktop (or Docker Engine with Compose v2). Go and Node are **not** needed; everything builds and tests in containers.

```bash
cp .env.example .env            # optional: every value has a development default
docker compose up -d --build    # postgres → bootstrap (migrate + seed) → api + worker
```

| What | Where |
|------|-------|
| API | `http://localhost:8080` |
| API reference (Redoc) | `http://localhost:8080/docs` |
| Try-it console (Swagger UI) | `http://localhost:8080/docs/try` |
| OpenAPI document | `http://localhost:8080/openapi.yaml` |
| Prometheus metrics | `http://localhost:9090/metrics` (API), worker on `:9091` inside the network |
| Postgres | `localhost:55432`, user/password/db `calendar` |

**Development credentials** (from `.env.example`; change them for any shared environment):

| Kind | Value |
|------|-------|
| Super admin | `admin@example.com` / `change-me-please-now` |
| Public API key | `pk_dev_local_public_key_0001` |
| Server API key | `sk_dev_local_server_key_0001` |

The commands in this guide use bash with `curl` and `jq` (Git Bash or WSL on Windows). Set these once:

```bash
export API=http://localhost:8080
export KEY=pk_dev_local_public_key_0001
```

First calls:

```bash
curl -s $API/healthz
curl -s -H "X-Api-Key: $KEY" "$API/v1/convert?ad=2026-09-24" | jq '.bs.date, .bs.monthName.en, .weekday.name.en'
# "2083-06-08"  "Ashwin"  "Thursday"
curl -s -H "X-Api-Key: $KEY" "$API/v1/manifest?app=web&clientVersion=1.0.0" | jq
```

For an interactive walkthrough in VS Code, open [`api/requests.http`](../api/requests.http) with the REST Client extension.

## 2. How the API is organised

| Area | Base path | Auth | Who calls it |
|------|-----------|------|--------------|
| **Sync** | `/v1/manifest`, `/v1/calendar/data/*`, `/v1/ui-config/*`, `/v1/events/buckets/*` | API key | Apps and websites, on start, foreground and every 15 minutes |
| **Dates** | `/v1/convert`, `/v1/today`, `/v1/months/*` | API key | Thin clients and other backends |
| **Events** | `/v1/events`, `/v1/events.ics`, `/v1/categories` | API key | Backends, search, calendar subscriptions |
| **Telemetry** | `/v1/telemetry` | API key | Apps (anonymous health signals) |
| **Admin** | `/v1/admin/*` | Bearer token | The admin panel |
| **Operations** | `/healthz`, `/readyz`, `/openapi.yaml`, `/docs` | none | Load balancers, developers |

**Design principle:** apps convert dates **locally** with a cached year table and never wait on the network. The server owns the data; the manifest tells clients what changed.

## 3. Authentication

### 3.1 API keys (public routes)

Send the key in a header:

```http
X-Api-Key: pk_dev_local_public_key_0001
```

| Key type | Prefix | Where it lives | Default rate |
|----------|--------|----------------|--------------|
| Public | `pk_` | Inside apps and websites. **Not a secret**: it identifies the app, enforces its origin allow-list and its rate limit. | 600/min |
| Server | `sk_` | Backends and Next.js server components. Never ship it to browsers. | 6000/min |

- Keys are created in the admin API (`POST /v1/admin/api-clients`) and **shown once**. The server stores only a SHA-256 hash.
- A key can carry `allowedOrigins`. A browser request with any other `Origin` gets `403 ORIGIN_NOT_ALLOWED`.
- Revocation (`DELETE /v1/admin/api-clients/{id}`) is immediate on the replica that handled it and takes up to one minute on others (key lookups are cached).
- **ICS exception:** calendar apps cannot send headers, so `/v1/events.ics` also accepts `?api_key=`. Use a dedicated public key for feeds.
- Public routes only ever return published, public data. That is why CDN caching is safe.

### 3.2 Admin sign-in (admin routes)

```bash
LOGIN=$(curl -s -X POST $API/v1/admin/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"change-me-please-now"}')
TOKEN=$(echo "$LOGIN" | jq -r .accessToken)
REFRESH=$(echo "$LOGIN" | jq -r .refreshToken)
curl -s $API/v1/admin/me -H "Authorization: Bearer $TOKEN" | jq
```

| Token | Lifetime | Use |
|-------|----------|-----|
| Access token (JWT, HS256) | 15 minutes (`JWT_ACCESS_TTL`) | `Authorization: Bearer <token>` on every admin call |
| Refresh token (`rt_…`) | 30 days (`JWT_REFRESH_TTL`) | `POST /v1/admin/auth/refresh` to get a new pair |

**Security properties**

- **Rotation:** every refresh returns a *new* refresh token; the old one stops working.
- **Reuse detection:** presenting an already-used refresh token revokes the whole session (`401 TOKEN_REUSED`). This catches a stolen token being replayed.
- **Live session check:** every admin request checks the session in the database. Logout, a disabled user, a password reset and role changes take effect **immediately**, not when the access token expires.
- **Brute force:** login is limited to 10 attempts per minute per IP. Wrong email and wrong password return the same `INVALID_CREDENTIALS` response in the same time.
- **Where to keep tokens:** the admin panel should keep the refresh token server-side (for example an httpOnly, SameSite=strict cookie set by the Next.js backend), never in `localStorage`.

```bash
# Rotate
NEW=$(curl -s -X POST $API/v1/admin/auth/refresh -H 'Content-Type: application/json' -d "{\"refreshToken\":\"$REFRESH\"}")
TOKEN=$(echo "$NEW" | jq -r .accessToken); REFRESH=$(echo "$NEW" | jq -r .refreshToken)
# Sign out
curl -s -X POST $API/v1/admin/auth/logout -H "Authorization: Bearer $TOKEN" -o /dev/null -w '%{http_code}\n'   # 204
```

### 3.3 Roles and permissions

| Permission | viewer | editor | designer | calendar_admin | super_admin |
|------------|:-:|:-:|:-:|:-:|:-:|
| `read` (all admin GETs, validate config) | ✓ | ✓ | ✓ | ✓ | ✓ |
| `events:write` | | ✓ | | ✓ | ✓ |
| `categories:write` | | | | ✓ | ✓ |
| `years:propose` | | | | ✓ | ✓ |
| `years:approve` (never your own draft) | | | | ✓ | ✓ |
| `config:draft` | | | ✓ | | ✓ |
| `config:publish` (never your own version) | | | ✓ | | ✓ |
| `platform:manage` (keys, webhooks, users) | | | | | ✓ |

`GET /v1/admin/me` returns the caller's effective permissions so the admin panel can hide buttons.

## 4. Conventions

| Topic | Rule |
|-------|------|
| **Dates** | `YYYY-MM-DD` strings, always labelled with their calendar (`ad`, `bs`, `basis`). BS dates can have day 32, so they are never typed as RFC 3339 dates. Devanagari digits are accepted on input. |
| **Date model** | Internally every date is an *epoch day* (days since 1970-01-01). Responses include `epochDay` where useful. |
| **JSON** | `camelCase`. Request bodies are strict: unknown fields are rejected with `422` naming the field. Clients must **ignore unknown response fields** (new fields are added over time). |
| **Errors** | `application/problem+json` (RFC 9457) with a stable `code` ([§8](#8-error-codes)). Validation errors list each bad field in `errors[]`. |
| **Request IDs** | Every response has `X-Request-Id`. Send your own (8-64 chars of `[A-Za-z0-9._-]`) to correlate logs across systems. |
| **Optimistic locking** | Admin resources have a `version` and an `ETag: "v3"`. `PATCH` and `DELETE` on events **require** `If-Match: "v3"` (`428` if missing, `412 VERSION_CONFLICT` with `currentVersion` if stale). |
| **Partial updates** | `PATCH` bodies are JSON Merge Patch (RFC 7396), sent as `application/merge-patch+json` (plain `application/json` is also accepted). Send only what changes; `null` clears an optional field; nested objects merge. |
| **Pagination** | Cursor-based: pass `nextCursor` back as `?cursor=`. `limit` is 1-200 (default 50). |
| **Soft delete** | Events are soft-deleted and can be restored; nothing an admin does is lost. Every write is in the audit log. |
| **Time zones** | Events store `tz` (default `Asia/Kathmandu`). "Today" in Nepal is computed as UTC + 5:45 (Nepal has no DST). |

## 5. Caching and versioning model

The API is built so that **almost every request is a CDN cache hit** and clients never download anything twice.

```
GET /v1/manifest                     ← small, changes when anything changes, cached 30 s
 ├─ dataVersion ─────────────► GET /v1/calendar/data/{dataVersion}           immutable
 ├─ config.version ──────────► GET /v1/ui-config/{app}/{version}             immutable
 └─ eventBuckets / default ──► GET /v1/events/buckets/{bsYear}/{version}     immutable
```

| Response | Cache-Control | ETag |
|----------|---------------|------|
| Manifest | `public, max-age=30, s-maxage=30, stale-while-revalidate=300` | Weak; ignores `serverTime`, so unchanged manifests return `304` |
| Year table, published config, bucket | `public, max-age=31536000, immutable` | Strong, from the version |
| Convert / today / month grid | 1 hour / 30 s / 5 minutes | Weak |
| Public events range, categories | 60 s | Weak |
| ICS feed | 1 hour | Weak |
| Admin responses, errors | `private, no-cache` / `no-store` | Events: `"v<version>"` |

**Bucket versions.** A bucket holds every published occurrence touching one BS year, with recurrences expanded. Its version is:

```
version(year) = manifest.eventBuckets[year] ?? manifest.defaultBucketVersion
```

Any change that affects a year (publishing an event, a category colour, a recurring event, a year-table correction) increases that number, so the URL changes and the CDN never serves stale data. Requesting an **old** version answers `302` to the current one (not cached); a **future** version answers `404 VERSION_NOT_FOUND`.

**Verifying the year table** (invariant I8 in reliable.md). The `sha256` field is the hex SHA-256 of one line per year, `<y>:<start>:<d1>,…,<d12>:<status>\n`. It does not depend on JSON formatting:

```ts
const text = data.years.map(y => [y.y, y.start, y.days.join(','), y.status].join(':') + '\n').join('');
const ok = (await sha256Hex(text)) === data.sha256;   // then check invariants before swapping it in
```

**Staged config rollouts.** When `manifest.config.candidate` is present, a client applies the candidate version only if `fnv1a32(installId) % 100 < candidate.percent`, otherwise `config.version`. FNV-1a 32-bit over the UTF-8 bytes of a random, persisted install id:

```ts
function fnv1a32(s: string): number {
  let h = 0x811c9dc5;
  for (const b of new TextEncoder().encode(s)) { h ^= b; h = Math.imul(h, 0x01000193) >>> 0; }
  return h;
}
```

## 6. Flows, step by step

Each flow shows the calls in order. Sequence diagrams for the same flows are in [flow.md](flow.md).

### 6.1 Client sync (apps and websites)

This is the only flow an app needs. It runs on start, when the app returns to the foreground, and every 15 minutes, throttled to once per 60 seconds, with exponential backoff on errors.

```bash
# 1. Manifest (send the previous ETag to get 304 when nothing changed)
curl -s -D - -H "X-Api-Key: $KEY" "$API/v1/manifest?app=mobile&clientVersion=1.4.0"
# 2. Only if dataVersion changed
curl -s -H "X-Api-Key: $KEY" "$API/v1/calendar/data/1" | jq '.version, .minYear, .maxYear, .sha256'
# 3. Only if config.version changed (0 = built-in default)
curl -s -H "X-Api-Key: $KEY" "$API/v1/ui-config/mobile/0" | jq .theme.light
# 4. Only buckets the app needs (current BS year ±1 and visible months) whose version changed
curl -s -H "X-Api-Key: $KEY" "$API/v1/events/buckets/2083/1" | jq '.events | length'
```

Reference client logic:

```ts
async function sync(s: Store, api: Api, app: string, clientVersion: string) {
  const res = await api.get(`/v1/manifest?app=${app}&clientVersion=${clientVersion}`, { ifNoneMatch: s.manifestETag });
  if (res.status === 304) return;
  const m = res.json; s.manifestETag = res.etag;
  if (m.updateRequired) s.flagUpdateRequired();                 // keep working with the cache

  if (m.dataVersion !== s.dataVersion) {
    const data = await api.get(m.links.data).then(r => r.json);
    if (await verifyChecksum(data) && checkInvariants(data)) s.saveData(data);   // atomic write-then-swap
    else api.telemetry('table_rejected', { detail: `v${data.version}` });        // keep the old table
  }
  const cfgVersion = m.config.candidate && fnv1a32(s.installId) % 100 < m.config.candidate.percent
    ? m.config.candidate.version : m.config.version;
  if (cfgVersion !== s.configVersion) s.saveConfig(await api.get(`/v1/ui-config/${app}/${cfgVersion}`).then(r => r.json));

  for (const year of s.neededBsYears(m.currentBsYear)) {        // e.g. current ±1 plus visible months
    const v = m.eventBuckets[year] ?? m.defaultBucketVersion;
    if (s.bucketVersion(year) !== v) s.saveBucket(year, await api.get(`/v1/events/buckets/${year}/${v}`).then(r => r.json));
  }
}
```

The app renders from its cache (or the bundled snapshot) **before** this runs. A failed sync changes nothing on screen.

### 6.2 Dates for thin clients and other backends

```bash
curl -s -H "X-Api-Key: $KEY" "$API/v1/convert?bs=2083-01-01" | jq .ad.date                 # "2026-04-14"
curl -s -H "X-Api-Key: $KEY" "$API/v1/today?tz=Asia/Kathmandu" | jq '.bs.date, .ad.date'
curl -s -H "X-Api-Key: $KEY" "$API/v1/months/BS/2083/6?weekStart=0&include=events" | jq '.cells[0], (.cells|length)'
curl -s -H "X-Api-Key: $KEY" "$API/v1/events?from=2083-06-01&to=2083-06-31&basis=BS" | jq '.count'
```

Month grids always have 42 cells (6 weeks), so layouts never jump. `bs` is `null` only outside the supported range (BS 1975–2100).

### 6.3 Publishing an event

```bash
# Create (always a draft; dates are validated against the year table)
EV=$(curl -s -X POST $API/v1/admin/events -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "category": "public_holiday",
  "title": { "en": "Office foundation day", "ne": "स्थापना दिवस" },
  "basis": "BS", "start": "2083-06-20"
}')
ID=$(echo "$EV" | jq -r .id); echo "$EV" | jq '.status, .ad, .version'     # "draft" {start: 2026-10-06, …} 1

# Edit with optimistic locking (JSON Merge Patch)
curl -s -X PATCH $API/v1/admin/events/$ID -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/merge-patch+json' -H 'If-Match: "v1"' \
  -d '{"description": {"en": "Offices closed"}}' | jq .version                      # 2

# Publish → bumps the 2083 bucket version and queues webhooks
curl -s -X POST $API/v1/admin/events/$ID/publish -H "Authorization: Bearer $TOKEN" -H 'If-Match: "v2"' | jq .status
curl -s -H "X-Api-Key: $KEY" $API/v1/manifest | jq .eventBuckets                   # {"2083": 2}
```

Other event types:

```jsonc
// Every 1 Baisakh (BS-fixed yearly). Day 32 clamps to the month's last day in shorter years.
{ "category": "observance", "title": {"en": "Nepali New Year"}, "basis": "BS", "start": "2083-01-01", "recurrence": "yearly_bs" }
// Every 1 January (AD-fixed yearly). 29 Feb falls on 28 Feb in other years.
{ "category": "observance", "title": {"en": "New Year's Day"}, "basis": "AD", "start": "2027-01-01", "recurrence": "yearly_ad" }
// Timed, second Friday of each month until the end of March (RFC 5545 RRULE, AD only)
{ "category": "event", "title": {"en": "Team review"}, "basis": "AD", "start": "2026-10-09", "allDay": false,
  "startTime": "10:00", "endTime": "11:00", "recurrence": "rrule", "rrule": "FREQ=MONTHLY;BYDAY=2FR", "recurUntil": "2027-03-31" }
// Multi-day (inclusive end, at most 366 days)
{ "category": "festival", "title": {"en": "Winter break"}, "basis": "BS", "start": "2083-09-10", "end": "2083-09-15" }
```

Lifecycle: `draft → publish → archive`, plus soft `DELETE` (needs `If-Match`) and `POST …/restore` (back to draft). Publish and archive are idempotent.

### 6.4 Bulk import and copy to next year

```bash
cat > events.csv <<'CSV'
category,title_en,title_ne,basis,start,end,is_holiday
festival,Example festival day 1,,BS,2083-07-01,,true
event,Annual meeting,,AD,2026-12-25,2026-12-26,
CSV
# Dry run (default): validates every row, creates nothing
curl -s -X POST "$API/v1/admin/events/import" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: text/csv' --data-binary @events.csv | jq '.valid, .invalid, .rows'
# Import: all rows as drafts in one transaction; nothing is created if any row is invalid
curl -s -X POST "$API/v1/admin/events/import?dryRun=false&skipDuplicates=true" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: text/csv' --data-binary @events.csv | jq .created

# Copy published one-off BS events of 2083 to 2084 as drafts (lunar festivals move: review each one!)
curl -s -X POST $API/v1/admin/events/copy-year -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"fromBsYear": 2083, "toBsYear": 2084, "categories": ["public_holiday", "festival"]}' | jq
```

### 6.5 Correcting the year table (four-eyes)

Changing month lengths changes what every date means, so it needs **two different calendar admins**, a written source, and an impact review.

```bash
# (super admin) create a second calendar admin
curl -s -X POST $API/v1/admin/users -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"email":"approver@example.com","role":"calendar_admin","password":"approver-password-1"}' | jq .id
APPROVER=$(curl -s -X POST $API/v1/admin/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"approver@example.com","password":"approver-password-1"}' | jq -r .accessToken)

# 1. Propose: mark 2084 as verified with the published month lengths
DAYS=$(curl -s $API/v1/admin/years -H "Authorization: Bearer $TOKEN" | jq -c '.items[] | select(.bsYear==2084) | .days')
DRAFT=$(curl -s -X POST $API/v1/admin/years/drafts -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"changes\":[{\"bsYear\":2084,\"days\":$DAYS,\"status\":\"verified\",\"source\":\"Official calendar 2084, page 3\"}]}")
echo "$DRAFT" | jq '.state, .warnings, .impact.movedEvents, .impact.invalidEvents'
DRAFT_ID=$(echo "$DRAFT" | jq -r .id)

# 2. The author cannot approve: 403 FOUR_EYES_REQUIRED
curl -s -X POST $API/v1/admin/years/drafts/$DRAFT_ID/approve -H "Authorization: Bearer $TOKEN" | jq .code
# 3. A second calendar admin approves: new data version, every replica reloads, webhooks go out
curl -s -X POST $API/v1/admin/years/drafts/$DRAFT_ID/approve -H "Authorization: Bearer $APPROVER" | jq .state
curl -s -H "X-Api-Key: $KEY" $API/v1/manifest | jq '.dataVersion, .firstProjectedBsYear'   # 2, 2085
```

Rules the server enforces on every draft:

- Each month has 29–32 days, years are contiguous, and 1 Baisakh falls between 10 and 18 April.
- Each year ends the day before the next year starts. If you change a year's total length, the next year's start moves too, so change both years in one draft.
- `source` is required: say where the numbers come from.
- Approval is **refused** (`409` with `invalidEvents`) if any event would fall on a date that no longer exists.
- `POST …/reject` with a `reason` closes a draft; authors may withdraw their own.

### 6.6 Changing the UI (review, staged rollout, rollback)

```bash
# Two designers are needed: one drafts, the other approves.
for u in designer1 designer2; do curl -s -X POST $API/v1/admin/users -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$u@example.com\",\"role\":\"designer\",\"password\":\"designer-password-1\"}" > /dev/null; done
D1=$(curl -s -X POST $API/v1/admin/auth/login -H 'Content-Type: application/json' -d '{"email":"designer1@example.com","password":"designer-password-1"}' | jq -r .accessToken)
D2=$(curl -s -X POST $API/v1/admin/auth/login -H 'Content-Type: application/json' -d '{"email":"designer2@example.com","password":"designer-password-1"}' | jq -r .accessToken)

# Start from the current default and change the light primary colour
CFG=$(curl -s -H "X-Api-Key: $KEY" $API/v1/ui-config/mobile/0 | jq -c '.theme.light.primary = "#1E40AF"')

# Validate without saving (JSON Schema + WCAG AA contrast in light AND dark)
curl -s -X POST $API/v1/admin/ui-configs/validate -H "Authorization: Bearer $D1" -H 'Content-Type: application/json' -d "$CFG" | jq

# Draft → submit → approve at 20 % → promote to 100 %
curl -s -X POST $API/v1/admin/ui-configs/mobile -H "Authorization: Bearer $D1" -H 'Content-Type: application/json' \
  -d "{\"config\": $CFG, \"minClientVersion\": \"1.0.0\", \"note\": \"Darker primary\"}" | jq '.version, .validation.valid'
curl -s -X POST $API/v1/admin/ui-configs/mobile/1/submit  -H "Authorization: Bearer $D1" | jq .status        # in_review
curl -s -X POST $API/v1/admin/ui-configs/mobile/1/approve -H "Authorization: Bearer $D2" \
  -H 'Content-Type: application/json' -d '{"rolloutPercent": 20}' | jq .status                             # published
curl -s -H "X-Api-Key: $KEY" "$API/v1/manifest?app=mobile" | jq .config                                   # candidate v1 at 20 %
curl -s -X POST $API/v1/admin/ui-configs/mobile/rollout -H "Authorization: Bearer $D2" \
  -H 'Content-Type: application/json' -d '{"percent": 100}' | jq                                          # stableVersion 1

# Later, if a newer version (say v2) turns out broken: emergency rollback (no review, audited).
# It republishes v1's content as a NEW version for everyone and marks v2 as rolled_back.
curl -s -X POST $API/v1/admin/ui-configs/mobile/rollback -H "Authorization: Bearer $D1" \
  -H 'Content-Type: application/json' -d '{"toVersion": 1, "reason": "dark mode unreadable"}' | jq '.version, .origin'
```

What admins can change is exactly what `fixtures/ui-config.schema.json` allows: palettes for light and dark, radius, density, font (from a list), default mode, locale, digits, week start, weekend days, and display toggles. Unknown fields are rejected. The version replaced by a rollback is marked `rolled_back` and is never offered again, not even to older apps. Apps older than a version's `minClientVersion` keep the newest compatible version.

### 6.7 API keys and webhooks

```bash
# Issue a public key for a website (shown once)
curl -s -X POST $API/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Marketing site","kind":"public","allowedOrigins":["https://www.example.com"]}' | jq '.key, .id'

# Register a webhook (start the dev receiver first: docker compose --profile tools up -d webhook-sink)
curl -s -X POST $API/v1/admin/webhooks -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"http://webhook-sink:8080/hook","topics":["events","config","data"]}' | jq '.id, .secret'
docker compose logs -f webhook-sink        # publish an event and watch the delivery arrive
curl -s "$API/v1/admin/webhooks/<id>/deliveries" -H "Authorization: Bearer $TOKEN" | jq '.items[0]'
```

## 7. Webhooks

Webhooks tell your systems that something changed. The typical use is revalidating a Next.js site within seconds of a publish. They are **notifications**, not data: fetch the manifest after receiving one.

| Topic | Event type | Fired when |
|-------|-----------|-----------|
| `events` | `events.changed` | A published event changes, is published, archived or deleted. `data.bsYears` lists affected years; `data.recurring` is true for recurring events. |
| `categories` | `categories.changed` | A category is created, changed or deleted. |
| `config` | `config.changed` | A UI config is published, promoted or rolled back. `data.app` names the app. |
| `data` | `data.published` | A year-table change is approved (sent to every tenant). `data.dataVersion` is the new version. |

**Request:** `POST <your url>`, JSON body (see `WebhookEvent` in the spec), headers:

| Header | Meaning |
|--------|---------|
| `X-Calendar-Event` | Event type |
| `X-Calendar-Delivery` | Delivery id. Retries reuse it, so de-duplicate on it. |
| `X-Calendar-Signature` | `t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<raw body>")>` |

**Delivery guarantees.** At least once. Rows are written in the same transaction as the change (transactional outbox), so a committed change is never "forgotten". Failed deliveries retry with backoff (30 s, 1 min, 2 min … capped at 6 h) for 15 attempts (about a day), then dead-letter. A slow or failing receiver never blocks admins. Receivers must answer 2xx within 10 seconds. In production, webhook URLs must be `https` and must not resolve to private addresses (SSRF protection); `ALLOW_PRIVATE_WEBHOOKS=true` relaxes this for development only.

**Verifying signatures (Node / Next.js route handler):**

```ts
// app/api/calendar-revalidate/route.ts
import crypto from 'node:crypto';
import { revalidateTag } from 'next/cache';

function verify(secret: string, header: string | null, rawBody: string, toleranceSec = 300): boolean {
  if (!header) return false;
  const parts = Object.fromEntries(header.split(',').map(p => p.trim().split('=') as [string, string]));
  const t = Number(parts.t);
  if (!t || Math.abs(Date.now() / 1000 - t) > toleranceSec) return false;          // replay protection
  const expected = crypto.createHmac('sha256', secret).update(`${t}.${rawBody}`).digest('hex');
  const given = parts.v1 ?? '';
  return given.length === expected.length && crypto.timingSafeEqual(Buffer.from(given), Buffer.from(expected));
}

export async function POST(req: Request) {
  const raw = await req.text();                                                     // verify the RAW body
  if (!verify(process.env.CAL_WEBHOOK_SECRET!, req.headers.get('x-calendar-signature'), raw)) {
    return new Response('bad signature', { status: 400 });
  }
  const evt = JSON.parse(raw);
  revalidateTag(`cal:${evt.topic}`);                                                // check your Next.js version's signature
  return Response.json({ ok: true });
}
```

**Go:**

```go
func verify(secret, header string, body []byte, tolerance time.Duration) bool {
	var ts int64; var sig string
	for _, p := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		if k == "t" { ts, _ = strconv.ParseInt(v, 10, 64) } else if k == "v1" { sig = v }
	}
	if ts == 0 || time.Since(time.Unix(ts, 0)).Abs() > tolerance { return false }
	m := hmac.New(sha256.New, []byte(secret)); fmt.Fprintf(m, "%d.", ts); m.Write(body)
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(m.Sum(nil))))
}
```

## 8. Error codes

Every error is a problem document:

```json
{
  "type": "urn:bs-calendar:problem:VALIDATION_FAILED",
  "title": "Validation failed",
  "status": 422,
  "code": "VALIDATION_FAILED",
  "detail": "One or more fields are invalid.",
  "instance": "/v1/admin/events",
  "requestId": "req_5c1e0f3a9b2d4e6f",
  "errors": [{ "field": "start", "message": "invalid BS date: BS 2083-03-33 does not exist" }]
}
```

| Code | HTTP | Meaning | What the caller should do |
|------|------|---------|---------------------------|
| `BAD_REQUEST` | 400 | Malformed JSON, header or cursor | Fix the request |
| `VALIDATION_FAILED` | 422 | Fields are invalid; see `errors[]` (and `report` for imports and UI configs, `issues` for year tables) | Show the field messages |
| `INVALID_DATE` | 422 | The date does not exist (BS month 13, Asar 33, AD 29 Feb in a common year) | Ask for a valid date |
| `OUT_OF_RANGE` | 422 | Outside the supported range (BS 1975–2100, AD 1918-04-13 to 2044-04-12) | Clamp pickers to `supportedRange` |
| `NOT_FOUND` | 404 | Resource or route does not exist | — |
| `VERSION_NOT_FOUND` | 404 | An immutable version that does not exist (yet) | Re-read the manifest |
| `UNAUTHORIZED` | 401 | Missing, invalid or expired token, or revoked session | Refresh, or sign in again |
| `INVALID_CREDENTIALS` | 401 | Wrong email or password | — |
| `INVALID_API_KEY` | 401 | Missing, unknown or revoked API key | Check configuration |
| `TOKEN_REUSED` | 401 | A refresh token was used twice; the session is revoked | Sign in again; investigate |
| `FORBIDDEN` | 403 | Your role lacks the permission | — |
| `ORIGIN_NOT_ALLOWED` | 403 | The API key is not allowed from this browser origin | Add the origin to the key |
| `FOUR_EYES_REQUIRED` | 403 | You cannot approve your own change | Ask a colleague |
| `CONFLICT` | 409 | Uniqueness or reference conflict (for example deleting a used category); may include `invalidEvents` | Resolve and retry |
| `INVALID_STATE` | 409 | Not allowed in the current state (for example approving a rejected draft) | Reload |
| `VERSION_CONFLICT` | 412 | Your `If-Match` is stale; body has `currentVersion` | Reload, reapply, retry |
| `PRECONDITION_REQUIRED` | 428 | `If-Match` is required | Send the `ETag` you read |
| `PAYLOAD_TOO_LARGE` | 413 | Body over 1 MB (10 MB for CSV import) | — |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | Wrong `Content-Type` | — |
| `RATE_LIMITED` | 429 | Too many requests; see `Retry-After` | Back off |
| `INTERNAL` | 500 | Unexpected server error (logged with the `requestId`) | Retry later; report the request id |
| `SERVICE_UNAVAILABLE` | 503 | Dependency down | Retry with backoff |

New codes may be added in `/v1`; clients should treat unknown codes by HTTP status.

## 9. Limits

| Limit | Value |
|-------|-------|
| Public key rate | 600 requests/min (burst 100), configurable per key |
| Server key rate | 6000 requests/min, configurable per key |
| Anonymous (only when `REQUIRE_API_KEY=false`) | 600 requests/min per IP |
| Admin user | 1200 requests/min |
| Login | 10 attempts/min per IP; refresh 60/min per IP |
| Request body | 1 MB; CSV import 10 MB and 5000 rows |
| Public event range query | 366 days |
| ICS feed | 5 BS years per request |
| Event span | 366 days per occurrence; recurrence expansion capped at 400 occurrences per window |
| Year draft | 50 years |
| Telemetry batch | 50 events |

Responses carry `RateLimit-Limit` and `RateLimit-Remaining`. A 429 carries `Retry-After`.

## 10. Operations: health, metrics, logs

| Endpoint | Use |
|----------|-----|
| `GET /healthz` | Liveness: the process is up. Container `HEALTHCHECK` uses `calendar-api healthcheck`. |
| `GET /readyz` | Readiness: database reachable and year table loaded. Route traffic only when 200. |
| `GET :9090/metrics` | Prometheus: `http_requests_total`, `http_request_duration_seconds`, `calendar_data_version`, `api_rate_limited_total`, `client_telemetry_events_total`, `outbox_*` (worker). Keep this port private. |

Logs are JSON lines on stdout (`LOG_FORMAT=text` for local reading), one per request with `requestId`, `route`, `status`, `durationMs`, `apiClient` or `adminUser`. Secrets, tokens and request bodies are never logged.

Useful commands:

```bash
docker compose logs -f api worker
docker compose exec postgres psql -U calendar -c "select action, entity, at from audit_log order by id desc limit 10"
docker compose run --rm api migrate status
docker compose run --rm api convert AD 2026-09-24          # offline conversion with the embedded seed table
```

## 11. Managing the API

The API is managed **contract-first**: `api/openapi.yaml` is designed and reviewed before code, and automation keeps code and contract in lock-step.

### 11.1 How a change is made

1. **Design in the spec.** Edit `api/openapi.yaml` in the pull request: paths, schemas (with `additionalProperties: false`), descriptions and examples.
2. **Lint.** `make lint-api` runs Redocly with [`redocly.yaml`](../redocly.yaml). Deliberate exceptions live in `.redocly.lint-ignore.yaml` with a comment.
3. **Implement** the handler in `services/calendar-api/internal/httpapi`.
4. **Extend the flow test** (`internal/httpapi/flow_test.go`). It validates every request and response against the spec, and it **fails if any operation in the spec is never called**, so new endpoints cannot ship undocumented or untested.
5. **Check compatibility.** CI runs `oasdiff breaking` against `main`; a breaking change fails the build (see 11.2).
6. **Record it** in [`api/CHANGELOG.md`](../api/CHANGELOG.md) and bump `info.version` (semver: minor for additions, patch for docs/fixes).
7. **Release.** The server embeds the spec, so `/openapi.yaml` and `/docs` always describe the running build.

### 11.2 Compatibility rules for `/v1`

| Allowed in v1 (non-breaking) | Not allowed in v1 (breaking → `/v2`) |
|------------------------------|--------------------------------------|
| New endpoints | Removing or renaming an endpoint, field or parameter |
| New optional request fields and query parameters | Making an optional input required |
| New response fields (clients ignore unknown fields) | Changing a field's type or meaning |
| New error codes (clients fall back to the HTTP status) | Changing an error's HTTP status |
| New enum values in responses, only where documented as extensible | Tightening validation on existing inputs without notice |
| Relaxing validation | Changing URL or caching semantics of immutable resources |

**Deprecation policy.** Mark the operation or field `deprecated: true` in the spec with a replacement in its description, announce it in the changelog, and keep it working for at least **6 months** (mobile apps update slowly). When a deprecated route is removed in a future version, it will first send `Deprecation` and `Sunset` response headers for the whole notice period. Use `MIN_SUPPORTED_CLIENT` to tell old apps to update (`manifest.updateRequired`).

### 11.3 Data and schema changes

- **Migrations are append-only.** Never edit a migration that has run anywhere shared; add `0000N_description.sql`. Follow expand → migrate → contract for zero-downtime changes. `bootstrap` runs migrations on every deploy under an advisory lock.
- **Year table changes** only go through drafts and approval ([§6.5](#65-correcting-the-year-table-four-eyes)), never through SQL. Every published version stays downloadable forever.
- **UI config schema changes:** additive changes keep `schemaVersion: 1`. A breaking change needs `schemaVersion: 2`, client support first, and `minClientVersion` on configs that use it.

### 11.4 Checks that run in CI

| Check | Command | Fails when |
|-------|---------|-----------|
| Format and vet | `gofmt -l`, `go vet ./...` | Unformatted or suspicious code |
| Unit tests | `go test ./...` | Any engine, validation or security test fails |
| Flow and contract test | `docker compose --profile test run --rm test` (CI uses a Postgres service) | A response differs from the spec, or an operation is uncovered |
| Spec lint | `make lint-api` | Invalid or low-quality OpenAPI |
| Breaking changes | `oasdiff breaking <main spec> api/openapi.yaml --fail-on ERR` | A `/v1` breaking change |
| Image build | `docker build -f services/calendar-api/Dockerfile .` | The service does not build |

See [`.github/workflows/ci.yml`](../.github/workflows/ci.yml).
