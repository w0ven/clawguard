# 定时发送与删除的故障恢复

从数据库迁移 055 起，自动定时发送在调用 Telegram 前记录任务及计划触发时点。相同任务和时点只认领一次，重启后不会自动重发该时点；后续独立时点仍按原调度周期运行，不补发停机期间错过的周期。从迁移 056 起，手动“立即发送”必须携带 `Idempotency-Key`：同一发送意图的双击、丢响应和网络重试复用原键，不会重新发送；只有明确的新发送意图使用新键。管理员、群、任务和发送配置与原键绑定，不匹配返回 409。执行中或结果不明不能自动换键重发；明确失败时，界面提供需确认的重新尝试。

前端将手动发送键保存在当前浏览器，清除存储或更换设备不能恢复原键；应先查群记录和后台结果，不按时间邻近推断是否重复。API 状态为 `sent`、`dispatching`、`unknown`、`failed`，仅 `sent` 返回已确认的 `message_id`。

Telegram 不支持这个接口的端到端幂等键，不能保证 exactly-once：`dispatching` 可能是发送前崩溃，也可能是 Telegram 已接收而本地未保存返回。`uncertain` 表示调用结果无法证明；未识别的 Telegram 错误也会保守归入该类。操作员应在相应群内核查，不要自动重发或假定已发送。只读排查示例（生产库权限与操作由运维控制）：

```sql
SELECT id, scheduled_message_id, chat_id, scheduled_at, state,
       telegram_message_id, created_at, updated_at, error
FROM scheduled_dispatch_attempts
WHERE state IN ('dispatching', 'uncertain')
ORDER BY created_at DESC;
```

只有确认的 Telegram 消息 ID 才能写入成功及相应的自动删除任务，二者同事务。若 Telegram 已发送但数据库提交失败，可能既无消息 ID 记录也无法创建自动删除任务；应根据群记录人工处理，不得伪造恢复。确认发送后的删除任务保留原群 ID 和消息 ID，租约过期可恢复，最多五次调用；`failed` 需要人工检查权限、消息是否已删除及错误信息：

```sql
SELECT id, attempt_id, chat_id, telegram_message_id, state, attempts,
       delete_at, lease_until, last_error
FROM scheduled_delete_jobs WHERE state = 'failed' ORDER BY id DESC;
```

删除本身可能已在 Telegram 执行而响应丢失：同一群和消息的重试可能因消息已不存在得到错误，因此失败状态并不必然证明消息仍在。升级前存在于旧进程内存中的延迟删除计时器没有持久信息，升级或重启后无法恢复。现有按用户配置的留存和每任务运行历史裁剪策略不变；新派发记录和删除任务不会自动清空。

迁移 055、056 的 Down 保留安全记录，重新 Up 可重复执行；但是旧版 Bot 不读取这些记录，也不会处理新的持久删除任务。若必须回滚至旧版，先由运维核查未知派发并处理待删除任务，不要把旧版运行视为具备新版本的防重与恢复保证。
