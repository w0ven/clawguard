# 🤝 贡献指南

谢谢你愿意改 ClawGuard。

## 怎么参与

1. Fork 仓库，从最新 `main` 开分支。
2. 只做一件事：一个 PR 解决一个问题。
3. 提交前跑相关测试。
4. 用能看懂的中文或英文写清「改了什么、为什么」。

## 测试

```bash
cd bot && go test ./...
cd web && npx tsc --noEmit
```

动到审核、过滤、配置保存时，补回归测试，不要只改行为。

更完整的本地步骤见 [docs/development.md](./docs/development.md)。

## 范围

这个项目已经过一轮生产审查。默认不要顺便做：

- 把 bot 扩成多副本
- 拆 `moderation.go` 之类的大重构
- media group 聚合、贴纸转码、shadow mode

除非 Issue 里明确要做，或你先开 Issue 说清楚收益。

## 安全

漏洞、密钥、可被利用的审核绕过，走 [SECURITY.md](./SECURITY.md)，不要开公开 Issue。
