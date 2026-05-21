# ClawGuard 最终审查 Findings 清单

> 作者最后一次大更新，零 bug / 零逻辑错误目标。
> 本清单串行累加：每轮 Codex 审查完 → Append 进本文件 → 进入下一轮。
> 修复阶段（全部审完后）按 P0→P1→P2→P3 顺序一条一条改，每条单 commit。

## 严重度定义
- **P0 critical**：数据损坏 / 服务崩溃 / 安全漏洞 / 终态被覆盖
- **P1 high**：可复现的逻辑错误、安全隐患、数据不一致
- **P2 medium**：可用性 / 资源 / 观测性问题
- **P3 nitpick**：设计待确认、审计完整性

---

## 进度（2026-04-25 11:30 更新）

| 阶段 | 状态 | 说明 |
|---|---|---|
| 审查 | ✅ 完成 | 预审 + 01/02/03/04/05/06 全部跑完，清单已汇总 |
| P0 critical | ✅ 全部关闭 | P0-1～P0-8 已修复、验证、部署；P0-9 `.env.example` 由作者确认未泄漏，略过 |
| P1 high | ✅ 全部关闭 | P1-2～P1-32 已修复、验证、部署；P1-1 降级 P2 已随 Batch P2-B 修复部署 |
| P2 medium | ✅ 全部关闭 | A/B/C+/D+ 四批已部署；P2-3/5/6/9/10/11 经评估**不做**，理由见下 |
| P3 nitpick | ✅ 已关闭 | P3-1 随 P2-C+ 部署 |
| 生产部署 | ✅ 当前线上 | `dc749cdxxx`（D+ Caddy transport 定制收尾后），DB migration version 21，bot/web/caddy 正常；5 个安全头确认上线 |

### 评估后不做（决策记录 2026-04-25）
- **P2-3** 按钮变量渲染 Background ctx：渲染纯计算 + 几条 SQL，卡了大不了一条按钮没出来；优先级低。
- **P2-5** fallback warn batch 放大：batch 路径触发频率极低，多发几条 warn 不致命。
- **P2-6** 前端 100 条触发 100 次 reload：UX 抖动不影响数据，工时性价比低。
- **P2-9 / P2-10 / P2-11** migration Down 卫生：生产事故走 PITR，不是 goose down；硬补 Down 反而引入新风险。后续作为 dev 文档说明"不支持 down，回滚靠 PITR"。

### 已知小遗留（不阻断 release）
- `truncateString` / `truncateProfileCheckLogBio` 是 byte-based，500/200 字节切到中文 utf-8 中间会出乱码尾巴。全项目所有 truncate helper 同款 byte-based，是历史包袱。下次专项做 rune-safe 替换时一起改。

### 已部署批次（追加）
- **Batch P2-A**：P2-16 echo timeout / P2-17 HTTP transport / P2-7 turnstile idempotency / P2-19 API 限流 + 修补 `fix(P2-19): drop unused GET turnstile verify route`。
- **Batch P2-B**：P2-14 Telegram 主动发送限流（25/s + 18/min/chat + 429 重试）/ P2-15 scheduled message 重入保护 / P2-1 daily Add 失败回滚 / P2-2 RunNow 允许 paused / P1-1（已降级 P2）delayed delete goroutine 接 lifecycle ctx + Stop 等待。

### 已部署批次
- **P0 批次**：P0-1～P0-8。
- **Batch 1**：P0-8 + P1-24～P1-28/P1-30。
- **Batch 2A**：P1-2/P1-4/P1-5/P1-6/P1-17/P1-18/P1-19/P1-20/P1-29/P1-31。
- **Batch 2B1**：P1-7～P1-15。
- **Batch 2B2**：P1-3/P1-16/P1-21/P1-22/P1-23/P1-32，并追加主助手复核补丁：
  - `7306e9c`：quota 预占后不再误标 `flag-only`。
  - `781ad8f`：`status_changed_at` 先回填再 `NOT NULL`，避免重置历史 suspicious 30 天窗口。

### 当前收尾决策（2026-05-21）

可乐确认：后续只做必要项，不再继续扩展功能或大重构。

必要项已完成：
- 生产 smoke 脚本：`scripts/prod-smoke.sh`，已在远程 `/root/clawguard` 实跑 PASS。
- DB 恢复演练脚本：`scripts/restore-rehearsal.sh`，已用真实旧备份 `backups/clawguard-20260420-050542.sql.gz` 实跑 PASS。
- restore rehearsal 已修复 plain SQL dump `OWNER TO clawguard` role 兼容和 Bash `$@` 正则展开问题。

明确暂停 / 不做，除非后续出现真实生产问题：
- media group / album 聚合
- telebot 升级或自定义 update dispatch
- 贴纸 / 图片统一转码
- AI 判定表与 violations 事件表大统一
- 人工复核队列
- shadow mode / 策略灰度
- Action dispatcher 统一重构
- CI Node 20 warning（当前不影响构建）

后续若继续，只从真实故障或明确需求出发，不再按旧 P2/P3 backlog 自动扩展。

---

## 🔴 P0 (critical) — 必须修

### P0-1 Prober 重启 false recovery
- **来源**：预审
- **文件**：`bot/internal/ai/llm_prober.go:190`
- **问题**：内存 `failStreak` 重启清零，已 unhealthy 的模型会被短暂写回 healthy；下游会把请求路由到实际已挂的 channel
- **修复方向**：结合 `prev.Healthy` 判断；持久化 failStreak 到 Redis 或从 DB 读取上一次 health snapshot 做恢复判断

### P0-2 Scheduler 失败禁用吞错导致任务复活
- **来源**：预审
- **文件**：`bot/internal/bot/scheduler.go:203`
- **问题**：`MarkScheduledMessageFailed` 报错时只从内存 cron 移除，但 DB 还是 active，重启后任务复活
- **修复方向**：DB 标记必须先成功；失败时保持内存和 DB 一致；考虑事务 + retry，最终失败告警但不静默

### P0-3 AI batch goroutine 无 recover
- **来源**：预审
- **文件**：`bot/internal/ai/moderator.go:195`
- **问题**：`go m.flushBatch` 裸跑，panic 直接打崩进程
- **修复方向**：包装统一的 `safeGo(fn)` helper（defer recover + zap.Error 日志），全项目所有 `go func()` 审计一遍都套上

### P0-4 管理员豁免逻辑错耦合
- **来源**：01
- **文件**：`bot/internal/bot/moderation.go:102, 112, 165`
- **问题**：`handleIncomingMessage` 只有 `policy.Filter.Links.ExemptAdmins=true` 时才查 `isChatAdmin`；关掉链接豁免，管理员会被当普通用户进入所有 filter 和 AI
- **修复方向**：身份判定和链接豁免开关解耦；链开头固定查一次 admin；`ExemptAdmins` 只影响 link filter 分支

### P0-5 AI verdict 未严格白名单化
- **来源**：01
- **文件**：`bot/internal/ai/moderator.go:23, 499`；`bot/internal/bot/moderation.go:709, 729`
- **问题**：prompt 允许 `clean/suspicious`，`normalizeVerdict` 只把 `clean→normal`；未知 verdict 在 `decideAIAction` 中默认 `ceiling=ban/floor=warn`，误伤正常用户
- **修复方向**：唯一白名单 `normal/ad/scam/spam/harass/porn/violence`；解析层降级其他值到 `normal`；`error/profile_violation` 放独立字段不写主 verdict

