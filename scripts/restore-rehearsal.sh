#!/usr/bin/env bash
set -euo pipefail

POSTGRES_IMAGE="${POSTGRES_IMAGE:-postgres:16-alpine}"
BACKUP_DIR="${BACKUP_DIR:-./backups}"
BACKUP_PATH="${BACKUP_PATH:-${1:-}}"
KEEP="${KEEP:-0}"

PGUSER_REHEARSAL="rehearsal"
PGPASSWORD_REHEARSAL="rehearsal"
REHEARSAL_ID="$(date -u +%Y%m%d%H%M%S)_$$"
CONTAINER_NAME="restore_rehearsal_${REHEARSAL_ID}"
DB_NAME="restore_rehearsal_${REHEARSAL_ID}"
RESTORE_MOUNT="/restore/backup"

TABLES=(
  groups
  admins
  ai_decisions
  violations
  config_audit
  user_trust
)

RESTORE_OK=0
VALIDATION_FAILED=0
OWNER_ROLES_CREATED=""
OWNER_ROLES_SKIPPED=""

log() {
  printf '%s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [[ "${KEEP}" == "1" ]]; then
    log ""
    log "KEEP=1 set; leaving temporary container for inspection:"
    log "  container: ${CONTAINER_NAME}"
    log "  database:  ${DB_NAME}"
    return
  fi

  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
}

trap cleanup EXIT

require_command() {
  local name="$1"
  command -v "${name}" >/dev/null 2>&1 || die "required command not found: ${name}"
}

