# BS/AD Calendar Platform

An API-controlled Bikram Sambat (BS) and Gregorian (AD) calendar: one Go service owns the year data,
events and admin-controlled UI configuration, and web (Next.js) and mobile (React Native) clients
sync from it and convert dates locally.

| Part | Status |
|------|--------|
| Go service `services/calendar-api` (public + admin API, worker, migrations) | **Implemented and tested** |
| OpenAPI contract `api/openapi.yaml`, docs at `/docs` | **Implemented**, enforced by tests |
| Docker Compose stack, CI workflow, smoke test | **Implemented** |
| TypeScript packages (`bs-core`, hooks, web and native components) | Planned (see docs/architecture.md §10) |
| Admin panel (Next.js) | Planned (the admin API it needs is complete) |

## Quick start

Only Docker is required.

```bash
cp .env.example .env              # optional; development defaults are built in
docker compose up -d --build      # postgres → bootstrap (migrate + seed) → api + worker
```

- API reference: http://localhost:8080/docs · try it: http://localhost:8080/docs/try
- Dev public API key: `pk_dev_local_public_key_0001`
- Dev super admin: `admin@example.com` / `change-me-please-now`

```bash
curl -s -H "X-Api-Key: pk_dev_local_public_key_0001" "http://localhost:8080/v1/convert?ad=2026-09-24"
docker compose --profile tools run --rm smoke     # 40 end-to-end checks that follow docs/api.md
docker compose --profile test run --rm test       # all Go tests incl. the flow + contract test
```

`make help` lists shortcuts (Git Bash or WSL on Windows).

## Documentation

| Document | For |
|----------|-----|
| [docs/api.md](docs/api.md) | Using and managing the API: auth, conventions, every flow with commands, webhooks, error codes, change process |
| [api/openapi.yaml](api/openapi.yaml) | The contract (source of truth for fields) |
| [api/requests.http](api/requests.http) | Click-through walkthrough in VS Code (REST Client) |
| [api/CHANGELOG.md](api/CHANGELOG.md) | API versions |
| [docs/architecture.md](docs/architecture.md) | Components, data model, design decisions |
| [docs/flow.md](docs/flow.md) | Sequence and state diagrams |
| [docs/reliable.md](docs/reliable.md) | Correctness, tests, failure modes, SLOs, runbooks |
| [.env.example](.env.example) | Every configuration variable |

## Repository layout

```
api/                      OpenAPI contract (embedded in the binary), changelog, REST Client walkthrough
fixtures/                 Shared data: BS year table seed, golden conversions, UI config schema + default
services/calendar-api/
  cmd/calendar-api/       one binary: serve | worker | bootstrap | migrate | convert | dump | healthcheck
  internal/bscal/         pure AD⇄BS engine (no I/O; tested on every day of the range)
  internal/httpapi/       handlers, middleware, flow + contract test
  internal/…              auth, events, calendardata, uiconfig, outbox, audit, store (migrations), app wiring
deploy/postgres/          database init (creates the test database)
scripts/smoke.sh          end-to-end smoke test (follows docs/api.md)
docker-compose.yml        local stack; profiles "tools" (smoke, webhook-sink) and "test"
.github/workflows/ci.yml  vet, tests, fuzzing, govulncheck, OpenAPI lint, breaking-change check, stack smoke
```

## Year data: read before production

The seed table (`fixtures/year-table.seed.json`) covers **BS 1975–2100**. It was built from three
open-source tables that were compared year by year:

- **BS 1975–2083:** at least two sources agree on every year, so these years are marked `verified`.
  The sources disagreed on 2004, 2062, 2082 and 2083; the table uses the majority value, which comes
  from the most recently maintained source.
- **BS 2084–2100:** the sources disagree, so these years are marked `projected`.

The file records its provenance. **A calendar admin still has to confirm the verified years against
the official government calendar before production** (docs/reliable.md §3.1). Corrections go through
the four-eyes draft workflow in the admin API and reach apps without a release.

## Tests at a glance

| Suite | What it proves |
|-------|----------------|
| `internal/bscal` | Golden dates from independent sources; every day in BS 1975–2100 round-trips with no gaps; integer date maths matches the standard library; invalid tables are rejected; fuzzing |
| `internal/httpapi` flow test | Real server + Postgres: every documented flow, **every request and response validated against `api/openapi.yaml`**, and a failure if any operation is never exercised |
| `internal/events`, `uiconfig`, `auth`, `outbox` | Recurrence edge cases, validation, merge patch, contrast gate, JWT and password rules, webhook signatures, SSRF guard |
| `scripts/smoke.sh` | The documented commands work against a running stack |
