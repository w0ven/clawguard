# ClawGuard

**Enterprise-grade Telegram Group Management Bot** — Verification · Anti-Spam · AI Moderation · Web Console

ClawGuard provides a complete management suite for Telegram groups (currently serving the RFC IDC group with ~5k members): from join verification and keyword/link filtering to LLM-powered intelligent moderation, all configurable through a visual web panel.

> 🇨🇳 [中文版本](./README.md)

---

## ✨ Core Features

### 🛡️ Join Verification
- **Multiple methods**: Button tap, math captcha, Cloudflare Turnstile
- Per-group configuration for verification method, timeout, and failure action (kick/ban/mute)
- Auto-kick on timeout with Redis-managed TTL countdowns

### 🔍 Anti-Spam Filtering
- **Keyword filtering** with case-sensitivity toggle
- **Regex matching** for flexible pattern detection
- **Link filtering** with whitelist support and stricter rules for new members
- **Rate limiting** with configurable message frequency thresholds
- **CAS sync**: Periodic Combot Anti-Spam blacklist fetch, auto-check on join

### 🤖 AI-Powered Moderation
- **User trust state machine**: `new → trusted / suspicious / banned` — new members' messages are reviewed by AI during observation period
- **Multi-model support**: Connect multiple LLM providers (OpenAI-compatible API) with primary + fallback chains
- **Flexible policies**: Configure different actions per message category (recruitment, dating, crypto, scam, etc.)
- **Cost control**: Daily budget caps, per-user limits, message caching, short-message skip, batch merging
- **Human review loop**: Confirm or flag false positives from the web panel to continuously improve accuracy
- **Prompt editor**: Visual system prompt and custom rule editing with version management and live testing

### ⚠️ Warning System
- Configurable max warnings, action at limit, and decay period
- Persistent warning records with group and user-level queries