abs_path() {
  local path="$1"
  if command -v realpath >/dev/null 2>&1; then
    realpath "${path}"
    return
  fi

  case "${path}" in
    /*) printf '%s\n' "${path}" ;;
    *) printf '%s/%s\n' "$(pwd)" "${path#./}" ;;
  esac
}

find_latest_backup() {
  local dir="$1"

  [[ -d "${dir}" ]] || return 0

  find "${dir}" -type f \( \
    -name '*.sql' -o \
    -name '*.sql.gz' -o \
    -name '*.dump' -o \
    -name '*.dump.gz' \
  \) -printf '%T@ %p\n' \
    | sort -nr \
    | awk 'NR == 1 { sub(/^[^ ]+ /, ""); print }'
}

wait_for_postgres() {
  local attempt

  for attempt in $(seq 1 60); do
    if docker exec "${CONTAINER_NAME}" pg_isready -U "${PGUSER_REHEARSAL}" -d "${DB_NAME}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done

  return 1
}

psql_query() {
  local sql="$1"

  docker exec \
    -e PGPASSWORD="${PGPASSWORD_REHEARSAL}" \
    "${CONTAINER_NAME}" \
    psql -AtX -v ON_ERROR_STOP=1 -U "${PGUSER_REHEARSAL}" -d "${DB_NAME}" -c "${sql}"
}

sql_literal() {
  local value="$1"

  printf "'%s'" "${value//\'/\'\'}"
}

join_by_comma() {
  local first=1
  local item

  for item in "$@"; do
    if [[ "${first}" == "1" ]]; then
      first=0
    else
      printf ', '
    fi
    printf '%s' "${item}"
  done
}

extract_plain_sql_owner_roles() {
  local backup="$1"

  case "${backup}" in
    *.sql)
      awk '
        BEGIN { IGNORECASE = 1 }
        {
          line = $0
          while (match(line, /OWNER[[:space:]]+TO[[:space:]]+("[^"]+"|[^[:space:];]+)/)) {
            role = substr(line, RSTART, RLENGTH)
            sub(/^[Oo][Ww][Nn][Ee][Rr][[:space:]]+[Tt][Oo][[:space:]]+/, "", role)
            print role
            line = substr(line, RSTART + RLENGTH)
          }
          line = $0
          while (match(line, /SET[[:space:]]+SESSION[[:space:]]+AUTHORIZATION[[:space:]]+("[^"]+"|[^[:space:];]+)/)) {
            role = substr(line, RSTART, RLENGTH)
            sub(/^[Ss][Ee][Tt][[:space:]]+[Ss][Ee][Ss][Ss][Ii][Oo][Nn][[:space:]]+[Aa][Uu][Tt][Hh][Oo][Rr][Ii][Zz][Aa][Tt][Ii][Oo][Nn][[:space:]]+/, "", role)
            print role
            line = substr(line, RSTART + RLENGTH)
          }
        }
      ' "${backup}"
      ;;
    *.sql.gz)
      gzip -dc "${backup}" | awk '
        BEGIN { IGNORECASE = 1 }
        {
          line = $0
          while (match(line, /OWNER[[:space:]]+TO[[:space:]]+("[^"]+"|[^[:space:];]+)/)) {
            role = substr(line, RSTART, RLENGTH)
            sub(/^[Oo][Ww][Nn][Ee][Rr][[:space:]]+[Tt][Oo][[:space:]]+/, "", role)
            print role
            line = substr(line, RSTART + RLENGTH)
          }
          line = $0
          while (match(line, /SET[[:space:]]+SESSION[[:space:]]+AUTHORIZATION[[:space:]]+("[^"]+"|[^[:space:];]+)/)) {
            role = substr(line, RSTART, RLENGTH)
            sub(/^[Ss][Ee][Tt][[:space:]]+[Ss][Ee][Ss][Ss][Ii][Oo][Nn][[:space:]]+[Aa][Uu][Tt][Hh][Oo][Rr][Ii][Zz][Aa][Tt][Ii][Oo][Nn][[:space:]]+/, "", role)
            print role
            line = substr(line, RSTART + RLENGTH)
          }
        }
      '
      ;;
  esac
}

normalize_owner_role() {
  local role="$1"

  role="${role%;}"
  if [[ "${role}" == \"*\" ]]; then
    role="${role#\"}"
    role="${role%\"}"
    role="${role//\"\"/\"}"
  fi

  printf '%s\n' "${role}"
}

is_skipped_owner_role() {
  local role_upper

  role_upper="$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')"
  case "${role_upper}" in
    CURRENT_ROLE|CURRENT_USER|SESSION_USER|PG_DATABASE_OWNER|POSTGRES)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

precreate_plain_sql_owner_roles() {
  local backup="$1"
  local raw_role
  local role
  local role_lit
  local created=()
  local skipped=()
  local seen=$'\n'

  case "${backup}" in
    *.sql|*.sql.gz) ;;
    *) return 0 ;;
  esac

  while IFS= read -r raw_role; do
    role="$(normalize_owner_role "${raw_role}")"
    [[ -n "${role}" ]] || continue

    if [[ "${seen}" == *$'\n'"${role}"$'\n'* ]]; then
      continue
    fi
    seen+="${role}"$'\n'

    if is_skipped_owner_role "${role}"; then
      skipped+=("${role}")
      continue
    fi

    if [[ ! "${role}" =~ ^[[:alnum:]_][[:alnum:]_.$@+.-]{0,127}$ ]]; then
      skipped+=("${role}")
      continue
    fi

    role_lit="$(sql_literal "${role}")"
    psql_query "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ${role_lit}) THEN EXECUTE format('CREATE ROLE %I NOLOGIN', ${role_lit}); END IF; END \$\$;"
    created+=("${role}")
  done < <(extract_plain_sql_owner_roles "${backup}")

  if ((${#created[@]} > 0)); then
    OWNER_ROLES_CREATED="$(join_by_comma "${created[@]}")"
  else
    OWNER_ROLES_CREATED="none"
  fi

  if ((${#skipped[@]} > 0)); then
    OWNER_ROLES_SKIPPED="$(join_by_comma "${skipped[@]}")"
  else
    OWNER_ROLES_SKIPPED="none"
  fi
}

restore_backup() {
  local backup="$1"

  case "${backup}" in
    *.sql)
      docker exec \
        -e PGPASSWORD="${PGPASSWORD_REHEARSAL}" \
        "${CONTAINER_NAME}" \
        psql -v ON_ERROR_STOP=1 -U "${PGUSER_REHEARSAL}" -d "${DB_NAME}" -f "${RESTORE_MOUNT}"
      ;;
    *.sql.gz)
      docker exec \
        -e PGPASSWORD="${PGPASSWORD_REHEARSAL}" \
        "${CONTAINER_NAME}" \
        sh -c 'gzip -dc "$1" | psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
        sh "${RESTORE_MOUNT}"
      ;;
    *.dump)
      docker exec \
        -e PGPASSWORD="${PGPASSWORD_REHEARSAL}" \
        "${CONTAINER_NAME}" \
        pg_restore --no-owner --no-acl -U "${PGUSER_REHEARSAL}" -d "${DB_NAME}" "${RESTORE_MOUNT}"
      ;;
    *.dump.gz)
      docker exec \
        -e PGPASSWORD="${PGPASSWORD_REHEARSAL}" \
        "${CONTAINER_NAME}" \
        sh -c 'gzip -dc "$1" | pg_restore --no-owner --no-acl -U "$POSTGRES_USER" -d "$POSTGRES_DB"' \
        sh "${RESTORE_MOUNT}"
      ;;
    *)
      die "unsupported backup extension: ${backup}"
      ;;
  esac
}

table_exists() {
  local table="$1"
  psql_query "SELECT to_regclass('public.${table}') IS NOT NULL;"
}

table_count() {
  local table="$1"
  psql_query "SELECT COUNT(*) FROM public.${table};"
}

migration_version() {
  local goose_exists
  local schema_migrations_exists

  goose_exists="$(psql_query "SELECT to_regclass('public.goose_db_version') IS NOT NULL;")"
  if [[ "${goose_exists}" == "t" ]]; then
    psql_query "SELECT COALESCE(MAX(version_id) FILTER (WHERE is_applied), 0) FROM public.goose_db_version;"
    return
  fi

  schema_migrations_exists="$(psql_query "SELECT to_regclass('public.schema_migrations') IS NOT NULL;")"
  if [[ "${schema_migrations_exists}" == "t" ]]; then
    psql_query "SELECT COALESCE(MAX(version), 0) FROM public.schema_migrations;"
    return
  fi

  printf 'MISSING\n'
}

print_report_header() {
  local backup="$1"

  log "ClawGuard DB restore rehearsal"
  log "================================"
  log "backup path:     ${backup}"
  log "restore method:  temporary PostgreSQL container (${POSTGRES_IMAGE})"
  log "container name:  ${CONTAINER_NAME}"
  log "database name:   ${DB_NAME}"
  log "cleanup:         $([[ "${KEEP}" == "1" ]] && printf 'disabled (KEEP=1)' || printf 'enabled')"
  log ""
}

require_command docker

if [[ -z "${BACKUP_PATH}" ]]; then
  BACKUP_PATH="$(find_latest_backup "${BACKUP_DIR}")"
fi

[[ -n "${BACKUP_PATH}" ]] || die "no backup found; set BACKUP_PATH=/path/to/backup or place backups in ${BACKUP_DIR}"
[[ -f "${BACKUP_PATH}" ]] || die "backup file does not exist: ${BACKUP_PATH}"

BACKUP_PATH="$(abs_path "${BACKUP_PATH}")"

case "${DB_NAME}" in
  restore_rehearsal_*) ;;
  *) die "internal safety check failed: temporary database name lacks restore_rehearsal_ prefix" ;;
esac

case "${CONTAINER_NAME}" in
  restore_rehearsal_*) ;;
  *) die "internal safety check failed: temporary container name lacks restore_rehearsal_ prefix" ;;
esac

print_report_header "${BACKUP_PATH}"

log "Starting temporary PostgreSQL container..."
docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
docker run -d \
  --name "${CONTAINER_NAME}" \
  -e POSTGRES_DB="${DB_NAME}" \
  -e POSTGRES_USER="${PGUSER_REHEARSAL}" \
  -e POSTGRES_PASSWORD="${PGPASSWORD_REHEARSAL}" \
  -v "${BACKUP_PATH}:${RESTORE_MOUNT}:ro" \
  "${POSTGRES_IMAGE}" >/dev/null

if ! wait_for_postgres; then
  log "RESULT: FAIL"
  die "temporary PostgreSQL did not become ready"
fi

log "Restoring backup..."
precreate_plain_sql_owner_roles "${BACKUP_PATH}"
if [[ "${BACKUP_PATH}" == *.sql || "${BACKUP_PATH}" == *.sql.gz ]]; then
  log "owner roles:     created=${OWNER_ROLES_CREATED}; skipped=${OWNER_ROLES_SKIPPED}"
else
  log "owner roles:     skipped (pg_restore --no-owner --no-acl)"
fi
if restore_backup "${BACKUP_PATH}"; then
  RESTORE_OK=1
  log "restore:         OK"
else
  log "restore:         FAIL"
  log ""
  log "RESULT: FAIL"
  exit 1
fi

log ""
log "Validation"
log "----------"

version="$(migration_version || printf 'ERROR')"
if [[ "${version}" == "MISSING" || "${version}" == "ERROR" ]]; then
  log "migration:       WARN (${version})"
  VALIDATION_FAILED=1
else
  log "migration:       version ${version}"
fi

log "table counts:"
for table in "${TABLES[@]}"; do
  exists="$(table_exists "${table}" || printf 'ERROR')"
  if [[ "${exists}" == "t" ]]; then
    count="$(table_count "${table}" || printf 'ERROR')"
    if [[ "${count}" == "ERROR" ]]; then
      printf '  %-14s ERROR\n' "${table}"
      VALIDATION_FAILED=1
    else
      printf '  %-14s %s\n' "${table}" "${count}"
    fi
  elif [[ "${exists}" == "f" ]]; then
    printf '  %-14s MISSING (warning)\n' "${table}"
    VALIDATION_FAILED=1
  else
    printf '  %-14s ERROR\n' "${table}"
    VALIDATION_FAILED=1
  fi
done

log ""
if [[ "${RESTORE_OK}" == "1" && "${VALIDATION_FAILED}" == "0" ]]; then
  log "RESULT: PASS"
else
  log "RESULT: FAIL"
  exit 1
fi
