# ClawGuard 设计文档

Telegram 群组反广告 / 反潜伏广告号 / 群管理平台。Bot + Web 面板一体。

---

## 1. 目标

- **入群验证**：按钮 / 数学题 / Cloudflare Turnstile，多模式可切换。
- **反硬广告**：关键词、正则、链接白名单、新人期加严、速率限制、CAS 联动。
- **反潜伏广告号**：用户信任状态机 + AI 内容审核 + Bio（简介）审核双层防线。
- **多群管理**：全局策略 + 群级覆盖，面板可视化配置。
- **人审闭环**：AI 判决可标注（正确 / 误封 / 漏判），支持回滚与学习。
- **成本可控**：多 Provider / 多模型 fallback、自动探针降级、每日预算、缓存。

---

## 2. 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| Bot 核心 | Go 1.22 + telebot.v3 | 单二进制、低内存、长轮询 / webhook 都支持 |
| Web API | Go + Echo v4 | 与 bot 同进程，共享 pgx / sqlc 数据层 |
| Web 面板 | Next.js 15 (App Router) + Tailwind + shadcn/ui | Telegram Login Widget 登录 |
| 数据库 | PostgreSQL 16 | 配置走 JSONB，日志走普通列 |
| 缓存 / 限流 | Redis 7 | 验证 TTL、Bio 缓存、速率窗口 |
| LLM | 任意 OpenAI 兼容网关（自建 newapi / ccproxy / 官方） | 多 Provider / 多模型 fallback |
| 反向代理 | Caddy 2 | Webhook / API / Web 入口一统 |
| 公网入口 | Cloudflare Tunnel | 不暴露真实 IP |
| 部署 | docker compose | 5 个 service：postgres / redis / bot / web / caddy |

---

## 3. 目录结构（当前实态）

```
clawguard/
├── docker-compose.yml
├── Caddyfile
├── .env                             # git ignore
├── bot/
│   ├── Dockerfile
│   ├── entrypoint.sh                # 先跑 migrate 再起 bot
│   ├── cmd/
│   │   ├── clawguard/main.go        # bot 主进程入口
│   │   └── migrate/main.go          # goose 迁移入口
│   ├── migrations/                  # 00001 ~ 00016 goose sql
│   ├── internal/
│   │   ├── bot/                     # telebot handler（扁平，无子包）
│   │   │   ├── bot.go               # 入群 / 异步验证 / 匹配联动
│   │   │   ├── moderation.go        # 消息审核主流水线 + AI action
│   │   │   ├── filter.go            # 硬规则过滤
│   │   │   ├── turnstile.go
│   │   │   ├── verify_math_image.go
│   │   │   ├── keyword_reply.go
│   │   │   ├── admin.go / admin_command_config.go
│   │   │   └── runtime_state.go / template_render.go ...
│   │   ├── ai/                      # LLM 抽象 + 多 provider
│   │   │   ├── moderator.go         # 审核入口 + 缓存 + 降级
│   │   │   ├── resolver.go          # 模型解析 + fallback 链
│   │   │   ├── registry.go          # provider / model 注册表
│   │   │   ├── openai.go            # OpenAI 兼容客户端
│   │   │   ├── crypto.go            # ENCRYPTION_KEY 加密 api key
│   │   │   └── legacy_migrate.go    # 从 LLM_PROVIDERS env 迁进 DB
│   │   ├── api/                     # Echo REST API
│   │   │   ├── server.go
│   │   │   ├── auth.go              # Telegram Login + JWT
│   │   │   ├── routes_admin.go      # 主要 admin 接口
│   │   │   ├── routes_admin_llm.go  # LLM 注册表 / 探针 / 测试
│   │   │   ├── routes_public.go     # magic 登录兑换等
│   │   │   └── routes_verify.go     # Turnstile 回调
│   │   ├── store/                   # sqlc 生成的 DAL
│   │   ├── config/                  # GuardPolicy + env 加载
│   │   ├── auth/                    # Telegram Login 校验
│   │   ├── casclient/               # CAS 同步
│   │   └── worker/                  # 后台周期任务
│   │       ├── daily_report.go
│   │       ├── retention_cleanup.go
│   │       ├── verification_expiry.go
│   │       ├── llm_prober.go        # 探针 + 降级触发
│   │       ├── llm_stats.go
│   │       ├── budget_reset.go
│   │       └── healthcheck.go
│   └── sqlc.yaml
├── web/
│   ├── Dockerfile
│   ├── app/                         # Next.js App Router
│   │   ├── page.tsx                 # Telegram Login 首页
│   │   ├── dashboard/               # 看板
│   │   ├── groups/                  # 群列表 + 群详情（/[chatId]）+ global-config
│   │   ├── trust/                   # 用户信任列表
│   │   ├── violations/              # 传统违规
│   │   ├── ai-review/               # AI 判决人审队列
│   │   ├── ai-costs/                # AI 成本仪表盘
│   │   ├── prompt-editor/           # AI prompt 编辑 + 测试
│   │   ├── llm/                     # LLM Provider / Model 管理
│   │   ├── admins/                  # 管理员管理
│   │   ├── authorized-groups/       # 授权群白名单
│   │   ├── audit/                   # 配置审计
│   │   ├── verify/[token]/          # Turnstile 验证页
│   │   └── auth/magic/              # 魔法链接登录
│   ├── components/
│   ├── lib/
│   └── middleware.ts                # JWT 校验 + 路由守卫
└── docs/
    └── DESIGN.md                    # 本文档
```

