# ClawGuard 群助手后端 API 契约

状态：后端实现契约（迁移 `bot/migrations/00032_group_assistant.sql`）。本文件描述真实服务端行为，不描述原型模拟器。

## 1. 通用约定

- Base URL：`/api/admin/groups/{chat_id}/assistant`。
- 所有接口要求现有管理员 JWT；写请求沿用现有 CSRF 机制。服务端以管理员 JWT 的群 scope 和数据库 `authorized_groups.enabled` 校验 `{chat_id}`，不接受客户端传入的替代身份。
- 不在响应中返回 provider `base_url`、API key、额外认证 header 或其他群的数据。
- 新群助手默认没有策略行，等价于 `chat_enabled=false`、`learning_enabled=false`；不因此调用模型、不写入聊天/记忆。它不改变既有 GuardPolicy、`ai_decisions`、`user_trust` 或审核模型路由。
- 所有成功写配置/记忆的请求均支持 CAS：首次创建 `expected_version: 0`，更新必须使用当前 `version`。版本不匹配返回 `409`，不会覆盖其他管理员的更新；资源不存在或不属于当前群返回 `404`。
- `valid_scope` 在记忆写入前由服务端规范化为 `today`、`this_week`、`this_month`、`current_group`、`long_term`、`weekly` 或 `retention_window`，并将 `expires_at` 截断到该范围边界；未知范围拒绝，不把任意文本默认为长期。
- 普通群消息原文只保留策略指定的 `retention_days`（默认 7，允许每群 1–30 天）；长期只保存提炼事实及必要出处摘要。忘记只移出本地召回/检索，不承诺删除 Telegram 远端消息。
- 错误响应统一至少含 `{ "error": "..." }`。常见状态：`400` 请求/字段无效，`401` 未登录，`403` scope/CSRF 拒绝，`404` 群或资源不存在，`409` CAS/事实冲突，`500` 存储或内部错误，`503` 既有 readiness 语义。

## 2. 设置

### `GET /api/admin/groups/{chat_id}/assistant`

返回：

```json
{
  "policy": {
    "chat_id": -1001, "version": 0, "chat_enabled": false,
    "learning_enabled": false, "trigger_mode": "mention_or_reply",
    "followup_window_sec": 300, "max_followup_turns": 5,
    "chat_model_ref": "provider:model", "learning_model_ref": "provider:small-model",
    "temperature": 0.3, "system_prompt": "", "history_limit": 30,
    "retention_days": 7,
    "collection_policy": "history_7d_and_long_term_summary",
    "tool_allowlist": ["knowledge_query", "conversation_recall", "webfetch_readonly"],
    "allow_domains": [], "max_queue_depth": 10, "max_queue_wait_sec": 15
  },
  "model_pool": { "version": 0, "strategy": "primary-overflow", "config": { "task_assignments": {}, "endpoints": [] } },
  "defaults": { "disabled": true, "retention_days": 7, "history_retention": "7 days", "remote_quota": "unknown" }
}
```

没有持久化行时返回 version `0` 的默认值，不自动创建。

### `PUT /api/admin/groups/{chat_id}/assistant`

请求字段与 `policy` 相同，另加 `expected_version`。`trigger_mode` 只能是 `mention_or_reply` 或 `mention_only`；system prompt 最长 8000 字符；工具只能是三个只读工具；`retention_days` 为 1–30。非空 `chat_model_ref` 必须是当前 registry 中 enabled 且 `SupportsTools=true` 的已有模型，learning 引用必须是 enabled 的已有模型。不能用请求体指定 provider URL/key。

### `GET/PUT /api/admin/groups/{chat_id}/assistant/model-pool`

PUT 请求：

```json
{
  "expected_version": 0,
  "strategy": "primary-overflow",
  "task_assignments": {
    "chat": { "primary": "ep-main", "backups": ["ep-backup-tools"] },
    "learning": { "primary": "ep-learn", "backups": ["ep-backup-text"] }
  },
  "endpoints": [
    {"id":"ep-main","name":"主模型","model_ref":"provider:model","role":"primary","priority":0,"max_concurrency":3,"timeout_ms":15000,"cooldown_duration_sec":30},
    {"id":"ep-backup-tools","model_ref":"other:gpt","role":"backup","priority":1,"max_concurrency":4,"timeout_ms":15000,"cooldown_duration_sec":30},
    {"id":"ep-learn","model_ref":"provider:small","role":"primary","priority":0,"max_concurrency":2,"timeout_ms":15000,"cooldown_duration_sec":30},
    {"id":"ep-backup-text","model_ref":"other:text","role":"backup","priority":1,"max_concurrency":2,"timeout_ms":10000,"cooldown_duration_sec":60}
  ],
  "max_queue_depth": 10,
  "max_queue_wait_sec": 15
}
```

