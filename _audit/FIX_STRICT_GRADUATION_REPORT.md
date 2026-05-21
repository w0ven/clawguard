# Fix Strict Graduation Report

时间：2026-05-21 15:37 GMT+8
范围：严格毕业、短消息 AI、消息类型覆盖、贴纸视觉审核、其他 bot 入群审核

## 验证

- `cd bot && sqlc generate`：当前系统 sqlc 可在修正既有 `bot_commands.sql` 歧义列后运行，但会把大量无关生成文件改成与当前代码不兼容的 pgtype/字段命名形态；已撤回该全量副作用，本次 `user_trust` 生成代码按既有风格手写同步并通过 build/test/vet。
- `cd bot && go build ./...`：通过
- `cd bot && go vet ./...`：通过
- `cd bot && go test ./...`：通过

## 修复明细

| 项目 | 改动文件 | 回归风险 |
| --- | --- | --- |
| 普通用户毕业只看 clean 消息数，默认 5 条 | `bot/internal/bot/moderation.go`<br>`bot/internal/config/policy.go` | 中低：老配置中的 `graduate_after_days` 不再延迟普通用户毕业。 |
| 未毕业用户短消息强制 AI | `bot/internal/ai/moderator.go`<br>`bot/internal/bot/moderation.go` | 中：new/suspicious 的短消息会增加 AI 调用量。 |
| 消息类型覆盖 | `bot/internal/bot/bot.go`<br>`bot/internal/bot/moderation.go` | 中：更多非文本类型进入 `non_text_messages` 默认 AI 审核路径。 |
| 贴纸视觉审核 | `bot/internal/bot/image_download.go`<br>`bot/internal/bot/moderation.go` | 中低：静态贴纸下载原图，动态/视频贴纸下载缩略图。 |
| 其他 bot 入群审核 | `bot/migrations/00025_user_trust_is_bot.sql`<br>`bot/internal/store/queries/user_trust.sql`<br>`bot/internal/store/user_trust.sql.go`<br>`bot/internal/store/models.go`<br>`bot/internal/bot/bot.go`<br>`bot/internal/bot/moderation.go` | 中：非白名单 bot 默认不再被隐式信任，会被标记并持续审核。 |

## 备注

- telebot.v3 v3.3.8 没有 `OnStory`、`OnPaidMedia`、`OnGiveaway`、`OnGiveawayCreated` handler 常量，也没有 `PaidMedia` 字段；已在 handler 注册处加 TODO。`Message.Story`/`ReplyToStory`/`Giveaway*` 字段仍在内容构建和媒体识别中覆盖。
