#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! "$1" =~ ^sha-[0-9a-f]{7,40}$ ]]; then
  echo "usage: $0 sha-<git-commit>" >&2
  exit 2
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NEW_TAG="$1"
ENV_FILE="${ROOT_DIR}/.env"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
ENV_BACKUP="${ENV_FILE}.before-${STAMP}"
ROLLBACK_TAG="rollback-${STAMP}"
DEPLOYED=0

cd "$ROOT_DIR"
test -f "$ENV_FILE"
cp -p "$ENV_FILE" "$ENV_BACKUP"

BOT_CONTAINER="$(docker compose ps -q bot)"
WEB_CONTAINER="$(docker compose ps -q web)"
test -n "$BOT_CONTAINER"
test -n "$WEB_CONTAINER"
BOT_IMAGE_ID="$(docker inspect -f '{{.Image}}' "$BOT_CONTAINER")"
WEB_IMAGE_ID="$(docker inspect -f '{{.Image}}' "$WEB_CONTAINER")"
docker image tag "$BOT_IMAGE_ID" "kelework/clawguard-bot:${ROLLBACK_TAG}"
docker image tag "$WEB_IMAGE_ID" "kelework/clawguard-web:${ROLLBACK_TAG}"

set_env_value() {
  local key="$1"
  local value="$2"
  local temp
  temp="$(mktemp "${ENV_FILE}.XXXXXX")"
  awk -v key="$key" -v value="$value" '
    BEGIN { updated = 0 }
    index($0, key "=") == 1 { print key "=" value; updated = 1; next }
    { print }
    END { if (!updated) print key "=" value }
  ' "$ENV_FILE" >"$temp"
  chmod --reference="$ENV_FILE" "$temp"
  chown --reference="$ENV_FILE" "$temp"
  mv "$temp" "$ENV_FILE"
}

set_env_tag() {
  set_env_value CLAWGUARD_TAG "$1"
}

rollback() {
  local rollback_failed=0
  local bot_ok web_ok _

  echo "deployment failed; restoring previous compose configuration" >&2

  if ! cp -p "$ENV_BACKUP" "$ENV_FILE"; then
    echo "rollback: failed to restore .env from ${ENV_BACKUP}" >&2
    rollback_failed=1
  fi

  if ! set_env_tag "$ROLLBACK_TAG"; then
    echo "rollback: failed to set CLAWGUARD_TAG=${ROLLBACK_TAG}" >&2
    rollback_failed=1
  fi

  if ! docker compose up -d --no-deps bot; then
    echo "rollback: failed to start bot" >&2
    rollback_failed=1
  else
    bot_ok=0
    for _ in $(seq 1 36); do
      if curl --fail --silent http://127.0.0.1:8080/readyz | grep -q '"status":"ready"'; then
        bot_ok=1
        break
      fi
      sleep 5
    done
    if [[ "$bot_ok" -ne 1 ]]; then
      echo "rollback: bot readyz check failed" >&2
      rollback_failed=1
    fi
  fi

  if ! docker compose up -d --no-deps web; then
    echo "rollback: failed to start web" >&2
    rollback_failed=1
  else
    web_ok=0
    for _ in $(seq 1 36); do
      if web_is_healthy; then
        web_ok=1
        break
      fi
      sleep 5
    done
    if [[ "$web_ok" -ne 1 ]]; then
      echo "rollback: web health check failed" >&2
      rollback_failed=1
    fi
  fi

  if [[ "$rollback_failed" -ne 0 ]]; then
    return 1
  fi
  return 0
}

finish() {
  local status=$?
  trap - EXIT
  if [[ "$status" -ne 0 && "$DEPLOYED" -ne 1 ]]; then
    if ! rollback; then
      echo "automatic rollback failed; manual recovery is required" >&2
    fi
  fi
  exit "$status"
}

web_is_healthy() {
  local container
  container="$(docker compose ps -q web)"
  [[ -n "$container" ]] &&
    [[ "$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$container")" == "healthy" ]]
}

trap finish EXIT

"${ROOT_DIR}/scripts/backup.sh"
if [[ "${ROTATE_WEBHOOK_SECRET:-0}" == "1" ]]; then
  set_env_value WEBHOOK_SECRET "$(openssl rand -hex 32)"
fi
set_env_tag "$NEW_TAG"
docker compose pull bot web
docker compose up -d --no-deps bot

for _ in $(seq 1 36); do
  if curl --fail --silent http://127.0.0.1:8080/readyz | grep -q '"status":"ready"'; then
    break
  fi
  sleep 5
done
curl --fail --silent http://127.0.0.1:8080/readyz | grep -q '"status":"ready"'

docker compose up -d --no-deps web

for _ in $(seq 1 36); do
  if web_is_healthy; then
    break
  fi
  sleep 5
done

curl --fail --silent http://127.0.0.1:8080/readyz | grep -q '"status":"ready"'
web_is_healthy
PUBLIC_URL="$(docker compose exec -T bot sh -ec 'printf %s "$PUBLIC_BASE_URL"')"
EXPECTED_REVISION="${NEW_TAG#sha-}" "${ROOT_DIR}/scripts/prod-smoke.sh" --url "$PUBLIC_URL"

DEPLOYED=1
rm -f "$ENV_BACKUP"
echo "deployed=${NEW_TAG}"
