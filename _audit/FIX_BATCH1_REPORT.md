# FIX_BATCH1_REPORT

生成时间：2026-04-25 08:26 GMT+8

## 修复清单

### P0-8 — 回复匿名/频道消息执行管理命令 nil panic + handler recover
- Commit: `61af5cc4f308bd6741a3fe89b17f3c129a55ccce`
- 改动文件：`bot/internal/bot/bot.go`
- 修复内容：回复目标缺少 `Sender` 时返回明确错误；所有 Telegram handler 统一经 `runHandler` recover；`Settings.OnError` 统一 zap 记录；`ProcessUpdate` 兜底 recover。
- 回归风险点：handler 包装层改变所有 update 的错误/异常路径；panic 会被记录并吞掉，单条 update 不再向 webhook 返回 500。

### P1-24 — 管理面板状态改变 API CSRF 防护
- Commit: `f2257fca1811706a959db4ca4011bd3929114200`
- 改动文件：`bot/internal/api/auth.go`、`bot/internal/api/routes_admin.go`、`bot/internal/api/routes_public.go`、`web/app/auth/magic/route.ts`、`web/lib/api.ts`
- 修复内容：登录/magic exchange 生成 `cg_csrf`；`/api/admin/*` 非安全方法要求 `X-CSRF-Token` 与 cookie 双提交匹配；管理 cookie 改为 `SameSite=Strict`；前端 `apiFetch` 自动带 CSRF header。
- 回归风险点：现有已登录会话没有 `cg_csrf` 时，状态改变请求会 403，需要重新登录；纯 Bearer 且无 cookie 的非浏览器调用不要求 CSRF。

### P1-25 — 非 owner 管理员创建管理员
- Commit: `43f56ae72d9d88f08272448e32f14d06e3f5cf69`
- 改动文件：`bot/internal/api/routes_admin.go`
- 修复内容：`POST /api/admin/admins` 后端强制 `requireOwner`，与更新/删除管理员保持一致。
- 回归风险点：普通 admin 即使有全局 scope 也不能再创建管理员；UI 已按 owner 控制，预期影响仅限绕过 UI 的 API 调用。

### P1-26 — 未授权群内命令回复
- Commit: `2caa61c082377a19791d3f7e513b205f39987ad1`
- 改动文件：`bot/internal/bot/bot.go`
- 修复内容：所有群内命令入口统一经过 `ignoreUnauthorizedGroupCommand`；未授权群静默返回；私聊命令保持可用；系统 frozen 时群内命令静默忽略。
- 回归风险点：授权检查依赖异常时群内命令会静默忽略；冻结状态下群内命令不再响应。

### P1-27 — 管理员豁免正缓存过长
- Commit: `8e12a0f0d9bd0b629eca463d577e3bc5664c8c69`
- 改动文件：`bot/internal/bot/moderation.go`
- 修复内容：`isChatAdmin` 只使用/写入非管理员负缓存；发现旧的正缓存 `1` 会删除；管理员正结果每次实时查 Telegram。
- 回归风险点：管理员消息/命令会增加 Telegram `getChatMember` 调用量；非管理员仍保留 10 秒负缓存。

### P1-28 — `/config` 未校验 group_scope
- Commit: `beeecfac0551f0077530cd1eff07a63d4bcdc16c`
- 改动文件：`bot/internal/bot/admin_command_config.go`、`bot/internal/api/routes_public.go`
- 修复内容：群内 `/config` 先校验授权群、Telegram 群 admin、面板 admin 以及 `group_scope`；越权群返回最小拒绝提示；magic exchange 对 `ScopeChatID` 再次校验授权群和 admin scope。
- 回归风险点：scoped admin 在未授权或越权群无法再获取/兑换该群 magic link；magic token 兑换可能因 scope 变更变为 403。

### P1-30 — 注销不吊销 JWT
- Commit: `585c3d9d9c80d618cbc99262b67ae04bc3040572`
- 改动文件：`bot/internal/api/auth.go`
- 修复内容：新签发 JWT 增加 `jti`；JWT 校验时查询 Redis revocation key；logout 将当前 `jti` 写入黑名单，TTL 等于 token 剩余寿命，并清理 auth/csrf cookie。
- 回归风险点：旧版无 `jti` 的 JWT 会被拒绝，需要重新登录；Redis 不可用时 JWT 校验会失败以避免绕过吊销检查。

## 最终验证结果

- `cd bot && go build ./...`：通过
- `cd bot && go vet ./...`：通过
- `cd bot && go test ./... -count=1 -timeout 180s`：通过

## SQL 同步检查

- 本批未修改 SQL、sqlc 查询或 `*.sql.go` 结构体/Scan 顺序。
