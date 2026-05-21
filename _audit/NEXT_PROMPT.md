# ClawGuard 接力状态（2026-05-21 收尾）

当前策略：可乐确认「剩下的只做必要的」。不要再按旧 P2/P3 backlog 自动扩展。

已完成必要项：
- `scripts/prod-smoke.sh`：生产 smoke 检查脚本，远程 `/root/clawguard` 实跑 PASS。
- `scripts/restore-rehearsal.sh`：DB 备份恢复演练脚本，真实旧备份 `backups/clawguard-20260420-050542.sql.gz` 实跑 PASS。
- restore rehearsal 已修复 plain SQL dump `OWNER TO clawguard` 角色兼容和 Bash `$@` 正则展开 bug。
- 最新相关 commits：`e2f18b6`, `5122ac9`, `72211bf`, `b37b59c`。

暂停项（除非真实生产问题/明确需求）：media group 聚合、telebot 升级、贴纸转码、事件表大统一、人工复核、shadow mode、Action dispatcher 重构、CI Node 20 warning。

继续规则：只处理真实故障、安全风险、备份/恢复/部署验证类必要项。