---

## 4. 数据模型

16 张核心表（按 goose migration 顺序）。`JSONB` 列即配置类（`GuardPolicy`），其余为日志/状态。

| 表 | 作用 | 关键列 |
|---|---|---|
| `admins` | 登录 Web 的管理员 | `telegram_id` 唯一，`role` = `super` / `admin`，`group_scope` 限制可见群 |
| `groups` | Bot 加入的 Telegram 群 | `chat_id` 唯一，`config JSONB` 群级覆盖 |
| `global_config` | 全局策略（单行表） | `config JSONB`，CHECK `id=1` |
| `authorized_groups` | 授权白名单，未授权群 bot 自动退 | `chat_id`, `authorized_by`, `enabled` |
| `pending_verifications` | 待验证新人 | `method`, `payload JSONB`, `expires_at`, `join_message_id` |
| `banned_users` | 黑名单（手动 + CAS） | `user_id` PK, `source` = `manual` / `cas` |
| `violations` | 硬规则违规日志 | `rule`, `matched`, `action`, `message_text` |
| `warnings` | 警告记录 | `reason`, `issued_by`, `consumed_at` |
| `config_audit` | 配置变更审计 | `scope`, `before`, `after`, `diff`, `action` |
| `user_trust` | **用户信任状态机**（M5 核心） | `status` = `new` / `suspicious` / `trusted` / `banned` / `archived`，`score`, `messages_checked`, `messages_clean`, `graduated_at` |
| `ai_decisions` | **AI 判决历史** | `verdict`, `confidence`, `category`, `action_taken`, `admin_override`, `model`, `provider_id`, `latency_ms`, `cost_cents`, `message_text` |
| `profile_check_logs` | Bio 审核记录（命中 / 通过 / 跳过 / 错误） | `check_mode` = `keyword` / `ai` / `on_message_keyword` / `on_message_ai`, `result` = `hit` / `pass` / `skip` / `error` |
| `system_state` | 全局运行态（暂停 AI / 暂停动作 / 冻结 / 预算锁） | `ai_paused`, `actions_paused`, `frozen`, `ai_budget_locked` |
| `llm_providers` | LLM 网关注册表 | `name`, `base_url`, `api_key_encrypted`（`ENCRYPTION_KEY` 加密） |
| `llm_models` | 每个 provider 下的可用模型 | `provider_id`, `model_key`, `capabilities`（JSON 数组，如 `["moderation","vision"]`）|
| `llm_model_stats` | 探针 / 实际调用统计 | 成功率、延迟、成本聚合（用于 /ai-costs 页面） |

