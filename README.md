# ClawGuard

ClawGuard 是一套面向 Telegram 群组的群管理系统，包含入群验证、入群洪泛防护、规则过滤、CAS 联动、AI 内容审核、用户信任状态机、定时消息和 Web 管理后台。

> [English documentation](./en_README.md)

## 当前状态

本文档基线为 **2026-07-16**，以 `main` 分支当前实现为准。

| 项目 | 当前状态 |
|---|---|
| 数据库 Schema | goose migration `00028` |
| 生产拓扑 | 单 bot 实例 + 单 web 实例 + PostgreSQL 16 + Redis 7 AOF + Caddy 2 |
| 发布方式 | GitHub Actions 测试并构建 bot/web 镜像，生产机使用 Docker Compose 原位升级 |
| 后端 CI | `go test ./...`、`go vet ./...`、关键包 race tests |
| 前端 CI | TypeScript 类型检查、Next.js 生产构建 |
| 入群洪泛防护 | 默认开启，Redis 原子状态机，数据库任务租约，Redis 清理兜底 |
| AI 审核 | 能力完整，默认策略关闭，需要配置 Provider/Model 后按群启用 |

当前部署按**单 bot 实例**设计。入群防护状态已放入 Redis，但定时消息、日报等任务尚未全部实现分布式 leader election，因此不要直接把 bot 扩成多个副本。

## 核心功能

### 入群验证

- 支持按钮、算术题、图片算术题、Emoji 四选一和 Cloudflare Turnstile。
- 可按群配置超时时间、失败动作、进群服务消息删除和欢迎语。
- 入群前可执行 CAS 查询和 Bio 关键词/AI 检查。
- 验证任务写入 PostgreSQL，使用 `next_attempt_at`、租约、重试次数和错误信息可靠处理。
- 服务重启后会继续处理未完成的验证和清理任务。
- 未授权群会被自动拒绝，群授权由 owner 在 Web 后台维护。

### 入群洪泛防护

该功能只处理短时间集中涌入的新账号，不会对群内老成员做批量动作。

默认参数：

| 参数 | 默认值 | 含义 |
|---|---:|---|
| `join_threshold` | 20 人 | 统计窗口内达到此人数进入防护 |
| `join_window_seconds` | 60 秒 | 入群统计窗口 |
| `protection_duration_seconds` | 900 秒 | 防护持续 15 分钟 |
| `temporary_ban_seconds` | 3600 秒 | 新账号临时封禁 60 分钟 |
| `admin_notify_interval_seconds` | 300 秒 | 管理员汇总提醒限频 |
| `max_pending_verifications` | 30 人 | 待验证任务接近上限时提前防护 |
| `telegram_failure_cooldown_seconds` | 300 秒 | Telegram 清理失败后的基础冷却 |

运行机制：

1. 正常入群继续执行原有验证。
2. 入群人数或待验证任务达到阈值后，进入临时防护。
3. 防护期间不生成验证题、不调用 CAS/Bio/AI，也不逐人刷提示。
4. 后续集中涌入的新账号被临时处理，防护到期后自动恢复正常验证。

状态和故障处理：

- Redis Lua 脚本原子完成窗口清理、计数、触发、拦截统计和提醒限频。
- Redis 保存防护结束时间、触发原因、拦截人数、最近提醒和 Telegram 故障熔断状态。
- Redis 暂时不可用时退化为单进程内存保护；数据库计数失败时按达到上限处理。
- Telegram 动作失败时先尝试受限兜底，再写 PostgreSQL 清理任务；数据库也失败时写 Redis 持久清理队列。
- 清理 Worker 每 15 秒处理恢复通知、数据库租约任务和 Redis 兜底任务。
- Web 状态卡显示当前状态、触发原因、结束时间、拦截人数、待验证数、清理冷却和延迟任务数。

首次入群账号不会被提前判为 trusted。后台只对数据库中已有本群毕业记录的返群用户保留免误伤保护。

