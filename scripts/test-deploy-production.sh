#!/usr/bin/env bash
# Hermetic release-script regression test; never calls a Docker daemon.
set -euo pipefail
unset CLAWGUARD_TAG COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_ENV_FILES COMPOSE_PROFILES || true
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/app/scripts" "$tmp/bin" "$tmp/state"
cp "$root/scripts/deploy-production.sh" "$tmp/app/scripts/"
printf '#!/bin/sh\nexit 0\n' >"$tmp/app/scripts/backup.sh"
printf '#!/bin/sh\nexit 0\n' >"$tmp/app/scripts/prod-smoke.sh"
chmod +x "$tmp/app/scripts/"*.sh
cat >"$tmp/app/docker-compose.yml" <<'YAML'
services:
  postgres:
    image: postgres:test
  bot:
    image: kelework/clawguard-bot:${CLAWGUARD_TAG}
    depends_on: [postgres]
  web:
    image: kelework/clawguard-web:${CLAWGUARD_TAG}
    depends_on: [bot]
YAML
cat >"$tmp/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$TEST_STATE/calls"
if [[ "$1" == compose && "$2" == config ]]; then
  shift 2
  exec "$REAL_DOCKER" compose config "$@"
fi
if [[ "$1" == compose && "$2" == ps ]]; then echo "${@: -1}"; exit; fi
if [[ "$1" == compose && "$2" == up ]]; then
  service="${@: -1}"; tag="$(sed -n 's/^CLAWGUARD_TAG=//p' .env)"
  case "$tag" in rollback-*) echo "old-$service" >"$TEST_STATE/$service" ;; *) echo "new-$service" >"$TEST_STATE/$service" ;; esac
  if [[ "${STALE_BOT:-0}" == 1 && "$service" == bot && "$tag" == sha-* ]]; then echo old-bot >"$TEST_STATE/bot"; fi
  if [[ "${FAIL_WEB_UP:-0}" == 1 && "$service" == web && "$tag" == sha-* ]]; then exit 1; fi
  exit
fi
if [[ "$1" == compose && "$2" == pull ]]; then exit; fi
if [[ "$1" == compose && "$2" == exec ]]; then echo https://example.test; exit; fi
if [[ "$1" == image && "$2" == tag ]]; then exit; fi
if [[ "$1" == image && "$2" == inspect ]]; then
  image="${@: -1}"
  case "$image" in *clawguard-bot:rollback-*) echo old-bot ;; *clawguard-web:rollback-*) echo old-web ;; *clawguard-bot:*) echo new-bot ;; *clawguard-web:*) echo new-web ;; *) exit 1 ;; esac
  exit
fi
if [[ "$1" == inspect ]]; then
  if [[ "$3" == *Health* ]]; then echo healthy; else cat "$TEST_STATE/${@: -1}"; fi
  exit
fi
exit 1
MOCK
cat >"$tmp/bin/curl" <<'MOCK'
#!/bin/sh
printf '%s\n' '{"status":"ready"}'
MOCK
chmod +x "$tmp/bin/docker" "$tmp/bin/curl"
REAL_DOCKER="$(command -v docker)"
export REAL_DOCKER PATH="$tmp/bin:$PATH" TEST_STATE="$tmp/state"
reset_case() {
  printf 'CLAWGUARD_TAG=sha-1111111\n' >"$tmp/app/.env"
  printf 'old-bot\n' >"$tmp/state/bot"; printf 'old-web\n' >"$tmp/state/web"
  : >"$tmp/state/calls"
  rm -f "$tmp/app/docker-compose.override.yml"
}
reset_case
(cd "$tmp/app" && bash scripts/deploy-production.sh sha-2222222) >"$tmp/success.log" 2>&1
grep -q '^deployed=sha-2222222$' "$tmp/success.log"
[[ "$(cat "$tmp/state/bot")" == new-bot && "$(cat "$tmp/state/web")" == new-web ]]
reset_case
cat >"$tmp/app/docker-compose.override.yml" <<'YAML'
services:
  bot:
    image: kelework/clawguard-bot:sha-1111111
  web:
    image: kelework/clawguard-web:sha-1111111
YAML
if (cd "$tmp/app" && bash scripts/deploy-production.sh sha-2222222) >"$tmp/override.log" 2>&1; then echo 'override incorrectly accepted' >&2; exit 1; fi
grep -q 'effective image mismatch' "$tmp/override.log"
! grep -q '^compose up ' "$tmp/state/calls"
grep -q '^CLAWGUARD_TAG=sha-1111111$' "$tmp/app/.env"
# Even an override pinned to the target cannot support automatic rollback.
sed -i 's/sha-1111111/sha-2222222/g' "$tmp/app/docker-compose.override.yml"
if (cd "$tmp/app" && bash scripts/deploy-production.sh sha-2222222) >"$tmp/pinned-target.log" 2>&1; then echo 'pinned target incorrectly accepted' >&2; exit 1; fi
grep -q 'effective image mismatch' "$tmp/pinned-target.log"
reset_case
if (cd "$tmp/app" && CLAWGUARD_TAG=sha-3333333 bash scripts/deploy-production.sh sha-2222222) >"$tmp/exported.log" 2>&1; then echo 'exported tag incorrectly accepted' >&2; exit 1; fi
grep -q 'effective image mismatch' "$tmp/exported.log"
! grep -q '^compose up ' "$tmp/state/calls"
reset_case
if (cd "$tmp/app" && STALE_BOT=1 bash scripts/deploy-production.sh sha-2222222) >"$tmp/stale.log" 2>&1; then echo 'stale container incorrectly accepted' >&2; exit 1; fi
grep -q 'running image mismatch for bot' "$tmp/stale.log"
[[ "$(cat "$tmp/state/bot")" == old-bot && "$(cat "$tmp/state/web")" == old-web ]]
reset_case
if (cd "$tmp/app" && FAIL_WEB_UP=1 bash scripts/deploy-production.sh sha-2222222) >"$tmp/rollback.log" 2>&1; then echo 'failed up incorrectly accepted' >&2; exit 1; fi
[[ "$(cat "$tmp/state/bot")" == old-bot && "$(cat "$tmp/state/web")" == old-web ]]
grep -q '^CLAWGUARD_TAG=rollback-' "$tmp/app/.env"
printf 'deploy regression tests passed\n'
