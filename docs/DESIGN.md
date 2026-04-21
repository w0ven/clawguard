# ClawGuard - Telegram 群管理 Bot 设计文档

## 1. 目标

为 RFC IDC 群（4-5k 人）及未来更多群提供企业级群管理能力：
- **新人验证**（多种方式可选，群独立配置）
- **反广告**（关键词、链接、用户名、新人初次消息加严）
- **多群管理**（全局策略 + 群覆盖）
- **Web 面板**（Telegram 登录，可视化配置 + 日志）

## 2. 技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| Bot 核心 | Go 1.22 + telebot.v3 | 单二进制、低内存、毫秒级响应 |
| Web API | Go (Echo v4) | 与 bot 同进程，共享数据层 |
| Web 面板 | Next.js 15 (App Router) + shadcn/ui + Tailwind | 现代化 UI，Telegram Login Widget 集成 |
| 数据库 | PostgreSQL 16 (Docker) | 强一致 + JSON 字段灵活 |
| 缓存/限流 | Redis 7 (Docker) | 验证码 TTL、新人冷却、速率限制 |
| 反向代理 | Caddy 2 (Docker) | 自动 TLS、内部健康检查 |
| 公网入口 | Cloudflare Tunnel → `guard.misaka.si` | 不暴露真实 IP |
| 部署 | docker compose + GitHub Actions（可选） | 一键启停、镜像化 |

## 3. 目录结构

```
/root/clawguard/
├── docker-compose.yml          # 容器编排
├── .env                        # 密钥/token（git 忽略）
├── Caddyfile                   # 反代规则
├── bot/                        # Go bot + API
│   ├── cmd/clawguard/main.go
│   ├── internal/
│   │   ├── bot/                # telebot 处理器
│   │   │   ├── handlers/       # 命令、按钮、消息
│   │   │   ├── verify/         # 验证码引擎
│   │   │   └── filter/         # 反广告过滤器
│   │   ├── api/                # REST API (Echo)
│   │   ├── store/              # 数据访问 (sqlc 生成)
│   │   ├── model/              # 领域模型
│   │   ├── config/             # 配置加载
│   │   └── auth/               # Telegram Login 校验
│   ├── migrations/             # SQL 迁移（goose）
│   ├── sqlc.yaml
│   ├── go.mod
│   └── Dockerfile
├── web/                        # Next.js 面板
│   ├── app/
│   │   ├── (auth)/login/       # Telegram Login
│   │   ├── (dashboard)/
│   │   │   ├── groups/         # 群列表/详情
│   │   │   ├── settings/       # 全局配置
│   │   │   ├── logs/           # 违规日志
│   │   │   └── verify/         # 待验证用户
│   │   └── api/                # 服务端 API（代理到 bot）
│   ├── components/
│   ├── lib/
│   ├── package.json
│   └── Dockerfile
└── docs/
    ├── DESIGN.md               # 本文档
    ├── API.md                  # API 接口
    └── DEPLOY.md               # 部署手册
```

## 4. 数据模型

### 4.1 PostgreSQL 表

```sql
-- 管理员（Telegram 登录后创建）
CREATE TABLE admins (
    id BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT UNIQUE NOT NULL,
    username TEXT,
    first_name TEXT,
    photo_url TEXT,
    role TEXT NOT NULL DEFAULT 'admin',  -- super, admin
    created_at TIMESTAMPTZ DEFAULT NOW(),
    last_login_at TIMESTAMPTZ
);

-- 群（bot 加入即写入）
CREATE TABLE groups (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT UNIQUE NOT NULL,         -- telegram chat id（负数）
    title TEXT NOT NULL,
    type TEXT NOT NULL,                     -- supergroup/channel
    member_count INT DEFAULT 0,
    enabled BOOLEAN DEFAULT TRUE,
    joined_at TIMESTAMPTZ DEFAULT NOW(),
    config JSONB NOT NULL DEFAULT '{}'      -- 群级配置覆盖
);

-- 全局配置（单行表）
CREATE TABLE global_config (
    id INT PRIMARY KEY DEFAULT 1,
    config JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    CHECK (id = 1)
);

-- 待验证用户
CREATE TABLE pending_verifications (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT,
    first_name TEXT,
    method TEXT NOT NULL,                   -- button/math/turnstile
    payload JSONB DEFAULT '{}',             -- 题目答案等
    join_message_id BIGINT,                 -- bot 发的验证消息 id（用于到期删除）
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(chat_id, user_id)
);

-- 违规日志
CREATE TABLE violations (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    username TEXT,
    rule TEXT NOT NULL,                     -- keyword/link/cas/new_user_link...
    matched TEXT,                           -- 命中内容（截断 200 字）
    action TEXT NOT NULL,                   -- delete/mute/kick/ban/warn
    message_text TEXT,                      -- 原消息（截断 1000 字）
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_violations_chat_created ON violations(chat_id, created_at DESC);

-- 用户黑名单（手动 + CAS 同步）
CREATE TABLE banned_users (
    user_id BIGINT PRIMARY KEY,
    reason TEXT,
    source TEXT NOT NULL,                   -- manual/cas/auto
    banned_at TIMESTAMPTZ DEFAULT NOW(),
    banned_by BIGINT                        -- admin telegram id
);

-- 警告记录
CREATE TABLE warnings (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    reason TEXT,
    issued_by BIGINT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_warnings_chat_user ON warnings(chat_id, user_id);

-- 群配置变更审计
CREATE TABLE config_audit (
    id BIGSERIAL PRIMARY KEY,
    scope TEXT NOT NULL,                    -- global/group
    chat_id BIGINT,
    admin_id BIGINT NOT NULL,
    diff JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);
```