### P0-6 终态保护不完整（user_trust）
- **来源**：01
- **文件**：`bot/internal/store/queries/user_trust.sql:34, 41, 51, 60, 72, 80`；`retention.sql:19`
- **问题**：只有 `UpsertUserTrust` CASE 保护了 `banned`，`archived` 未保护；`IncrementUserTrustCounters`/`UpdateUserTrustStatus`/`ResetUserTrustClean`/`AdjustUserTrustScore` 全部无保护。archived 用户重新入群被覆盖回 new
- **修复方向**：明确终态集合（banned/archived）；所有 upsert/update/counter/reset/score 加 CASE 或 `WHERE status NOT IN (...)`；解除终态用显式独立 query（unban/reactivate）

---

## 🟠 P1 (high) — 强烈建议修

### P1-1 ~~auto-delete goroutine 不可取消~~（作者决定：降回 P2，定时消息 auto-delete 用得少）
- **来源**：预审
- **状态**：**降级为 P2**（作者 2026-04-25 决定）
- **文件**：scheduler auto-delete 相关 goroutine
- **问题**：`docker compose restart` 被卡住；生产重启分钟级，1 小时会被 systemd/K8s 强杀
- **修复方向**：绑定到 app 主 context；SIGTERM 时 ctx cancel 立即退出；待删任务改重启后重新 schedule
- **注**：本次 P1 阶段跳过，和 P2 一起修

### P1-2 按钮 URL 没限制 http(s)
- **来源**：预审（从 P2 升级）
- **文件**：scheduler / 按钮构造相关
- **问题**：`tg://`、`javascript:`、自定义 scheme 能通过；TG client 某些版本处理 deeplink 是攻击面
- **修复方向**：URL 解析后强制 scheme ∈ {http, https}；拒绝并返回用户明确错误

### P1-3 ai-review 批量操作 catch 不 rethrow → 失败当成功
- **来源**：预审（从 P2 升级）
- **文件**：`web/` ai-review 批量操作
- **问题**：前端 catch 后不 rethrow 也不记录失败，管理员以为操作成功但实际失败；数据正确性问题
- **修复方向**：Promise.allSettled → 明确分 success/failed 两栏显示；或至少 toast 错误数

### P1-4 trusted 用户命中 `ai.trigger_keywords` 仍进 AI
- **来源**：01
- **文件**：`bot/internal/bot/moderation.go:179, 182, 189`
- **问题**：trusted 跳过被 trigger_keywords 旁路，违反"AI 仅未毕业 / trusted 跳过"
- **修复方向**：删除 trusted trigger 旁路；或改为"trusted 默认跳过，仅白名单管理员显式启用 trigger 时例外"并加配置开关

### P1-5 `telegram.me` preview 展开缺失（绕审）
- **来源**：01
- **文件**：`bot/internal/config/policy.go:263`；`bot/internal/bot/link_preview_fetch.go:136, 190`；`moderation.go:989`
- **问题**：默认白名单含 `telegram.me`，但 `normalizeTmeURL` 只认 `t.me`；`https://telegram.me/...` 绕过 preview 展开
- **作者决定**：方案 A — `normalizeTmeURL` 同时接受 `t.me`/`telegram.me`/`www.t.me`/`www.telegram.me`，统一规范到 `t.me/path`；前提：**不能影响现有运行正常**（原有 `t.me` 路径行为不变，只新增识别）
- **修复方向**：修改 `normalizeTmeURL` host 判断 accept-list；`extractTmeURLs` 对所有 host 变体都触发；全部经过 `t.me` 同一后续处理链；补测试覆盖 `t.me`/`telegram.me`/带/不带 www/`http` vs `https`/带查询串；**不要删除白名单里的 `telegram.me`**（删了会让用户手打 telegram.me 被过滤，影响正常使用）

### P1-6 Bio "发言前审核" 实际异步，触发消息不删
- **来源**：01
- **文件**：`bot/internal/bot/moderation.go:208, 210, 2106, 2140, 2211`
- **问题**：注释写"发言前"但实际 goroutine 异步；命中 ban 但触发消息不回溯删除；若正文短/正常可能被主 AI 放行
- **作者决定**：方案 A — 命中即删触发消息（只删触发那条，不维护 ring buffer）
- **修复方向**：`asyncProfileCheck` 判定 Bio 违规 ban 用户后，调用 bot 删除 API 删除触发消息；删除失败（消息已被删/权限不足）只 warn 日志不阻塞 ban；评估是否需把 trigger message id 作为参数传进 goroutine

### P1-7 startVerification 缺授权群/冻结检查
- **来源**：01
- **文件**：`bot/internal/bot/bot.go:361, 380, 390, 411`
- **问题**：消息链查 authorized/frozen 但入群验证不查；取消授权或 frozen 后仍 Restrict 新人启动 CAS/Bio/Turnstile
- **修复方向**：`startVerification` 开头复用 `IsAuthorizedGroup` + `GetSystemState` 检查；frozen 时全停

---

## 🟡 P2 (medium)

### P2-1 Scheduler daily 多时间点 partial Add 产生 orphan entry
- **来源**：预审
- **修复方向**：所有 Add 成功或全部回滚；失败时清理已 Add 的 entry

### P2-2 Scheduler RunNow 拒绝 paused 任务（和 UI 语义矛盾）
- **来源**：预审
- **修复方向**：RunNow 应允许 paused 任务试发（UI 按钮可见即可调用）；或 UI 禁用该按钮

### P2-3 按钮变量渲染用 Background ctx 没超时
- **来源**：预审
- **修复方向**：使用 request ctx 或 `context.WithTimeout(5s)`

### P2-4 AI 层 Background ctx + 吞错
- **来源**：预审
- **问题**：cache / 预算写失败零观测
- **修复方向**：统一 context；失败 metric + 采样日志

### P2-5 fallback warn 在 batch 场景放大
- **来源**：预审
- **问题**：batch 里每条失败都 warn，日志爆炸
- **修复方向**：batch 级聚合 warn（per model per batch 一条）

### P2-6 前端 100 条批量触发 100 次 reload
- **来源**：预审
- **修复方向**：批量操作后单次 reload；或增量更新 local state

---



### P2-13 去重锁 Redis 错误时 fail-open，并发 join 事件无去重
- **来源**：今日 live 检查（非 Codex 审查）
- **文件**：`bot/internal/bot/bot.go:391`
- **问题**：`SetNX` 返回 `(set, lockErr)`，只有 `lockErr == nil && !set` 才 return 跳过；当 Redis 报错（连接断开/超时）时 `set` 零值 false、`lockErr != nil`，条件不成立继续执行，导致双事件去重完全失效，Redis 抖动期间每次入群跑两遍 Restrict + 两个验证流程
- **复现**：停掉 Redis（或 `CLIENT PAUSE`），触发用户 join；`OnUserJoined` + `OnChatMember` 两次都进 `startVerification`，都执行 `Restrict(NoRights)`，都调用 `startAsyncVerificationChecks`，产生双 CAS/Bio 请求和双 pending 写入尝试
- **修复方向**：改为 fail-safe：`SetNX` 错误时 log.Warn 后依然 return 跳过（或基于 DB `pending_verifications` 做第二道去重）；同时考虑 Redis down 时告警

## 🟢 P3 (nitpick / 待确认)

### P3-1 `delete_warn` 非文本分支没写 violation 审计
- **来源**：01
- **文件**：`moderation.go:272`
- **修复方向**：复用 `applyFilterAction` 或补 `rule=filter_non_text_message` 的 InsertViolation

### P3-2 → P1-16 suspicious 用户允许自动毕业（作者已决定）
- **来源**：01（从 P3 升级为 P1-16，因确定要改）
- **文件**：`moderation.go:1539-1543`
- **作者决定**：suspicious 允许自动恢复到 trusted；门槛：**30 天 OR 5 条无问题发言**（任一满足）
- **修复方向**：`maybeGraduateUser` 对 suspicious 分支：去掉提前 return；判定条件改为 `messages_clean >= 5 OR time.Since(last_status_change) >= 30*24h`；毕业后 status=trusted、messages_clean 保留/清零（按产品语义确认，默认清零）；同时确保状态变更时更新 `last_status_change` 时间戳（若字段不存在需在迁移里补）

