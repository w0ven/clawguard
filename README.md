# ClawGuard

**企业级 Telegram 群管理 Bot** — 新人验证 · 反广告 · AI 审核 · Web 控制台

ClawGuard 为 Telegram 群组（当前服务 RFC IDC 群 ~5k 人）提供全套管理能力：从入群验证、关键词/链接过滤到 LLM 驱动的智能审核，所有功能均可通过 Web 面板可视化配置。

> 🇺🇸 [English version](./en_README.md)

---

## ✨ 核心功能

### 🛡️ 新人验证
- **多种验证方式**：按钮点击、数学题、Cloudflare Turnstile
- 可按群独立配置验证方式、超时时间和失败动作（踢出/封禁/禁言）
- 验证超时自动踢人，Redis 管理 TTL 倒计时

### 🔍 反广告过滤
- **关键词过滤**：支持大小写敏感配置
- **正则匹配**：灵活的模式匹配
- **链接过滤**：白名单机制，新人链接加严
- **频率限制**：可配置消息速率阈值
- **CAS 同步**：定期拉取 Combot Anti-Spam 黑名单，新人入群自动校验

### 🤖 AI 智能审核
- **用户信任状态机**：`new → trusted / suspicious / banned`，新人观察期内消息经 AI 审核
- **多模型支持**：可接入多个 LLM Provider（OpenAI 兼容接口），支持主模型 + fallback 链
- **灵活策略**：按消息类别（招聘/交友/币圈/刷单等）配置不同处置动作
- **调用控制**：每用户调用限制、消息缓存、短消息跳过、批量合并
- **人审闭环**：AI 判决可通过 Web 面板确认/标记误判，持续优化准确率
- **Prompt 编辑器**：可视化编辑系统提示词、自定义规则，支持版本管理和在线测试

### ⚠️ 警告系统
- 可配置最大警告次数、超限动作和警告衰减天数
- 警告记录持久化，支持群级和用户级查询

### 🌐 Web 管理面板
- **Telegram Login** 登录 + JWT 鉴权
- **仪表盘**：群数、今日违规、AI 调用次数、信任用户数概览
- **群管理**：群列表、单群配置编辑（验证/过滤/AI 策略等）
- **违规日志**：传统违规 + AI 判决审核队列
- **信任管理**：用户信任状态筛选、手动标记
- **AI 调用统计**：按日/群/模型的调用次数分析
- **Prompt 编辑器**：可视化编辑 + 在线测试沙箱
- **审计日志**：配置变更记录
- **管理员管理**：角色权限（owner/admin）

---

## 🏗️ 技术架构

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

## 📁 项目结构

```
clawguard/
├── docker-compose.yml           # 容器编排
├── Caddyfile                    # 反向代理规则
├── .env.example                 # 环境变量模板
├── bot/                         # Go 后端
│   ├── cmd/
│   │   ├── clawguard/main.go    # 主入口
│   │   └── migrate/main.go      # 迁移工具
│   ├── internal/
│   │   ├── ai/                  # LLM 多模型注册/解析/加密
│   │   ├── api/                 # REST API (Echo v4)
│   │   ├── auth/                # JWT 认证
│   │   ├── bot/                 # telebot 处理器（验证/过滤/审核）
│   │   ├── casclient/           # CAS 黑名单客户端
│   │   ├── config/              # 配置加载
│   │   ├── store/               # sqlc 生成的数据访问层
│   │   └── worker/              # 后台任务（验证过期/LLM探测/日报等）
│   ├── migrations/              # 数据库迁移（goose，22 个版本）
│   └── sqlc.yaml
├── web/                         # Next.js 前端
│   ├── app/                     # App Router 页面
│   │   ├── dashboard/           # 仪表盘
│   │   ├── groups/              # 群管理
│   │   ├── violations/          # 违规日志
│   │   ├── ai-review/           # AI 审核队列
│   │   ├── ai-calls/            # AI 调用统计
│   │   ├── trust/               # 信任管理
│   │   ├── prompt-editor/       # Prompt 编辑器
│   │   ├── admins/              # 管理员
│   │   ├── audit/               # 审计日志
│   │   └── verify/[token]/      # Turnstile 验证页
│   └── components/              # UI 组件（shadcn/ui）
└── docs/
    ├── DESIGN.md                # 完整设计文档
    └── M5_AI_TRUST.md           # AI 审核 + 信任系统设计
```

