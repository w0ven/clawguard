# Smart_Group_Bot 源码归属

来源：Smart_Group_Bot，提交 `82c3703daba218b36255132c9bf51ebc444c6480`。
版权：Copyright (c) 2025 Sanite&Ava，MIT（同目录 `LICENSE`）。

- `persona.md`、`decision.md`、`compress.md`、`reply_mode.md`、`style_distill.md`：对应源 `prompt/` 同名文件正文原样保留。
- `casual.md`、`proactive_topic.md`：仅适配 ClawGuard 事实/摘要区块名称、只读群查询与域名白名单。
- `skill_tools_v2.md`：源正文按现有授权工具集适配；不引入写记忆、规则管理、搜索或其他供应商服务。保留相关技能判断、结果反馈、多轮及媒体防重复规则。
- Go 移植：`assistant_reply_output.go` 对应 `bot/services/reply_output.py`、`handlers/group.py::_next_pending_reply_flush_at`；`assistant_reply_delivery.go` 对应 reply plans、`ReplyModeService`；`assistant_context.go`、`assistant_recall.go` 对应 `memory.py` token 裁剪、混合召回和索引卡片；`group_assistant.go::dispatchTools` 对应 `skills/service.py::answer_with_skill` 的授权技能循环。

有意保留 ClawGuard 差异：审核后才可读；论坛话题隔离；用户显式开关/Prompt 优先；普通群聊自动学习及来源权威/有效期；PostgreSQL 持久词法与向量数组；现有 Telegram 纯文本发送适配（不新增 SGB 富消息网页服务）；全局/群共享 registry 模型负载。不是无差异复制。