### 规则过滤与反垃圾

- 关键词过滤，支持区分大小写和多种处置动作。
- 正则规则、用户名黑名单、链接白名单和管理员豁免。
- 未毕业用户可限制链接、转发、媒体、邀请成员和每分钟消息数。
- 非文字消息可关闭、删除、删除并警告或交给 AI。
- CAS 黑名单同步和入群检查。
- 消息速率限制和警告累计/衰减/升级。
- 其他 bot 可配置为审核、移出、封禁或忽略，并支持 username 白名单。
- 可识别 sender chat、via bot、编辑消息、跨聊天引用和 t.me 链接预览。
- 关键词自动回复支持模糊、精确、正则、冷却、管理员跳过和自动删除。

### AI 审核与信任状态

- 支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 兼容 Provider。
- Provider、Model、API Key、能力标签、优先级和探活设置可在 Web 后台维护。
- 支持主模型、fallback 链、模型健康探测、自动降级、缓存、合批和每用户调用上限。
- 支持文本、图片、视频抽帧、VideoNote、贴纸、动图、语音/音频占位、文件、联系人、投票、位置、地点、游戏、Invoice、Story 和 Giveaway 等内容。
- 支持消息前 Bio 审核，避免用户入群后修改简介绕过检查。
- AI 判决和动作按场景、类别、置信度阈值记录，可在 Web 后台人工复核。

用户状态：

```text
new -> suspicious -> trusted
  \         |          |
   +-------> banned <---+

new(长期无发言) -> archived
```

- `new` 和 `suspicious` 必须累计 `graduate_after_messages` 条 AI 审核通过的干净消息才会毕业，默认 5 条。
- `graduate_after_days` 仅保留旧配置兼容，不参与毕业判定。
- 已毕业真人不会因后续普通内容审核被自动降回 `suspicious`，但关键词、链接等内容过滤仍正常执行。
- 明确的封禁动作仍可把任何违规用户写为 `banned`。
- 僵尸归档只处理长期无发言、`messages_checked=0` 的 `new` 用户，不会删除 trusted 毕业用户。

### 动作反馈与定时消息

- 删除、禁言、踢出、封禁、警告、验证结果、CAS 命中、信任毕业和管理员动作均可配置反馈模板。
- 模板支持用户/管理员 mention、原因、时长等变量，支持 MarkdownV2、HTML 和自动删除。
- 每群最多 20 条定时消息，支持每 N 分钟或每日多个北京时间。
- 定时消息支持变量、链接按钮、自动删除、立即试发和最近 50 条运行历史。
- 同一条定时消息有进程内重入锁，上一条仍等待自动删除时可跳过后续发送。

### Web 管理后台

后台包含以下页面：

- 总览：系统健康、群数、活跃验证、今日违规和系统状态。
- 群管理：基础、验证、入群防护、过滤、关键词回复、警告、反垃圾、AI、动作反馈、日志、审计和定时消息。
- 全局配置：全局默认策略，群配置在此基础上覆盖。
- 群授权：控制 bot 可以服务的群。
- 违规与警告：筛选、清除警告、封禁和解封。
- AI 复核、AI 调用统计、信任系统、Prompt 编辑器和模型管理。
- 审计日志：配置变更、AI 自动动作、管理员命令、警告升级和其他 bot 事件。
- 管理员：owner/admin 角色和群范围权限。
- 系统开关：暂停 AI、暂停 Telegram 动作或冻结系统。

## Telegram 命令

群内管理员命令：

| 命令 | 用途 |
|---|---|
| `/start`, `/help` | 机器人介绍和命令列表 |
| `/status` | 本群今日统计 |
| `/trust` | 回复消息或 `@用户` 查询信任状态 |
| `/warn` | 警告用户并进入累计升级流程 |
| `/unban` | 解封用户并清理本地封禁状态 |
| `/spam` | 回复、`@用户` 或 user ID 快捷封禁 |
| `/cas` | 查询 CAS 状态 |
| `/warn_status` | 查看警告记录 |
| `/config` | 获取一次性管理后台登录链接 |

