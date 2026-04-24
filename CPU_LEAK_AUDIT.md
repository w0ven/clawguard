# ClawGuard Bot CPU 泄漏审计报告

## 高危嫌疑点（必须修）
- [bot/internal/bot/moderation.go:168](./bot/internal/bot/moderation.go) `CheckProfileOnMessage` 被挂在每条消息的 AI 热路径前，而且默认会走 `ai` 模式；[bot/internal/bot/moderation.go:1906](./bot/internal/bot/moderation.go) 到 [bot/internal/bot/moderation.go:1978](./bot/internal/bot/moderation.go) 显示未毕业用户每发一条消息，都会先读 bio，再做一次独立的 bio AI 审核，然后才进入正文 AI 审核。这意味着一条消息最差会触发“两次 AI 审核 + 一次 Telegram getChat/Redis”，是最近 `91b9293` 引入/强化的同步前置路径。若群里长期存在大量 `new/suspicious` 用户，CPU 会被稳定抬高，而且日志只会显示正常审核，不会有 panic。修复建议：把 on-message bio 审核改成节流后的异步复查，或者至少对 `(chat_id,user_id,bio_hash)` 做更强缓存/最小重检间隔；不要在每条消息前同步跑一遍完整 AI 链。
- [bot/internal/ai/moderator.go:181](./bot/internal/ai/moderator.go) 到 [bot/internal/ai/moderator.go:225](./bot/internal/ai/moderator.go) 的 batching 机制存在“调用方已超时/返回，但后台仍继续完整执行”的 orphan work 模式：`CheckMessage` 为每个 key 起一个 goroutine，`flushBatch` 先 `time.Sleep(window)`，随后用 `context.Background()` 执行 `checkBatch`。这会绕开原始调用链上的取消信号，导致消息侧即便已经超时、请求已经结束，后台 LLM 检查、缓存写入、预算更新仍继续跑。结合 `5a392a3` 把 join profile_check timeout 扩大到按 fallback 链计算，这类孤儿任务的存活时间进一步拉长。修复建议：`flushBatch` 必须继承 batch 内 request context 的最短 deadline，或在 batch 结构里维护共享 ctx；至少不要用 `context.Background()` 跑真正昂贵的 AI 调用。
- [bot/internal/bot/moderation.go:910](./bot/internal/bot/moderation.go) 到 [bot/internal/bot/moderation.go:942](./bot/internal/bot/moderation.go)、[bot/internal/bot/link_preview_fetch.go:51](./bot/internal/bot/link_preview_fetch.go) 到 [bot/internal/bot/link_preview_fetch.go:115](./bot/internal/bot/link_preview_fetch.go) 的 t.me 预览抓取在消息主路径同步执行，单条消息最多并发抓 3 个链接，每个 3 秒超时、读取最多 512 KiB HTML、再跑整页 meta 正则。这里我没有发现递归调用或循环抓取，所以“递归死循环”嫌疑不成立；但这是确定存在的新 CPU 放大器，尤其在广告流量刻意塞多个 t.me 链接时会放大字符串解析、JSON/Redis、HTML 扫描成本。修复建议：增加 feature flag / sampling / per-chat 限速；对失败结果做短 TTL negative cache；把预览抓取放到异步 enrich，而不是阻塞消息审核主路径。

