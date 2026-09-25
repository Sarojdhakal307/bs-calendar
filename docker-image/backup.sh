#!/bin/sh
# Daily pg_dump into /backups, keeping BACKUP_KEEP_DAYS days.
# Restore: see README.md "Backups".
set -eu
KEEP_DAYS=${BACKUP_KEEP_DAYS:-14}
while true; do
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  file="/backups/calendar-$stamp.dump"
  if pg_dump --format=custom --no-owner --file="$file.partial"; then
    mv "$file.partial" "$file"
    echo "backup ok: $file"
  else
    rm -f "$file.partial"
    echo "backup FAILED at $stamp" >&2
  fi
  find /backups -name 'calendar-*.dump' -mtime +"$KEEP_DAYS" -delete
  sleep 86400
done