服务端只保存已有 model/provider 引用；endpoint 的 `supports_tools` 以 registry 实际能力为准。`chat` 主/备用全部必须支持 tools；`learning` 可使用纯文本模型。策略为 `primary-overflow`：主端点可用且有本地容量时优先；容量满、429 冷却或故障时按 backups 顺序选择兼容端点；全部满则进入有界队列，超时返回结构化失败。已取得主端点但本次请求随后遇到 `429`、`5xx`、网络故障或该端点超时时，若尚未产生工具调用/答案，当前请求会在共享 30 秒回退预算内按 priority 尝试尚未尝试的兼容备用；相同 provider/model 只尝试一次。每次实际尝试均记录端点、原因和结果，失败端点先释放 lease 并更新 cooldown/Retry-After/unhealthy；认证、参数等不可恢复错误不分流。工具调用一旦产生，工具多回合持有同一 endpoint，不在工具执行后重启到另一个 endpoint。模型引用的进程级共享上限按当前所有 `chat_enabled` 或 `learning_enabled` 群池的有效配置动态取最小并发；停用低配群或提高其配置后，后续请求可恢复上限，不由 GET/status 永久下调或修改持久化配置。

### `GET /api/admin/groups/{chat_id}/assistant/status`

返回每个端点的本地真实 `current_active`、动态计算的共享 `max_concurrency`、`is_full`、`status`（`unknown`、`healthy`、`cooldown`、`unhealthy`、`half_open`）、冷却剩余、最近错误和队列深度。该 GET 只读，不创建/修改 runtime、策略或共享 cap；共享 cap 只依据当前启用群池计算，移除低配群或更新限额后可恢复。`remote_quota_observed` 固定如实标注远程额度未知；不会伪造余额、usage 或限额数字。冷却结束允许一次 half-open 重试，不在未观测前宣称探活成功。

### `GET /api/admin/groups/{chat_id}/assistant/tools`

返回三项工具及当前启用状态；`write_tools` 始终为空，`server_bound_scope=true`。工具执行身份、group scope、保留期由服务器绑定，模型不能指定其他群或其他成员私密状态。

## 3. 记忆、冲突和历史

### `GET /memories?q=&include_inactive=&limit=`

列出当前群的基础/学习/待处理记忆和来源摘要。默认仅返回 `active=true` 且未过期记录。

### `POST /memories`

管理员预置基础事实。请求：`subject`、`content`、`valid_scope`、可选 `expires_at`、`source_type=admin_base`、可选 `source_snippet`。Telegram 消息来源只能由服务器在真实 update 中生成；客户端若提供 `source_message_id`（POST/PUT）直接返回 `400`，不会静默忽略。服务端从 JWT 注入 operator ID，强制 `memory_type=base`、`authority_level=admin_base`，不会信任客户端的管理员身份字段。

### `GET /memories/{id}`、`GET /memories/{id}/versions`

返回当前群内资源及版本链、来源摘要。记忆本身及其版本链的跨群/不存在 ID 均返回 `404`。

### `PUT /memories/{id}`

请求为当前记忆字段加 `expected_version`。服务端以 `(id, chat_id, expected_version)` 严格限定更新，客户端不能用其他群的 ID 读写当前群资源；跨群/不存在返回 `404`，版本不匹配返回 `409`。服务端强制本次管理员更正使用 `memory_type=base`、`authority_level=admin_explicit`，并在事务中 CAS 更新和追加版本。编辑已有 Telegram 来源时，服务端保留已保存的 `source_message_id/source_chat_id/source_content_hash` 并在同一事务锁定校验；来源被编辑、撤销或哈希不可验证返回 `409`，不会置空来源绕过校验。客户端不能通过请求体覆盖来源 ID。

