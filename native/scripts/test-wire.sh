#!/bin/sh
# Isolated local validation only. Never use a deployment database or real API.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
if [ -z "${GO_BINARY:-}" ]; then
  if [ -x /root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go ]; then
    GO_BINARY=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
  else
    GO_BINARY=go
  fi
fi
name="cg-native-test-$$"
docker run -d --name "$name" --label openbear-purpose=native-isolated-test \
  -e POSTGRES_USER=cg_native_test -e POSTGRES_PASSWORD=isolated-test-only \
  -e POSTGRES_DB=cg_native_test -p 127.0.0.1::5432 \
  --tmpfs /var/lib/postgresql/data postgres:16-alpine >/dev/null
trap 'docker rm -f "$name" >/dev/null' EXIT INT TERM
port=$(docker port "$name" 5432/tcp | sed 's/.*://')
for i in $(seq 1 50); do
  if docker exec "$name" pg_isready -U cg_native_test >/dev/null 2>&1; then break; fi
  sleep 0.2
done
export CG_NATIVE_TEST_DATABASE_URL="postgres://cg_native_test:isolated-test-only@127.0.0.1:$port/cg_native_test?sslmode=disable"
cd "$root/bot"
"$GO_BINARY" test ./internal/bot -run 'TestNativeAssistant(RealGoPythonSQLAndTG|ActivationLiveGoPython|InboundMediaGoPython|PoolMaxTokensOverlayGoPython|ControlTrueReadWriteAndLegacyFreeze)$' -v -count=1
"$GO_BINARY" test ./internal/api -run 'TestNativeAssistantHTTPRequiresJWTAndGlobalAdmin|TestAssistantVerificationAPINoJWT|TestAssistantGlobalVerificationAPINoJWT|TestAssistantVerificationAPIOutOfScopeAllRoutes' -count=1