### 4.1 `user_trust` 状态流转

```
       入群 + 验证通过
              │
              ▼
         ┌────────┐  第 N 条消息走硬规则→ AI 审核
         │  new   │──┐
         └────┬───┘  │
              │      │ clean_cnt ≥ GraduateAfterMessages
              │      │ 或 now-joined_at ≥ GraduateAfterDays
              │      ▼
              │  ┌─────────┐   AI/手动重新置回 → suspicious / banned
              │  │ trusted │
              │  └─────────┘
              │
              ▼ AI 命中中等置信度 或 Bio 命中
         ┌────────────┐
         │ suspicious │── 累犯 → banned
         └────────────┘
              │ AI 命中高置信度 / 管理员点 ban / CAS 命中
              ▼
         ┌────────┐
         │ banned │── retention_cleanup 过期 → archived
         └────────┘
```

### 4.2 `ai_decisions` 的三种来源

Bug 修复后（见 git 历史），AI 复核队列同时收录三类判决：

1. **消息内容 AI 审核**：`moderation.go applyAIModeration` → 正常 AI 路径。
2. **入群 Bio 审核命中**：`bot.go` 异步 verify 分支 → `recordProfileViolationDecision`，`model` 字段写 `profile_check:join_ai` / `profile_check:join_keyword`。
3. **发言前 Bio 审核命中**（未毕业用户改了 Bio 再发广告的场景）：`moderation.go on-message profile check` → 同 helper，`model` 写 `profile_check:on_message_*`。

三类都会同步更新 `user_trust.status = banned` 并写 `violations` 记录，消息原文保留在 `ai_decisions.message_text`。

---

## 5. 核心子系统

### 5.1 新人验证

```
new_chat_members 事件
  ├─ bot restrictChatMember（剥夺所有权限）
  ├─ upsert user_trust(status='new', score=0.5)
  ├─ 查 banned_users / CAS → 命中则直接 ban + 不发验证
  ├─ 按 policy.Verify.Method 发挑战：
  │   ├─ button   → "我是人类" 按钮
  │   ├─ math     → "3 + 4 = ?" 多选
  │   └─ turnstile→ 发 https://{PUBLIC_BASE_URL}/verify/{jwt} 链接
  ├─ 写 pending_verifications（含 expires_at）
  └─ 异步并行：
      ├─ 用户通过 → unrestrict + 删验证消息 + 删 pending + 欢迎语
      └─ 到期 → 按 FailAction（kick/ban）处理
```

Turnstile 路径细节：JWT 带 `chat_id + user_id + exp`，Web 前端渲染 Turnstile widget，提交时后端校验 `turnstile-response` + JWT。

### 5.2 Bio（简介）审核

防"入群时 Bio 干净、之后偷偷改 Bio 加广告"的场景。两处入口：

- **入群时**：`checkProfile`（`moderation.go`），模式 = `policy.Verify.ProfileCheckMode`（`off` / `keyword` / `ai`）。命中 → `performAsyncVerificationMatch` 走 ban + 记录链路。
- **未毕业用户发言前**：`checkProfileOnMessage`（`moderation.go`），开关 `policy.AI.CheckProfileOnMessage`，模式 `policy.AI.ProfileOnMessageMode`。命中 → ban + 写 `ai_decisions` + `resetTrustAfterViolation` + `violations`（含消息原文）。

Bio 通过 `getChat` 抓取，`policy.AI.BioCacheTTLMinutes` 控制 Redis 缓存 TTL 节流。每次命中 / 通过 / 跳过 / 错误都写 `profile_check_logs`，Web 面板 `/verify` 可看。

### 5.3 硬规则过滤管道

`filter.go applyFilterChecks` 顺序：