---

## 🛠️ 技术栈

| 层 | 选型 |
|---|---|
| Bot 核心 | Go 1.22 + [telebot.v3](https://github.com/go-telegram-bot-api/telebot) |
| Web API | Go [Echo v4](https://github.com/labstack/echo)（与 Bot 同进程） |
| Web 面板 | [Next.js 15](https://nextjs.org) (App Router) + [shadcn/ui](https://ui.shadcn.com) + Tailwind CSS |
| 数据库 | PostgreSQL 16 |
| 缓存/限流 | Redis 7 |
| 数据层 | [sqlc](https://sqlc.dev) + [pgx/v5](https://github.com/jackc/pgx) |
| 数据库迁移 | [goose](https://github.com/pressly/goose) |
| 反向代理 | Caddy 2 |
| 公网入口 | Cloudflare Tunnel |
| 部署 | Docker Compose |

---

## 🚀 快速开始

### 前置要求

- Docker + Docker Compose
- 一个 Telegram Bot Token（通过 [@BotFather](https://t.me/BotFather) 获取）

### 1. 配置环境变量

```bash
cp .env.example .env
```

编辑 `.env`，至少填写以下变量：

```env
BOT_TOKEN=你的Telegram Bot Token
BOT_USERNAME=你的Bot用户名
WEBHOOK_SECRET=自定义webhook密钥
SUPER_ADMIN_IDS=你的Telegram用户ID
POSTGRES_PASSWORD=数据库密码
REDIS_PASSWORD=Redis密码
JWT_SECRET=JWT签名密钥
```

### 2. 启动服务

```bash
docker compose up -d --build
```

首次启动会自动执行数据库迁移。服务就绪后：

- Bot 通过 webhook 接收 Telegram 消息
- Web 面板通过 `http://127.0.0.1:3000` 访问
- API 通过 `http://127.0.0.1:8080` 访问

### 3. 设置 Webhook（如需手动）

Bot 启动时会自动注册 webhook。如需手动设置：

```bash
curl -F "url=https://your-domain.com/webhook/YOUR_SECRET" \
     -F "secret_token=YOUR_WEBHOOK_SECRET" \
     "https://api.telegram.org/bot<BOT_TOKEN>/setWebhook"
```

---

## 🚢 部署

### 部署到生产服务器

本项目已迁移到 **CI/CD 自动构建镜像** 流程，不再使用 rsync 同步源码：

```bash
# 1. 本地改完代码，push 到 main
git push origin main

# 2. GitHub Actions 自动构建 bot/web 镜像并推送到 Docker Hub
#    - kelework/clawguard-bot:latest
#    - kelework/clawguard-web:latest
gh run watch --repo <owner>/<repo> --exit-status

# 3. 远程拉新镜像重启（只动改动的服务，不动 postgres/redis）
ssh root@YOUR_SERVER "cd /root/clawguard && docker compose pull bot web && docker compose up -d --no-deps bot web"
```

**前端环境变量**（`NEXT_PUBLIC_*`）必须在 GitHub Repository Variables 配置，因为 Next.js 会在 build 时把它们编进 bundle；远程 `.env` 只作用于后端 runtime。

### Cloudflare Tunnel 配置

Tunnel 入口指向宿主机 `127.0.0.1:8090`：

```yaml
- hostname: guard.misaka.si
  service: tcp://23.80.90.86:8090
```

---

## 🔒 安全设计

| 方面 | 措施 |
|---|---|
| Webhook 验证 | Telegram `secret_token` 校验 `X-Telegram-Bot-Api-Secret-Token` |
| Web 登录 | Telegram Login Widget + hash 校验 + 白名单用户限制 |
| API 鉴权 | JWT (HS256)，48h 过期，Bearer Token 方式 |
| SQL 安全 | sqlc 生成参数化查询 |
| 密钥管理 | 所有密钥通过 `.env` 注入，Git 忽略 |
| CSRF | API 走 Bearer Token，不依赖 Cookie |
| 公网暴露 | Cloudflare Tunnel 隐藏真实 IP |

---

## ⚙️ 环境变量

完整变量列表参见 `.env.example`，主要分为：

| 分组 | 变量 | 说明 |
|---|---|---|
| Bot | `BOT_TOKEN`, `BOT_USERNAME` | Telegram Bot 凭证 |
| Auth | `WEBHOOK_SECRET`, `JWT_SECRET`, `SUPER_ADMIN_IDS`, `ADMIN_TELEGRAM_IDS` | 认证相关 |
| Database | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` | PostgreSQL 连接 |
| Redis | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` | Redis 连接 |
| AI | `LLM_PROVIDERS`, `NEWAPI_KEY`, `CCPROXY_KEY` | LLM Provider 配置 |
| Turnstile | `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET_KEY` | Cloudflare Turnstile |
| Misc | `PUBLIC_BASE_URL`, `DAILY_REPORT_ENABLED`, `RETENTION_ZOMBIE_DAYS` | 通用配置 |

---

## 📊 后台 Worker

Bot 启动后运行多个后台任务：

| Worker | 职责 |
|---|---|
| VerificationExpiry | 验证超时自动踢人 |
| Healthcheck | 服务健康检查 |
| LLMProber | 定期探测 LLM Provider 可用性 |
| LLMStatsAggregator | AI 调用统计聚合 |
| DailyReport | 每日管理报告推送 |
| RetentionCleanup | 过期数据清理 |

---

## 📈 性能预期

- Bot 启动 < 100ms（Go 静态编译）
- 单条消息处理 < 5ms（命中过滤器）
- 5k 人群吞吐：> 1000 msg/s
- 总内存占用 < 500MB（Bot ~50MB + Web ~150MB + PG ~100MB + Redis ~30MB）

---

## 📝 开发进度

| 里程碑 | 内容 | 状态 |
|---|---|---|
| M1 | 项目骨架 + Docker Compose + 数据库 Schema | ✅ |
| M2 | Bot 核心：Webhook + 命令路由 + 新人验证 | ✅ |
| M3 | 反广告过滤器 + 警告系统 + CAS 同步 | ✅ |
| M4 | REST API + Telegram Login + Web 面板骨架 | ✅ |
| M5 | AI 审核 + 信任系统 + 多模型 + Prompt 编辑器 | ✅ |
| M6 | Turnstile 验证流程 | ✅ |
| M7 | AI 审核增强：跨聊天引用、t.me 预览、消息前 Bio 审核 | ✅ |
| M8 | 生产部署：RFC / TOP 群正式接入 | ✅ |

### 近期更新

- **2026-04-25 ✅ 最终版本** — 终极审计修复全部上线（6 路 Codex 审查 + 主助手亲自 diff，关闭 P0 8 条、P1 30 条、P2 14 条、P3 1 条 + rune-safe 字符串收尾）。重点改动：
  - **服务边界**：Echo HTTP server 加 ReadHeader/Read/Write/Idle timeout 防 slowloris；所有 outbound HTTP client 配 Transport（image/link preview/AI/Turnstile）；Caddy 加 5 个安全响应头（HSTS / nosniff / Referrer-Policy / X-Frame-Options / Permissions-Policy）+ `-Server`。
  - **限流防爆破**：登录/logout/Turnstile/admin write 接口全部 Redis INCR/EXPIRE 限流（fail-open + Retry-After）；Telegram 主动外推消息加 `golang.org/x/time/rate` 双层限流（全局 25/s + per-chat 18/min），429 自动 RetryAfter 重试一次。
  - **Turnstile 加固**：siteverify 加 10s 专用 timeout + idempotency_key（cf_response 用 sha256 hash 后 60s 内 Redis 缓存，幂等重试）。
  - **Web 鉴权**：Next.js middleware 改反向白名单，`admins/ai-calls/ai-review/authorized-groups/llm/prompt-editor/trust` 等所有后台路由强制 cookie 校验，仅 `/auth /verify /api` 与静态资源放行。
  - **Scheduler 稳健性**：daily 多时间点 Add 失败回滚已注册 entry，避免 orphan；定时消息 cron 加 per-msg-id 重入互斥；RunNow 允许执行 paused 任务（与 UI 语义一致）。
  - **Lifecycle 收敛**：所有 `time.AfterFunc` / `go func sleep` delayed-delete goroutine 接 lifecycleCtx + sync.WaitGroup，Service/Scheduler Stop 各等 5s 优雅退出。
  - **数据隔离**：scoped admin 的 `groups/violations/warnings` 改为 SQL `BIGINT[]` chat scope 下推（empty/NULL = owner 全局），count 也跟着 scope；删除内存过滤兜底，杜绝越权聚合。
  - **隐私最小化**：profile_check_logs bio 写入 500 byte / 回显 200 byte+ellipsis；新增 90 天 retention worker 每日 03:00 自动清理。
  - **AI 上下文**：Moderator 接受 lifecycle ctx；saveCache 用 `WithTimeout(lifeCtx, 5s)` 派生，错误从 `_ =` 吞掉降为 Debug log。
  - **审计完整性**：non_text_messages 策略 `delete` / `delete_warn` 分支补 `filter_non_text_message` violation 写入。
  - **Redis 抖动防护**：bio 检查 inflight 锁本地路径加 60s timer 自动释放；keyword reply 冷却 Redis 失败时改 fail-closed 防重复广告。
  - **Rune-safe 截断**：`truncateString` / `truncateProfileCheckLogBio` / `llm_prober.truncateError` 全部改用 `unicode/utf8.RuneCountInString` + `[]rune` 切片，避免中文 utf-8 边界被切坏出乱码字节；max 语义从 byte → 字符数（CJK 容量约 3×）。
- **2026-04-24** 数学图片验证题死循环修复 + 降级路径重构
  - 根因：`sendMathImageChallenge` 的 `for answer < 0` 循环只 re-roll `a`，当 `op1='-' op2='-' b+c>29` 时永久死循环，两个入群用户把 bot CPU 烧到 220%
  - 修复：整体重 roll + 50 次上限 + 3 秒 context timeout + 1000 seed 回归测试
  - 降级策略改为"同形态降级"：异常时仍然走图片题（保底 `5 + 3 + 2`），不再退到更弱的纯文本题
  - 图片渲染真失败时：Error 日志 + @用户发故障提示（30 秒自删）+ 用户保持 restricted
- **2026-04** 反馈模板新增 `{user_mention}` / `{admin_mention}` 可点击变量
- **2026-04** Banned 状态终态保护 + 封禁元数据（banned_at/banned_reason）+ 后台 UI 展示
- **2026-04** CPU 热路径优化：flushBatch 30s 超时、bio 审核异步、t.me 预览限速
- **2026-04** 欢迎语改用 MarkdownV2，修复格式嵌套导致的 mention link 失效
- **2026-04** CI/CD：GitHub Actions → Docker Hub → 远程 pull，替换原 rsync 流程

---

## 📄 许可

私有项目，未授权禁止使用。

## 群定时消息

- 在 Web 管理面板进入「群管理」→ 选择群 →「定时消息」Tab，可为单个群创建最多 20 条定时消息。
- 支持「每 N 分钟」和「每日多个北京时间 HH:MM」两种触发方式；后端按 UTC 入库并在 Bot 进程启动时全量重载。
- 消息正文使用 MarkdownV2 原样发送，支持变量 `{group_title}` `{member_count}` `{date}` `{time}` `{weekday}`；未知变量会原样保留。
- 可配置链接按钮与 0-3600 秒自动删除；若上一条消息尚未到自动删除时间，下一次触发会跳过并记录原因。
- 每次发送/失败/跳过会保留最近 50 条历史，支持在群管理页查看，也可点击「立即试发」验证模板。
