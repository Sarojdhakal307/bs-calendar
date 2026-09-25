# Run the BS/AD Calendar API on a server

This folder runs the published image **`ghcr.io/sarojdhakal307/calendar-api`** on one Linux server.
nginx on the host serves **https://calendar.oneclickinfosys.com** and forwards to the containers.

| File | Purpose |
|------|---------|
| `docker-compose.yml` | PostgreSQL, bootstrap (migrations), API (also serves the website, dashboard and docs), worker, daily backup |
| `.env.server` | Your settings and secrets (fill in every `CHANGE_ME`) |
| `nginx/calendar-api.conf` | nginx site for `/etc/nginx/sites-available` |
| `init-db.sh` | Creates the least-privilege database users on first start |
| `backup.sh` | Daily database dump into `./Dockerdata/backups` |

## 1. Prepare the server

- Linux server with 2 vCPU, 2 GB RAM, 20 GB disk (Ubuntu 24.04 or similar).
- Install Docker: `curl -fsSL https://get.docker.com | sh`
- DNS: an **A record** for `calendar.oneclickinfosys.com` pointing to the server's IP.
- Open ports **80** and **443** in the firewall.

## 2. Copy this folder and configure

```bash
scp -r docker-image user@your-server:/opt/bs-calendar
ssh user@your-server
cd /opt/bs-calendar
chmod 600 .env.server
chmod +x init-db.sh backup.sh
mkdir -p Dockerdata/postgres Dockerdata/backups
```

Generate secrets (run once per value) and edit `.env.server`:

```bash
openssl rand -hex 32
nano .env.server
```

Fill in every `CHANGE_ME`:

| Setting | What to put |
|---------|-------------|
| `POSTGRES_PASSWORD` | Random value |
| `CALENDAR_OWNER_PASSWORD`, `CALENDAR_APP_PASSWORD` | Random hex values, **also pasted into** `MIGRATION_DATABASE_URL` and `DATABASE_URL` |
| `JWT_SIGNING_KEY`, `WEBHOOK_SECRET_KEY` | Two different random values (never change the webhook key later) |
| `BOOTSTRAP_ADMIN_EMAIL`, `BOOTSTRAP_ADMIN_PASSWORD` | The first admin login (at least 12 characters) |
| `CORS_ALLOWED_ORIGINS` | Your website address(es), for example `https://oneclickinfosys.com` |

Before starting, make sure nothing is left to fill in (this must print nothing):

```bash
grep -n CHANGE_ME .env.server
```

## 3. Start the containers

```bash
docker compose --env-file .env.server up -d
curl http://127.0.0.1:8080/api/readyz      # {"status":"ready",...}
```

Everything runs on one port, `127.0.0.1:8080` (change it with `WEB_PORT`). It is reachable only from
this machine, so the internet reaches it through nginx:

| Path | What it serves |
|------|----------------|
| `/` | Public website about the system |
| `/api/...` | The API, for example `/api/v1/today` |
| `/admin/` | Admin dashboard (sign in with the bootstrap admin) |
| `/docs` | API reference (`/docs/try` to send test requests) |

On your own computer, open `http://localhost:8080` directly.

## 4. nginx and HTTPS

```bash
sudo apt install -y nginx certbot python3-certbot-nginx

sudo cp nginx/calendar-api.conf /etc/nginx/sites-available/calendar-api.conf
sudo ln -s /etc/nginx/sites-available/calendar-api.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx

# Gets the certificate, adds HTTPS and the HTTP-to-HTTPS redirect to the site file
sudo certbot --nginx -d calendar.oneclickinfosys.com
```

Certificates renew automatically. Check with `sudo certbot renew --dry-run`.

Check it:

```bash
curl https://calendar.oneclickinfosys.com/api/readyz # {"status":"ready",...}
docker compose --env-file .env.server ps
```

You should see `api` as **healthy**, `bootstrap` **exited (0)**, and `postgres`, `worker`, `backup`
running. Website: `https://calendar.oneclickinfosys.com`, dashboard: `/admin/`, API reference: `/docs`.

## 5. First steps