---

## 已核对无问题（01 轮）
- filter 主路径命中后立即 return（无重复 warn）
- 关键词回复位于 filter 之后 AI 之前，命中 return
- new 用户毕业边界正确（第 10 条、7*24h 瞬间）
- 跨聊天引用 ExternalReplyInfo+Quote 强制进 AI


## 轮次 02 — 入群验证

### P0-7 验证成功先解除限制再删 pending，删失败会被过期任务踢回
- **来源**：02
- **文件**：`bot/internal/bot/bot.go:1087`
- **问题**：`completeVerification` 先 `Restrict(...NoRestrictions())` 放行，再删除 `pending_verifications`；如果删除 pending 因 DB/ctx 临时失败，用户已经能发言但 pending 仍保留，后续 `HandleVerificationExpiry` 会按超时终态再次执行 kick/ban/mute，覆盖已成功终态。
- **复现**：让 button/math/random 用户答对；在 `Restrict` 成功后模拟 `DeletePendingVerification` 返回错误；等待 `expires_at` 被 `verification_expiry` 扫到，已验证用户会进入 `applyVerificationFailAction` 并被踢/封/静音。
- **修复方向**：成功终态必须原子化或至少先消费 pending 再放行；推荐事务/状态机（pending→passed）+ 幂等处理，放行失败时可恢复 pending，删除/标记失败时不得解除限制。

### P1-8 去重锁在 Restrict 之后，重复 join 事件可把已通过用户重新禁言
- **来源**：02
- **文件**：`bot/internal/bot/bot.go:390`
- **问题**：双事件去重锁 `SetNX` 发生在 `Restrict(...NoRights())` 之后；第二个 OnUserJoined/OnChatMember 即使命中锁直接返回，也已经重新 Restrict。若用户在第一次流程中已完成验证，10 秒锁 TTL 内到达的重复事件会把已放行用户改回无权限，且不创建 pending、不会有过期清理。
- **复现**：同一用户入群触发第一条事件并完成 button 验证；在 `clawguard:verify:lock:<chat>:<user>` 未过期前投递重复 join transition；第二次 `startVerification` 先执行 NoRights，再因锁存在返回，用户留在禁言态。
- **修复方向**：把去重锁移动到任何 Telegram Restrict 之前；锁命中不得产生副作用；也可在锁 value 中记录流程状态并对已通过/处理中事件做幂等判断。

### P1-9 发送或存储验证题失败后没有失败终态，新人会卡在禁言态
- **来源**：02
- **文件**：`bot/internal/bot/bot.go:418`
- **问题**：新人已在前面被 Restrict；随后 `startVerificationPrompt` 中发送消息、渲染 math_image、或 `storePendingVerification` 任一步失败都会直接返回错误，没有解除 Restrict、没有 pending 记录、也没有失败动作，过期 worker 无法接管。
- **复现**：配置 `verify.method=math_image` 并让字体/渲染失败，或模拟 Telegram `Send` 成功后 DB 写 pending 失败；`startVerification` 返回错误，但用户仍是 `NoRights`，数据库无 pending，之后不会自动踢出或放行。
- **修复方向**：Restrict 后的任一 prompt/pending 失败必须进入明确终态：安全优先可 kick/ban 并清理消息，或回滚 Restrict；发送成功但写 pending 失败时还要删除已发验证消息。

### P1-10 button callback_data 只有 user_id，无 nonce/签名/消息绑定，可被旧按钮重放
- **来源**：02
- **文件**：`bot/internal/bot/bot.go:760`
- **问题**：button 验证按钮 data 仅为十进制 `user.ID`，服务端只校验 sender 和当前 pending.method；pending payload 里的 `ButtonUnique` 没参与校验，也没有 nonce、签名、过期时间或 message_id 绑定。若旧验证消息删除失败，同一用户再次入群时可点击旧按钮通过新 pending。
- **复现**：用户 A 第一次入群生成 button 验证消息，故意让 `deleteVerificationMessage` 失败保留旧消息；A 离群再入群产生新的 button pending；点击旧消息按钮，`c.Data()` 仍是 A 的 user_id，`GetPendingVerification(chat,A)` 命中新 pending，3 秒后直接 `completeVerification`。
- **修复方向**：callback_data 使用短随机 nonce + HMAC(chat_id,user_id,message_id,method,expires_at,nonce)，pending 中保存 nonce/签名材料；回调必须校验签名、method、message_id/nonce、expires_at，并消费后失效。

### P1-11 Turnstile token 消费不检查 expires_at，过期窗口内仍可通过
- **来源**：02
- **文件**：`bot/internal/store/queries/pending_verifications.sql:35`
- **问题**：`DeletePendingVerificationByToken` 只按 token 删除并返回 pending，不要求 `expires_at > NOW()`；过期处理 worker 30 秒扫一次，因此 token 到期后、worker 删除前仍可完成 Cloudflare 校验并解除限制，违背验证超时终态。
- **复现**：Turnstile pending 的 `expires_at` 到达后立刻提交 `/api/verify/turnstile/:token`，只要 worker 尚未处理该行，`VerifyTurnstileToken` 会先通过 Cloudflare，再删除 pending 并 `Restrict(...NoRestrictions())` 放行。
- **修复方向**：消费 token 的 SQL 加 `AND expires_at > NOW()`；过期 token 返回明确错误并交由过期处理或立即执行失败终态；API 层也应区分 expired 与 not found。

### P2-7 Turnstile siteverify 没有本地超时和 idempotency_key，API goroutine 可被外部请求拖住
- **来源**：02
- **文件**：`bot/internal/bot/turnstile.go:150`
- **问题**：Cloudflare 校验使用 `http.DefaultClient.Do(req)`，只继承 API request context，没有本地 `context.WithTimeout` 或 client timeout；如果上游/网络卡住且客户端不断开，该 HTTP handler goroutine 可长期占用。请求体也未发送 Turnstile 支持的 `idempotency_key`，重试/并发提交无法在 Cloudflare 侧幂等归并。
- **复现**：让 `challenges.cloudflare.com` 建连后不返回，保持客户端连接不断开；`handleVerifyTurnstile` 会一直等待 `VerifyTurnstileToken`，不受入群异步检查的 30 秒预算覆盖。
- **修复方向**：为 siteverify 单独设置短超时（如 3-5 秒）和专用 `http.Client{Timeout:...}`；每次 Turnstile pending 生成并保存 idempotency key，提交 siteverify 时带上，重试使用同一 key。

### P2-8 验证失败/超时先执行 Telegram 动作再删 pending，删除失败会重复执行终态
- **来源**：02
- **文件**：`bot/internal/bot/bot.go:1133`
- **问题**：`HandleVerificationExpiry` 和 `failVerificationImmediately` 都先 kick/ban/mute，再删除 pending；如果 Telegram 动作成功但 DB 删除失败，pending 保留，worker 下轮会重复执行失败动作并重复插入/尝试插入 violation，造成终态处理非幂等。
- **复现**：让答错或超时用户的 `applyVerificationFailAction` 成功，随后模拟 `DeletePendingVerification` 失败；pending 仍在表中，下一轮 expiry 会再次对同一 chat/user 执行 fail action。
- **修复方向**：引入状态字段或原子 claim（pending→failed/processing）后再执行 Telegram 动作；所有失败终态按状态幂等，删除失败不应导致下轮重复踢/封。