### 4.2 配置 Schema（JSONB）

```jsonc
{
  "verify": {
    "enabled": true,
    "method": "button",                     // button | math | turnstile | random
    "timeout_seconds": 300,                 // 超时踢人
    "fail_action": "kick",                  // kick | ban | mute_permanent
    "delete_join_message": true,            // 删除"X 加入群组"系统消息
    "welcome_message": null                 // 通过验证后的欢迎语（可选）
  },
  "filter": {
    "keywords": {
      "enabled": true,
      "list": ["USDT", "加V", "私聊出"],
      "action": "delete_warn",              // delete | delete_warn | delete_mute | delete_ban
      "case_sensitive": false
    },
    "regex": {
      "enabled": false,
      "patterns": []
    },
    "links": {
      "enabled": true,
      "whitelist": ["t.me/clawguard", "github.com"],
      "action": "delete_warn",
      "exempt_admins": true
    },
    "usernames": {                          // @xxx mention 过滤
      "enabled": false,
      "blacklist": []
    },
    "new_user": {                           // 新人加严
      "enabled": true,
      "duration_hours": 24,
      "no_links": true,
      "no_forwards": true,
      "no_media": false,
      "max_messages_per_minute": 5
    }
  },
  "anti_spam": {
    "cas_enabled": true,                    // Combot Anti-Spam
    "rate_limit": {
      "enabled": true,
      "messages_per_10s": 8,
      "action": "mute_5m"
    }
  },
  "warnings": {
    "max_warns": 3,                         // 警告 3 次踢
    "action_at_max": "kick",
    "decay_days": 30                        // 30 天后失效
  },
  "logging": {
    "log_chat_id": null                     // 可选：违规推送到指定群
  }
}
```

合并规则：群配置 deep-merge 到全局配置之上，群侧 `null` 表示继承全局。

## 5. 核心流程

### 5.1 新人验证

```
新成员加入
  ├─ bot 收到 message.new_chat_members
  ├─ 立即 restrictChatMember（剥夺所有权限）
  ├─ 根据群配置选择验证方式
  │   ├─ button:    发"我是人类"按钮
  │   ├─ math:      发"3 + 4 = ?"+ 选项按钮
  │   └─ turnstile: 发链接 → guard.misaka.si/verify/<token>
  ├─ 写 pending_verifications（带 expires_at）
  ├─ Redis 记 TTL 倒计时 key
  └─ 异步：
      ├─ 用户点击按钮/答对题：解除限制 + 删消息 + 删验证记录
      └─ TTL 到期：踢人 + 删消息 + 删验证记录
```

**Cloudflare Turnstile 流程**：
1. bot 发链接 `https://guard.misaka.si/verify/<jwt>` （token 含 chat_id+user_id+exp）
2. 用户打开 → 渲染 Turnstile widget
3. 用户通过 → 前端 POST `/api/verify/submit` 带 turnstile-response
4. 后端校验 token + 校验 turnstile → 调 telegram unrestrictChatMember + 删验证消息

### 5.2 消息过滤管道

```
新消息
  ├─ 是 admin？→ 跳过（除非 exempt_admins=false）
  ├─ 命中 keyword/regex？→ delete + action
  ├─ 含 link？
  │   ├─ 在 whitelist？→ 放行
  │   ├─ 是新人时段？→ delete
  │   └─ 否则 → delete_warn
  ├─ rate_limit 命中？→ mute_5m
  └─ 累计违规超阈值？→ kick/ban
```

### 5.3 CAS 同步

每小时拉 `https://api.cas.chat/export.csv`，diff 写入 `banned_users(source='cas')`。
新人加入时先查 banned_users，命中直接 ban + 不发验证消息。