私聊管理员可使用 `/start`、`/help`、`/status`、`/trust <user_id>` 和 `/config`。

## 技术架构

```text
Telegram / Browser
        |
Cloudflare Tunnel
        |
Caddy :80
  |             |
  |             +--> Next.js 15 Web :3000
  |
  +--> Go Bot + Echo API :8080
             |          |
       PostgreSQL 16   Redis 7 AOF
```

| 层 | 技术 |
|---|---|
| Bot/API | Go 1.22、telebot.v3、Echo v4 |
| Web | Node.js 22、Next.js 15 App Router、React 19、Tailwind CSS |
| 数据 | PostgreSQL 16、pgx/v5、sqlc、goose migration |
| 状态/限流 | Redis 7，AOF `everysec` |
| 媒体 | ffmpeg、DejaVu 字体、Go 图片渲染 |
| 入口 | Caddy 2、Cloudflare Tunnel |
| 部署 | Docker Compose、Docker Hub、GitHub Actions |

## 项目结构

```text
clawguard/
├── .github/workflows/          # CI 和 Docker 镜像发布
├── bot/
│   ├── cmd/clawguard/          # Bot/API 主进程
│   ├── cmd/migrate/            # goose 迁移入口
│   ├── internal/ai/            # Provider、Model、调用与加密
│   ├── internal/api/           # REST API、认证、限流和 CSRF
│   ├── internal/bot/           # Telegram 处理、验证、过滤和审核
│   ├── internal/scheduler/     # 群定时消息
│   ├── internal/store/         # sqlc 数据访问层
│   ├── internal/worker/        # 后台 Worker
│   └── migrations/             # 数据库迁移，当前到 00028
├── web/
│   ├── app/                    # Next.js 页面和 Route Handler
│   ├── components/             # 管理后台组件
│   └── lib/                    # API client、类型和工具
├── scripts/
│   ├── prod-smoke.sh           # 生产升级后检查
│   └── restore-rehearsal.sh    # 备份恢复演练
├── docs/
│   ├── DESIGN.md               # 详细设计
│   └── restore-rehearsal.md    # 恢复演练说明
├── Caddyfile
├── docker-compose.yml
└── .env.example
```

## 配置模型

最终策略按以下顺序合并，后者覆盖前者：

```text
代码内置默认值 -> global_config -> group.config
```

配置写入前会使用和运行时相同的合并、默认值和校验逻辑。群配置可以只保存差异字段；敏感的全局配置更新会拒绝疑似截断的文档。

## 快速开始

### 前置条件

- Linux 主机
- Docker Engine 和 Docker Compose v2
- Telegram Bot Token
- 一个 HTTPS 公网域名或 Cloudflare Tunnel

### 1. 配置环境

```bash
cp .env.example .env
openssl rand -hex 32
```

至少填写：

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

如果在 Web 后台保存 LLM Provider API Key，生产环境必须设置稳定的 `ENCRYPTION_KEY`。留空会在每次启动生成临时密钥，重启后无法解密之前保存的 Key。

### 2. 启动