### P3-3 → P1-32 用户离群 transition 立即清 pending/运行态（作者已决定）
- **来源**：02（从 P3 升级为 P1-32）
- **文件**：`bot/internal/bot/bot.go:315`
- **作者决定**：2026-04-25 08:14 走"离群即清"方案（干净）
- **修复方向**：在 `handleChatMemberUpdate` 检测 `left/kicked` transition 时：
  1. 删除 `pending_verifications` 记录
  2. 删除验证消息（忽略 message-not-found 错误）
  3. 清 Redis 运行态锁 `clawguard:verify:lock:<chat>:<user>`
  4. 清 `bioCheckInFlight`/`profileCheckInFlight` 等内存 map
  5. 不区分用户主动 leave vs admin kick（语义一致）


## 轮次 03 — 数据层 & SQL

### P1-12 非 Turnstile 验证消费不检查 expires_at，过期窗口内仍可通过
- **来源**：03
- **文件**：`bot/internal/store/pending_verifications.sql.go:27`
- **问题**：`GetPendingVerification` 只按 `(chat_id,user_id)` 读取 pending，不要求 `expires_at > NOW()`；button/math/math_image/random 回调拿到 pending 后直接 `completeVerification`。只要过期 worker 尚未删除该行，已超时用户仍可点击旧按钮/答案通过验证，和超时失败终态冲突。
- **复现**：配置 button 或 math 验证，等 `pending_verifications.expires_at` 到达后、`verification_expiry` 下一轮扫描前点击正确按钮；`handleVerifyButton`/`handleVerifyMath`/`handleVerifyRandom` 通过 `GetPendingVerification` 读到过期行并放行。
- **修复方向**：消费型读取拆成独立 SQL，增加 `AND expires_at > NOW()` 并最好原子 claim/delete；过期行返回 expired/not found，交给过期终态处理或立即进入失败终态。

### P1-13 00007 回滚会删除 00006 已创建的 action_taken 列
- **来源**：03
- **文件**：`bot/migrations/00007_ai_action_taken.sql:2`
- **问题**：`00006_user_trust.sql` 已在创建 `ai_decisions` 时包含 `action_taken`，`00007` 的 Up 用 `ADD COLUMN IF NOT EXISTS` 实际通常不做事，但 Down 无条件 `DROP COLUMN IF EXISTS action_taken`。从版本 7 回滚到 6 后，数据库处于“00006 已应用”状态却缺少 00006 schema 中应有的列，store 的 INSERT/SELECT 会直接失败。
- **复现**：在新库执行到 00007 后执行 goose down 一步；`ai_decisions.action_taken` 被删除；随后调用 `InsertAIDecision` 或 `ListAIDecisions`，SQL 引用 `action_taken` 报 column does not exist。
- **修复方向**：删除或改正重复迁移；`00007` Down 不应删除 00006 拥有的列，或用补偿迁移确保回滚到 00006 后 schema 仍包含 `action_taken`。

### P1-14 定时消息发送成功后 DB 状态和 run 日志非原子，失败会产生幽灵投递
- **来源**：03
- **文件**：`bot/internal/scheduler/scheduler.go:212`
- **问题**：Telegram `Send` 成功后，`MarkScheduledMessageSent` 失败只打 warn，随后仍 `InsertScheduledMessageRun(success=true)` 并启动 auto-delete goroutine。DB 中 `last_run_at/last_message_id` 可能仍是旧值或 NULL，但 run 表显示成功；重启或下一轮调度时无法基于最新 `last_message_id` 做“上一条未删则跳过”，可能重复发消息且后台状态与真实 Telegram 投递不一致。
- **复现**：让定时消息实际发送成功，随后模拟 `MarkScheduledMessageSent` DB 错误但 `InsertScheduledMessageRun` 成功；查看 `scheduled_message_runs` 有成功记录，而 `scheduled_messages.last_message_id` 未更新；重启后下一次 cron 仍按旧状态判断并可能再次发送。
- **修复方向**：发送后的状态更新和 run 记录用同一 DB 事务提交；若主状态更新失败，不应写成功 run 或应写入需要补偿的 outbox/repair 状态；auto-delete 也应只在持久化成功后启动。

### P1-15 Redis in-flight 锁无 token，超时后旧 owner 会删除新锁
- **来源**：03
- **文件**：`bot/internal/bot/moderation.go:2157`
- **问题**：`acquireProfileCheckInFlight` 用 `SetNX(key,"1",60s)` 加锁，释放时直接 `Del(key)`，没有随机 token/fencing 校验。若一次 bio/profile 检查超过 60 秒或 goroutine 被调度拖延，锁过期后第二个请求可获得新锁；第一个请求结束时会删除第二个请求的锁，导致第三个请求并发进入，重复外部 RPC/AI 检查和可能重复执行处置。
- **复现**：让用户 profile 检查持有超过 60 秒；第二条消息在 TTL 过期后获得同 key 新锁；第一条随后 defer `Del` 删除该新锁；第三条消息再次 `SetNX` 成功，出现同一 user 多个 profile check 并发。
- **修复方向**：锁 value 使用随机 token，释放用 Lua compare-and-del；对需要顺序保证的处置增加 fencing token/版本检查，或改用本地 singleflight + Redis token 双层幂等。

### P2-9 00019 缺少 goose Down，定时消息表无法安全回滚
- **来源**：03
- **文件**：`bot/migrations/00019_scheduled_messages.sql:1`
- **问题**：`00019_scheduled_messages.sql` 只有 `-- +goose Up`，没有 `-- +goose Down`。迁移系统回滚到 00018 时无法对称删除 `scheduled_messages`/`scheduled_message_runs` 及索引，rollback 行为不可预期，生产回滚会留下新表和新数据结构。
- **复现**：应用到 00019 后执行 goose down；该文件没有 Down 段可执行，回滚不会恢复到 00018 的 schema 边界，后续重新 up 或代码降级时仍能看到定时消息表。
- **修复方向**：补充明确 Down：按依赖顺序 drop `scheduled_message_runs`、索引、`scheduled_messages`；若要保护生产数据，Down 前先文档化备份/导出策略或拒绝自动 destructive down。

### P2-10 00004 Down 未恢复 diff 的 JSONB NOT NULL 语义
- **来源**：03
- **文件**：`bot/migrations/00004_config_audit_v2.sql:7`
- **问题**：Up 把 `config_audit.diff` 从 `JSONB NOT NULL` 改成 nullable `TEXT`，但 Down 只删除 `after/before/action`，没有把 `diff` 改回 `JSONB NOT NULL`。回滚到 00003 后 schema 与 00001 定义不一致，旧代码若按 JSONB 写入/查询会遇到类型不匹配或 NULL 语义变化。
- **复现**：执行到 00004 后 goose down 一步；检查 `config_audit.diff` 仍是 TEXT 且可 NULL，而 00003 期望它是 JSONB NOT NULL。
- **修复方向**：Down 中 `ALTER COLUMN diff TYPE JSONB USING COALESCE(diff,'{}')::jsonb` 并恢复 `SET NOT NULL`；无法转换的历史文本需先清洗或备份。

### P2-11 00017 数据回填不可逆且无备份，回滚无法恢复原 trust 状态
- **来源**：03
- **文件**：`bot/migrations/00017_user_trust_banned_backfill.sql:3`
- **问题**：Up 批量把有 ban violation 的 `user_trust` 改为 `banned`、重置 `score=0` 并改写 `notes`，Down 只有 `SELECT 1`，没有保存原 `status/score/notes`。如果回填规则误伤或需要回滚版本，原信任状态永久丢失。
- **复现**：准备一条 `user_trust.status='trusted', score=0.9` 且存在历史 `violations.action='ban'` 的记录；执行 00017 后变成 banned/0；执行 goose down 后仍是 banned/0，无法还原 trusted/0.9。
- **修复方向**：回填前创建审计/备份表记录原值，Down 按备份恢复；或将该迁移标记为不可逆并在发布流程要求离线备份和人工确认。

## 轮次 04 — Telegram 边界与消息处理

