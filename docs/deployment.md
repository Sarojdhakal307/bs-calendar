# Deployment (Docker)

The whole service runs with Docker Compose: locally with `docker-compose.yml`, in production on one Linux
server with `deploy/compose/docker-compose.prod.yml`. Nothing else is required.

## 1. What runs

| Container | Role | Production notes |
|-----------|------|------------------|
| `caddy` | HTTPS in front of the API. Gets and renews Let's Encrypt certificates automatically. | Only this container is reachable from the internet (ports 80 and 443). |
| `bootstrap` | Runs once on every start: database migrations, then seed data. Exits. | The API waits for it. Safe to run again and again. |
| `api` ×2 | The HTTP API (`/v1/...`, `/docs`). | Two copies, so one can restart while the other serves. |
| `worker` | Delivers webhooks with retries. | One copy is enough. |
| `postgres` | The database. | Optional (profile `bundled-db`). A managed database (AWS RDS, DigitalOcean, Supabase…) is better for production. |
| `backup` | Daily `pg_dump` of the bundled database. | Only with `bundled-db`. |

All app containers run as a non-root user with a read-only file system, no Linux capabilities, memory
and CPU limits, and log rotation.

## 2. Local development

```bash
cp .env.example .env          # optional: development defaults are built in
docker compose up -d --build
```

| URL | What |
|-----|------|
| http://localhost:8080/docs | Developer guide and integrations |
| http://localhost:8080/docs/reference | API reference |
| http://localhost:8080/docs/try | Try the API in the browser |
| http://localhost:9090/metrics | Prometheus metrics |

Development login: `admin@example.com` / `change-me-please-now`. Development public key:
`pk_dev_local_public_key_0001`.

```bash
docker compose --profile tools run --rm smoke     # end-to-end check of the running stack
docker compose --profile test run --rm test       # all Go tests
docker compose down                               # stop (keeps data)
docker compose down -v                            # stop and delete all local data
```

## 3. Production on one server

### 3.1 What you need

- A Linux server with 2 vCPU, 2 GB RAM and 20 GB disk (Ubuntu 24.04 or similar).
- Docker Engine with the Compose plugin: `curl -fsSL https://get.docker.com | sh`.
- A domain name, for example `calendar-api.example.org`, with an A (and AAAA) record pointing to the server.
- Ports 80 and 443 open in the firewall (80 is needed for the certificate challenge and redirects).

### 3.2 Get the image

Choose one:

- **From GitHub (recommended).** Push a tag and the release workflow builds and publishes the image:
  ```bash
  git tag v1.0.0 && git push origin v1.0.0
  # → ghcr.io/<your-github-user-or-org>/calendar-api:v1.0.0
  ```
  If the GitHub package is private, log in on the server once:
  `echo <token-with-read:packages> | docker login ghcr.io -u <user> --password-stdin`.
- **Build on the server.** Clone the repository and run:
  ```bash
  docker build -f services/calendar-api/Dockerfile -t calendar-api:v1.0.0 --build-arg VERSION=v1.0.0 .
  ```
  Then use `IMAGE=calendar-api` in the next step.

### 3.3 Configure

```bash
git clone <your repository> /opt/bs-calendar && cd /opt/bs-calendar/deploy/compose
cp .env.production.example .env.production
chmod 600 .env.production
openssl rand -base64 48     # run once per secret and paste into .env.production
```

Fill in every value marked REQUIRED. The most important ones:

| Variable | Value |
|----------|-------|
| `IMAGE` | `ghcr.io/<owner>/calendar-api` (or `calendar-api` if built on the server) |
| `DOMAIN`, `PUBLIC_BASE_URL` | `calendar-api.example.org`, `https://calendar-api.example.org` |
| `ACME_EMAIL` | Your e-mail for Let's Encrypt |
| `JWT_SIGNING_KEY`, `WEBHOOK_SECRET_KEY` | Two different random values (never change the webhook key later) |
| `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD` | The first super admin (change the password after the first login) |
| `CORS_ALLOWED_ORIGINS` | Your website origins, for example `https://www.example.org` |
| `ADMIN_ALLOWED_CIDRS` | Optional: office or VPN addresses allowed to use the admin API |

