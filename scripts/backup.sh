#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/clawguard}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
BACKUP_OFFSITE_DIR="${BACKUP_OFFSITE_DIR:-}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"

cd "$ROOT_DIR"
umask 077
mkdir -p "$BACKUP_DIR"
# Concurrent invocations in the same second must not share a temporary file
# or overwrite an existing verified dump.
TEMP_PATH="$(mktemp "${BACKUP_DIR}/clawguard-${STAMP}-XXXXXX.dump.tmp")"
FINAL_PATH="${TEMP_PATH%.tmp}"
trap 'rm -f "$TEMP_PATH"' EXIT

docker compose exec -T postgres sh -ec \
  'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --compress=6 --no-owner --no-acl' \
  >"$TEMP_PATH"

test -s "$TEMP_PATH"
docker compose exec -T postgres pg_restore --list <"$TEMP_PATH" >/dev/null
# Link is atomic and refuses to overwrite even if a final name already exists.
ln "$TEMP_PATH" "$FINAL_PATH"
rm -f "$TEMP_PATH"
# Store a relative filename so an offsite copy remains independently verifiable.
(cd "$BACKUP_DIR" && sha256sum "$(basename "$FINAL_PATH")") >"${FINAL_PATH}.sha256"

if [[ -n "$BACKUP_OFFSITE_DIR" ]]; then
  mkdir -p "$BACKUP_OFFSITE_DIR"
  cp --preserve=timestamps "$FINAL_PATH" "${FINAL_PATH}.sha256" "$BACKUP_OFFSITE_DIR/"
fi

find "$BACKUP_DIR" -maxdepth 1 -type f \
  \( -name 'clawguard-*.dump' -o -name 'clawguard-*.dump.sha256' \) \
  -mtime "+$BACKUP_RETENTION_DAYS" -delete

docker compose exec -T redis sh -ec \
  'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli SET clawguard:ops:last-backup-at "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'" >/dev/null'

printf 'backup=%s\n' "$FINAL_PATH"