### P0-8 回复匿名/频道消息执行管理命令会 nil panic，且 handler 无 recover 会打崩进程
- **来源**：04
- **文件**：`bot/internal/bot/bot.go:1778`
- **问题**：`resolveCommandTarget` 在回复消息路径直接读 `msg.ReplyTo.Sender.ID/Username`，`resolveWarnTargetAndReason` 同样直接读 `msg.ReplyTo.Sender`；Telegram 里匿名管理员、频道身份、部分 service/linked-channel 消息可能没有 `from`，只有 `sender_chat`，会触发 nil pointer。当前 telebot `Settings{Synchronous:true}` 没有配置 `OnError` recover，`runHandler` 也不 recover，panic 会直接终止 bot 进程。
- **复现**：在群内找一条 `reply_to_message.from == nil` 的消息（匿名管理员或频道身份消息），管理员回复执行 `/spam` 或 `/warn 原因`；代码进入 `resolveCommandTarget`/`resolveWarnTargetAndReason`，解引用 `msg.ReplyTo.Sender.ID` 后 panic，进程退出。
- **修复方向**：所有 reply target 先判空 `ReplyTo.Sender`，缺失时返回明确错误（例如“不支持处理匿名/频道身份消息，请用 user_id”）；给所有 Telegram handler 包统一 `defer recover`，并配置 zap 化 `OnError`，确保单条异常 update 不会打崩进程。

### P1-17 `edited_message` 已进入 webhook allowlist，但没有 handler，编辑后内容完全绕过审核
- **来源**：04
- **文件**：`bot/internal/bot/bot.go:134`
- **问题**：webhook `AllowedUpdates` 包含 `edited_message`，telebot 收到后只会分发到 `tele.OnEdited`，但 `registerHandlers` 只注册了新消息的 `OnText/OnPhoto/OnVideo/OnDocument/...`，没有注册 `tele.OnEdited`。用户可先发干净文本通过审核，再编辑为广告链接、违规文案或媒体 caption，过滤器、t.me preview、AI、bio-on-message 都不会跑。
- **复现**：发送一条正常文本；随后编辑为 `https://t.me/...` 引流或黑名单关键词；Telegram 推送 `edited_message`，因没有 `OnEdited` handler，`handleIncomingMessage` 不执行，消息留在群内。
- **修复方向**：注册 `tele.OnEdited` 并复用 `handleIncomingMessage` 的审核链；注意 edited caption 也要覆盖，必要时在审核输入里标注 scene=`edited_message`，并避免对编辑消息重复计算不适合的入群/毕业副作用。

### P1-18 新版 `forward_origin` 未纳入转发来源识别，`no_forwards` 和 AI 转发上下文可被绕过
- **来源**：04
- **文件**：`bot/internal/bot/moderation.go:2462`
- **问题**：`extractForwardSource` 只看旧字段 `OriginalChat/OriginalSender/OriginalSenderName`，但 telebot 的 `Message` 已有新版 Bot API 字段 `Origin *MessageOrigin`（`forward_origin`）。当 Telegram 只填 `forward_origin` 时，`extractForwardSource` 返回空，`Filter.NewUser.NoForwards` 判断不到转发，`matchesTriggerKeywords` 不会匹配来源名，AI 入参 `ForwardFrom` 也为空。
- **复现**：构造或实际接收一条只有 `forward_origin`、旧 `forward_from*` 为空的转发消息；开启新用户 `no_forwards` 或 AI trigger keyword；消息通过 `extractForwardSource == ""` 分支，不会被当作转发处理。
- **修复方向**：在 `extractForwardSource` 中兼容 `msg.Origin` 的 `SenderChat/Chat/Sender/SenderUsername/Signature`，并让 `messageHasRestrictedMedia`、trigger keyword、AI `ForwardFrom` 共用同一来源解析结果；补测试覆盖 legacy forward 与 `forward_origin` 两类 update。

### P1-19 无 caption 的 video/document 在 non_text_messages 策略前被 Skip，媒体可绕过删除/AI
- **来源**：04
- **文件**：`bot/internal/bot/moderation.go:127`
- **问题**：`handleIncomingMessage` 在 `content.Skip` 时立即 return；而 `extractReviewableContent` 对无 caption 的 video 返回 `Skip:true`，对无 caption 的 document 也返回 `Skip:true`。因此即便 `policy.Filter.NonTextMessages` 配置为 `delete`、`delete_warn` 或默认 `ai_review`，这两类媒体都会在策略分支前退出。photo 无 caption 会得到 `[图片]`，但 video/document 无 caption 不一致地放行。
- **复现**：把 `filter.non_text_messages` 设为 `delete` 或 `delete_warn`；普通用户发送无 caption 的视频或文件；`extractReviewableContent` 返回 Skip，代码在第 127 行直接 return，消息不会删除、不会警告、不会送 AI。
- **修复方向**：不要让已注册的非文本消息用 `Skip:true` 绕过 non-text 策略；为 video/document/audio 等无 caption 媒体返回明确 `Kind` 和占位文本（如 `[视频]`/`[文件]`），再由 `NonTextMessages` 决定 delete/delete_warn/ai_review/off。

### P1-20 缺少 bot 管理权限预检与群内告警，Restrict/Ban 失败时群实际处于未保护状态
- **来源**：04
- **文件**：`bot/internal/bot/bot.go:390`
- **问题**：入群验证依赖 bot 能收到成员更新并执行 `Restrict`，处置依赖 `Delete/Ban/Restrict`；但代码没有在 bot 加群/升降权时检查 `can_restrict_members/can_delete_messages` 等权限，也没有在 `Restrict` 失败时给群管理员清晰提示。`startVerification` 第一步 Restrict 失败只 log 并返回 error，telebot 默认 `OnError` 仅标准输出，群内看不到“bot 权限不足”，新人实际未被禁言且不会进入验证流程。
- **复现**：把 bot 加入已授权群但不给管理员或移除封禁/删除权限；新人入群触发 `startVerification`，`s.bot.Restrict` 返回 Telegram 权限错误；函数返回，未发送验证提示、未写 pending，群管理员无明确告警，新人保持可发言。
- **修复方向**：在 `OnMyChatMember`/启动健康检查中读取 bot 成员权限，缺少必要权限时写 runtime audit 并向 owner/群管理员告警；`Restrict/Delete/Ban` 权限错误要归一化为可读提示，避免静默 fail-open。

### P2-14 发送消息没有 per-chat / global 限流，突发反馈会撞 Telegram 1/s per chat 与 30/s overall
- **来源**：04
- **文件**：`bot/internal/bot/action_feedback.go:48`
- **问题**：群内反馈、验证提示、欢迎语、关键词回复、管理命令回复、定时推送都直接调用 `s.bot.Send`/`c.Send`，未经过统一发送队列或限流器。Telegram 对 bot 有单群约 1 msg/s、全局约 30 msg/s 限制；入群潮、批量违规、关键词自动回复或多个定时任务同一时间触发时，会产生 429，当前多处只 log/返回，缺少 Retry-After 重试与排队，导致验证提示/处罚反馈/定时消息丢失。
- **复现**：同一群短时间触发多名新人入群或多条关键词回复；每条都会直接 `Send` 验证提示/反馈。超过 Telegram per-chat 限制后 API 返回 429，部分消息发送失败，验证 pending 可能未创建或管理员看不到处置反馈。
- **修复方向**：封装统一 Telegram sender，按 chat 做 1/s token bucket、全局 30/s token bucket；识别 429 `retry_after` 后排队重试；验证流程中“发送提示 + 写 pending”应在发送失败时进入明确失败终态或可重试队列。

## 轮次 05 — 并发 & 资源管理