```
消息进来
  ├─ 关键词命中（FilterKeywords）     → action
  ├─ 正则命中（FilterRegex）          → action
  ├─ 链接（FilterLinks，带白名单）    → action（ExemptAdmins 可放行管理员）
  ├─ 用户名黑名单（FilterUsernames）  → action
  ├─ 新人期（FilterNewUser，24h 内）  → no_links / no_forwards / no_media / max_msg_per_minute
  ├─ 速率（AntiSpamRateLimit）        → mute_5m 等
  └─ 未命中 → 进下一层（AI）
```

### 5.4 CAS 同步

`casclient` 周期拉 `https://api.cas.chat/export.csv`，diff 写 `banned_users(source='cas')`。新人加入时先查本地 `banned_users`，命中直接 ban，跳过验证。

### 5.5 AI 消息审核 + 信任状态机

```
消息 → applyAIModeration
  │
  ├─ trust.status = trusted → 跳过 AI（除非命中 TriggerKeywords）
  ├─ trust.status = banned  → 删 + ban（防漏网）
  ├─ status = new/suspicious：
  │   ├─ CheckProfileOnMessage 开关打开 → 先走 Bio 审核（5.2）
  │   │    命中则 ban 链路 + return
  │   ├─ 短消息跳过（SkipMessagesShorterThan）
  │   ├─ 图片启用 → 下载 + hash + 调 vision model
  │   ├─ 引用 / 转发 / t.me 链接预览 → 拼进审核上下文
  │   └─ aiModerator.CheckMessage → verdict / confidence / category
  │       │
  │       ▼
  │   decideAIAction(policy.AI, output):
  │     confidence ≥ Thresholds.Ban  → ban
  │     confidence ≥ Thresholds.Mute → mute
  │     confidence ≥ Thresholds.Warn → warn
  │     confidence ≥ Thresholds.Flag → flag（仅标记 suspicious）
  │     否则 → none（messages_clean++）
  │   再按 ActionsByCategory 重写（某些类别整体升级 / 降级）
  │   │
  │   └─ applyAIAction：执行 + 写 ai_decisions + resetTrustAfterViolation
```

毕业条件（`maybeGraduateUser`）：`messages_clean ≥ GraduateAfterMessages` 或入群时长 ≥ `GraduateAfterDays`。

### 5.6 LLM Provider / Model 注册表

替代早期 `LLM_PROVIDERS` env JSON，改为 DB 驱动：

- **`llm_providers`**：name、base_url、加密的 api_key（`ENCRYPTION_KEY` 为 32B 对称密钥，AES-GCM 加密；留空则每次重启换临时 key，已入库 key 解密失败 → 降级）。
- **`llm_models`**：`(provider_id, model_key)` 唯一，`capabilities` 数组（`moderation` / `vision` / `json_mode` ...）。
- **`resolver`**：解析 `policy.AI.PrimaryProvider/Model` + `FallbackChain`，按 `CapabilityRequirements` 过滤，排好序后依次尝试（超时 / 失败切下一个）。
- **`llm_prober` worker**（`ProbeEnabled + ProbeIntervalSeconds`）：周期对每个启用模型发一次小请求，写 `llm_model_stats`，连续失败触发 `AutoDegrade` 标记 → resolver 临时跳过。
- **env fallback**：老部署的 `LLM_PROVIDERS` env 仍可用，启动时 `legacy_migrate.go` 自动迁到 DB（历史数据一次性搬家）。

### 5.7 成本与缓存

- **每日预算**：`DailyBudgetCents`，`budget_reset` worker 每日零点清零；超支 → `system_state.ai_budget_locked = true`，AI 调用全部跳过（硬规则仍生效）。
- **Per-user 限流**：`PerUserDailyLimit`，Redis 计数。
- **短消息跳过**：`SkipMessagesShorterThan`，默认 5 字符。
- **消息哈希缓存**：`CacheTTLHours`，相同文本 24h 内复用判决（广告号常复制粘贴）。
- **Bio 缓存**：`BioCacheTTLMinutes`，发言前 Bio 审核节流 `getChat`。

### 5.8 人审闭环

Web `/ai-review`：列出所有 `ai_decisions`（含 Bio 命中的合成记录）。每条可标注：

