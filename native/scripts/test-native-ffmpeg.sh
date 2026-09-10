#!/bin/sh
# Decisive local check: the accepted native image can transcode default ogg/opus
# with ffmpeg as the pinned SGB TTS path does. Isolated; no real TTS/Telegram.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
image="${NATIVE_IMAGE:-openbear/clawguard-assistant-native:local-acceptance}"
stamp=$(date +%Y%m%d-%H%M)
evidence="${1:-/opt/openbear/workspace/artifacts/clawguard-wiki-sgb-exact-20260910/native-implementation/evidence/native-ffmpeg-ogg-$stamp.log}"
mkdir -p "$(dirname "$evidence")"
{
  echo "image=$image"
  docker image inspect "$image" --format '{{.Id}} {{index .RepoTags 0}}'
  docker run --rm --user 10001:10001 --entrypoint ffmpeg "$image" -version
  docker run --rm --user 10001:10001 --entrypoint python "$image" -c '
import asyncio
from pathlib import Path
from bot.services.doubao_tts import DoubaoTTSService

async def main():
    mp3 = Path("/tmp/fixture.mp3")
    proc = await asyncio.create_subprocess_exec(
        "ffmpeg","-hide_banner","-loglevel","error","-y","-f","lavfi","-i","anullsrc=r=24000:cl=mono","-t","0.2","-q:a","9",str(mp3),
        stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
    _, err = await proc.communicate()
    if proc.returncode != 0:
        raise SystemExit(err.decode() or "mp3 fixture failed")
    ogg = await DoubaoTTSService._convert_mp3_to_ogg_opus_unbounded(mp3.read_bytes())
    if len(ogg) < 32 or ogg[:4] != b"OggS":
        raise SystemExit("ogg magic missing len=%s" % len(ogg))
    print("ogg_opus_bytes=%s magic=OggS" % len(ogg))

asyncio.run(main())
'
} | tee "$evidence"
echo "ffmpeg evidence: $evidence"