## 中危嫌疑点（值得改）
- [bot/internal/bot/moderation.go:1984](./bot/internal/bot/moderation.go) 到 [bot/internal/bot/moderation.go:2022](./bot/internal/bot/moderation.go) 的 bio 缓存本身不是 busy-loop，但当 `BioCacheTTLMinutes<=0` 或缓存命中率很低时，每条消息都会同步打 Telegram `getChat`。这不是纯 CPU 死循环，不过会把 on-message profile 路径的总体成本再抬一截。修复建议：强制最小 TTL，或至少对“最近刚查过且 bio 未变”做本地/Redis 去重。
- [bot/internal/bot/bot.go:457](./bot/internal/bot/bot.go) 到 [bot/internal/bot/bot.go:540](./bot/internal/bot/bot.go) 的入群异步检查 goroutine 退出条件基本健全，但它内部又串了 CAS、profile_check、ban/cleanup 等多个步骤，且 profile timeout 现在会按 fallback 链放大。若群有大量 join 事件，这一段会持续制造长寿命 goroutine。修复建议：给 join 异步链加并发上限，或把 profile_check 放到队列 worker，而不是每次 join 都直接起 goroutine。
- [bot/internal/bot/action_feedback.go:56](./bot/internal/bot/action_feedback.go)、[bot/internal/bot/keyword_reply.go:304](./bot/internal/bot/keyword_reply.go)、[bot/internal/bot/bot.go:1633](./bot/internal/bot/bot.go)、[bot/internal/bot/bot.go:1690](./bot/internal/bot/bot.go) 这些 auto-delete goroutine / `time.AfterFunc` 都没有绑定 service 级 ctx。单个看问题不大，但高频群里会产生大量短命定时任务。修复建议：统一收敛到一个可取消的 delayed-delete 调度器，避免散落的 one-shot goroutine。

## 低危/代码风格
- [bot/internal/worker/verification_expiry.go:32](./bot/internal/worker/verification_expiry.go)、[bot/internal/worker/llm_prober.go:81](./bot/internal/worker/llm_prober.go)、[bot/internal/worker/retention_cleanup.go:18](./bot/internal/worker/retention_cleanup.go) 这些后台 worker 的 `for { select { ... } }`、ticker/timer 使用都带 `ctx.Done()` 或 `Stop()`，没有发现典型 ticker 泄漏。
- [bot/internal/bot/link_preview_fetch.go:81](./bot/internal/bot/link_preview_fetch.go)、[bot/internal/bot/image_download.go:52](./bot/internal/bot/image_download.go)、[bot/internal/ai/openai.go:74](./bot/internal/ai/openai.go)、[bot/internal/casclient/casclient.go:73](./bot/internal/casclient/casclient.go) 的 HTTP 请求都带 timeout / context，响应体也有 `Close()`；未发现 `http.Client.Do` 级别的典型 FD/body 泄漏。
- 递归函数方面，本次扫描没有发现 t.me 预览相关的递归调用，也没发现“抓到页面里的 t.me 再次触发抓取”的代码路径。当前实现只从 Telegram message entity 里提取原始 URL，不会二次解析抓取结果里的链接。
- 全仓扫描未发现 `defer` 写在热点循环体内的明显泄漏点；`for {}` 无条件循环主要集中在 worker，结构基本正常。

## 最近提交对比结论
- `a7bcbef` 只改文档/忽略文件，不涉及 bot 运行时代码，不像 CPU 根因。
- `91b9293` 是最可疑的运行时变更：它把 on-message bio 违规命中后的记录/封禁链补全，同时保留了每条消息前的 bio AI 审核路径，是本次审计里最像“持续性 CPU 升高”的改动。
- `5a392a3` 修复了外层 5 秒硬超时，但副作用是 join profile_check 可以跑得更久；如果再叠加 `Moderator.flushBatch` 的 background 执行，孤儿任务成本会更明显。
- `12bafde` 引入 message/bio scene 区分与缓存 key 变化，本身不是死循环，但它让 bio 审核正式成为独立 AI 场景，给后续 `checkProfileOnMessage` 铺了路。
- `a1f9e5c` 只是修正 bio category 文案，不像 CPU 根因。
- t.me 预览抓取代码在当前仓库历史里从初始提交就已存在，没有找到“昨天新加”的独立 commit；如果生产镜像确实是昨天才上线该功能，说明本地仓库历史不完整，或该功能是通过配置/分支合并进入生产，而不是单独可追溯 commit。

## 结论
一句话：最可能的根因不是 t.me 递归死循环，而是 `91b9293` 之后“未毕业用户每条消息前同步做 bio AI 审核”叠加 `Moderator.flushBatch` 的 orphan work，导致 AI 热路径持续放大；建议优先收掉 `checkProfileOnMessage` 的同步前置执行，再修 `flushBatch` 不继承 caller ctx 的问题。
