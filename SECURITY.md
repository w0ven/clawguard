# 🔒 安全披露

如果你发现 ClawGuard 有安全问题，请**不要**开公开 GitHub Issue。

## 怎么报告

请给仓库 owner [w0ven](https://github.com/w0ven) 发 GitHub 私信，或通过 GitHub Security Advisory 提交。

报告里尽量带上：

- 影响范围（未授权访问、审核绕过、密钥泄漏、RCE 等）
- 复现步骤
- 你使用的版本 / 镜像标签（例如 `sha-92b751a`）
- 是否已在公开群里被利用

## 我们会怎么处理

- 确认后优先修，再公开细节
- 需要的话发新的 `sha-<commit>` 镜像
- 感谢负责任的披露

## 部署时请自己守住的边界

- 生产必须使用 HTTPS `PUBLIC_BASE_URL`
- `JWT_SECRET`、`WEBHOOK_SECRET`、`ENCRYPTION_KEY` 各用独立的长随机值
- `ENCRYPTION_KEY` 与数据库一起备份；轮换后旧 LLM Key 无法解密
- Compose 端口只绑 `127.0.0.1`，公网走 Tunnel / 反代
- 不要把 `.env`、备份文件、数据库 dump 提交进 git
