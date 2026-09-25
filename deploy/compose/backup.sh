#!/bin/sh
# Daily logical backup of the bundled database (profile bundled-db). Managed Postgres should use
# the provider's point-in-time recovery instead; see docs/deployment.md "Backups".
#
# Restore:  pg_restore --clean --if-exists --no-owner -d "$DATABASE_URL" /backups/calendar-<stamp>.dump
set -eu
KEEP_DAYS=${BACKUP_KEEP_DAYS:-14}
INTERVAL=${BACKUP_INTERVAL_SECONDS:-86400}

while true; do
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  file="/backups/calendar-$stamp.dump"
  if pg_dump --format=custom --no-owner --file="$file.partial"; then
    mv "$file.partial" "$file"
    echo "backup ok: $file ($(du -h "$file" | cut -f1))"
  else
    rm -f "$file.partial"
    echo "backup FAILED at $stamp" >&2
  fi
  find /backups -name 'calendar-*.dump' -mtime +"$KEEP_DAYS" -delete
  sleep "$INTERVAL"
done
