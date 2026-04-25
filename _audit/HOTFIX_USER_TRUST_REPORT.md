# HOTFIX_USER_TRUST_REPORT

## 时间
- 2026-04-25 09:39 GMT+8 起处理

## 问题
- 管理面板信任系统接口返回 `count user trust failed`。
- 最近 scoped admin 修复为 `CountUserTrust` / `ListUserTrustPaginated` 增加了 `scopeGlobal bool, scopeChatIDs []int64` 和 `chat_id = ANY($n::BIGINT[])` 范围过滤。

## 根因
- `bot/internal/store/queries/user_trust.sql` 中的 `CountUserTrust` 已包含 scoped 条件：`AND ($7::BOOLEAN OR chat_id = ANY($8::BIGINT[]))`。
- 但生成后的 `bot/internal/store/user_trust.sql.go` 中 `countUserTrust` 常量缺少该 scoped 条件，同时 Go 调用仍传入 8 个参数，导致运行时 SQL 参数数量与 SQL 占位符不一致，触发 `count user trust failed`。
- 同时，scoped 查询直接把 `[]int64` 传给 `ANY($n::BIGINT[])` 风险较高，补丁显式使用 pgx 的 `pgtype.FlatArray[int64]` 包装 bigint 数组参数，避免数组编码不稳定。

## 修复方式
- 补齐 `bot/internal/store/user_trust.sql.go` 中 `countUserTrust` 的 scoped SQL 条件，保持与 SQL 源文件一致。
- 新增 `bot/internal/store/pg_array.go`，通过 `pgInt64Array([]int64)` 将 scoped chat IDs 转为 `pgtype.FlatArray[int64]`。
- `CountUserTrust` 和 `ListUserTrustPaginated` 都改为传入 `pgInt64Array(...)`，不再直接传 `[]int64` 到 `ANY($n::BIGINT[])`。
- scoped admin 仍由数据库层执行 `($scopeGlobal OR chat_id = ANY($scopeChatIDs::BIGINT[]))`，不会泄漏全局 count；空 scoped list 在非全局权限下返回 0 行/0 count。

## 改动文件
- `bot/internal/store/user_trust.sql.go`
- `bot/internal/store/pg_array.go`
- `_audit/HOTFIX_USER_TRUST_REPORT.md`

## 验证结果
- `cd bot && go build ./...`：通过
- `cd bot && go vet ./...`：通过
- `cd bot && go test ./... -count=1 -timeout 180s`：通过