当前 Compose 使用已发布镜像：

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:8080/readyz
```

bot 启动时自动执行 goose migration、注册 Telegram webhook、加载模型注册表和后台任务。`readyz` 只有在 PostgreSQL 与 Redis 都可用时才返回 ready。

### 3. 授权群

1. 把 bot 加入群并授予删除消息、限制/封禁成员和邀请成员等必要权限。
2. 使用 owner 登录 Web 后台。
3. 在“群授权”中添加 chat ID。
4. 进入“群管理”按群调整策略。

## 环境变量

完整模板见 [`.env.example`](./.env.example)。

| 分组 | 变量 | 说明 |
|---|---|---|
| Telegram | `BOT_TOKEN`, `BOT_USERNAME`, `TELEGRAM_LOGIN_BOT_USERNAME` | Bot 和 Web 登录身份 |
| Webhook | `WEBHOOK_SECRET`, `PUBLIC_BASE_URL`, `WEB_BASE_URL` | webhook 校验和公网地址 |
| Admin | `SUPER_ADMIN_IDS`, `ADMIN_TELEGRAM_IDS` | 初始 owner/admin Telegram ID |
| PostgreSQL | `POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` | 数据库连接 |
| Redis | `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD` | 状态、限流、缓存、任务兜底和 AOF |
| Auth | `JWT_SECRET`, `ENCRYPTION_KEY` | 登录 JWT 与 Provider Key 加密 |
| Turnstile | `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET_KEY` | 新人入群验证验证码 |
| LLM bootstrap | `LLM_PROVIDERS` 及 Provider Key env | 旧环境配置迁移和初始 Provider |
| Runtime | `APP_ENV`, `HTTP_PORT`, `DAILY_REPORT_ENABLED`, `CLAWGUARD_LOG_RAW_UPDATES` | 运行模式、端口、日报和诊断日志 |
| Retention | `RETENTION_EVENTS_DAYS`, `RETENTION_PROFILE_CHECK_LOGS_DAYS`, `RETENTION_ZOMBIE_DAYS`, `RETENTION_BANNED_DAYS` | 当前实际生效的数据保留参数 |
| Web build | `NEXT_PUBLIC_BOT_USERNAME`, `NEXT_PUBLIC_TURNSTILE_SITE_KEY` | GitHub Actions 构建时写入浏览器 bundle |

`NEXT_PUBLIC_*` 必须配置在 GitHub Repository Variables；只修改生产机 `.env` 不会改变已构建的前端 bundle。

## 后台任务

| 任务 | 频率/触发 | 职责 |
|---|---|---|
| VerificationExpiry | 轮询 | 领取到期验证任务并执行失败动作 |
| JoinProtectionRecovery | 每 15 秒 | 防护恢复通知、数据库清理任务和 Redis 兜底队列 |
| Healthcheck | 每 5 分钟 | 连续 AI 审核失败监测和 owner 限频告警 |
| LLMProber | 配置频率 | 模型探活、健康状态和 owner 告警 |
| LLMStatsAggregator | 定时 | 聚合 AI 调用统计 |
| DailyReport | 每日 | 向 owner 发送运营报告 |
| RetentionCleanup | 启动时及每日 03:00 | 清理事件、归档僵尸用户、清理旧 banned 记录 |
| ProfileCheckLogsRetention | 启动时及每日 03:00 | 清理 Bio 检查日志 |
| ScheduledMessages | cron | 发送群定时消息并保存运行历史 |

## 安全设计

| 方面 | 当前实现 |
|---|---|
| Webhook | URL secret 与 `X-Telegram-Bot-Api-Secret-Token` 双校验 |
| Web 登录 | Telegram Login 或一次性 magic link，仅数据库管理员可登录 |
| Session | 24 小时 HS256 JWT，`Secure`、`HttpOnly`、`SameSite=Strict` Cookie |
| CSRF | Cookie 会话的写请求必须携带双提交 `X-CSRF-Token` |
| 权限 | owner/admin 角色，admin 可限制群范围，SQL 查询下推 scope |
| API 限流 | 登录、登出、Turnstile 和管理写接口使用 Redis 限流 |
| Telegram 限流 | 全局和单群发送限流，429 按 RetryAfter 重试 |
| LLM Key | 使用 `ENCRYPTION_KEY` 加密后存入 PostgreSQL |
| SQL | sqlc/pgx 参数化查询 |
| HTTP | 服务端 timeout、Caddy 安全响应头、仅回环端口暴露给 Tunnel |
| 日志 | 错误经过 redact 包处理，原始 Telegram update 默认关闭 |

## 发布与升级

### CI/CD

Pull Request 会执行 bot 单测/vet/race 和 web 类型检查/生产构建。合并到 `main` 后，GitHub Actions 构建：

```text
kelework/clawguard-bot:latest
kelework/clawguard-bot:sha-<commit>
kelework/clawguard-web:latest
kelework/clawguard-web:sha-<commit>
```

镜像带 `org.opencontainers.image.revision` label，可在生产机核对实际 commit。

### 生产原位升级

升级前先给当前镜像打回滚标签，然后分服务升级：

```bash
cd /root/clawguard

