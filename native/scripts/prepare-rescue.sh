#!/bin/sh
# Prepare an OLD guard-only rescue image source without checking out or changing
# the active worktree. No production, credentials, DB writes or network requests.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
base=abe20897450fa46d1693aff476e6a6c15ca9d1f5
dest=${1:?usage: prepare-rescue.sh NEW_DESTINATION}
if [ -e "$dest" ]; then echo 'refusing existing destination' >&2; exit 1; fi
mkdir -p "$dest"
git -C "$root" cat-file -e "$base^{commit}"
git -C "$root" archive "$base" bot | tar -x -C "$dest"
python3 - "$dest" "$base" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1]);changes={}
patches={
 'bot/internal/bot/group_assistant.go':(
  'func NewGroupAssistant(service *Service) *GroupAssistant {\n',
  'func NewGroupAssistant(service *Service) *GroupAssistant {\n\t// Rescue build: old moderation runs, but no assistant is constructed.\n\t// This is unconditional for real Service instances; no config can re-enable it.\n\tif service != nil { return nil }\n'),
 'bot/internal/api/routes_admin.go':(
  '\ts.registerGroupAssistantRoutes(admin)\n\ts.registerAssistantGlobalRoutes(admin)\n',
  '\t// Rescue build: assistant APIs are intentionally unavailable.\n'),
}
for name,(old,new) in patches.items():
 path=root/name;original=path.read_text()
 if original.count(old)!=1:raise RuntimeError('fixed rescue baseline mismatch: '+name)
 changed=original.replace(old,new);path.write_text(changed)
 changes[name]={'before':hashlib.sha256(original.encode()).hexdigest(),'after':hashlib.sha256(changed.encode()).hexdigest()}
manifest={'baseline':sys.argv[2],'purpose':'old ClawGuard moderation only; assistant construction and APIs disabled','changes':changes,'entrypoint':'/usr/local/bin/clawguard','skip_migration_reason':'retain additive PG schema36; never downgrade live data'}
(root/'rescue-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps(manifest,indent=2))
PY
# Tests are supplied beside the preparation script, never added to baseline Git.
cp "$root/native/rescue/assistant_off_test.go" "$dest/bot/internal/bot/rescue_assistant_off_test.go"
cp "$root/native/rescue/api_off_test.go" "$dest/bot/internal/api/rescue_assistant_off_test.go"
