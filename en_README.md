# ClawGuard

ClawGuard is a Telegram group-management system with join verification, join-flood protection, rule-based filtering, CAS integration, AI moderation, a user-trust state machine, scheduled messages, and a Web administration console.

> [Chinese documentation](./README.md)

## Current Status

The current schema is goose migration `00031`.

| Area | Status |
|---|---|
| Database schema | goose migration `00031` |
| Production topology | One bot, one web service, PostgreSQL 16, Redis 7 AOF, and Caddy 2 |
| Delivery | GitHub Actions tests and publishes bot/web images; production is upgraded in place with Docker Compose |
| Backend CI | Unit tests, vet, race, govulncheck, and a real PostgreSQL migration smoke test |
| Frontend CI | TypeScript, production build, npm audit, and desktop/mobile Playwright smoke tests |
| Join-flood protection | Enabled by default, Redis atomic state, leased database jobs, and a Redis cleanup fallback |
| AI moderation | Feature-complete but disabled by default until providers and models are configured |

The current deployment is designed for **one bot replica**. Join-protection state is shared through Redis, but scheduled messages, daily reports, and some periodic jobs do not yet use distributed leader election. Do not scale the bot horizontally without adding that coordination.

## Main Features

### Join Verification

- Button, arithmetic, image arithmetic, emoji-choice, and Cloudflare Turnstile challenges.
- Per-group timeout, failure action, join-message cleanup, and welcome-message settings.
- Optional CAS and Bio keyword/AI checks before verification. Failed image-arithmetic challenges delete the original photo so a stale puzzle does not look active.
- When AdKiller is enabled, bios are scored before keyword/AI review; confirmed ads follow the configured score bands.
- PostgreSQL-backed verification jobs with due times, leases, retries, and last-error tracking.
- Pending verification and cleanup jobs survive service restarts.
- The bot automatically rejects unauthorized groups; owners manage authorization in the Web console.

### Join-Flood Protection

The flood guard handles bursts of incoming accounts. It does not perform bulk actions against existing group members.

| Setting | Default | Meaning |
|---|---:|---|
| `join_threshold` | 20 | Joins required to trigger protection |
| `join_window_seconds` | 60 seconds | Sliding join window |
| `protection_duration_seconds` | 900 seconds | Protection remains active for 15 minutes |
| `temporary_ban_seconds` | 3600 seconds | New accounts are temporarily banned for 60 minutes |
| `admin_notify_interval_seconds` | 300 seconds | Admin summary notification throttle |
| `max_pending_verifications` | 30 | Protect before verification work reaches this limit |
| `telegram_failure_cooldown_seconds` | 300 seconds | Base cooldown after Telegram cleanup failures |

Normal joins follow the configured verification flow. Once a threshold is reached, incoming accounts are handled temporarily without generating challenges, calling CAS/Bio/AI, or posting one notification per user. Normal verification resumes automatically when the protection interval ends.

Operational behavior:

- Redis Lua scripts atomically trim the window, count joins, trigger protection, increment intercepted totals, and throttle notifications.
- Redis stores the active deadline, trigger, counters, notification timestamps, and Telegram cleanup breaker state.
- If Redis is unavailable, the bot falls back to single-process memory state. A database count failure enters protection fail-safe.
- Failed Telegram actions are persisted to leased PostgreSQL jobs; if PostgreSQL also fails, a persistent Redis cleanup queue is used.
- A 15-second worker handles recovery notifications, database cleanup jobs, and Redis fallback tasks.
- The Web status card exposes protection, pending verification, cooldown, error, and deferred-task state.

A first-time joiner cannot already be trusted. The backend only retains an anti-false-positive bypass for users who previously graduated in the same group and later rejoin.

### Rules and Anti-Spam

- Keyword, regex, username, and link filtering with configurable actions and allowlists.
- Restrictions for ungraduated users: links, forwards, media, invitations, and message rate.
- Configurable handling for non-text messages, sender chats, other bots, and bot inviters.
- CAS synchronization and join checks.
- Warning accumulation, decay, and escalation.
- Edited messages, via-bot content, external replies, and t.me preview context.
- Keyword replies with fuzzy, exact, or regex matching, cooldowns, parse modes, and auto-delete.