### P1-21 AI 审核 fallback 链无总超时，默认配置下一条消息可阻塞 webhook 约 90 秒
- **来源**：05
- **文件**：`bot/internal/bot/moderation.go:78`
- **问题**：消息 handler 使用 `context.Background()` 作为整条审核链根 ctx，`CheckMessage` 内只给每个模型单次调用加 `timeout_ms`；外层没有 request/handler 总 deadline。默认 `timeout_ms=10000`、`max_retries=2`、主模型 + 2 个 fallback 时，最坏会串行跑 `3 attempts × 3 models × 10s ≈ 90s`，且每次失败立即切下一家/下一轮，没有 backoff。`tele.NewBot` 又启用 `Synchronous:true`，webhook HTTP 请求会一直等 handler 返回，外部 AI 抖动时容易堆积请求并触发 Telegram webhook 重试。
- **复现**：开启 AI 审核，配置默认 fallback 链；让所有 provider 连接超时或黑洞 10s；发送一条需 AI 审核的群消息。`handleWebhook -> ProcessUpdate -> handleIncomingMessage -> CheckMessage` 会在一个 webhook 请求内串行等待所有 attempt/model 超时，期间请求不返回，重复消息/并发 webhook 会继续占用 goroutine 和 DB/Redis 连接。
- **修复方向**：在消息 handler 顶层创建总 ctx（例如 `context.WithTimeout(..., 15~25s)` 或按策略显式 `total_timeout_ms`），传入 AI、DB、Redis、HTTP；fallback 链使用总 deadline 剩余时间，失败重试加指数 backoff + jitter，并限制总 attempts/总耗时；webhook 侧可考虑快速 ack + 内部队列，但必须保证幂等。

### P1-22 AI per-user/daily budget 限流是“成功后计数”，并发请求可绕过限额且无全局/单群 QPS 阀
- **来源**：05
- **文件**：`bot/internal/ai/moderator.go:175`
- **问题**：进入 AI 前只读 Redis 的 per-user daily counter / daily budget；实际 `bumpUserCounter` 和 `BumpBudget` 在模型成功返回后才执行。多个并发消息会同时看到旧计数并全部放行，失败请求还不计数；同时代码没有单群/全局 QPS 或并发 semaphore。攻击者或入群潮可在计数落库前并发打满 AI provider，造成成本暴涨、provider 429、handler goroutine 堆积。
- **复现**：把 `per_user_daily_limit` 设为 1，清空 Redis 计数；同一用户并发发送多条需 AI 的消息，或让多名新用户同时触发 bio/消息 AI。所有请求在第 175 行检查时都未 over limit，随后并发进入 `client.Check`；只有成功返回后才在第 431-433 行递增计数和预算。
- **修复方向**：进入 AI 前用 Redis Lua/事务原子“预占” per-user、per-chat、global token（含 TTL），失败时按策略归还或记录 failed quota；增加单用户/单群/全局 token bucket + in-flight semaphore；provider 429 按 Retry-After/backoff 排队，避免直接雪崩。

### P1-23 ProviderRegistry `Client` 与 `Reload` 竞态可把旧 provider client 写回新 registry
- **来源**：05
- **文件**：`bot/internal/ai/registry.go:125`
- **问题**：`Client` 在 RLock 下读取 `provider` 后释放锁，再拿写锁创建 client；期间如果后台/管理 API 调用 `Reload` 替换了 `byKey` 和 `clients`，旧 goroutine 仍会用 reload 前的 base_url/api_key/timeout 创建 client 并写入新的 `clients` map。若 owner 刚更新 API key、禁用后又启用、或切换 provider endpoint，后续审核可能继续复用旧 client，直到下一次 reload 才恢复。
- **复现**：并发压测 AI 审核让多个 goroutine 首次调用 `providers.Client("x")`；同时在 Web 面板更新 provider API key/base_url 触发 `reloadAIRegistries`。构造时序：goroutine A 读到旧 provider 后释放 RLock；goroutine B 完成 Reload 并清空 clients；goroutine A 拿 Lock 后用旧 provider 创建 client 写回 map；后续请求命中新 map 中的旧 client。
- **修复方向**：在写锁内重新读取当前 `byKey[key]` 并验证 enabled/version 与第一次读取一致，或把 provider 配置拷贝和 client map 作为不可变 snapshot 原子替换；Reload 增加 generation，Client 创建前后校验 generation，避免 stale client 回写。

### P2-15 定时消息 cron 没有同任务重入保护，慢发送/重复时间点会并发投递同一任务
- **来源**：05
- **文件**：`bot/internal/scheduler/scheduler.go:77`
- **问题**：robfig/cron 默认允许 job 重入，当前 `Add` 直接注册 `FuncJob`，`run` 内也没有 per-message in-flight 锁。一次发送最少可能经历 0/10/30 秒三次重试，Telegram 慢响应、DB 慢查询或同一 daily time 重复配置时，同一个 scheduled_message 可并发跑多个 `s.run`，导致重复发消息、`last_message_id` 被后完成的任务覆盖、auto-delete 删除错上一条或漏删。
- **复现**：配置 interval=1 分钟并让 Telegram `Send` 前两次失败/慢返回，或配置 daily_times 含重复时间；在上一次 `s.run` 尚未完成时 cron 再触发同一 ID。两个 goroutine 都会读取 active 状态并发送，随后分别 `MarkScheduledMessageSent` 和 `insertRun`。
- **修复方向**：给 Scheduler 增加 per scheduled_message in-flight 锁/Redis lock，或使用 cron `SkipIfStillRunning`/`DelayIfStillRunning` 包装；DB 层可加 advisory lock 或 `scheduled_message_runs` 幂等 key；daily_times 入库时去重。

### P2-16 HTTP 服务使用 Echo 默认 server，没有 ReadHeader/Read/Write/IdleTimeout
- **来源**：05
- **文件**：`bot/internal/api/server.go:49`
- **问题**：`echo.Start(addr)` 使用默认 `http.Server` 超时，未设置 `ReadHeaderTimeout`、`ReadTimeout`、`WriteTimeout`、`IdleTimeout`。公网暴露 webhook/admin API 时，慢客户端/慢上传可长期占用连接和 goroutine；结合 webhook 同步处理，AI 或 Telegram 慢调用还会继续拖住响应写出，资源上限不可控。
- **复现**：对 `/webhook/:secret` 或任意 admin 路由建立大量连接后缓慢发送 header/body；默认 server 不会按应用配置主动回收这些慢连接，连接数和 goroutine 持续上涨。
- **修复方向**：改为显式 `http.Server` 并设置 `ReadHeaderTimeout`（如 5s）、`ReadTimeout`、`WriteTimeout`、`IdleTimeout`、`MaxHeaderBytes`；webhook body 加大小限制；长处理从请求链路拆出或确保 handler 总超时小于 `WriteTimeout`。

### P2-17 AI 和链接预览 HTTP client 未配置 Transport，默认连接池在高并发下容易连接抖动
- **来源**：05
- **文件**：`bot/internal/ai/openai.go:30`
- **问题**：OpenAI-compatible client 和 t.me preview client 都只设置 `http.Client.Timeout`，未配置专用 `Transport` 的 `MaxIdleConns`、`MaxIdleConnsPerHost`、`IdleConnTimeout`、TLS/Dial timeout 等；默认 `MaxIdleConnsPerHost=2` 对并发 AI 审核/链接预览偏小，会频繁新建 TCP/TLS 连接，增加延迟和 FD/端口压力。provider client 虽然复用，不是每次 new client，但底层连接池参数仍不适合群聊突发。
- **复现**：并发触发多条 AI 审核或多用户同时发送 t.me 链接；抓取 `httptrace`/连接数可看到大量新建连接而非复用 idle 连接，provider 延迟升高时更容易把 goroutine 堆在 dial/TLS/response 等待上。
- **修复方向**：为 AI、t.me、CAS/Turnstile/Telegram 文件下载统一封装可复用 HTTP client/transport，配置合理 `MaxIdleConns`、`MaxIdleConnsPerHost`、`IdleConnTimeout`、`TLSHandshakeTimeout`、`ResponseHeaderTimeout`、`ExpectContinueTimeout`，并按 provider 维度复用。

