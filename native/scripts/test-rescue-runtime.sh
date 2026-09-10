#!/bin/sh
# True rescue image startup against disposable PG/Redis and TLS fake Telegram.
# --internal network + Docker DNS alias makes real Telegram/model access impossible.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
image=${RESCUE_IMAGE:-openbear/clawguard-guard-rescue:abe2089-assistant-off}
output=${1:?usage: test-rescue-runtime.sh NEW_EVIDENCE_DIRECTORY}
mkdir -p "$output"
output=$(CDPATH= cd -- "$output" && pwd)
name="cg-rescue-test-$$"
network="$name-net";pg="$name-pg";redis="$name-redis";tg="$name-tg";app="$name-app"
cleanup(){
 docker logs "$app" >"$output/app.log" 2>&1 || true
 docker logs "$tg" >"$output/fake-telegram.log" 2>&1 || true
 docker logs "$pg" >"$output/postgres.log" 2>&1 || true
 docker rm -f "$app" "$tg" "$redis" "$pg" >/dev/null 2>&1 || true
 docker network rm "$network" >/dev/null 2>&1 || true
 rm -f "$output/key.pem"
}
trap cleanup EXIT INT TERM
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout "$output/key.pem" -out "$output/cert.pem" -subj /CN=api.telegram.org -addext subjectAltName=DNS:api.telegram.org >/dev/null 2>&1
chmod 600 "$output/key.pem"
cp "$root/native/rescue/fake_telegram.py" "$output/fake_telegram.py"
docker network create --internal --label openbear-task=a5854b6b "$network" >/dev/null
docker run -d --name "$pg" --label openbear-task=a5854b6b --network "$network" --network-alias postgres --tmpfs /var/lib/postgresql/data -e POSTGRES_USER=cg_native_test -e POSTGRES_PASSWORD=isolated-test-only -e POSTGRES_DB=cg_native_test postgres:16-alpine >/dev/null
docker run -d --name "$redis" --label openbear-task=a5854b6b --network "$network" --network-alias redis redis:7-alpine >/dev/null
for i in $(seq 1 100); do
 if docker logs "$pg" 2>&1 | grep -q 'PostgreSQL init process complete' && docker exec "$pg" pg_isready -U cg_native_test >/dev/null 2>&1; then break; fi
 sleep .2
done
python3 - "$root/bot/migrations" >"$output/schema.sql" <<'PY'
from pathlib import Path
import sys
for p in sorted(Path(sys.argv[1]).glob('*.sql')):print(p.read_text().split('-- +goose Down')[0])
print("CREATE TABLE goose_db_version(version_id bigint,is_applied boolean); INSERT INTO goose_db_version VALUES(36,true);")
print("INSERT INTO group_assistant_policies(chat_id,chat_enabled,learning_enabled,mimic_target_user_id,proactive_cold_topic_enabled) VALUES(-123,true,true,258605875,true);")
print("INSERT INTO group_assistant_messages(chat_id,telegram_message_id,role,text,content_hash,expires_at) VALUES(-123,1,'user','retained old source','hash',now()-interval '1 day');")
PY
docker exec -i "$pg" psql -v ON_ERROR_STOP=1 -U cg_native_test -d cg_native_test <"$output/schema.sql" >"$output/schema.log"
docker run -d --name "$tg" --label openbear-task=a5854b6b --network "$network" --network-alias api.telegram.org -v "$output:/test:ro" python:3.12.14-slim-bookworm python /test/fake_telegram.py >/dev/null
docker run -d --name "$app" --label openbear-task=a5854b6b --network "$network" \
 --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
 -v "$output/cert.pem:/test-cert.pem:ro" --entrypoint /usr/local/bin/clawguard \
 -e SSL_CERT_FILE=/test-cert.pem -e APP_ENV=development -e HTTP_PORT=8080 \
 -e BOT_TOKEN=999:isolated-test-only -e BOT_USERNAME=rescue_test_bot -e TELEGRAM_LOGIN_BOT_USERNAME=rescue_test_bot \
 -e WEBHOOK_SECRET=local-webhook -e PUBLIC_BASE_URL=https://rescue.invalid -e DAILY_REPORT_ENABLED=false \
 -e POSTGRES_HOST=postgres -e POSTGRES_DB=cg_native_test -e POSTGRES_USER=cg_native_test -e POSTGRES_PASSWORD=isolated-test-only \
 -e REDIS_HOST=redis -e JWT_SECRET=local-jwt-not-production -e ENCRYPTION_KEY=local-encryption-not-production \
 "$image" >/dev/null
ready=0
for i in $(seq 1 80); do
 if docker exec "$app" wget -q -O- http://127.0.0.1:8080/readyz >"$output/ready.json" 2>/dev/null; then ready=1;break;fi
 if [ "$(docker inspect --format '{{.State.Running}}' "$app")" != true ]; then break;fi
 sleep .25
done
if [ "$ready" != 1 ]; then docker logs "$app"; exit 1;fi
docker exec "$app" wget -q -O- http://127.0.0.1:8080/healthz >"$output/health.json"
docker logs "$tg" >"$output/fake-telegram.log"
if grep -q UNEXPECTED_TELEGRAM_METHOD "$output/fake-telegram.log"; then exit 1; fi
docker inspect --format '{{json .Mounts}}' "$app" >"$output/mounts.json"
docker exec "$pg" psql -tA -U cg_native_test -d cg_native_test -c "SELECT json_build_object('schema',max(version_id),'applied',bool_and(is_applied)) FROM goose_db_version" >"$output/schema-state.json"
docker exec "$pg" psql -tA -U cg_native_test -d cg_native_test -c "SELECT json_build_object('policies',(SELECT count(*) FROM group_assistant_policies WHERE chat_enabled AND learning_enabled AND mimic_target_user_id=258605875),'originals',(SELECT count(*) FROM group_assistant_messages),'new_facts',(SELECT count(*) FROM group_assistant_memories),'stickers',(SELECT count(*) FROM group_assistant_sticker_samples))" >"$output/assistant-state.json"
python3 - "$output" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1])
assert json.loads((p/'ready.json').read_text())['status']=='ready'
assert json.loads((p/'schema-state.json').read_text())=={'schema':36,'applied':True}
assert json.loads((p/'assistant-state.json').read_text())=={'policies':1,'originals':1,'new_facts':0,'stickers':0}
mounts=json.loads((p/'mounts.json').read_text());assert len(mounts)==1 and mounts[0]['Destination']=='/test-cert.pem'
print('PASS: old guard-only image healthy on schema36, old assistant flags untouched, no native domain mounted, no new facts/stickers')
PY
