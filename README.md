# ClawGuard

ClawGuard 是一个面向 Telegram 群组的管理 Bot，目标是提供新人验证、反广告、审计日志和后续 Web 控制台能力。当前仓库完成了 M1 的基础骨架：容器编排、数据库 schema、Go bot/API 入口、按钮式新人验证链路和 Next.js 占位前端。

## 技术栈

- Go 1.22 + `gopkg.in/telebot.v3`
- Echo v4
- PostgreSQL 16
- Redis 7
- sqlc + pgx/v5
- goose migrations
- Next.js 15 + TypeScript + Tailwind CSS
- Caddy 2
- Docker Compose

## 本地启动

1. 确认根目录 `.env` 已存在并填好所需变量。
2. 启动容器：

```bash
docker compose up -d --build
```

3. 首次部署后，在 bot 容器内或宿主机执行数据库迁移。

## 部署到 `23.80.90.86`

```bash
rsync -avz --delete /root/.openclaw/workspace/projects/clawguard/ root@23.80.90.86:/root/clawguard/
ssh root@23.80.90.86 'cd /root/clawguard && docker compose up -d --build'
```

Cloudflare Tunnel 应指向宿主机 `127.0.0.1:8090` 对应的 `23.80.90.86:8090` 入口。

## 当前进度

- M1：已完成
- M2-M8：待办