stamp=$(date +%Y%m%d-%H%M%S)
bot_image=$(docker inspect -f '{{.Image}}' "$(docker compose ps -q bot)")
web_image=$(docker inspect -f '{{.Image}}' "$(docker compose ps -q web)")
docker image tag "$bot_image" "kelework/clawguard-bot:rollback-$stamp"
docker image tag "$web_image" "kelework/clawguard-web:rollback-$stamp"

docker compose pull bot web
docker compose up -d --no-deps bot
curl -fsS http://127.0.0.1:8080/readyz
docker compose up -d --no-deps web
```

只更新文档不需要重启生产服务。

### 升级后检查

```bash
cd /root/clawguard
bash scripts/prod-smoke.sh \
  --since 5m \
  --url https://your-domain.example \
  --compose docker-compose.yml
```

脚本检查 Compose 服务、bot 错误日志、webhook 注册、公开入口安全响应头、镜像 revision 和 digest，不会主动向 Telegram 发消息。

### Cloudflare Tunnel

Tunnel 应指向宿主机回环 HTTP 入口：

```yaml
ingress:
  - hostname: your-domain.example
    service: http://127.0.0.1:8090
  - service: http_status:404
```

### 备份恢复演练

恢复演练脚本会把已有备份恢复到临时 PostgreSQL 容器，不会触碰生产数据库：

```bash
cd /root/clawguard
bash scripts/restore-rehearsal.sh

# 或指定文件
BACKUP_PATH=/path/to/clawguard.sql.gz bash scripts/restore-rehearsal.sh
```

`restore-rehearsal.sh` 只负责验证备份，不负责创建生产备份。生产环境仍需由 cron/systemd timer 或外部备份系统定期执行 `pg_dump` 并设置异地保留。

## 测试现状与边界

2026-07-16 本地基线：

- Go 代码有 53 个 `_test.go` 文件，总 statement coverage 约 30.3%。
- 核心包覆盖率：bot 39.0%、AI 49.4%、config 55.2%、API 13.9%、scheduler 22.4%、worker 2.7%。
- Web 有 TypeScript 类型检查和 Next.js build，但目前没有前端单元测试或 Playwright E2E。
- CI 会构建并发布镜像，但当前生产 Compose 仍使用 `latest`；发布前后必须核对 image revision。
- 当前不支持无脑横向扩容 bot，多实例前需要为 scheduler、日报和其他周期任务增加 leader election 或分布式锁。
- 仓库提供恢复演练，没有内置生产备份定时器。

## 近期版本变化

- 新增入群洪泛防护配置页、运行状态卡、模拟触发和管理员汇总通知。
- 入群防护状态迁移到 Redis 原子状态机，支持跨重启恢复和多实例共享计数。
- Telegram 清理失败加入指数冷却、PostgreSQL 租约任务和 Redis 持久兜底队列。
- 防护激活后跳过重复数据库 COUNT，待验证查询增加复合索引。
- 修复延迟清理动作语义、重新入群代际校验和已离群用户误处理。
- 已毕业真人不会被普通内容审核自动降回 suspicious；内容过滤仍照常执行。
- 加强全局配置截断保护、Telegram 动作暂停复核和关键 moderator 查询错误处理。
- 入群防护界面改为“达到阈值 -> 临时处理新账号 -> 自动恢复”的简明流程。

## 进一步设计

- [系统设计](./docs/DESIGN.md)
- [数据库备份恢复演练](./docs/restore-rehearsal.md)

## License

私有项目，未授权禁止使用。