### AI Moderation and Trust

- OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages compatible providers.
- Provider/model registry, encrypted API keys, capability tags, priority, probes, fallback chains, and automatic degradation.
- Text, image, video-frame, VideoNote, sticker, animation, voice/audio placeholder, document, contact, poll, location, venue, game, Invoice, Story, and Giveaway content.
- Bio checks before a message is processed, preventing post-join profile changes from bypassing review.
- Optional AdKiller advertising prefilter with a complete 0-100 `score_bands` partition; incomplete overlays are rejected.
- Scene/category/confidence-based decisions with human review in the Web console.

Trust states:

```text
new -> suspicious -> trusted
  \         |          |
   +-------> banned <---+

new (long-term inactive) -> archived
```

`new` and `suspicious` users graduate after the configured number of AI-approved clean messages, five by default. `graduate_after_days` is retained only for configuration compatibility. A trusted human is not automatically downgraded to `suspicious` by later ordinary moderation, while content filters and explicit ban actions still apply. Zombie retention only archives inactive `new` users with no checked messages; it does not delete trusted graduates.

### Feedback, Scheduling, and Web Administration

- Configurable feedback for delete, mute, kick, ban, warning, verification, CAS, graduation, and admin actions.
- MarkdownV2/HTML templates, mentions, variables, and auto-delete.
- Up to 20 scheduled messages per group with interval or multiple daily schedules, buttons, variables, manual run, auto-delete, and 50 recent run records.
- Dashboard health and system controls: pause AI, pause Telegram actions, or freeze the system.
- Group authorization, global and per-group policy, violations, warnings, AI review, AI usage, trust management, prompts, model management, audit, and scoped administrators.

## Telegram Commands

| Command | Purpose |
|---|---|
| `/start`, `/help` | Introduction and command list |
| `/status` | Group daily statistics or private service status |
| `/trust` | Inspect a user's trust state |
| `/warn` | Warn a user and apply escalation rules |
| `/unban` | Unban a user and clear local ban state |
| `/spam` | Quickly ban by reply, username, or user ID |
| `/cas` | Query CAS status |
| `/warn_status` | Inspect warning history |
| `/config` | Issue a one-time Web admin login link |

## Telegram Mini App

The "Admin Console" button in the bot's private-chat menu opens `/miniapp`. The backend validates Telegram `initData` with HMAC-SHA-256, accepts only users present in the `admins` table, and reuses the existing JWT, CSRF, authorization scope, and audit controls.

The Mini App exposes the complete console: dashboard, groups, group authorization, violations, AI review and statistics, trust, prompts, model management, audit, and administrators. It follows Telegram themes and safe areas, supports the native back button and haptics, and provides mobile bottom navigation plus an all-features menu while preserving the desktop console layout.

Configure the Mini App/Web App allowed domain in BotFather before release. It must match the HTTPS `PUBLIC_BASE_URL`. The bot synchronizes its default Mini App menu button whenever it registers the webhook.

## Architecture

```text
Telegram / Browser
        |
Cloudflare Tunnel
        |
Caddy :80
  |             |
  |             +--> Next.js 16 Web :3000
  |
  +--> Go Bot + Echo API :8080
             |          |
       PostgreSQL 16   Redis 7 AOF
```

| Layer | Technology |
|---|---|
| Bot/API | Go 1.26.6, telebot.v3, Echo v4 |
| Web | Node.js 22, Next.js 16 App Router, React 19, Tailwind CSS |
| Data | PostgreSQL 16, pgx/v5, sqlc, goose |
| State and rate limits | Redis 7 with AOF `everysec` |
| Media | ffmpeg, DejaVu fonts, Go image rendering |
| Edge | Caddy 2 and Cloudflare Tunnel |
| Delivery | Docker Compose, Docker Hub, GitHub Actions |

## Repository Layout

