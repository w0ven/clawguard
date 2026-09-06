# 数据库备份恢复演练

在放置 Compose 与 `.env` 的目录定期跑恢复演练，确认最近 PostgreSQL 备份能恢复到临时库并完成基础校验。

脚本默认从生产备份目录 `/var/backups/clawguard` 查找最新的 `.sql`、`.sql.gz`、`.dump`、`.dump.gz` 文件；也可以显式指定备份路径。

```bash
cd /path/to/clawguard
bash scripts/restore-rehearsal.sh

# 或指定备份文件
BACKUP_PATH=/var/backups/clawguard/clawguard-20260521.dump bash scripts/restore-rehearsal.sh
```

脚本会启动独立的临时 PostgreSQL 容器，容器名和库名都带 `restore_rehearsal_` 前缀，不连接 Compose 中的生产 `postgres` 服务，也不会 drop/truncate 生产库。

演练报告包含：

- backup path
- 恢复方式
- migration version
- `groups`、`admins`、`ai_decisions`、`violations`、`config_audit`、`user_trust` 表行数
- 最终 `PASS`/`FAIL`

默认结束后会清理临时容器；排查失败时可保留现场：

```bash
KEEP=1 BACKUP_PATH=/path/to/backup.dump bash scripts/restore-rehearsal.sh
```

如果远程备份目录不是 `/var/backups/clawguard`，可以指定搜索目录：

```bash
BACKUP_DIR=/mnt/offsite/clawguard bash scripts/restore-rehearsal.sh
```
