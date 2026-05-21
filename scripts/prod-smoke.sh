#!/usr/bin/env bash
set -u

APP_DIR=${APP_DIR:-/root/clawguard}
COMPOSE_FILE=docker-compose.yml
SINCE=5m
WEBHOOK_LOG_WINDOW=24h
PUBLIC_URL=https://rfcguard.misaka.si
EXPECTED_DOMAIN=rfcguard.misaka.si
SERVICES="bot web postgres redis caddy"

failures=0

usage() {
  cat <<'EOF'
Usage: scripts/prod-smoke.sh [--since 5m] [--webhook-log-window 24h] [--url https://rfcguard.misaka.si] [--compose docker-compose.yml]

Runs production smoke checks from /root/clawguard by default. Override the
directory with APP_DIR=/path/to/clawguard.
EOF
}

log() {
  printf '%s\n' "$*"
}

pass() {
  log "PASS: $*"
}

fail() {
  failures=$((failures + 1))
  log "FAIL: $*"
}

warn() {
  log "WARN: $*"
}

have() {
  command -v "$1" >/dev/null 2>&1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --since)
      if [ "$#" -lt 2 ]; then
        log "Missing value for --since"
        usage
        exit 2
      fi
      SINCE=$2
      shift 2
      ;;
    --webhook-log-window)
      if [ "$#" -lt 2 ]; then
        log "Missing value for --webhook-log-window"
        usage
        exit 2
      fi
      WEBHOOK_LOG_WINDOW=$2
      shift 2
      ;;
    --url)
      if [ "$#" -lt 2 ]; then
        log "Missing value for --url"
        usage
        exit 2
      fi
      PUBLIC_URL=$2
      shift 2
      ;;
    --compose)
      if [ "$#" -lt 2 ]; then
        log "Missing value for --compose"
        usage
        exit 2
      fi
      COMPOSE_FILE=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      log "Unknown argument: $1"
      usage
      exit 2
      ;;
  esac
done

case "$PUBLIC_URL" in
  https://*) url_host=${PUBLIC_URL#https://} ;;
  http://*) url_host=${PUBLIC_URL#http://} ;;
  *) url_host=$PUBLIC_URL ;;
