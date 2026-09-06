# 🛠️ 本地开发

面向要改代码、跑测试、或自己构建镜像的人。生产部署请看仓库根目录 [README.md](../README.md)。

## 环境

| 组件 | 版本 |
| :--- | :--- |
| Go | 1.26（toolchain 1.26.6，见 `bot/go.mod`） |
| Node.js | 22 |
| PostgreSQL | 16 |
| Redis | 7 |

模块路径是 `github.com/openclaw/clawguard`，和 GitHub 仓库 `w0ven/clawguard` 不是同一个名字，改 import 时不要跟仓库 URL 搞混。

## Bot

```bash
cd bot
go test ./...
go vet ./...
go test -race ./internal/bot ./internal/api ./internal/config ./internal/worker
```

需要真实 PostgreSQL 时：

```bash
go run ./cmd/migrate
go test -tags=integration -timeout 180s ./internal/store ./internal/api
```

开发模式默认 `APP_ENV=development`，允许 HTTP 的 `PUBLIC_BASE_URL` 和较短密钥。生产校验更严。

进程会依次尝试 `.env`、`../.env`、`../../.env`。在 `bot/` 下启动时，仓库根目录的 `.env` 能被读到。

```bash
cd bot
go run ./cmd/migrate
go run ./cmd/clawguard
```

本地最少要有 PostgreSQL、Redis，以及 `BOT_TOKEN`、`BOT_USERNAME`、`TELEGRAM_LOGIN_BOT_USERNAME`、`WEBHOOK_SECRET`、`PUBLIC_BASE_URL`、`POSTGRES_*`、`REDIS_HOST`、`JWT_SECRET`。

## Web

```bash
cd web
npm ci
npx tsc --noEmit
npm run dev
```

开发服务器默认 `:3000`。管理 API 走 bot 的 `:8080`。

浏览器冒烟：

```bash
npx playwright install --with-deps chromium
npm run test:e2e
```

`NEXT_PUBLIC_BOT_USERNAME` 和 `NEXT_PUBLIC_TURNSTILE_SITE_KEY` 在 **构建镜像时** 打进 bundle。本地 `next dev` 读当前环境；生产镜像要在 GitHub Actions 的 Repository Variables 里配置。

## 自己构建镜像

官方镜像命名空间是 `kelework/clawguard-bot` 和 `kelework/clawguard-web`。Fork 后请改成你的 Docker Hub 用户名，或本地构建：

```bash
docker build -t clawguard-bot:local ./bot
docker build \
  --build-arg NEXT_PUBLIC_BOT_USERNAME=your_bot \
  --build-arg NEXT_PUBLIC_TURNSTILE_SITE_KEY= \
  -t clawguard-web:local ./web
```

然后把 `docker-compose.yml` 里的 `image:` 临时改成这两个本地标签，或用 compose override。当前官方镜像只发布 `linux/amd64`。

## 数据库迁移

goose 文件在 `bot/migrations/`，当前到 `00031`。容器入口会先跑 `migrate` 再启动 bot，一般不必手跑 SQL。

不要依赖 goose `down` 做生产回滚。回滚靠备份 / PITR，见 [restore-rehearsal.md](./restore-rehearsal.md)。