- `confirm` → 认同 AI 判决，加分到信任评分。
- `false_positive` → AI 判错了，自动解封用户（若被 ban），把该样本作为反例。
- `false_negative` → AI 漏判，补处罚，作为正例。

面板还提供：`prompt-editor`（system prompt + 自定义规则 + 测试沙箱，判决不入库）、`ai-costs`（按日 / 按群 / 按模型统计）、`trust`（信任列表筛选 + 手动 `graduate` / `ban`）。

### 5.9 后台 worker

所有 worker 都在 bot 进程内，按不同间隔跑：

| worker | 频率 | 作用 |
|---|---|---|
| `verification_expiry` | 10s | 扫 `pending_verifications.expires_at`，过期就踢 / 删消息 |
| `retention_cleanup` | 每天 | 按 `RETENTION_*` 阈值清理日志 / 归档僵尸用户 |
| `daily_report` | 每天 | `DAILY_REPORT_ENABLED` 打开时给 super admin 发日报 |
| `llm_prober` | `ProbeIntervalSeconds` | 对启用模型发探针，写 `llm_model_stats` |
| `llm_stats` | 分钟级 | 聚合 `ai_decisions` 成本到统计表 |
| `budget_reset` | 每天零点 | 清零预算锁 |
| `healthcheck` | 秒级 | `/healthz` 就绪态 |

---

## 6. 配置体系

### 6.1 合并规则

群配置（`groups.config`）**deep-merge** 到全局配置（`global_config.config`）之上。群侧字段为 `null` / 缺省 → 继承全局。审计表 `config_audit` 记录每次变更的 `before` / `after` / `diff`。

### 6.2 `GuardPolicy` 关键字段（见 `bot/internal/config/policy.go`）

```go
type GuardPolicy struct {
    Verify   VerifyPolicy       // 入群验证 + 入群 Bio 审核
    Filter   FilterConfig       // 硬规则过滤 5 大块
    Messages MessagesPolicy     // 关键词自动回复
    AntiSpam AntiSpamPolicy     // CAS + 速率限制
    Warnings WarningsConfig     // 累计警告 → 动作
    Logging  LoggingConfig      // 违规日志推送到指定群
    AI       AIPolicy           // AI 审核 + 信任 + 成本 + Bio-on-message
    Feedback ActionFeedbackPolicy // 每种动作的群内反馈模板（可关）
}
```

#### `AIPolicy` 摘要

```go
type AIPolicy struct {
    Enabled                 bool
    ImageModerationEnabled  bool
    PrimaryProvider         string      // 对应 llm_providers.name
    PrimaryModel            string      // 对应 llm_models.model_key
    FallbackChain           []string    // "provider/model" 格式
    CapabilityRequirements  []string    // 如 ["moderation"]
    AutoDegrade             bool        // 探针失败时自动跳过
    ProbeEnabled            bool
    ProbeIntervalSeconds    int
    Temperature             float64
    TimeoutMs               int
    MaxRetries              int
    GraduateAfterMessages   int         // 毕业阈值
    GraduateAfterDays       int
    DailyBudgetCents        int
    PerUserDailyLimit       int
    SkipMessagesShorterThan int
    BatchWindowMs           int
    CacheTTLHours           int
    CustomRules             string      // 额外规则文本
    MessageRules            string      // 消息审核的 system prompt
    BioRules                string      // Bio 审核的 system prompt
    Thresholds              AIThresholds // ban/mute/warn/flag 四档置信度
    ActionsByCategory       map[string]string // 招聘=warn 币圈=ban ...
    TriggerKeywords         []string    // trusted 用户命中关键词也送 AI
    CheckProfileOnMessage   bool
    ProfileOnMessageMode    string      // "keyword" | "ai"
    BioCacheTTLMinutes      int
}
```

#### 动作反馈模板

`Feedback` 每种动作有独立开关、模板、自动删除时间、是否引用原消息。模板变量支持 `{user}` / `{group}` / `{reason}` / `{category}` / `{duration}` / `{current}` / `{limit}` 等。