```text
clawguard/
├── .github/workflows/          # CI and image publishing
├── bot/
│   ├── cmd/clawguard/          # Main bot/API process
│   ├── cmd/migrate/            # goose migration entry point
│   ├── internal/ai/            # Providers, models, calls, encryption
│   ├── internal/api/           # REST API, auth, rate limits, CSRF
│   ├── internal/bot/           # Telegram handlers and moderation
│   ├── internal/scheduler/     # Scheduled group messages
│   ├── internal/store/         # sqlc data layer
│   ├── internal/worker/        # Background workers
│   └── migrations/             # Database migrations through 00031
├── web/                        # Next.js administration console
├── scripts/                    # Production smoke and restore rehearsal
├── docs/                       # Design and restore documentation
├── Caddyfile
├── docker-compose.yml
└── .env.example
```

## Configuration Model

Policies merge in this order, with later documents overriding earlier ones:

```text
built-in defaults -> global_config -> group.config
```

Writes use the same merge, defaulting, and validation behavior as runtime loading. Per-group documents may contain only overrides, while dangerous or apparently truncated global updates are rejected.

## Quick Start

Requirements:

- Linux with Docker Engine and Docker Compose v2
- A Telegram Bot Token
- An HTTPS domain or Cloudflare Tunnel

Create the environment file and generate independent secrets:

```bash
cp .env.example .env
openssl rand -hex 32
```

Required baseline values:

```env
BOT_TOKEN=...
BOT_USERNAME=...
TELEGRAM_LOGIN_BOT_USERNAME=...
WEBHOOK_SECRET=...
SUPER_ADMIN_IDS=...
POSTGRES_PASSWORD=...
REDIS_PASSWORD=...
JWT_SECRET=...
PUBLIC_BASE_URL=https://your-domain.example
```

Set a stable `ENCRYPTION_KEY` in production if provider API keys are stored through the Web console. Leaving it empty creates a temporary key, so stored keys cannot be decrypted after a restart.

Start the published images:

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/readyz
```

At startup, the bot applies goose migrations, registers its Telegram webhook, loads the model registry, and starts background workers. `/readyz` reports ready only when PostgreSQL and Redis are available.

## Environment Variables

See [`.env.example`](./.env.example) for the deployment template.

| Group | Variables |
|---|---|
| Telegram | `BOT_TOKEN`, `BOT_USERNAME`, `TELEGRAM_LOGIN_BOT_USERNAME` |
| Webhook and URLs | `WEBHOOK_SECRET`, `PUBLIC_BASE_URL`, `WEB_BASE_URL` |
| Admin bootstrap | `SUPER_ADMIN_IDS`, `ADMIN_TELEGRAM_IDS` |
| PostgreSQL | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` |
| Redis | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` |
| Auth and encryption | `JWT_SECRET`, `ENCRYPTION_KEY` |
| Turnstile | `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET_KEY` |
| LLM bootstrap | `LLM_PROVIDERS` and provider-key environment variables |
| Runtime | `APP_ENV`, `HTTP_PORT`, `DAILY_REPORT_ENABLED`, `CLAWGUARD_LOG_RAW_UPDATES` |
| Retention | `RETENTION_VIOLATIONS_DAYS`, `RETENTION_AI_DECISIONS_DAYS`, `RETENTION_CONFIG_AUDIT_DAYS`, `RETENTION_PROFILE_CHECK_LOGS_DAYS`, `RETENTION_ZOMBIE_DAYS`, `RETENTION_BANNED_DAYS` |
| Web build | `NEXT_PUBLIC_BOT_USERNAME`, `NEXT_PUBLIC_TURNSTILE_SITE_KEY` |

`NEXT_PUBLIC_*` values must be configured as GitHub Repository Variables because Next.js embeds them into the browser bundle at build time.

## Background Tasks

| Task | Responsibility |
|---|---|
| VerificationExpiry | Claim due verification jobs and apply failure actions |
| JoinProtectionRecovery | Recovery notifications, database cleanup jobs, and Redis fallback tasks every 15 seconds |
| Healthcheck | Detect consecutive AI moderation failures and send throttled owner alerts |
| LLMProber | Model probes, health state, and owner alerts |
| LLMStatsAggregator | AI usage aggregation |
| DailyReport | Daily owner report |
| RetentionCleanup | Event cleanup, zombie archiving, and old banned-record cleanup |
| ProfileCheckLogsRetention | Bio-check log retention |
| ScheduledMessages | Cron dispatch and execution history |

## Security

| Area | Current implementation |
|---|---|
| Webhook | URL secret plus `X-Telegram-Bot-Api-Secret-Token` validation |
| Login | Telegram Login or one-time magic links, restricted to database administrators |
| Session | 24-hour HS256 JWT in a `Secure`, `HttpOnly`, `SameSite=Strict` cookie |
| CSRF | Cookie write requests require a double-submit `X-CSRF-Token` |
| Authorization | owner/admin roles with SQL-enforced group scopes |
| Rate limits | Redis limits on login, logout, Turnstile, admin writes, and Telegram sends |
| LLM keys | Encrypted in PostgreSQL using `ENCRYPTION_KEY` |
| HTTP | Server timeouts, Caddy security headers, loopback-only host ports |
| Logging | Error redaction and raw Telegram update logging disabled by default |

Production startup rejects placeholder or shorter-than-32-character `JWT_SECRET`, `WEBHOOK_SECRET`, and `ENCRYPTION_KEY` values, and requires an HTTPS `PUBLIC_BASE_URL`. Webhook registration logs never include the secret path.

## CI/CD and Production Upgrades

Pull Requests run backend tests/vet/race/vulnerability/migration checks and frontend type/build/audit/Playwright checks. A `main` push publishes `latest` and `sha-<commit>` tags for both images. Production pins `CLAWGUARD_TAG=sha-<commit>`, and images include the `org.opencontainers.image.revision` label.

Use the pinned deployment script. It creates a database backup and rollback image tags, updates bot and web separately, verifies health and OCI revisions, and restores the previous images on failure:

```bash
cd /root/clawguard
bash scripts/deploy-production.sh sha-<commit>
```

If a webhook secret may have reached logs, run `ROTATE_WEBHOOK_SECRET=1 bash scripts/deploy-production.sh sha-<commit>` to rotate it inside the rollback-safe deployment.

Run the production smoke suite afterward:

```bash
bash scripts/prod-smoke.sh \
  --since 5m \
  --url https://your-domain.example \
  --compose docker-compose.yml