esac
url_host=${url_host%%/*}
url_host=${url_host%%:*}

log "ClawGuard production smoke"
log "app_dir=$APP_DIR"
log "compose=$COMPOSE_FILE"
log "since=$SINCE"
log "webhook_log_window=$WEBHOOK_LOG_WINDOW"
log "url=$PUBLIC_URL"
log ""

if [ ! -d "$APP_DIR" ]; then
  fail "application directory not found: $APP_DIR"
else
  cd "$APP_DIR" || exit 1
fi

if [ ! -f "$COMPOSE_FILE" ]; then
  fail "compose file not found: $COMPOSE_FILE"
fi

if docker compose version >/dev/null 2>&1; then
  compose() {
    docker compose -f "$COMPOSE_FILE" "$@"
  }
elif have docker-compose; then
  compose() {
    docker-compose -f "$COMPOSE_FILE" "$@"
  }
else
  fail "docker compose is not available"
  compose() {
    return 127
  }
fi

check_services() {
  log "== Compose services =="
  for service in $SERVICES; do
    cid=$(compose ps -q "$service" 2>/dev/null)
    if [ -z "$cid" ]; then
      fail "$service has no running container"
      continue
    fi

    state=$(docker inspect -f '{{.State.Status}}' "$cid" 2>/dev/null || true)
    health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null || true)
    image=$(docker inspect -f '{{.Config.Image}}' "$cid" 2>/dev/null || true)

    if [ "$state" = "running" ] && { [ "$health" = "healthy" ] || [ "$health" = "none" ]; }; then
      pass "$service status=$state health=$health image=$image"
    else
      fail "$service status=$state health=$health image=$image"
    fi
  done
  log ""
}

check_bot_logs() {
  log "== Bot logs =="
  bot_logs=$(compose logs --since "$SINCE" bot 2>&1 || true)
  if [ -z "$bot_logs" ]; then
    warn "no bot logs found in the last $SINCE"
  fi

  ignored_transient=$(printf '%s\n' "$bot_logs" \
    | grep -Eai 'llm probe transient failure \(not yet unhealthy\)|cas lookup failed, skip|ai call failed, trying next model|retention cleanup' || true)
  ignored_transient_count=$(printf '%s\n' "$ignored_transient" | awk 'NF {count++} END {print count + 0}')

  errors=$(printf '%s\n' "$bot_logs" \
    | grep -Eai '(^|[^[:alnum:]_])level=error([^[:alnum:]_]|$)|"level"[[:space:]]*:[[:space:]]*"error"|(^|[^[:alnum:]_])panic([^[:alnum:]_]|$)|no rows in result set|telegram handler error|(^|[^[:alnum:]_])fatal([^[:alnum:]_]|$)|clawguard stopped|process exit' \
    | grep -Eavi 'llm probe transient failure \(not yet unhealthy\)|cas lookup failed, skip|ai call failed, trying next model|retention cleanup' || true)

  if [ -n "$errors" ]; then
    fail "bot logs contain fatal/error lines in the last $SINCE"
    printf '%s\n' "$errors" | tail -n 20
  else
    pass "bot logs have no fatal/error lines in the last $SINCE"
    if [ "$ignored_transient_count" -gt 0 ]; then
      warn "ignored transient/fallback bot log lines in the last $SINCE: $ignored_transient_count"
    fi
  fi

  webhook_lines=$(printf '%s\n' "$bot_logs" | grep -Eai 'webhook.*registered|registered.*webhook' || true)
  webhook_match=$(printf '%s\n' "$webhook_lines" | grep -F "$EXPECTED_DOMAIN" || true)
  if [ -n "$webhook_match" ]; then
    pass "webhook registered log includes $EXPECTED_DOMAIN"
    printf '%s\n' "$webhook_match" | tail -n 3
  else
    webhook_window_logs=$(compose logs --since "$WEBHOOK_LOG_WINDOW" bot 2>&1 || true)
    webhook_window_lines=$(printf '%s\n' "$webhook_window_logs" | grep -Eai 'webhook.*registered|registered.*webhook' || true)
    webhook_window_match=$(printf '%s\n' "$webhook_window_lines" | grep -F "$EXPECTED_DOMAIN" || true)
    if [ -n "$webhook_window_match" ]; then
      pass "webhook registered log includes $EXPECTED_DOMAIN within $WEBHOOK_LOG_WINDOW"
      warn "webhook registration was not found in the last $SINCE; bot may not have restarted recently"
      printf '%s\n' "$webhook_window_match" | tail -n 3
    else
      fail "webhook registered log with domain $EXPECTED_DOMAIN not found in the last $SINCE or $WEBHOOK_LOG_WINDOW"
      if [ -n "$webhook_lines" ]; then
        log "Recent webhook registration lines:"
        printf '%s\n' "$webhook_lines" | tail -n 5
      fi
      if [ -n "$webhook_window_lines" ]; then
        log "Webhook registration lines within $WEBHOOK_LOG_WINDOW:"
        printf '%s\n' "$webhook_window_lines" | tail -n 5
      fi
    fi
  fi
  log ""
}

check_public_url() {
  log "== Public URL =="
  if [ "$url_host" != "$EXPECTED_DOMAIN" ]; then
    fail "public URL host is $url_host, expected $EXPECTED_DOMAIN"
  else
    pass "public URL host is $EXPECTED_DOMAIN"
  fi

  if ! have curl; then
    fail "curl is not available"
    log ""
    return
  fi

  tmp_headers=$(mktemp)
  tmp_error=$(mktemp)
  http_code=$(curl -sS -L -o /dev/null -D "$tmp_headers" -w '%{http_code}' --max-time 20 "$PUBLIC_URL" 2>"$tmp_error" || true)
  curl_error=$(cat "$tmp_error" 2>/dev/null || true)
  rm -f "$tmp_error"

  if [ "$http_code" -ge 200 ] 2>/dev/null && [ "$http_code" -lt 400 ] 2>/dev/null; then
    pass "$PUBLIC_URL returned HTTP $http_code"
  else
    fail "$PUBLIC_URL returned HTTP ${http_code:-none}${curl_error:+ ($curl_error)}"
  fi

  for header in x-frame-options x-content-type-options referrer-policy strict-transport-security; do
    value=$(awk -v h="$header" '{line=$0; sub(/\r$/, "", line); lower=tolower(line); if (index(lower, h ":") == 1) {print line; found=1}} END{if (!found) exit 1}' "$tmp_headers" 2>/dev/null || true)
    if [ -n "$value" ]; then
      pass "header present: $value"
    else
      fail "header missing: $header"
    fi
  done
  rm -f "$tmp_headers"
  log ""
}

print_image_revision() {
  log "== Image revisions =="
  for service in $SERVICES; do
    cid=$(compose ps -q "$service" 2>/dev/null)
    if [ -z "$cid" ]; then
      fail "$service image revision unavailable because container is missing"
      continue
    fi

    config_image=$(docker inspect -f '{{.Config.Image}}' "$cid" 2>/dev/null || true)
    image_id=$(docker inspect -f '{{.Image}}' "$cid" 2>/dev/null || true)
    repo_digests=$(docker image inspect -f '{{range .RepoDigests}}{{println .}}{{end}}' "$image_id" 2>/dev/null || true)
    revision=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image_id" 2>/dev/null || true)
    source=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.source"}}' "$image_id" 2>/dev/null || true)
    version=$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.version"}}' "$image_id" 2>/dev/null || true)

    log "$service:"
    log "  image: ${config_image:-unknown}"
    log "  image_id: ${image_id:-unknown}"
    if [ -n "$repo_digests" ]; then
      printf '%s\n' "$repo_digests" | sed 's/^/  repo_digest: /'
    else
      warn "$service repo digest unavailable locally"
    fi
    log "  oci_revision: ${revision:-unavailable}"
    log "  oci_source: ${source:-unavailable}"
    log "  oci_version: ${version:-unavailable}"
  done
  log ""

  if have git && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    current_sha=$(git rev-parse HEAD 2>/dev/null || true)
    log "git_sha: ${current_sha:-unavailable}"
    log ""
  else
    warn "git sha unavailable: $APP_DIR is not a git worktree"
    log ""
  fi
}

check_services
check_bot_logs
check_public_url
print_image_revision

if [ "$failures" -eq 0 ]; then
  log "OVERALL: PASS"
  exit 0
fi

log "OVERALL: FAIL ($failures failed checks)"
exit 1
