# Plan：修复 bio 审核命中后 信任状态未封禁 + AI 复核无记录

## 背景与现象

**真实复现场景**（2026-04-22 用户反馈）：

1. 广告号入群时 bio 干净 → 通过入群 bio 审核；
2. 入群后偷偷把 bio 改成带广告内容，并发广告消息；
3. 发言时触发未毕业用户发言前 bio 审核（on-message profile check），bot 删消息 + ban 用户 + 发反馈；
4. 但管理端表现异常：
   - **信任系统页**：该用户仍处于 `new/suspicious`（"待定"），未切到 `banned`；
   - **AI 复核页**：找不到那条触发消息的记录；
   - 仅在"验证 → 简介审查记录"里能看到一条 `hit`。

## 根因定位

### Bug A：on-message bio 审核路径

**位置**：[bot/internal/bot/moderation.go:174-203](bot/internal/bot/moderation.go#L174-L203)

```go
} else if matched != "" {
    s.deleteMessage(msg)
    s.banUser(msg.Chat, msg.Sender)
    s.queries.InsertViolation(ctx, ...)   // MessageText 只存了 payload JSON，不是用户原文
    s.sendActionFeedback(...)
    return nil
    // ❌ 没有更新 user_trust 状态
    // ❌ 没有写入 ai_decisions
    // ❌ 没有保留触发消息的原文
}
```

对照：正规 AI 审核路径 [moderation.go:1128-1214](bot/internal/bot/moderation.go#L1128-L1214) 在 `applyAIAction` 里会完整地做三件事：`UpdateUserTrustStatus` + `recordAIDecision` + `resetTrustAfterViolation`。

### Bug B：入群时 bio 审核路径（并列孪生 bug）

**位置**：[bot/internal/bot/bot.go:513-528](bot/internal/bot/bot.go#L513-L528) → [bot/internal/bot/bot.go:649-686](bot/internal/bot/bot.go#L649-L686) (`performAsyncVerificationMatch`)

`performAsyncVerificationMatch` 只做了 `banUser` + 清理 pending_verification + `InsertViolation` + 反馈，同样缺少：
- `UpdateUserTrustStatus` / `resetTrustAfterViolation`
- `recordAIDecision`

注：该函数同时被 CAS 命中和 profile 命中共用。本次修复时 CAS 路径也应一起补（CAS 同样需要 banned 状态同步 + 记录在 AI 复核页可见）。

### Bug C：AI 复核前端缺少时间列

**位置**：[web/app/ai-review/page.tsx:192-336](web/app/ai-review/page.tsx#L192-L336)

表头只有 消息 / AI 判定 / 已执行 / 置信度 / 标注，没有时间列。`ai_decisions.created_at` 和 TS 类型 `AIDecision.created_at` 都已存在，纯展示层修改即可。

## 修复方案

### 1. 新增通用 helper：`recordProfileViolationDecision`

**新增位置**：`bot/internal/bot/moderation.go`（放在 `recordAIDecision` 附近）

目的：把"bio 审核命中导致 ban"写入 `ai_decisions` 表，让 AI 复核页能看到。

```go
// recordProfileViolationDecision 把 bio 审核命中导致 ban 的事件写入 ai_decisions，
// 让 AI 复核页能看到这条记录，并允许管理员标注误封/正确/漏判。
//
// triggerMsg: 触发 bio 审核的那条消息（on-message 路径）；入群路径传 nil。
// matched:    checkProfileOnMessage / checkProfile 返回的命中说明（keyword 为原词，AI 模式为 "bio违规:<category>"）。
// mode:       "on_message_ai" / "on_message_keyword" / "join_ai" / "join_keyword"。
// aiOutput:   AI 模式下的原始 output，供保留 model/provider/confidence/latency/cost；keyword 模式传 nil。
func (s *Service) recordProfileViolationDecision(
    ctx context.Context,
    chat *tele.Chat,
    user *tele.User,
    triggerMsg *tele.Message,
    matched string,
    mode string,
    aiOutput *ai.CheckOutput,
) error {
    // 解析 matched：AI 模式返回 "bio违规:ad" → verdict=ad, category=bio违规；keyword 模式 matched 即关键词
    verdict := "profile_violation"
    category := "bio_match"
    if strings.HasPrefix(matched, "bio违规:") {
        category = strings.TrimPrefix(matched, "bio违规:")
        if aiOutput != nil {
            v := strings.ToLower(strings.TrimSpace(aiOutput.Verdict.Verdict))
            if v != "" {
                verdict = v
            }
        }
    }

    messageID := int64(0)
    var messageText *string
    if triggerMsg != nil {
        messageID = int64(triggerMsg.ID)
        if txt := strings.TrimSpace(triggerMsg.Text); txt != "" {
            messageText = stringPtr(truncateString(txt, 2000))
        }
    }

    params := store.InsertAIDecisionParams{
        ChatID:        chat.ID,
        UserID:        user.ID,
        MessageID:     messageID,
        MessageText:   messageText,
        Model:         "profile_check:" + mode,
        PromptVersion: "",
        Verdict:       verdict,
        Confidence:    1.0,
        Category:      category,
        Reason:        stringPtr("bio 审核命中: " + matched),
        ActionTaken:   "ban",
        LatencyMs:     0,
        CostCents:     0,
    }
    if aiOutput != nil {
        params.ProviderID = int64PtrIfPositive(aiOutput.ProviderID)
        params.ModelID = int64PtrIfPositive(aiOutput.ModelID)
        if aiOutput.Model != "" {
            params.Model = aiOutput.Model
        }
        params.PromptVersion = aiOutput.PromptVersion
        if aiOutput.Verdict.Confidence > 0 {
            params.Confidence = aiOutput.Verdict.Confidence
        }
        if c := strings.TrimSpace(aiOutput.Verdict.Category); c != "" && !strings.EqualFold(c, "正常") {
            params.Category = c
        }
        if r := strings.TrimSpace(aiOutput.Verdict.Reason); r != "" {
            params.Reason = stringPtr(r)
        }
        params.LatencyMs = int32(aiOutput.LatencyMs)
        params.CostCents = aiOutput.CostCents
    }

    _, err := s.queries.InsertAIDecision(ctx, params)
    return err
}
```

### 2. 让 `checkProfileOnMessage` / `checkProfile` 返回 aiOutput

改动：

- `checkProfileOnMessage(ctx, chat, user, policy)` 签名改为 `(matched string, aiOutput *ai.CheckOutput, err error)`；
- `checkProfile(ctx, chat, user, policy)` 同上；
- keyword 路径 / 跳过 / error 路径返回 `nil` 为 `aiOutput`；
- AI 路径在 `CheckMessage` 成功后把 `&output` 作为第二返回值（即便 pass 也返回，让未来复用）。

位置：[moderation.go:1693-1818](bot/internal/bot/moderation.go#L1693-L1818)（checkProfile）和 [moderation.go:1823-1896](bot/internal/bot/moderation.go#L1823-L1896)（checkProfileOnMessage）。

> 注意：调用方除 `bot.go:503` 和 `moderation.go:169` 外没有其他引用（可以通过 grep 确认）。修改后两处更新取第一个返回值即可。

### 3. 修复 Bug A（on-message 路径）

**位置**：[moderation.go:174-203](bot/internal/bot/moderation.go#L174-L203)

改动后：

```go
} else if matched != "" {
    // 1. 删消息 + ban
    if err := s.deleteMessage(msg); err != nil {
        s.logger.Warn("delete msg on profile match failed", zap.Error(err))
    }
    if err := s.banUser(msg.Chat, msg.Sender); err != nil {
        s.logger.Error("ban on-message profile match user failed",
            zap.Error(err),
            zap.Int64("chat_id", msg.Chat.ID),
            zap.Int64("user_id", msg.Sender.ID))
        return err
    }

    // 2. 写 violations：保留用户原始消息文本（之前只存了 payload JSON）
    if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
        ChatID:      msg.Chat.ID,
        UserID:      msg.Sender.ID,
        Username:    stringPtr(msg.Sender.Username),
        Rule:        "profile_match_on_message",
        Matched:     stringPtr(matched),
        Action:      "ban",
        MessageText: stringPtr(truncateString(msg.Text, 2000)), // ← 改为用户原文
    }); err != nil {
        s.logger.Warn("insert on-message profile violation failed", zap.Error(err))
    }

    // 3. 写 ai_decisions → AI 复核页可见
    mode := "on_message_keyword"
    if policy.AI.ProfileOnMessageMode == "ai" || policy.AI.ProfileOnMessageMode == "" {
        mode = "on_message_ai"
    }
    if err := s.recordProfileViolationDecision(ctx, msg.Chat, msg.Sender, msg, matched, mode, aiOutput); err != nil {
        s.logger.Warn("record profile violation ai_decision failed", zap.Error(err))
    }

    // 4. 同步 user_trust → 信任系统显示 banned
    s.resetTrustAfterViolation(ctx, msg, "ban", stringPtr("profile_match_on_message: "+matched))

    // 5. 反馈
    s.sendActionFeedback(msg.Chat, nil, policy.Feedback.Ban, map[string]string{
        "user":   feedbackUserLabel(msg.Sender, policy.Feedback.Ban.ParseMode),
        "reason": "资料简介违规：" + matched,
    })
    return nil
}
```

其中 `aiOutput` 从步骤 2 的新签名拿到：

```go
if matched, aiOutput, err := s.checkProfileOnMessage(ctx, msg.Chat, msg.Sender, policy); err != nil {
    ...
} else if matched != "" {
    // 把 aiOutput 传进去
}
```

### 4. 修复 Bug B（入群路径）

扩展 `asyncVerificationMatch` 和 `asyncVerificationMatchOps`：

**位置**：[bot.go:592-613](bot/internal/bot/bot.go#L592-L613)

```go
type asyncVerificationMatch struct {
    chat         *tele.Chat
    user         *tele.User
    policy       config.GuardPolicy
    rule         string
    matched      *string
    messageText  *string
    feedbackText string
    feedbackKind string
    logLabel     string
    // 新增
    aiOutput     *ai.CheckOutput // profile AI 命中时带，keyword/CAS 为 nil
    decisionMode string          // "join_ai" / "join_keyword" / "cas"；为空则跳过 ai_decisions 记录
}

type asyncVerificationMatchOps struct {
    banUser                   func(*tele.Chat, *tele.User) error
    awaitPendingVerification  func(context.Context, int64, int64, time.Duration) (store.PendingVerification, error)
    deleteVerificationMessage func(*tele.Chat, *int64)
    deletePendingVerification func(context.Context, int64, int64) error
    insertViolation           func(context.Context, store.InsertViolationParams) error
    sendCASFeedback           func()
    sendProfileFeedback       func()
    logger                    *zap.Logger
    // 新增
    recordAIDecision          func(context.Context, *tele.Chat, *tele.User, string, string, *ai.CheckOutput) error
    updateTrustBanned         func(context.Context, int64, int64, string) // 内部走 resetTrustAfterViolation 的简化版
}
```

**位置**：[bot.go:649-686](bot/internal/bot/bot.go#L649-L686) `performAsyncVerificationMatch`

在 `insertViolation` 之后、`feedback` 之前追加：

```go
// 新增：把命中写入 ai_decisions（profile 路径）
if ops.recordAIDecision != nil && match.decisionMode != "" && match.decisionMode != "cas" && match.matched != nil {
    if err := ops.recordAIDecision(ctx, match.chat, match.user, *match.matched, match.decisionMode, match.aiOutput); err != nil {
        ops.logger.Warn("record profile violation ai_decision failed", zap.Error(err), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID))
    }
}

// 新增：同步 user_trust 到 banned
if ops.updateTrustBanned != nil {
    reason := match.rule
    if match.matched != nil {
        reason = match.rule + ": " + *match.matched
    }
    ops.updateTrustBanned(ctx, match.chat.ID, match.user.ID, reason)
}
```

**位置**：[bot.go:615-647](bot/internal/bot/bot.go#L615-L647) `handleAsyncVerificationMatch`

把两个新 ops 绑到 `s`：

```go
recordAIDecision: s.recordProfileViolationDecision,
updateTrustBanned: func(ctx context.Context, chatID, userID int64, notes string) {
    // 入群时没有 msg，构造一个仅含 chat/sender 的占位 msg
    fakeMsg := &tele.Message{
        Chat:   match.chat,
        Sender: match.user,
    }
    s.resetTrustAfterViolation(ctx, fakeMsg, "ban", stringPtr(notes))
},
```

> `resetTrustAfterViolation` 内部只用到 `msg.Chat.ID` / `msg.Sender.ID` / action / notes（见 [moderation.go:1256-1326](bot/internal/bot/moderation.go#L1256-L1326)），占位 msg 安全。

**位置**：[bot.go:513-528](bot/internal/bot/bot.go#L513-L528) 入群 bio 审核 caller

调用 `checkProfile` 取到 `aiOutput` 后把它带入 match struct：

```go
matched, aiOutput, err := s.checkProfile(profileCtx, chat, user, policy)
...
if matched != "" {
    mode := "join_keyword"
    if strings.EqualFold(policy.Verify.ProfileCheckMode, "ai") {
        mode = "join_ai"
    }
    s.handleAsyncVerificationMatch(ctx, asyncVerificationMatch{
        chat:         chat,
        user:         user,
        policy:       policy,
        rule:         "profile_match",
        matched:      stringPtr(matched),
        messageText:  stringPtr(string(payload)),
        feedbackText: "个人简介违规：" + matched,
        feedbackKind: "profile",
        logLabel:     "profile matched user",
        aiOutput:     aiOutput,   // 新增
        decisionMode: mode,       // 新增
    })
}
```

CAS 路径搜索 `handleAsyncVerificationMatch` 所有其它调用点，将 `decisionMode: "cas"`（或留空）即可；performAsyncVerificationMatch 内会跳过 ai_decisions 写入（保持现状）。若后续想让 CAS 也进 AI 复核页，另提需求。

### 5. 修复 Bug C（前端加时间列）

**位置**：[web/app/ai-review/page.tsx:192-336](web/app/ai-review/page.tsx#L192-L336)

改动：

1. 表头在"消息"前增加"时间"列：
   ```tsx
   <TableHeaderCell className="w-36">时间</TableHeaderCell>
   <TableHeaderCell>消息</TableHeaderCell>
   ```
2. 行内对应新增一列：
   ```tsx
   <TableCell className="whitespace-nowrap text-xs tabular-nums text-[var(--text-muted)]">
     {new Date(item.created_at).toLocaleString("zh-CN", {
       year: "numeric", month: "2-digit", day: "2-digit",
       hour: "2-digit", minute: "2-digit", second: "2-digit",
       hour12: false,
     })}
   </TableCell>
   ```
3. 展开行 `colSpan` 由 7 改为 8。
4. 若希望默认降序，后端 `/api/admin/ai-decisions` 应已按 `created_at DESC` 返回（`idx_ai_decisions_user` / `idx_ai_decisions_created_day` 都是 DESC，按约定不动后端）。如果实际返回顺序不对，作为 follow-up 单独提。

## 单元测试

### 需更新

- [bot/internal/bot/bot_async_verification_test.go](bot/internal/bot/bot_async_verification_test.go)：`asyncVerificationMatchOps` 新增了字段，测试里的 ops struct 需要补字段（可以传 `recordAIDecision: func(...) error { return nil }` 和空的 `updateTrustBanned`，或用计数器断言被调用）。

### 建议新增

1. `TestPerformAsyncVerificationMatch_ProfilePath_RecordsAIDecisionAndBansTrust`：
   - 传 `decisionMode: "join_ai"` + mock ops，断言 `recordAIDecision` 被调用一次且参数正确，`updateTrustBanned` 被调用一次。
2. `TestPerformAsyncVerificationMatch_CASPath_SkipsAIDecision`：
   - 传 `decisionMode: "cas"`，断言 `recordAIDecision` 未被调用。
3. moderation 新增覆盖 on-message 路径（可参考 `moderation_filter_test.go` 的风格）：profile 命中后 `ai_decisions` 表插入一条，`user_trust.status == "banned"`。

## 实施顺序建议（给 codex）

1. **先加 helper + 改签名**：新增 `recordProfileViolationDecision`，修改 `checkProfileOnMessage` / `checkProfile` 返回值，更新调用方编译通过。
2. **修 Bug A**：改 moderation.go:174-203 代码块，`go build` + `go test ./...` 跑通。
3. **修 Bug B**：扩 ops struct + performAsyncVerificationMatch + handleAsyncVerificationMatch + caller；修旧测试，加新测试。
4. **修 Bug C**：前端 page.tsx 加时间列；`npm run build` 通过。
5. **手动冒烟**（推荐用户本地跑）：
   - 复现场景：新用户入群 bio 干净 → 改 bio 为违规 → 发广告 → 确认 被 ban + 信任系统 banned + AI 复核可见该条消息原文 + 时间显示。

## 非目标（本次不做）

- 不改 `profile_check_logs` 表或相关查询；
- 不动 CAS 路径的 `ai_decisions` 记录（仅扩展 struct 字段保留可选性，但不为 CAS 写入）；
- 不做数据回填（历史被 bio 命中的记录不会倒追写进 ai_decisions）；
- 不改 AI 复核列表的后端排序/分页（仅前端增加时间列）。

## 风险与回滚

- `checkProfile` / `checkProfileOnMessage` 签名变化会波及所有调用点，grep `checkProfile\(` + `checkProfileOnMessage\(` 已确认只有 2 处调用，改动可控；
- `resetTrustAfterViolation` 接收占位 msg 的用法要保证 `msg.Chat` / `msg.Sender` 都非 nil（代码里都已构造）；
- `ai_decisions.message_id` NOT NULL，入群路径传 `0` 可行（`idx_ai_decisions_user` 不要求唯一）；
- 若回滚：本次只改代码、无 SQL migration，`git revert` 即可。
