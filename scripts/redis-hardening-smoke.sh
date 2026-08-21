#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="$(mktemp -d "${RUNNER_TEMP:-/tmp}/clawguard-redis-hardening.XXXXXX")"
PROJECTS=()

cleanup() {
  local project
  for project in "${PROJECTS[@]}"; do
    docker rm -f "${project}-legacy" >/dev/null 2>&1 || true
    docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" down -v --remove-orphans >/dev/null 2>&1 || true
    docker volume rm "${project}_redisdata" >/dev/null 2>&1 || true
  done
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

cp "$ROOT_DIR/docker-compose.yml" "$WORK_DIR/docker-compose.yml"
cat >"$WORK_DIR/.env" <<'EOF'
POSTGRES_DB=clawguard
POSTGRES_USER=clawguard
POSTGRES_PASSWORD=dummy
POSTGRES_HOST=postgres
REDIS_PASSWORD=dummy-password
REDIS_HOST=redis
BOT_USERNAME=dummy
TURNSTILE_SITE_KEY=dummy
CLAWGUARD_TAG=sha-deadbee
EOF

docker compose -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" config --quiet

wait_legacy() {
  local container=$1
  for _ in $(seq 1 30); do
    if docker exec "$container" redis-cli ping </dev/null >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  docker logs "$container" >&2 || true
  return 1
}

wait_hardened() {
  local project=$1 id state health
  for _ in $(seq 1 60); do
    id="$(docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" ps -q redis)"
    if [[ -n "$id" ]]; then
      state="$(docker inspect -f '{{.State.Status}}' "$id")"
      health="$(docker inspect -f '{{.State.Health.Status}}' "$id")"
      if [[ "$state" == running && "$health" == healthy ]]; then
        return 0
      fi
    fi
    sleep 1
  done
  docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" ps >&2 || true
  docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" logs --tail 120 redis >&2 || true
  return 1
}

run_case() {
  local kind project volume legacy id value uid cap
  kind=$1
  project="cg-redis-${kind}-${RANDOM}"
  project="${project,,}"
  volume="${project}_redisdata"
  legacy="${project}-legacy"
  PROJECTS+=("$project")

  docker volume create "$volume" >/dev/null
  docker run -d --name "$legacy" -v "$volume:/data" --entrypoint sh redis:7-alpine -ec 'exec redis-server --appendonly no' >/dev/null
  wait_legacy "$legacy"
  docker exec "$legacy" redis-cli set compatibility-key "${kind}-preserved" </dev/null >/dev/null

  if [[ "$kind" == aof ]]; then
    docker exec "$legacy" redis-cli config set appendonly yes </dev/null >/dev/null
    for _ in $(seq 1 60); do
      if docker exec "$legacy" sh -ec 'test -f /data/appendonlydir/appendonly.aof.manifest && redis-cli INFO persistence | grep -q "aof_rewrite_in_progress:0" && redis-cli INFO persistence | grep -q "aof_last_bgrewrite_status:ok"' </dev/null; then
        break
      fi
      sleep 1
    done
    docker exec "$legacy" test -f /data/appendonlydir/appendonly.aof.manifest </dev/null
  else
    docker exec "$legacy" redis-cli save </dev/null >/dev/null
    docker exec "$legacy" test -f /data/dump.rdb </dev/null
  fi
  docker rm -f "$legacy" >/dev/null

  docker run --rm --read-only -v "$volume:/data:ro" --entrypoint sh redis:7-alpine -ec 'test -n "$(find /data -user 0 -print -quit)"' </dev/null
  docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" up -d redis
  wait_hardened "$project"

  id="$(docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" ps -q redis)"
  value="$(docker exec -e REDISCLI_AUTH=dummy-password "$id" redis-cli get compatibility-key </dev/null)"
  uid="$(docker exec "$id" awk '/^Uid:/ {print $2}' /proc/1/status </dev/null)"
  cap="$(docker exec "$id" awk '/^CapEff:/ {print $2}' /proc/1/status </dev/null)"
  [[ "$value" == "${kind}-preserved" ]]
  [[ "$uid" == 999 ]]
  [[ "$cap" == 0000000000000000 ]]
  docker exec -e REDISCLI_AUTH=dummy-password "$id" sh -ec 'redis-cli INFO persistence | grep -q "aof_enabled:1"' </dev/null
  docker run --rm --read-only -v "$volume:/data:ro" --entrypoint sh redis:7-alpine -ec 'test -z "$(find /data ! -user 999 -print -quit)"' </dev/null

  docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" restart redis
  wait_hardened "$project"
  id="$(docker compose -p "$project" -f "$WORK_DIR/docker-compose.yml" --env-file "$WORK_DIR/.env" ps -q redis)"
  value="$(docker exec -e REDISCLI_AUTH=dummy-password "$id" redis-cli get compatibility-key </dev/null)"
  uid="$(docker exec "$id" awk '/^Uid:/ {print $2}' /proc/1/status </dev/null)"
  cap="$(docker exec "$id" awk '/^CapEff:/ {print $2}' /proc/1/status </dev/null)"
  [[ "$value" == "${kind}-preserved" ]]
  [[ "$uid" == 999 ]]
  [[ "$cap" == 0000000000000000 ]]
  printf '%s migration: value=%s uid=%s cap_eff=%s\n' "$kind" "$value" "$uid" "$cap"
}

run_case rdb
run_case aof
printf 'redis_hardening_smoke=ok\n'