管理员记忆请求的 `valid_scope` 会先规范化和截断 `expires_at`，因此响应中的范围/截止时间可能与输入的别名或更晚时间不同。

### `POST /memories/{id}/forget`

将记录从 active 召回、有效期和检索范围移除，保留最小变更审计/版本链；响应明确 `remote_telegram_deleted=false`。重复忘记返回 404，不会自动复活。

### `GET /conflicts?status=pending`

列出同级权威或时效不明的待处理候选事实及出处。

### `POST /conflicts/{id}/resolve`

拒绝请求为 `{ "accept": false }`；接受必须明确请求 `{ "accept": true, "expected_memory_version": N, "resolution_mode": "admin_explicit_correction" }`。仅当前群管理员 scope 可调用；资源被忘记/失活/过期返回 `409`，关联记忆版本改变返回 `409`，不能清理 forgotten 墓碑或复活记忆。普通自动候选不能静默降低或覆盖 `admin_base`/`admin_explicit`/`pinned_announcement` 权威。

显式接受是单独的人工纠正动作：响应/记忆来源记录真实管理员 `source_operator_id/name`、`source_type=admin_conflict_accept` 和候选的原始 `source_message_id/source_chat_id/source_snippet`，不会把普通成员消息伪造成管理员原文。拒绝只标记候选，不把普通成员内容升级为权威。

### `GET /history?q=&thread_id=&sender_id=&limit=`

只返回当前群、已审核、已送达且仍在保留期的历史消息。`q` 是大小写不敏感的普通字面子串搜索，不是 SQL `LIKE` 模式，通配符不会扩大查询范围。聊天上下文在运行时按 `chat_id + thread_id + sender_id` 隔离追问；编辑消息只更新来源行，不重新触发聊天或学习。

### `GET /dispatches?limit=`

返回模型池真实调度记录（任务、端点、原因、状态、延迟和经过裁剪的错误），不含密钥或提示全文。

## 4. Telegram 到模型的真实流程

1. 既有授权、冻结、sender-chat、删除账号、静态过滤、关键词回复和审核顺序先执行；`applyAIModeration` 产生显式 eligible/blocked/unknown 结果。审核异常/状态未知不会进入群助手。
2. 只有管理员明确启用 chat/learning 后，已通过审核的文本才写入独立历史；拦截内容不写入。关键词已经发送回复时不会再触发模型双回复。
3. chat 仅响应 `@Bot`、回复 Bot，或同一 `chat_id/thread_id/user_id` 在窗口和轮次内的连续追问；不主动插话。事实、历史都以不可信数据块注入用户消息上下文，和 system 指令隔离。
4. 聊天经工具能力硬门槛的 primary-overflow pool 进入最多四轮工具回合；三个工具分别是本群有效公开知识、当前群保留期历史、白名单公开 URL 正文截断读取。shell、处罚、群设置写工具不存在。
5. 发送成功才记录 assistant delivery；学习任务进入独立有界后台队列。学习模型必须通过 provider 请求并返回严格 JSON：事实有 subject/content/valid_scope/source_quote；来源引文不在原文中、超长、过期或解析不合法时丢弃。自动学习只能产生 `learned_fact`，与管理员/置顶权威冲突进入 pending。
6. pinned 消息只在 Telegram update 能验证当前群管理员或关联频道 `LinkedChatID`、当前 `PinnedMessage` 时才提升为 `pinned_announcement`；无法验证为 unknown，不升权。召回前再次核验置顶仍存在，取消置顶后不伪造为当前权威。

## 5. 网络技能边界

`webfetch_readonly` 仅 GET；scheme/host/domain allowlist、无 userinfo、显式端口仅允许 80/443（其他端口拒绝并保留 80/443 端口语义）；allowlist 只接受多标签域名，不接受 IP 或单标签域名；DNS 解析后的每个地址必须为公网地址，拒绝 loopback/private/link-local/metadata/shared、NAT64/6to4/Teredo 及嵌入私网 IPv4 的保留地址；连接使用已解析 IP，重定向每跳重新校验并禁用代理；不转发认证 cookie/header；10 秒超时、文本/JSON/HTML/XML 内容类型限制（空 `Content-Type` 也拒绝）、正文最多 2 MiB、最多 3 跳并返回 `truncated`。失败作为结构化工具结果回注，不假称成功。