The easiest way is the dashboard at `/admin/`: change your password under Users, add API keys, events and
holidays, and set the app theme under UI & theme. The same steps with curl:

```bash
API=https://calendar.oneclickinfosys.com/api

# Log in
TOKEN=$(curl -s -X POST $API/v1/admin/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"YOUR_BOOTSTRAP_EMAIL","password":"YOUR_BOOTSTRAP_PASSWORD"}' | sed -E 's/.*"accessToken":"([^"]+)".*/\1/')

# Change the bootstrap password
ME=$(curl -s $API/v1/admin/me -H "Authorization: Bearer $TOKEN" | sed -E 's/.*"id":"([^"]+)".*/\1/')
curl -X PATCH $API/v1/admin/users/$ME -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"password":"a-new-long-password"}'

# Log in again with the new password, then issue API keys (each key is shown only once):
curl -X POST $API/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Website","kind":"public","allowedOrigins":["https://oneclickinfosys.com"]}'
curl -X POST $API/v1/admin/api-clients -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Mobile app","kind":"public"}'

# Test a key
curl -H "X-Api-Key: pk_..." "$API/v1/convert?ad=2026-09-24"
```

Then add holidays and events, and create other admins (a second `calendar_admin` is needed to approve
year-table changes). All admin commands: `docs/api.md` in the repository. Using the API from a website
or a mobile app: `docs/web.md` and `docs/react-native.md`.

## 6. Update to a new version

```bash
cd /opt/bs-calendar
sed -i 's/^VERSION=.*/VERSION=v1.1.0/' .env.server
docker compose --env-file .env.server pull
docker compose --env-file .env.server up -d
```

`bootstrap` migrates the database first, then the API container is replaced. The site is
unavailable for a few seconds during the restart. **Rollback:** set the previous `VERSION` and run the
same two commands.

## 7. Backups

The `backup` container writes `Dockerdata/backups/calendar-<time>.dump` every 24 hours and keeps 14 days.
Copy the `Dockerdata/backups` folder to another machine or to cloud storage regularly.

Restore a backup:

```bash
docker compose --env-file .env.server stop api worker
docker compose --env-file .env.server exec -T postgres \
  pg_restore --clean --if-exists --no-owner -U calendar_admin -d calendar < Dockerdata/backups/calendar-<time>.dump
docker compose --env-file .env.server up -d
```

## 8. Everyday commands

| Task | Command |
|------|---------|
| Status | `docker compose --env-file .env.server ps` |
| Logs | `docker compose --env-file .env.server logs -f api worker` |
| Restart | `docker compose --env-file .env.server restart api` |
| Stop (keeps data) | `docker compose --env-file .env.server down` |
| Database shell | `docker compose --env-file .env.server exec postgres psql -U calendar_admin -d calendar` |
| nginx logs | `sudo tail -f /var/log/nginx/error.log` |

## 9. Common problems

| Problem | Fix |
|---------|-----|
| `api` restarts, log starts with `configuration:` | A value in `.env.server` is still a placeholder or unsafe; the log lists each one. |
| `bootstrap` fails with `password authentication failed` | The passwords in the two database URLs differ from `CALENDAR_OWNER_PASSWORD` / `CALENDAR_APP_PASSWORD`. |
| nginx shows 502 Bad Gateway | The API containers are not running. Check `docker compose --env-file .env.server ps`. |
| certbot fails | DNS does not point to this server yet, or port 80 is closed. |
| Browser CORS errors | Add the site to `CORS_ALLOWED_ORIGINS`, then `docker compose --env-file .env.server up -d`. |
| `pull` says access denied | Make the GitHub package public, or `docker login ghcr.io` with a token that has `read:packages`. |

## 10. Where the data lives

| Folder | Contents |
|--------|----------|
| `Dockerdata/postgres` | The database files |
| `Dockerdata/backups` | Daily database dumps |
| `/etc/letsencrypt` (host) | HTTPS certificates |

Never delete `Dockerdata`. To move to another server, stop the stack, copy this whole folder including
`Dockerdata` and `.env.server`, then start it there.