---

## 7. 消息处理总流水线

```
Telegram Update
   │
   ▼
[1] 鉴权：群是否在 authorized_groups？否则静默退群
[2] system_state.frozen / actions_paused → 根据标记短路
[3] 命令路由（/start, /admin_*）
[4] applyFilterChecks（硬规则，5.3）── 命中 → 动作 + 写 violations
[5] applyAIModeration（5.5）
     ├─ 按 trust.status 分流
     ├─ 未毕业 + CheckProfileOnMessage → Bio 审核（5.2）命中 return
     ├─ 短消息跳过
     ├─ 图片 / 引用 / 预览构建上下文
     ├─ aiModerator.CheckMessage（cache / budget / rate-limit / provider fallback）
     └─ 判决 → 动作 → 写 ai_decisions + 更新 user_trust
```

管理员全程豁免（`ExemptAdmins=true`）。

---

## 8. Web 面板页面

| 路径 | 作用 |
|---|---|
| `/` | Telegram Login Widget + 登录态跳转 |
| `/dashboard` | 今日违规 / AI 成本 / 活跃群 / 信任用户数 |
| `/groups` | 群列表 |
| `/groups/[chatId]` | 单群配置（基础 / 验证 / 过滤 / 警告 / 反垃圾 / AI / Feedback / 审计） |
| `/groups/global-config` | 全局配置 |
| `/authorized-groups` | 授权白名单 |
| `/violations` | 硬规则违规日志 |
| `/ai-review` | AI 判决人审（含 Bio 命中） |
| `/trust` | 信任列表（按 status 筛选，手动 graduate / ban） |
| `/ai-costs` | 成本仪表盘（按日 / 群 / 模型） |
| `/prompt-editor` | AI prompt 编辑 + 测试沙箱 |
| `/llm` | LLM Provider / Model 管理 + 探针 |
| `/admins` | 管理员管理（仅 super / owner 可见） |
| `/audit` | 配置变更审计 |
| `/verify/[token]` | Turnstile 验证页（公开，无需登录） |
| `/auth/magic` | 魔法链接登录 |

---

## 9. REST API

所有 `/api/admin/*` 走 JWT 鉴权（Telegram Login 颁发），`/api/public/*` 和 `/webhook/*` 公开。

**分组**：

- **群 / 配置**：`/admin/groups`, `/admin/groups/:chat_id`, `/admin/groups/:chat_id/config`, `/admin/global-config`, `/admin/system-state`
- **授权**：`/admin/authorized-groups`（CRUD）
- **管理员**：`/admin/admins`（CRUD，需 owner）
- **事件 / 日志**：`/admin/events`, `/admin/violations`, `/admin/profile-check-logs`, `/admin/audit`
- **处罚**：`/admin/ban`, `/admin/unban`, `/admin/warnings`, `/admin/warnings/clear`
- **AI**：`/admin/ai-decisions`（GET/PUT override），`/admin/ai-test`, `/admin/ai-prompt-preview`, `/admin/ai-cache-stats`, `/admin/ai-costs`, `/admin/ai-models`
- **信任**：`/admin/user-trust`, `/admin/user-trust/:chat_id/:user_id`
- **LLM 注册表**：`/admin/llm/providers`, `/admin/llm/models`, `/admin/llm/models/:p/:m/probe`, `/admin/llm/models/:p/:m/test`, `/admin/llm/stats`, `/admin/llm/reload`（仅 owner）
- **公开**：`POST /api/public/magic/exchange`, `POST /api/verify/turnstile/:token`
- **运维**：`POST /webhook/:secret`（Telegram 回调），`GET /healthz`

---

## 10. 部署

### 10.1 docker-compose 结构

五个 service：`postgres` / `redis` / `bot` / `web` / `caddy`。`bot` 和 `web` 都从 `.env` 读配置；`caddy` 只挂 `Caddyfile`。详见项目根 [docker-compose.yml](../docker-compose.yml)。

### 10.2 Caddyfile 路由分发

