# BS/AD Calendar API

A Dockerized, API-controlled Bikram Sambat (BS) and Gregorian (AD) calendar. One Go service owns the
BS year table, holidays and events, and the look of the calendar; websites and mobile apps call its API
to draw date pickers and event calendars in light or dark mode.

## Quick start

Only Docker is required.

```bash
docker compose up -d --build        # postgres → bootstrap (migrate + seed) → api + worker
```

- Developer guide: http://localhost:8080/docs · API reference: http://localhost:8080/docs/reference · try it: http://localhost:8080/docs/try
- Development public key: `pk_dev_local_public_key_0001`
- Development admin: `admin@example.com` / `change-me-please-now`

```bash
curl -H "X-Api-Key: pk_dev_local_public_key_0001" "http://localhost:8080/v1/convert?ad=2026-09-24"
curl -H "X-Api-Key: pk_dev_local_public_key_0001" "http://localhost:8080/v1/months/BS/2083/6?include=events"
docker compose --profile tools run --rm smoke     # end-to-end check of the running stack
docker compose --profile test run --rm test       # all Go tests (including the API contract test)
```

## Documentation

| Read this | To |
|-----------|----|
| [docs/how-it-works.md](docs/how-it-works.md) | Understand the whole system in plain language |
| [docs/web.md](docs/web.md) | Add the AD/BS date picker and event calendar to a React or Next.js site |
| [docs/react-native.md](docs/react-native.md) | Add them to a React Native (Expo) app |
| [docs/deployment.md](docs/deployment.md) | Run it in production with Docker Compose, HTTPS, backups and updates |
| [docs/api.md](docs/api.md) | Use the admin and public API: auth, every flow with commands, errors, webhooks |
| [api/openapi.yaml](api/openapi.yaml) | Exact API contract (also served at `/docs/reference`) |
| [docs/architecture.md](docs/architecture.md), [docs/flow.md](docs/flow.md), [docs/reliable.md](docs/reliable.md) | Design, diagrams, and correctness and operations detail |

## Repository layout

```
api/                     OpenAPI contract (embedded in the binary), changelog, VS Code REST walkthrough
fixtures/                BS year table seed (1975-2100, with sources), golden conversions, UI config schema
services/calendar-api/   the Go service: one binary with serve | worker | bootstrap | migrate | convert ...
deploy/compose/          production Docker Compose + Caddy (HTTPS) + backups
deploy/postgres/         least-privilege database roles
scripts/smoke.sh         end-to-end smoke test
docker-compose.yml       local development stack
.github/workflows/       CI (tests, vulnerability scan, API lint, breaking-change check) and image release
```

## Before production

- **Confirm the year data.** The seed table was built by comparing three open-source tables. BS 1975-2083
  are marked verified, where at least two sources agree; 2084 onward are projected. A calendar admin must
  still check the verified years against the official calendar ([docs/reliable.md §3.1](docs/reliable.md#31-sourcing-the-year-table-phase-0)).
- **Follow [docs/deployment.md](docs/deployment.md).** Production mode refuses to start with development secrets.
