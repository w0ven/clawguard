# ClawGuard

**企业级 Telegram 群管理 Bot** — 新人验证 · 反广告 · AI 审核 · Web 控制台

ClawGuard 为 Telegram 群组（当前服务 RFC IDC 群 ~5k 人）提供全套管理能力：从入群验证、关键词/链接过滤到 LLM 驱动的智能审核，所有功能均可通过 Web 面板可视化配置。

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
- **成本控制**：每日预算上限、每用户调用限制、消息缓存、短消息跳过、批量合并
- **人审闭环**：AI 判决可通过 Web 面板确认/标记误判，持续优化准确率
- **Prompt 编辑器**：可视化编辑系统提示词、自定义规则，支持版本管理和在线测试

### ⚠️ 警告系统
- 可配置最大警告次数、超限动作和警告衰减天数
- 警告记录持久化，支持群级和用户级查询

### 🌐 Web 管理面板
- **Telegram Login** 登录 + JWT 鉴权
- **仪表盘**：群数、今日违规、AI 成本、信任用户数概览
- **群管理**：群列表、单群配置编辑（验证/过滤/AI 策略等）
- **违规日志**：传统违规 + AI 判决审核队列
- **信任管理**：用户信任状态筛选、手动标记
- **AI 成本统计**：按日/群/模型的成本分析
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
│   │   └── worker/              # 后台任务（验证过期/预算重置/LLM探测/日报等）
│   ├── migrations/              # 数据库迁移（goose，16 个版本）
│   └── sqlc.yaml
├── web/                         # Next.js 前端
│   ├── app/                     # App Router 页面
│   │   ├── dashboard/           # 仪表盘
│   │   ├── groups/              # 群管理
│   │   ├── violations/          # 违规日志
│   │   ├── ai-review/           # AI 审核队列
│   │   ├── ai-costs/            # AI 成本统计
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

```bash
# 同步代码
rsync -avz --delete ./ root@YOUR_SERVER:/root/clawguard/

# 远程构建并启动
ssh root@YOUR_SERVER 'cd /root/clawguard && docker compose up -d --build'
```

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
| BudgetReset | 每日 AI 预算重置 |
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
| M1 | 项目骨架 + Docker Compose + 数据库 Schema | ✅ 已完成 |
| M2 | Bot 核心：Webhook + 命令路由 + 新人验证 | ✅ 已完成 |
| M3 | 反广告过滤器 + 警告系统 + CAS 同步 | ✅ 已完成 |
| M4 | REST API + Telegram Login + Web 面板骨架 | ✅ 已完成 |
| M5 | AI 审核 + 信任系统 + 多模型 + Prompt 编辑器 | ✅ 已完成 |
| M6 | Turnstile 验证流程 | ✅ 已完成 |
| M7 | 审查优化 + Bug 修复 | 🔄 进行中 |
| M8 | 生产部署 + CF Tunnel + 首群接入 | 🔄 进行中 |

---

## 📄 许可

私有项目，未授权禁止使用。