```

Cloudflare Tunnel should target the loopback HTTP entry:

```yaml
ingress:
  - hostname: your-domain.example
    service: http://127.0.0.1:8090
  - service: http_status:404
```

Documentation-only changes do not require a production restart.

## Backup Restore Rehearsal

The rehearsal selects the latest backup from `/var/backups/clawguard` by default, restores it into a temporary PostgreSQL container, and never touches production:

```bash
bash scripts/restore-rehearsal.sh
BACKUP_PATH=/path/to/clawguard.sql.gz bash scripts/restore-rehearsal.sh
```

`scripts/backup.sh` creates validated custom-format PostgreSQL archives with checksums and retention. The included systemd timer runs daily; set `BACKUP_OFFSITE_DIR` to an external mount for an off-host copy.

```bash
install -m 0644 deploy/systemd/clawguard-backup.service /etc/systemd/system/
install -m 0644 deploy/systemd/clawguard-backup.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now clawguard-backup.timer
```

## Test Status and Known Boundaries

Baseline measured on 2026-07-16:

- 57 Go test files and about 30.3% total statement coverage.
- Package coverage: bot 38.9%, AI 49.4%, config 56.7%, API 13.9%, scheduler 22.4%, worker 6.2%.
- The Web project has desktop/mobile Playwright entry tests; authenticated configuration workflows still need broader coverage.
- Production Compose pins a `sha-<commit>` tag and the deployment script verifies the image revision and rolls back on failure.
- Horizontal bot scaling requires leader election or distributed locking for periodic jobs.
- A production backup timer and restore rehearsal are included; an actual off-host destination must still be configured.

## Recent Changes

- Redis-backed atomic join-flood state with restart recovery and shared counters.
- PostgreSQL leased cleanup jobs, Redis persistent fallback queue, and Telegram failure backoff.
- Join-protection runtime status, simulation, and throttled admin summaries.
- Trusted-human status preservation without bypassing content filters.
- Global-config truncation guards and stricter Telegram action-pause checks.
- A simpler operator flow: threshold reached, incoming accounts handled temporarily, automatic recovery.

## Additional Documentation

- [System design](./docs/DESIGN.md)
- [Database restore rehearsal](./docs/restore-rehearsal.md)

## License

[MIT](./LICENSE)