### 3.4 Choose the database

**Option 1: bundled Postgres on the same server (simplest).** Set these in `.env.production`:

```bash
POSTGRES_PASSWORD=<random>
CALENDAR_OWNER_PASSWORD=<random, letters and digits only>
CALENDAR_APP_PASSWORD=<random, letters and digits only>
MIGRATION_DATABASE_URL=postgres://calendar_owner:<owner password>@postgres:5432/calendar?sslmode=disable
DATABASE_URL=postgres://calendar_app:<app password>@postgres:5432/calendar?sslmode=disable
```

On first start the database creates two users automatically (from `deploy/postgres/roles.sql`):
`calendar_owner` runs migrations; `calendar_app`, used by the API, can read and write data but cannot
change tables or delete the audit log. Add `--profile bundled-db` to every `docker compose` command.

**Option 2: managed Postgres.** Create an empty database called `calendar`, then run the role script
once as the database administrator:

```bash
psql "postgres://admin@db-host:5432/calendar?sslmode=require" \
  -v owner_password="<random>" -v app_password="<random>" -f deploy/postgres/roles.sql
```

and set `MIGRATION_DATABASE_URL` and `DATABASE_URL` to the two users with `sslmode=require`.

### 3.5 Start

```bash
export VERSION=v1.0.0
docker compose -f docker-compose.prod.yml --env-file .env.production up -d                        # managed DB
docker compose -f docker-compose.prod.yml --env-file .env.production --profile bundled-db up -d  # bundled DB
```

Check it:

```bash
docker compose -f docker-compose.prod.yml --env-file .env.production ps     # api "healthy", bootstrap "exited (0)"
curl https://calendar-api.example.org/readyz                                 # {"status":"ready",...}
docker compose -f docker-compose.prod.yml --env-file .env.production logs api | grep -i warn   # read every warning
```

End-to-end check without changing data (from any machine with curl and jq):

```bash
API=https://calendar-api.example.org ADMIN_EMAIL=you@example.org ADMIN_PASSWORD='…' SMOKE_WRITE=false sh scripts/smoke.sh
```

### 3.6 First steps after the first deploy

1. Sign in and **change the bootstrap password**: `POST /v1/admin/auth/login`, then
   `PATCH /v1/admin/users/{your id}` with `{"password":"<new>"}`.
2. Create the team: a second `calendar_admin` (year-table changes need two people), `designer`s for the
   theme, `editor`s for events (`POST /v1/admin/users`).
3. Issue API keys: one public key for the website (with `allowedOrigins`), one for the mobile app
   (without), and a server key for backends (`POST /v1/admin/api-clients`).
