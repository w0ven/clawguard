#!/bin/sh
# Disposable local PG only. Never exports or modifies deployment data.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
name="cg-native-migration-test-$$"
docker run -d --name "$name" --label openbear-task=a5854b6b --label openbear-purpose=native-isolated-test \
  -e POSTGRES_USER=cg_native_test -e POSTGRES_PASSWORD=isolated-test-only \
  -e POSTGRES_DB=cg_native_migration_test -p 127.0.0.1::5432 \
  --tmpfs /var/lib/postgresql/data postgres:16-alpine >/dev/null
trap 'docker rm -f "$name" >/dev/null' EXIT INT TERM
port=$(docker port "$name" 5432/tcp | sed 's/.*://')
for i in $(seq 1 50); do
  if docker exec "$name" pg_isready -U cg_native_test >/dev/null 2>&1; then break; fi
  sleep 0.2
done
export CG_NATIVE_MIGRATION_TEST_DATABASE_URL="postgres://cg_native_test:isolated-test-only@127.0.0.1:$port/cg_native_migration_test?sslmode=disable"
cd "$root/native"
uv run python -m pytest tests/test_migration.py -v "$@"