## 轮次 06 — 安全 & 权限 & Web 面板

### TOP critical
- **P0-9**：`.env.example` 提交了真实 Telegram Bot Token 与 Turnstile Secret，必须立即轮换。

### ~~P0-9 `.env.example` 提交真实生产密钥~~（作者确认：未泄漏，略过）
- **来源**：06
- **状态**：**N/A — 作者 2026-04-25 08:12 确认这些值不是真实生产密钥，跳过**
- **文件**：`.env.example:13`
- **备注**：FINDINGS 里保留记录，但不纳入修复清单；后续维护时如要引入 gitleaks 可单独开 issue

### P1-24 管理面板状态改变 API 仅靠 SameSite Cookie，无 CSRF 防护
- **来源**：06
- **文件**：`bot/internal/api/routes_admin.go:44`
- **问题**：`/api/admin/*` 大量 `PUT/POST/DELETE` 只校验 `cg_admin` JWT cookie，没有 CSRF token、Origin/Referer 校验或双提交 cookie；cookie 设置为 `SameSite=Lax`，无法覆盖所有同站/子域/重定向链路风险，且 API 同时接受 cookie 与 Bearer，状态改变接口没有统一要求自定义 header。
- **攻击场景**：管理员登录面板后访问攻击者页面；若部署在同站子域、被开放重定向/反代同源入口串联，攻击者可诱导浏览器带上 cookie 调用 `PUT /api/admin/system-state`、`POST /api/admin/ban`、`PUT /api/admin/groups/:chat_id/config` 等接口，冻结系统或篡改群策略。
- **修复方向**：所有非 GET/HEAD 管理 API 增加 CSRF token（服务端 session/双提交均可）并校验 `Origin`/`Sec-Fetch-Site`；cookie 改 `SameSite=Strict`（能接受的话）；对 Bearer 和 cookie 两种认证路径明确区分，浏览器 cookie 路径必须带 CSRF。

### P1-25 非 owner 管理员可创建新管理员，形成权限委派/扩散
- **来源**：06
- **文件**：`bot/internal/api/routes_admin.go:65`
- **问题**：`POST /api/admin/admins` 没有 `requireOwner`，任意已登录 admin 都能创建 role=admin 的新账号；如果当前 admin 是全局 admin（空 `group_scope` 即全局），即可无限扩散全局后台访问权。UI 只在 owner 时显示按钮，但后端没有同等强制。
- **攻击场景**：一个普通 admin 的浏览器/JWT 被短暂拿到，攻击者直接 POST 新增自己的 Telegram ID 为 admin；即使原 admin 后续改密或退出，攻击者可继续通过自己的 Telegram 登录拿到新 JWT。
- **修复方向**：创建/更新/删除管理员统一 owner-only；如确实允许 scoped admin 邀请同范围协作者，必须单独设计“不可再委派”的 role、强制非空 scope、审计告警，并禁止创建全局 admin。

### P1-26 群内 `/start`、`/help` 不做授权群检查，会在未授权群回复
- **来源**：06
- **文件**：`bot/internal/bot/start_command.go:24`
- **问题**：消息审核主链会 `IsAuthorizedGroup` 后静默忽略未授权群，但命令 handler 独立注册；`/start`、`/help` 在任意群直接 `c.Send`，不检查授权群，也不检查 frozen。违背“未授权群完全忽略消息（不回复不留痕）”要求。
- **攻击场景**：任意用户把 bot 拉进未授权群后发送 `/help`，bot 会公开回复命令列表和接入流程；这既暴露部署存在，也会让未授权群产生 bot 互动痕迹，无法满足隔离语义。
- **修复方向**：所有群内命令入口先走统一 `ignoreUnauthorizedGroup`/`system frozen` guard；未授权群返回 nil，不回复、不写业务审计；只保留 bot 自身入群事件的离群逻辑。

### P1-27 管理员豁免结果正缓存 5 分钟，降权后仍可绕过审核/执行命令
- **来源**：06
- **文件**：`bot/internal/bot/moderation.go:1724`
- **问题**：`isChatAdmin` 对 admin 结果缓存 `chat_admin:<chat>:<user>=1` 5 分钟；Telegram 群管理员被撤销后，缓存窗口内仍被视作管理员。该结果用于审核豁免，也被 `/warn`、`/spam`、`/unban` 等命令权限复用。
- **攻击场景**：群主撤销某管理员权限后，该用户在 5 分钟内继续发送广告/违规内容可跳过 AI 和 filter 的管理员豁免分支；同时还能执行手动封禁/警告等群管命令，造成越权窗口。
- **修复方向**：管理员正缓存 TTL 降到极短或只缓存负结果；接入 `chat_member` 管理员变更事件主动删除相关 cache；高危命令不要使用可陈旧缓存，改为实时 `getChatMember` 或带 generation 的短缓存。

### P1-28 `/config` 群内授权未校验面板 admin 的 group_scope
- **来源**：06
- **文件**：`bot/internal/bot/admin_command_config.go:106`
- **问题**：群内 `/config` 只要求发送者在 Telegram 群内是 admin 且存在于面板 admins 表；没有复用 `botAdminCanAccessChat`/`adminCanAccessChat` 校验该面板 admin 是否有当前 `chat.ID` 的管理范围，也没有先确认该群为授权群。
- **攻击场景**：一个只应管理 A 群的 scoped admin，如果同时是 B 群 Telegram admin，可在 B 群获取带 `ScopeChatID=B` 的 magic login；虽然部分 API 后续会因 scope 拒绝，但登录链接签发、审计记录和前端跳转已经跨域发生，若后续新增接口漏 scope，会直接变成 IDOR 入口。
- **修复方向**：`authorizeConfigCommand` 在群内必须同时检查 `IsAuthorizedGroup(chat.ID)` 与 `botAdminCanAccessChat(admin, chat.ID)`；失败时静默或返回最小错误；magic payload 的 scope 也要在 exchange 时二次校验。

### P1-29 user_trust 列表先全局计数再内存过滤，泄漏越权聚合数据
- **来源**：06
- **文件**：`bot/internal/api/routes_admin.go:1314`
- **问题**：当 scoped admin 不传 `chat_id` 时，`CountUserTrust` 和各状态 `counts` 用全库条件统计；随后只对 `items` 做 `adminCanAccessChat` 内存过滤。响应里的 `total/counts/has_more` 会包含无权群的数据，且分页 offset 也基于全局结果，导致 scoped admin 看到跨群用户规模与状态分布。
- **攻击场景**：只授权管理一个小群的 admin 调用 `/api/admin/user-trust?status=banned`，可通过 `total` 和 `counts.banned` 推断全站封禁人数；不断调不同筛选条件还能枚举其它群的用户活跃/封禁分布，属于 IDOR 型元数据泄漏。
- **修复方向**：把 `adminScopeFilter` 下推到 `CountUserTrust`/`ListUserTrustPaginated` SQL；不要先全局查再过滤；`total/has_more/counts` 必须只基于请求者可访问 chat 集合。

### P1-30 注销只清 cookie，不会使已签发 JWT 失效
- **来源**：06
- **文件**：`bot/internal/api/auth.go:136`
- **问题**：`/api/auth/logout` 只删除浏览器 cookie；JWT 是 24 小时自包含令牌，服务端没有 `jti`、session version、黑名单或 `not_before` 校验。被复制的 cookie/Bearer token 在 logout 后仍可使用到过期。
- **攻击场景**：管理员在共享电脑或被 XSS/代理抓到 `cg_admin` 后点击退出；攻击者继续用旧 JWT 调用 `/api/admin/*` 修改策略、封禁用户或新增管理员，直到 24 小时过期。
- **修复方向**：JWT 增加 `jti` 并在 Redis/DB 维护会话表，logout 删除/吊销当前 session；admins 表增加 `token_version`/`revoked_before`，密码级事件、删除/降权 admin 时统一失效旧 token；高危操作可要求近期登录。