```
:80 {
    handle_path /webhook/*   { reverse_proxy bot:8080 }
    handle      /api/*       { reverse_proxy bot:8080 }
    handle      /verify/*    { reverse_proxy web:3000 }
    handle                    { reverse_proxy web:3000 }
}
```

Cloudflare Tunnel 把公网域名打到本机 8090 → caddy:80。

### 10.3 启动顺序

`bot` 容器 entrypoint：先跑 `/usr/local/bin/migrate`（goose 自动执行 `00001 ~ 00016` 未跑的 migration），再起 `/usr/local/bin/clawguard` 主进程。

### 10.4 环境变量

见 `.env.example`。所有必填项都带 `[必填]` 注释。`__GENERATED__` 的值需用 `openssl rand -hex 32` 自行生成。

关键点：
- `ENCRYPTION_KEY` 若留空，DB 里加密存的 LLM api key 每次重启会失效；生产必填。
- `WEBHOOK_SECRET` 同时作为 Telegram setWebhook 的 `secret_token` 和反代路径 `/webhook/:secret`，两处必须一致。
- `SUPER_ADMIN_IDS` 是唯一「owner 级」凭据；owner 可以增删其他 admin。

---

## 11. 安全

- **Webhook**：Telegram setWebhook 带 `secret_token`，bot 校验 `X-Telegram-Bot-Api-Secret-Token` header，URL 再带一层 secret path。
- **Telegram Login**：校验 hash + `auth_date < 1h`；首次登录仅 `SUPER_ADMIN_IDS` 列表放行，之后由 owner 在 `/admins` 加人。
- **JWT**：HS256，`JWT_SECRET` 签名，48h 过期，Authorization Bearer 方式。
- **LLM api key**：AES-GCM 加密存 DB，密钥从 `ENCRYPTION_KEY` 派生。
- **SQL 注入**：sqlc 全参数化，无字符串拼接。
- **CSRF**：API 走 Bearer，不用 cookie。
- **XSS**：Web 全 React 渲染；消息文本在面板展示走专门的 `MessageTextBlock` 组件（截断 + escape）。
- **Rate limit**：webhook 入口 + AI 调用 Per-user 双重限流。
- **最小暴露**：只有 caddy 对外；bot / web / postgres / redis 全内网。

---

## 12. 性能与成本预期

- Bot 冷启动 < 200ms（Go 静态编译 + migrate）
- 单消息硬规则处理 < 2ms；命中 AI 路径 P50 < 2s（取决于上游 LLM）
- 5k 人群观测值：日均 AI 调用 200~800 次，月成本 ¥5~¥30（主用国产模型）
- 内存占用（稳态）：bot ~80MB，web ~180MB，postgres ~150MB，redis ~40MB

---

## 附录：关键设计决策

| 决策 | 为什么 |
|---|---|
| 信任状态机而非一刀切 | 避免老成员被 AI 误伤；AI 只审新人期，成本大幅下降 |
| Bio 审核独立路径 | 发言前 Bio 审核是专门针对「入群干净、改 Bio 后发广告」的潜伏号，硬规则 + AI 都接不住 |
| Bio 命中也写 `ai_decisions` | 统一到 AI 复核队列，管理员一个页面处理所有 AI 相关决策 |
| LLM 注册表入 DB | 支持热增删 provider / model，探针自动降级，不用改 env 重启 |
| Provider api key 加密 | 面板直接填明文，DB 落加密；导出备份不泄密 |
| 动作反馈模板化 | 群风格差异大，模板 + 自动删除时间 + 可开关，避免「机器人味」 |
| 配置 deep-merge | 群覆盖全局，群侧 null 表示继承；所有字段都可群级独立调 |
| `system_state` 全局开关 | 一键 `ai_paused` / `actions_paused` / `frozen`，故障时快速止损 |
| `authorized_groups` 白名单 | 被拉进未授权群自动退，防止被白嫖 |
| pgx + sqlc 栈 | 类型安全、参数化、零反射；DAL 层自动生成 |
