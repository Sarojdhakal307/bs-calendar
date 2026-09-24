#!/bin/sh
# Creates a separate database for integration and contract tests (docker compose run --rm test).
set -e
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -c "CREATE DATABASE calendar_test OWNER \"$POSTGRES_USER\";"