### P1-31 Telegram Markdown legacy 模板变量不转义，用户字段可注入格式/链接
- **来源**：06
- **文件**：`bot/internal/bot/template_render.go:22`
- **问题**：`RenderMessageTemplate` 在 `tele.ModeMarkdown` 下对 plainVars 使用 no-op escape；欢迎语、关键词回复、反馈模板中的 `{user_name}`、`{group_title}`、`{rules_link}`、`{keyword}` 等可能来自用户/群资料/配置输入，能插入 Markdown 链接或破坏模板结构。HTML 和 MarkdownV2 分支有 escape，legacy Markdown 分支没有。
- **攻击场景**：用户把姓名改成 `[点我](https://phish.example)` 或包含 `](` 的构造；管理员配置欢迎语 `欢迎 {user_name}` 且 parse_mode=Markdown 后，新人通过验证时 bot 发送可点击钓鱼链接/伪装文本，污染群公告式反馈。
- **修复方向**：不再支持 legacy Markdown，统一迁移到 HTML/MarkdownV2；或为 legacy Markdown 实现完整 plain var escape（至少 `\ _ * [ ] ( )` 等），并把结构变量与纯文本变量严格分离。

### P2-18 Next.js middleware 只保护 4 类页面，多个后台页面可未登录直接打开
- **来源**：06
- **文件**：`web/middleware.ts:4`
- **问题**：middleware 只匹配 `/dashboard`、`/groups`、`/violations`、`/audit`；但实际后台还有 `/admins`、`/authorized-groups`、`/trust`、`/llm`、`/ai-review`、`/ai-costs`、`/prompt-editor` 等。API 仍会 401，但页面壳、功能名称、部分客户端逻辑可被未登录用户直接加载。
- **攻击场景**：未登录访问 `/llm` 或 `/admins` 可看到后台页面结构和功能入口，再由客户端 fetch 触发跳转；这扩大了信息暴露面，也容易在未来页面加入 server-side 数据时变成真实未授权读取。
- **修复方向**：middleware 改为默认保护除 `/`、`/auth/*`、`/verify/*`、静态资源外的所有面板路径；或维护完整 allowlist，而不是少量 protectedPrefixes；服务端组件读取敏感数据前也要校验 JWT。

### P2-19 登录、magic exchange、Turnstile 与高危管理操作没有速率限制
- **来源**：06
- **文件**：`bot/internal/api/auth.go:41`
- **问题**：代码中没有 Echo rate limiter/Redis token bucket；`/api/auth/telegram-login`、`/api/public/magic/exchange`、`/api/verify/turnstile/:token`、`/api/admin/ai-test`、`/api/admin/ban` 等都可被同一 IP/账号/群高频调用。Telegram login 有签名校验，magic token 足够随机，但缺少速率限制仍会造成 Redis/DB/外部验证服务压力和审计噪声。
- **攻击场景**：攻击者对 magic exchange 或 Turnstile verify endpoint 大量 POST 随机 token，持续打 Redis、DB 与 Cloudflare siteverify；已登录低权限 admin 也可反复调用 AI test/probe 消耗 provider quota。
- **修复方向**：按 IP、admin_id、telegram_id、chat_id 分层加 Redis token bucket；登录/verify 公共接口更严格，高危管理操作加 per-admin 限流和审计告警；AI test/probe 单独接入成本预算。

### P2-20 Caddy 站点缺 TLS 站点名与安全响应头
- **来源**：06
- **文件**：`Caddyfile:1`
- **问题**：Caddyfile 使用 `:80` 明文站点，仅反代路径，没有 HSTS、`X-Content-Type-Options`、`Referrer-Policy`、`X-Frame-Options`/`frame-ancestors`、基础 CSP 等安全头。docker-compose 又只把 Caddy 绑定到 `127.0.0.1:8090`，说明前面可能还有一层公网反代；当前仓库配置本身无法保证 TLS 与浏览器侧硬化。
- **攻击场景**：如果运维直接暴露该 Caddy 或上游反代未补安全头，管理面板 cookie 虽为 Secure 但站点无自动 HTTPS/HSTS，用户可能被降级/点击劫持；缺 CSP 时一旦出现前端注入点，影响范围更大。
- **修复方向**：Caddyfile 使用真实域名启用自动 HTTPS；增加 HSTS、nosniff、referrer、frame-ancestors/self CSP；如必须由上游 TLS 终止，也在仓库明确记录上游必须设置的安全头并在 Caddy 内补齐可补部分。

### P2-21 profile_check_logs 长期保存并返回完整 bio，缺少脱敏/最小化
- **来源**：06
- **文件**：`bot/internal/store/queries/profile_check_logs.sql:7`
- **问题**：入群/发言前 Bio 审核日志把完整 `bio` 写入 `profile_check_logs`，列表 API 也原样返回并支持按 bio 模糊搜索。用户 bio 可能包含手机号、邮箱、个人链接等隐私信息；这类“检查日志”没有长度截断、脱敏、分级权限或默认短 TTL。
- **攻击场景**：任意有该群面板权限的 admin 可以批量检索和导出新人/成员历史 bio 全文；如果面板账号泄漏，攻击者拿到的不是必要审计摘要，而是完整个人资料库。
- **修复方向**：日志只保存命中片段/哈希/截断摘要（如前后各 N 字）和规则 ID；完整 bio 仅短期加密保存并默认几天清理；API 展示默认脱敏，导出/查看全文需 owner 或二次确认审计。



### P2-22 handleListGroups 用内存过滤而非 SQL scope（同 P1-29 类型）
- **来源**：第二批 A 修复期间 live 检查
- **文件**：`bot/internal/api/routes_admin.go:83`
- **问题**：`handleListGroups` 先 `ListGroups()` 全查所有群，再用 `adminCanAccessChat` 内存过滤；和 P1-29 user_trust 是同一类型问题，scoped admin 拿到响应中虽不含越权群数据，但能间接观察总群数量信息
- **修复方向**：复用 `adminScopeFilter(admin)` 推 SQL；新增 `ListGroupsScoped` query 接受 `scope_global bool, scope_chat_ids []int64` 参数

### P2-23 filterViolationsByScope / filterWarningsByScope 内存过滤（count 仍全局）
- **来源**：第二批 A 修复期间 live 检查
- **文件**：`bot/internal/api/routes_admin.go`（filterViolationsByScope, filterWarningsByScope, handleListViolations, handleListWarnings）
- **问题**：违规列表/警告列表先全查再内存按 scope 过滤；count 接口（如有）仍是全局聚合；同 P1-29 类型
- **修复方向**：参照 P1-29 推 SQL，给 ListViolations / ListWarnings / Count 加 scope 参数；同步检查 ListAIDecisions / ListAudit 等其他列表 API

### 已核对无新增问题（06）
- `/api/admin`、`/api/admin/llm`、scheduled message 路由整体挂在 `requireAdminJWT` / `requireOwner` group 下，未发现裸露的管理 API。
- SQL 查询主要由 sqlc 生成并使用参数占位；本轮未发现手拼用户输入 SQL。
- 未发现 Echo CORS middleware 放宽 `Access-Control-Allow-Origin`；当前主要风险是 CSRF 而不是跨域读。
- 前端除欢迎语预览外未发现其它 `dangerouslySetInnerHTML`；该预览当前先 escape 模板再替换固定示例变量，未单独列 XSS finding。