### 🌐 Web Admin Panel
- **Telegram Login** authentication + JWT authorization
- **Dashboard**: Group count, today's violations, AI calls, trusted user overview
- **Group management**: Group list, per-group config editing (verification/filtering/AI policies, etc.)
- **Violation logs**: Traditional violations + AI decision review queue
- **Trust management**: Filter and manually tag user trust statuses
- **AI call analytics**: Call breakdown by day/group/model
- **Prompt editor**: Visual editing + live testing sandbox
- **Audit trail**: Configuration change history
- **Admin management**: Role-based permissions (owner/admin)

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Cloudflare Tunnel                   │
│               guard.misaka.si → :8090                │
└──────────────────────┬──────────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────────┐
│                    Caddy 2 (:80)                     │
│    /webhook/*, /api/* → bot:8080                     │
│    /verify/*, /*      → web:3000                     │
└────────┬──────────────────────────────┬─────────────┘
         │                              │
┌────────▼────────┐           ┌────────▼────────┐
│   Bot + API     │           │   Next.js Web   │
│   Go (Echo v4)  │◄─────────│   (App Router)   │
│   telebot.v3    │  INTERNAL │   shadcn/ui      │
└──┬─────────┬────┘   API     └─────────────────┘
   │         │
┌──▼───┐ ┌───▼───┐
│ Pg16 │ │ Redis │
└──────┘ └───────┘
```

---

## 📁 Project Structure

```
clawguard/
├── docker-compose.yml           # Container orchestration
├── Caddyfile                    # Reverse proxy rules
├── .env.example                 # Environment variable template
├── bot/                         # Go backend
│   ├── cmd/
│   │   ├── clawguard/main.go    # Main entry point
│   │   └── migrate/main.go      # Migration tool
│   ├── internal/
│   │   ├── ai/                  # LLM multi-model registry/parsing/encryption
│   │   ├── api/                 # REST API (Echo v4)
│   │   ├── auth/                # JWT authentication
│   │   ├── bot/                 # telebot handlers (verification/filtering/moderation)
│   │   ├── casclient/           # CAS blacklist client
│   │   ├── config/              # Configuration loading
│   │   ├── store/               # sqlc-generated data access layer
│   │   └── worker/              # Background tasks (expiry/budget/probe/reports, etc.)
│   ├── migrations/              # Database migrations (goose, 16 versions)
│   └── sqlc.yaml
├── web/                         # Next.js frontend
│   ├── app/                     # App Router pages
│   │   ├── dashboard/           # Dashboard
│   │   ├── groups/              # Group management
│   │   ├── violations/          # Violation logs
│   │   ├── ai-review/           # AI review queue
│   │   ├── ai-calls/            # AI call analytics
│   │   ├── trust/               # Trust management
│   │   ├── prompt-editor/       # Prompt editor
│   │   ├── admins/              # Admin management
│   │   ├── audit/               # Audit logs
│   │   └── verify/[token]/      # Turnstile verification page
│   └── components/              # UI components (shadcn/ui)
└── docs/
    ├── DESIGN.md                # Full design document
    └── M5_AI_TRUST.md           # AI moderation + trust system design
```

---

## 🛠️ Tech Stack

| Layer | Technology |
|---|---|
| Bot Core | Go 1.22 + [telebot.v3](https://github.com/go-telegram-bot-api/telebot) |
| Web API | Go [Echo v4](https://github.com/labstack/echo) (same process as Bot) |
| Web Panel | [Next.js 15](https://nextjs.org) (App Router) + [shadcn/ui](https://ui.shadcn.com) + Tailwind CSS |
| Database | PostgreSQL 16 |
| Cache / Rate Limiting | Redis 7 |
| Data Layer | [sqlc](https://sqlc.dev) + [pgx/v5](https://github.com/jackc/pgx) |
| Database Migration | [goose](https://github.com/pressly/goose) |
| Reverse Proxy | Caddy 2 |
| Public Endpoint | Cloudflare Tunnel |
| Deployment | Docker Compose |

---

## 🚀 Quick Start

### Prerequisites

- Docker + Docker Compose
- A Telegram Bot Token (obtain from [@BotFather](https://t.me/BotFather))

### 1. Configure Environment

```bash
cp .env.example .env
```

Edit `.env` and fill in at least these variables:

```env
BOT_TOKEN=your_telegram_bot_token
BOT_USERNAME=your_bot_username
WEBHOOK_SECRET=custom_webhook_secret
SUPER_ADMIN_IDS=your_telegram_user_id
POSTGRES_PASSWORD=database_password
REDIS_PASSWORD=redis_password
JWT_SECRET=jwt_signing_key
```

### 2. Start Services

```bash
docker compose up -d --build
```

Database migrations run automatically on first startup. Once ready:

- Bot receives Telegram messages via webhook
- Web panel accessible at `http://127.0.0.1:3000`
- API accessible at `http://127.0.0.1:8080`

### 3. Set Webhook (if manual setup needed)

The bot auto-registers its webhook on startup. For manual setup:

```bash
curl -F "url=https://your-domain.com/webhook/YOUR_SECRET" \
     -F "secret_token=YOUR_WEBHOOK_SECRET" \
     "https://api.telegram.org/bot<BOT_TOKEN>/setWebhook"
```

---

## 🚢 Deployment

### Deploy to Production

This project has migrated to **CI/CD-built images** — rsync-based source sync is no longer used:

```bash
# 1. Commit and push to main
git push origin main

# 2. GitHub Actions builds bot/web images and pushes to Docker Hub
#    - kelework/clawguard-bot:latest
#    - kelework/clawguard-web:latest
gh run watch --repo <owner>/<repo> --exit-status

# 3. Pull new images on the production host and restart only the touched services
ssh root@YOUR_SERVER "cd /root/clawguard && docker compose pull bot web && docker compose up -d --no-deps bot web"
```

**Frontend env vars** (`NEXT_PUBLIC_*`) must be set as GitHub Repository Variables — Next.js inlines them into the bundle at build time; the remote `.env` only affects backend runtime.

### Cloudflare Tunnel Configuration

Point the tunnel ingress to the host machine at `127.0.0.1:8090`:

```yaml
- hostname: guard.misaka.si
  service: tcp://23.80.90.86:8090
```

---

## 🔒 Security Design

| Aspect | Measure |
|---|---|
| Webhook Verification | Telegram `secret_token` validates `X-Telegram-Bot-Api-Secret-Token` header |
| Web Login | Telegram Login Widget + hash verification + user allowlist |
| API Auth | JWT (HS256), 48h expiry, Bearer Token |
| SQL Safety | sqlc generates parameterized queries |
| Secret Management | All secrets injected via `.env`, git-ignored |
| CSRF Protection | API uses Bearer Tokens, no cookie dependency |
| Public Exposure | Cloudflare Tunnel hides real server IP |

---

## ⚙️ Environment Variables

See `.env.example` for the full list. Key groups:

| Group | Variables | Description |
|---|---|---|
| Bot | `BOT_TOKEN`, `BOT_USERNAME` | Telegram Bot credentials |
| Auth | `WEBHOOK_SECRET`, `JWT_SECRET`, `SUPER_ADMIN_IDS`, `ADMIN_TELEGRAM_IDS` | Authentication |
| Database | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` | PostgreSQL connection |
| Redis | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` | Redis connection |
| AI | `LLM_PROVIDERS`, `NEWAPI_KEY`, `CCPROXY_KEY` | LLM Provider configuration |
| Turnstile | `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET_KEY` | Cloudflare Turnstile |
| Misc | `PUBLIC_BASE_URL`, `DAILY_REPORT_ENABLED`, `RETENTION_ZOMBIE_DAYS` | General settings |

---

## 📊 Background Workers

The bot runs multiple background tasks after startup:

| Worker | Responsibility |
|---|---|
| VerificationExpiry | Auto-kick on verification timeout |
| Healthcheck | Service health monitoring |
| LLMProber | Periodic LLM Provider availability probing |
| LLMStatsAggregator | AI call statistics aggregation |
| DailyReport | Daily management report push |
| RetentionCleanup | Expired data cleanup |

---

## 📈 Performance

- Bot startup < 100ms (Go static binary)
- Single message processing < 5ms (filter hit)
- 5k-member group throughput: > 1000 msg/s
- Total memory footprint < 500MB (Bot ~50MB + Web ~150MB + PG ~100MB + Redis ~30MB)

---

## 📝 Development Progress

| Milestone | Content | Status |
|---|---|---|
| M1 | Project skeleton + Docker Compose + Database Schema | ✅ |
| M2 | Bot core: Webhook + command routing + join verification | ✅ |
| M3 | Anti-spam filters + warning system + CAS sync | ✅ |
| M4 | REST API + Telegram Login + web panel skeleton | ✅ |
| M5 | AI moderation + trust system + multi-model + prompt editor | ✅ |
| M6 | Turnstile verification flow | ✅ |
| M7 | AI moderation enhancements: cross-chat quotes, t.me previews, pre-message bio review | ✅ |
| M8 | Production deployment: RFC / TOP groups officially onboarded | ✅ |

### Recent Updates

- **2026-04-24** Fixed math image challenge infinite loop + refactored fallback strategy
  - Root cause: `sendMathImageChallenge`'s `for answer < 0` loop only re-rolls `a` while `b/c/op1/op2` stay fixed. When `op1='-' op2='-' b+c>29`, any `a` in `[10,29]` yields a negative answer forever. Two joiners pinned the bot at 220% CPU.
  - Fix: whole-tuple re-roll + 50-attempt cap + 3s context timeout + 1000-seed regression test
  - Degradation changed to **same-form fallback**: anomalies still produce an image challenge (fallback expression `5 + 3 + 2`) instead of dropping to a weaker text challenge
  - When image render truly fails: Error log + @user notice ("verification system temporarily unavailable", auto-deleted after 30s) + user stays restricted
- **2026-04** Feedback templates added clickable `{user_mention}` / `{admin_mention}` variables
- **2026-04** Banned terminal-state protection + banned metadata (banned_at/banned_reason) + admin UI display
- **2026-04** CPU hot path optimization: 30s flushBatch timeout, async bio review, t.me preview rate limiting
- **2026-04** Welcome messages switched to MarkdownV2, fixing mention link failures caused by nested formatting
- **2026-04** CI/CD: GitHub Actions → Docker Hub → remote pull, replacing the previous rsync flow

---

## 📄 License

Private project. Unauthorized use prohibited.

## Group Scheduled Messages

- Open the web admin panel, go to Group Management → select a group → Scheduled Messages to create up to 20 scheduled messages per group.
- Supports interval triggers every N minutes and multiple daily HH:MM times shown in Asia/Shanghai; times are stored in UTC and reloaded when the bot process starts.
- Message bodies are sent as MarkdownV2 as written and support `{group_title}` `{member_count}` `{date}` `{time}` `{weekday}`; unknown variables are preserved.
- Optional URL inline buttons and 0-3600 second auto-delete are supported; if the previous message is still within its auto-delete window, the next run is skipped and recorded.
- The latest 50 send attempts are retained for history, and admins can use Run Now from the group page to test a template immediately.
