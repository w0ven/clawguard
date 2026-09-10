FROM python:3.12.14-slim-bookworm

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LITELLM_MODE=PRODUCTION \
    LITELLM_LOCAL_MODEL_COST_MAP=True \
    TZ=Asia/Shanghai \
    UV_PYTHON_DOWNLOADS=never \
    PATH=/app/.venv/bin:$PATH \
    XDG_CACHE_HOME=/tmp/native-cache \
    TIKTOKEN_CACHE_DIR=/tmp/tiktoken-cache

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir uv==0.12.9 \
    && groupadd --gid 10001 native \
    && useradd --uid 10001 --gid 10001 --no-create-home native \
    && mkdir /data && chown 10001:10001 /data
WORKDIR /app
COPY pyproject.toml uv.lock ./
COPY vendor ./vendor
RUN uv sync --frozen --no-dev
COPY clawguard_native ./clawguard_native
COPY knowledge ./knowledge
COPY MIGRATION.md ./MIGRATION.md

LABEL clawguard.sgb-source="82c3703daba218b36255132c9bf51ebc444c6480"
EXPOSE 8091
USER 10001:10001
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=4 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8091/healthz',timeout=4)"
CMD ["python", "-m", "clawguard_native.app"]
