# ClawGuard 设计说明

Telegram 群组管理系统：入群验证、规则过滤、CAS 联动、AI 内容审核、用户信任状态机、定时消息，以及 Web / Mini App 管理后台。

## 目录

- [定位](#定位)
- [技术栈](#技术栈)
- [架构](#架构)
- [项目结构](#项目结构)
- [配置模型](#配置模型)
- [数据模型](#数据模型)
- [信任状态机](#信任状态机)
- [消息审核流水线](#消息审核流水线)
- [入群验证](#入群验证)
- [认证与权限](#认证与权限)
- [后台任务](#后台任务)
- [部署边界](#部署边界)

## 定位

| 能力 | 说明 |
|---|---|
| 入群验证 | 按钮、算术题、图片算术题、Emoji、Cloudflare Turnstile；任务带租约，重启后续跑 |
| 入群洪泛防护 | Redis Lua 原子状态机；短窗口集中涌入时临时处理新账号 |
| 规则过滤 | 关键词、正则、用户名黑名单、链接白名单、未毕业加严、速率限制 |
| CAS | 同步黑名单，入群命中直接封禁 |
| AdKiller | 广告预筛，命中后再按分段动作处理 |
| AI 审核 | 文本 / 图片 / 视频抽帧；多 Provider 与 fallback |
| 信任状态机 | `new` → `trusted` / `suspicious` / `banned` |
| 人审 | 面板可标注正确、误封、漏判 |
| 管理后台 | Next.js Web + Telegram Mini App |

未授权群会被拒绝。群授权由 owner 在后台维护。禁用授权群只禁止改该群配置，入群防护、定时消息、封禁等手工操作仍按管理员权限执行。

## 技术栈

| 层 | 技术 |
|---|---|
| Bot / API | Go 1.26（toolchain 1.26.6）、telebot.v3、Echo v4 |
| Web | Node.js 22、Next.js 16、React 19、Tailwind CSS |
| 数据 | PostgreSQL 16、pgx/v5、sqlc、goose（当前到 `00031`） |
| 状态 / 限流 | Redis 7，AOF `everysec` |
| 入口 | Caddy 2、Cloudflare Tunnel |
| 部署 | Docker Compose；GitHub Actions 构建 bot/web 镜像 |

LLM 走任意 OpenAI 兼容网关。密钥用 `ENCRYPTION_KEY` 加密入库。

## 架构

```text
Telegram / Browser
        |
HTTPS 入口（Cloudflare Tunnel 或自有反代）
        |
Caddy
  |             |
  |             +--> Next.js Web :3000
  |
  +--> Go Bot + Echo API :8080
             |          |
       PostgreSQL 16   Redis 7
```

Bot 与 API 同进程，共享 sqlc 数据层。Web 只做面板，写操作走 Bot 上的管理 API。

## 项目结构

```text
clawguard/
├── bot/
│   ├── cmd/clawguard/              # Bot + API 主进程
│   ├── cmd/migrate/                # goose 迁移
│   ├── internal/ai/                # Provider、模型、调用与加密
│   ├── internal/api/               # REST、Telegram 登录、限流、CSRF
│   ├── internal/bot/               # Telegram 处理、验证、过滤、审核
│   ├── internal/scheduler/         # 群定时消息
│   ├── internal/store/             # sqlc
│   ├── internal/worker/            # 后台 Worker
│   └── migrations/                 # 00001 … 00031
├── web/                            # Next.js 管理后台
├── scripts/                        # 备份、恢复演练、生产升级
├── docs/                           # DESIGN、本地开发、恢复演练
├── Caddyfile
├── docker-compose.yml
└── .env.example
```

## 配置模型

最终策略按下面顺序合并，后者覆盖前者：

```text
代码内置默认值  ->  global_config  ->  group.config
```

群配置只保存差异字段。`null` / 缺省表示继承全局。

全局配置按分段写入（Prompt、AdKiller、LLM 等），整份保存带 `version` 乐观锁：冲突返回 409，避免两个页面互相覆盖。写入前使用与运行时相同的合并、默认值和校验。

## 数据模型

核心表（按职责，不是迁移编号）：

| 表 | 作用 |
|---|---|
| `admins` | 面板管理员，`role` 为 super / admin，可限制可见群 |
| `groups` | Bot 所在群，`config` JSONB 为群级覆盖 |
| `global_config` | 单行全局策略，`config` JSONB + `version` |
| `authorized_groups` | 授权白名单；`enabled` 控制是否改群配置 |
| `pending_verifications` | 入群验证任务，含租约与重试 |
| `user_trust` | 信任状态机 |
| `banned_users` | 手动或 CAS 黑名单 |
| `violations` / `warnings` | 硬规则违规与警告 |
| `ai_decisions` | AI 判决，可供人审 |
| `profile_check_logs` | Bio 审核日志 |
| `config_audit` | 配置变更审计 |
| `system_state` | 暂停 AI / 暂停动作 / 冻结 |
| `llm_providers` / `llm_models` / `llm_model_stats` | 模型注册与探针统计 |
| `scheduled_messages` / `scheduled_message_runs` | 定时消息 |

## 信任状态机

```text
入群 + 验证通过
        │
        ▼
     ┌─────┐  未毕业消息走硬规则 → AdKiller → AI
     │ new │
     └──┬──┘
        │ messages_clean ≥ GraduateAfterMessages
        ▼
   ┌─────────┐
   │ trusted │  仍走关键词 / 正则 / 链接 / 速率；非封禁处罚不降级
   └─────────┘
        │
        ▼ AI 中等置信度或 Bio 命中
  ┌────────────┐
  │ suspicious │── 累犯 → banned
  └────────────┘
        │ 高置信度 / 管理员封禁 / CAS
        ▼
     ┌────────┐
     │ banned │── 保留策略到期 → archived
     └────────┘
```

`GraduateAfterDays` 仅为兼容旧配置保留，不参与毕业判定。

## 消息审核流水线

```text
入站消息
  │
  ├─ 硬过滤（关键词 / 正则 / 用户名 / 链接）
  │     语料含用户可见结构化字段：联系人姓名与电话、投票题与选项、
  │     地点标题与地址、发票、游戏、文件名等；不含 AI 占位标签
  │
  ├─ 未毕业限制：链接 / 转发 / 媒体 / 每分钟条数
  │     NoMedia 删除后，contact / poll / venue / invoice / game / document
  │     继续后续审核；纯图片、骰子、位置等停止
  │
  ├─ 关键词自动回复（只匹配 Text + Caption）
  │     回复成功、冷却、Redis 失败、发送失败都不结束审核
  │
  ├─ 非文本策略（off / delete / ai_review）按配置执行，不被 ContinueReview 绕过
  │
  ├─ AdKiller 预筛
  │
  └─ AI
        ├─ 视频按当前消息的 Video / Animation / VideoNote 抽帧，caption 不改变是否抽帧
        ├─ 引用消息的视频不会被误抽
        └─ 判决写入 ai_decisions，动作按置信度阈值与类别覆盖
```

管理员、可信用户等豁免规则保持现有策略，不因结构化卡片或抽帧门控放宽。

## 入群验证

`new_chat_members` 后限制权限、写入 `user_trust(status=new)`，再按群策略发挑战。CAS / 本地黑名单命中则直接封禁。

验证任务写入 PostgreSQL，使用 `next_attempt_at`、租约、重试次数。Worker 扫描过期任务并按 `FailAction` 处理。服务重启后继续未完成任务。

未毕业权限按 `filter.new_user.no_media` / `no_invites` 同步 Telegram 成员权限；毕业或标为 `trusted` 后恢复。

## 认证与权限

- Web 登录：Telegram Login Widget → `GET/POST /api/auth/telegram-login`。签名有效窗口约 24 小时。GET 与 POST 共用 IP（30/分钟）和用户（10/分钟）限流。
- Mini App：Telegram `initData` HMAC 校验。
- 会话：JWT，HttpOnly，SameSite Strict。管理写接口带 CSRF。
- 面板写请求受管理员角色与群范围约束。

## 后台任务

全部跑在单个 bot 进程内：

| 任务 | 作用 |
|---|---|
| 验证过期 | 扫描并处理过期入群验证 |
| 入群防护清理 | 恢复通知、租约任务、Redis 兜底队列 |
| 定时消息 | 按群调度，无 leader election |
| 日报 | 可选，发给 super admin |
| LLM 探针 | 探测启用模型，失败可自动降级 |
| 统计聚合 | AI 调用与延迟 |
| 保留清理 | 按阈值清理日志与归档用户 |

## 部署边界

生产拓扑是 **单 bot + 单 web + PostgreSQL 16 + Redis 7 + Caddy**。

入群防护状态在 Redis 中，可以随 Redis 恢复。定时消息和日报没有分布式选主，不要把 bot 扩成多副本。

容器只读根文件系统并 drop 多余 capability。升级走 Compose 原位替换；失败时脚本逐步回滚 bot/web，并检查就绪状态。备份恢复演练支持 `.sql` / `.dump` 及其 gzip，解压失败会中止恢复。