4. **Confirm the year table** against the official calendar (see [reliable.md §3.1](reliable.md#31-sourcing-the-year-table-phase-0)).
5. Enter this year's and next year's public holidays and festivals.

The exact commands for each step are in [api.md](api.md).

## 4. Updating and rolling back

```bash
cd /opt/bs-calendar/deploy/compose
export VERSION=v1.1.0
docker compose -f docker-compose.prod.yml --env-file .env.production pull
docker compose -f docker-compose.prod.yml --env-file .env.production up -d
```

What happens: `bootstrap` migrates the database first; then the `api` containers are replaced. Each old
container reports "not ready" for a few seconds before stopping, and Caddy holds incoming requests for up
to 10 seconds until a new container answers. In our test, 3,109 requests sent during a redeploy all
succeeded.

**Rollback:** run the same commands with the previous `VERSION`. Database migrations are written to stay
compatible with the previous release, so rolling back the image is safe. If a release note says a
migration is not backward compatible, restore the backup taken before the update instead.

## 5. Backups

| Database | How |
|----------|-----|
| Managed Postgres | Turn on the provider's automatic backups with point-in-time recovery (7 to 30 days). |
| Bundled Postgres | The `backup` container writes `backups/calendar-<time>.dump` every 24 hours and keeps 14 days. Copy that folder off the server regularly (for example with `rclone` to object storage). |

Restore a dump:

```bash
docker compose -f docker-compose.prod.yml --env-file .env.production --profile bundled-db stop api worker
docker compose -f docker-compose.prod.yml --env-file .env.production --profile bundled-db exec -T postgres \
  pg_restore --clean --if-exists --no-owner -U calendar_admin -d calendar < backups/calendar-<time>.dump
docker compose -f docker-compose.prod.yml --env-file .env.production --profile bundled-db up -d
```

Practise a restore on a spare server at least once before you rely on it.

## 6. Monitoring and logs

| What | How |
|------|-----|
| Is it up? | `GET /healthz` (process alive), `GET /readyz` (database reachable and year table loaded). Point an uptime checker (UptimeRobot, Better Stack…) at `https://<domain>/readyz`. |
| Logs | `docker compose … logs -f api worker`. JSON lines with `requestId`, `route`, `status`, `durationMs`. |
| Metrics | Prometheus format on port 9090 (api) and 9091 (worker), inside the Docker network only: `docker run --rm --network bs-calendar_default curlimages/curl -s http://api:9090/metrics`. |
| Worth alerting on | `/readyz` failing; many HTTP 5xx; `outbox_dead` above 0 (webhooks giving up); the year table not yet verified 90 days before a new BS year (`GET /v1/admin/health/data`). |

## 7. Safety built into production mode

With `APP_ENV=production` the API **refuses to start** when:

- `JWT_SIGNING_KEY`, `WEBHOOK_SECRET_KEY`, the bootstrap password or keys look like development values;
- `PUBLIC_BASE_URL` is not `https`;
- `REQUIRE_API_KEY=false` or `ALLOW_PRIVATE_WEBHOOKS=true`.

It **warns** at startup when CORS allows every origin, `TRUST_PROXY` is off, the database connection is
not encrypted, or the admin API is open to every network. It also refuses to start when the database is
behind the code (migrations not run), sends HSTS headers, times out slow requests (30 s) and slow
database queries (15 s), and drains connections before stopping.

Secrets can come from files instead of the env file: set `JWT_SIGNING_KEY_FILE=/run/secrets/jwt` (any
variable plus `_FILE`), which works with Docker secrets.

## 8. When one server is not enough

- **More traffic:** raise `API_REPLICAS` and `DB_MAX_CONNS`, or give the server more CPU.
- **A CDN in front** (for example Cloudflare, proxied DNS): the API already sends the right caching headers,
  so most requests are answered by the CDN. Set Caddy's `trusted_proxies` to the CDN's addresses (see the
  comment in `deploy/compose/Caddyfile`) so client IPs stay correct.
- **Managed container platforms** (Google Cloud Run, AWS ECS, Fly.io, Railway, Render): run the same
  image with the same environment variables. Run `bootstrap` as a one-off job before each release,
  `serve` as the web service, and `worker` as a background service.

## 9. Troubleshooting

| Problem | Fix |
|---------|-----|
| `bootstrap` exits with an error | `docker compose … logs bootstrap`. Usually a wrong `MIGRATION_DATABASE_URL` or password. |
| `api` restarts with "configuration: …" | The message lists every setting production refuses. Fix them in `.env.production`. |
| `api` says "database schema is at migration N but this build needs M" | `bootstrap` did not run or failed; run `up -d` again and read its log. |
| Caddy logs certificate errors | DNS does not point to the server yet, or port 80 is blocked. |
| Browsers get CORS errors | Add the site to `CORS_ALLOWED_ORIGINS` and restart the api (`up -d`). |
| Everyone shares one rate limit or has the same IP in the audit log | `TRUST_PROXY=true` is missing (Caddy forwards the real client IP). |

## 10. What was verified

These checks were run on the production Compose file with `APP_ENV=production`, fresh random secrets,
the bundled database and Caddy's HTTPS:

| Check | Result |
|-------|--------|
| End-to-end smoke test through HTTPS (HTTP/2) | 43 of 43 passed |
| Requests during an API redeploy | 3,109 sent, 0 failed |
| The API running as the restricted database user | All flows work; editing or deleting the audit log, altering snapshots, dropping tables and faking migrations are all refused |
| Startup with development secrets in production mode | Refused, listing each problem |
| Startup against an empty database | Refused with "run `calendar-api bootstrap` first" |
| Plain HTTP | Redirects to HTTPS; `/metrics` is not reachable from outside |