## 6. API 设计

所有 `/api/*` 走 Telegram Login JWT 鉴权。

```
POST   /api/auth/telegram         # Telegram Login Widget 回调
GET    /api/me                    # 当前管理员

GET    /api/groups                # 群列表
GET    /api/groups/:chat_id       # 群详情
PATCH  /api/groups/:chat_id/config
DELETE /api/groups/:chat_id       # bot 退群

GET    /api/global/config
PATCH  /api/global/config

GET    /api/violations?chat_id=&page=
GET    /api/pending_verifications?chat_id=
DELETE /api/pending_verifications/:id   # 手动放行

POST   /api/users/:user_id/ban
POST   /api/users/:user_id/unban
GET    /api/banned_users

# 公开端点（无鉴权）
POST   /webhook/<secret>          # Telegram webhook
GET    /verify/:token             # Turnstile 验证页（HTML，由 Next.js 渲染）
POST   /api/verify/submit         # Turnstile 提交
GET    /healthz
```

## 7. 部署

### 7.1 docker-compose.yml

```yaml
services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: clawguard
      POSTGRES_USER: clawguard
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes: [pgdata:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U clawguard"]

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    command: redis-server --requirepass ${REDIS_PASSWORD}
    volumes: [redisdata:/data]

  bot:
    build: ./bot
    restart: unless-stopped
    env_file: .env
    depends_on:
      postgres: { condition: service_healthy }
      redis:    { condition: service_started }
    ports: ["127.0.0.1:8080:8080"]   # 不直接对外，由 caddy 反代

  web:
    build: ./web
    restart: unless-stopped
    environment:
      INTERNAL_API: http://bot:8080
      NEXT_PUBLIC_BOT_USERNAME: ${BOT_USERNAME}
      NEXT_PUBLIC_TURNSTILE_SITE_KEY: ${TURNSTILE_SITE_KEY}
    ports: ["127.0.0.1:3000:3000"]

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports: ["127.0.0.1:8090:80"]      # CF Tunnel 进这个端口
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddydata:/data

volumes:
  pgdata:
  redisdata:
  caddydata:
```

### 7.2 Caddyfile

```
:80 {
    handle_path /webhook/* {
        reverse_proxy bot:8080
    }
    handle /api/* {
        reverse_proxy bot:8080
    }
    handle /verify/* {
        reverse_proxy web:3000
    }
    handle {
        reverse_proxy web:3000
    }
}
```

### 7.3 公网入口

CF Tunnel（在我本机的 cloudflared）加 ingress：
```yaml
- hostname: guard.misaka.si
  service: tcp://23.80.90.86:8090
```
或在 23.80.90.86 自己装 cloudflared 接 misaka.si tunnel（推荐，避免本机做中转）。

## 8. 安全

- **Webhook secret**：Telegram setWebhook 带 `secret_token`，bot 校验 `X-Telegram-Bot-Api-Secret-Token` header
- **Telegram Login**：校验 hash + auth_date < 1h，仅允许 `super_admin_telegram_ids` 列表中的用户首次登录
- **JWT**：HS256，48h 过期
- **数据库密码**、redis 密码、bot token 仅在 `.env`，git ignore
- **CSRF**：API 走 Bearer token，不依赖 cookie
- **SQL 注入**：sqlc 生成参数化查询
- **fail2ban**：22456 端口已有保护，无新增暴露

## 9. 性能预期

- bot 启动 < 100ms（Go 静态编译）
- 单条消息处理 < 5ms（命中过滤器后）
- 验证按钮响应 < 50ms
- 5k 人群理论吞吐：> 1000 msg/s（telegram 自身速率上限远低于此）
- 内存占用：bot ~50MB，web ~150MB，postgres ~100MB，redis ~30MB
- 总计 < 500MB（机器 5.8G，富裕）

## 10. 里程碑

| 阶段 | 内容 | 负责 |
|---|---|---|
| M0 | 设计文档（本文） | Claude |
| M1 | 项目骨架 + docker-compose + 数据库 schema | Codex |
| M2 | Bot 核心：webhook + 命令路由 + 新人验证（button） | Codex |
| M3 | 反广告过滤器 + 警告系统 + CAS 同步 | Codex |
| M4 | REST API + Telegram Login | Codex |
| M5 | Next.js 面板：登录 + 群列表 + 配置编辑 + 日志 | Codex |
| M6 | Turnstile 验证流程 | Codex |
| M7 | Claude 审查 + 修 bug + 优化前端审美 | Claude |
| M8 | 部署 + CF Tunnel + 接入第一个群 | Claude |
