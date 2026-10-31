package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/messagefmt"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

const shanghaiTimezone = "Asia/Shanghai"

type Button struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type Scheduler struct {
	cron        *cron.Cron
	queries     *store.Queries
	bot         *tele.Bot
	sendLimiter interface {
		WaitChat(context.Context, int64) error
		WaitGlobal(context.Context) error
	}
	logger  *zap.Logger
	running sync.Map
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu      sync.Mutex
	entries map[int64][]cron.EntryID
}

func New(logger *zap.Logger, queries *store.Queries, bot *tele.Bot, sendLimiter interface {
	WaitChat(context.Context, int64) error
	WaitGlobal(context.Context) error
}) *Scheduler {
	logger = redact.ZapLogger(logger)
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cron:        cron.New(cron.WithLocation(time.UTC), cron.WithSeconds()),
		queries:     queries,
		bot:         bot,
		sendLimiter: sendLimiter,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		entries:     make(map[int64][]cron.EntryID),
	}
}

func (s *Scheduler) Start() {
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.deleteWorker(s.ctx) }()
	s.cron.Start()
}

func (s *Scheduler) Stop() context.Context {
	if s.cancel != nil {
		s.cancel()
	}
	cronCtx := s.cron.Stop()
	done := make(chan struct{})
	go func() {
		<-cronCtx.Done()
		s.wg.Wait()
		close(done)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			if s.logger != nil {
				s.logger.Warn("scheduler stop timeout waiting delayed jobs")
			}
		}
	}()
	return ctx
}

func (s *Scheduler) LoadActive(ctx context.Context) error {
	items, err := s.queries.ListActiveScheduledMessages(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := s.Add(item); err != nil {
			s.logger.Warn("load scheduled message failed", zap.Error(err), zap.Int64("scheduled_message_id", item.ID))
		}
	}
	return nil
}

func (s *Scheduler) Add(msg store.ScheduledMessage) error {
	if !msg.Enabled || msg.Status != "active" {
		return nil
	}

	var ids []cron.EntryID
	rollback := func() {
		for _, id := range ids {
			s.cron.Remove(id)
		}
	}
	switch msg.ScheduleType {
	case "interval":
		if msg.IntervalMinutes == nil || *msg.IntervalMinutes < 1 {
			return fmt.Errorf("invalid interval minutes")
		}
		id := s.scheduleSlot(cron.Every(time.Duration(*msg.IntervalMinutes)*time.Minute), msg.ID)
		ids = append(ids, id)
	case "daily":
		if len(msg.DailyTimes) == 0 {
			return fmt.Errorf("daily times required")
		}
		for _, value := range msg.DailyTimes {
			hour, minute, err := parseHHMM(value)
			if err != nil {
				rollback()
				return err
			}
			spec := fmt.Sprintf("0 %d %d * * *", minute, hour)
			parsed, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(spec)
			if err != nil {
				rollback()
				return err
			}
			ids = append(ids, s.scheduleSlot(parsed, msg.ID))
		}
	default:
		return fmt.Errorf("invalid schedule type")
	}

	s.mu.Lock()
	for _, old := range s.entries[msg.ID] {
		s.cron.Remove(old)
	}
	s.entries[msg.ID] = ids
	s.mu.Unlock()
	return nil
}

func (s *Scheduler) Remove(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entryID := range s.entries[id] {
		s.cron.Remove(entryID)
	}
	delete(s.entries, id)
}

func (s *Scheduler) Reload(msg store.ScheduledMessage) error {
	s.Remove(msg.ID)
	return s.Add(msg)
}

func (s *Scheduler) NextRun(id int64) *time.Time {
	s.mu.Lock()
	entryIDs := append([]cron.EntryID(nil), s.entries[id]...)
	s.mu.Unlock()

	var next *time.Time
	for _, entryID := range entryIDs {
		entry := s.cron.Entry(entryID)
		if entry.ID == 0 || entry.Next.IsZero() {
			continue
		}
		candidate := entry.Next.UTC()
		if next == nil || candidate.Before(*next) {
			next = &candidate
		}
	}
	return next
}

// RunClaimedManual executes exactly the immutable snapshot already reserved in
// the database. No caller may obtain a second dispatch from the same claim.
func (s *Scheduler) RunClaimedManual(ctx context.Context, claim store.ScheduledManualResult) (string, *int64) {
	msg := claim.Message
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if s.ctx != nil {
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
	}
	settle := func(state string, id *int64, detail string) bool {
		settleCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		if err := s.queries.FinishScheduledDispatch(settleCtx, claim.AttemptID, msg.ChatID, id, msg.AutoDeleteSeconds, state, detail); err != nil {
			s.logger.Error("settle manual scheduled dispatch failed", zap.Int64("attempt_id", claim.AttemptID), zap.Error(err))
			return false
		}
		return true
	}
	if !s.acquireRun(msg.ID) {
		if !settle("failed", nil, "scheduled message is already running") {
			return "unknown", nil
		}
		return "failed", nil
	}
	defer s.releaseRun(msg.ID)
	if ctx.Err() != nil {
		if !settle("failed", nil, "request canceled before sending") {
			return "unknown", nil
		}
		return "failed", nil
	}
	rendered := RenderScheduledMessage(msg.Content, s.renderVars(ctx, msg)).Text
	if ctx.Err() != nil {
		if !settle("failed", nil, "request canceled before sending") {
			return "unknown", nil
		}
		return "failed", nil
	}
	messageID, rejected, err := s.sendManualSnapshot(ctx, msg, rendered)
	if err != nil || messageID <= 0 {
		state := "uncertain"
		if rejected {
			state = "failed"
		}
		detail := "send result unknown; verify Telegram"
		if err != nil {
			detail = "send failed or result unknown; verify Telegram: " + redact.ErrorString(err)
		}
		if !settle(state, nil, detail) {
			return "unknown", nil
		}
		settleCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		s.insertRun(settleCtx, msg.ID, false, nil, &rendered, &detail, started)
		done()
		if state == "failed" {
			return "failed", nil
		}
		return "unknown", nil
	}
	if !settle("sent", &messageID, "") {
		return "unknown", nil
	}
	settleCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	s.insertRun(settleCtx, msg.ID, true, &messageID, &rendered, nil, started)
	done()
	return "sent", &messageID
}

// sendManualSnapshot uses Raw so that a syntactically valid Telegram 4xx
// remains distinguishable from an unknown transport result even when Telebot
// maps an unfamiliar description to an untyped error. It deliberately never
// retries a request, including a rejected rate limit.
func (s *Scheduler) sendManualSnapshot(ctx context.Context, msg store.ScheduledMessage, rendered string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, true, err
	}
	if s.sendLimiter != nil {
		if err := s.sendLimiter.WaitChat(ctx, msg.ChatID); err != nil {
			return 0, true, err
		}
		if err := s.sendLimiter.WaitGlobal(ctx); err != nil {
			return 0, true, err
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, true, err
	}
	payload := map[string]any{"chat_id": msg.ChatID, "text": rendered, "parse_mode": tele.ModeMarkdownV2, "disable_web_page_preview": true}
	if markup := buildReplyMarkup(msg.Buttons); markup != nil {
		payload["reply_markup"] = markup
	}
	raw, err := s.bot.Raw("sendMessage", payload)
	var response struct {
		OK     *bool `json:"ok"`
		Code   int   `json:"error_code"`
		Result *struct {
			ID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) == nil && response.OK != nil {
		if !*response.OK && response.Code >= 400 && response.Code < 500 {
			return 0, true, fmt.Errorf("Telegram rejected send (%d)", response.Code)
		}
		if *response.OK && response.Result != nil && response.Result.ID > 0 && err == nil {
			return response.Result.ID, false, nil
		}
	}
	if err == nil {
		err = errors.New("invalid Telegram send response")
	}
	return 0, false, err
}

func (s *Scheduler) run(ctx context.Context, id int64, manual bool) error {
	return s.runSlot(ctx, id, manual, nil)
}

func (s *Scheduler) runSlot(ctx context.Context, id int64, manual bool, slot *time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if s.ctx != nil {
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
	}
	started := time.Now()
	if !s.acquireRun(id) {
		if manual {
			return errors.New("scheduled message is already running")
		}
		reason := "reentry skipped"
		s.logger.Warn("skip scheduled message reentry", zap.Int64("scheduled_message_id", id))
		s.insertRun(ctx, id, false, nil, nil, &reason, started)
		return nil
	}
	defer s.releaseRun(id)
	msg, err := s.queries.GetScheduledMessage(ctx, id)
	if err != nil {
		s.logger.Warn("load scheduled message before run failed", zap.Error(err), zap.Int64("scheduled_message_id", id))
		return err
	}
	if err := canRunScheduledMessage(msg, manual); err != nil {
		return err
	}
	if !manual && (!msg.Enabled || msg.Status != "active") {
		return nil
	}

	if !manual && msg.AutoDeleteSeconds > 0 && msg.LastMessageID != nil && msg.LastRunAt != nil {
		deleteAt := msg.LastRunAt.Add(time.Duration(msg.AutoDeleteSeconds) * time.Second)
		if deleteAt.After(time.Now()) {
			reason := "上一条消息尚未到自动删除时间，已跳过本次发送"
			s.logger.Warn("skip scheduled message because previous message is alive", zap.Int64("scheduled_message_id", msg.ID), zap.Int64("chat_id", msg.ChatID))
			_ = s.queries.MarkScheduledMessageSkipped(ctx, msg.ID, reason)
			s.insertRun(ctx, msg.ID, false, nil, nil, &reason, started)
			return nil
		}
	}

	rendered := RenderScheduledMessage(msg.Content, s.renderVars(ctx, msg)).Text
	if err := ctx.Err(); err != nil {
		return err // no send was attempted
	}
	options := scheduledSendOptions(msg.Buttons)

	attempt, claimed, err := s.queries.ClaimScheduledDispatch(ctx, msg.ID, msg.ChatID, slot)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	chat := &tele.Chat{ID: msg.ChatID}
	// A timeout or transport error can mean Telegram sent the message but the
	// response was lost. Only an explicit flood rejection is retried inside
	// sendThrottled; blindly retrying other errors can duplicate messages.
	sent, sendErr := s.sendThrottled(ctx, chat, rendered, options)
	// The send may finish after the caller's deadline or shutdown. Settlement
	// still needs a short independent window to record the result and prevent
	// an unknown send from remaining active solely because ctx was cancelled.
	settleCtx, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer settleCancel()

	if sendErr != nil {
		state := "uncertain"
		var te *tele.Error
		if errors.As(sendErr, &te) && te.Code >= 400 && te.Code < 500 {
			state = "failed"
		}
		errText := "send failed or result unknown; verify Telegram: " + redact.ErrorString(sendErr)
		if finishErr := s.queries.FinishScheduledDispatch(settleCtx, attempt, msg.ChatID, nil, 0, state, errText); finishErr != nil {
			s.logger.Error("settle scheduled dispatch failed; result unknown", zap.Int64("attempt_id", attempt), zap.Error(finishErr))
		}
		s.insertRun(settleCtx, msg.ID, false, nil, &rendered, &errText, started)
		return sendErr
	}

	if sent == nil || sent.ID <= 0 {
		return errors.New("Telegram returned invalid message ID; dispatch remains uncertain")
	}
	messageID := int64(sent.ID)
	if err := s.queries.FinishScheduledDispatch(settleCtx, attempt, msg.ChatID, &messageID, msg.AutoDeleteSeconds, "sent", ""); err != nil {
		s.logger.Error("settle scheduled dispatch failed; inspect Telegram and attempt", zap.Int64("attempt_id", attempt), zap.Error(err))
		return err
	}
	if !manual {
		if err := s.persistScheduledMessageSent(settleCtx, msg.ID, messageID, rendered, started); err != nil {
			s.logger.Warn("persist scheduled message success failed", zap.Error(err), zap.Int64("scheduled_message_id", msg.ID))
			return err
		}
	} else {
		s.insertRun(settleCtx, msg.ID, true, &messageID, &rendered, nil, started)
	}

	return nil
}

func canRunScheduledMessage(msg store.ScheduledMessage, manual bool) error {
	switch msg.Status {
	case "deleted", "archived":
		if manual {
			return errors.New("scheduled message is archived/deleted, cannot run")
		}
	}
	return nil
}

func (s *Scheduler) acquireRun(id int64) bool {
	_, loaded := s.running.LoadOrStore(id, struct{}{})
	return !loaded
}

func (s *Scheduler) releaseRun(id int64) {
	s.running.Delete(id)
}

func (s *Scheduler) Render(msg store.ScheduledMessage) string {
	return RenderScheduledMessage(msg.Content, s.renderVars(context.Background(), msg)).Text
}

func RenderScheduledMessage(template string, plainVars map[string]string) messagefmt.RenderedTemplate {
	return messagefmt.RenderMessageTemplate(template, tele.ModeMarkdownV2, nil, plainVars)
}

func (s *Scheduler) renderVars(ctx context.Context, msg store.ScheduledMessage) map[string]string {
	loc, err := time.LoadLocation(shanghaiTimezone)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	groupTitle := fmt.Sprintf("%d", msg.ChatID)
	if group, err := s.queries.GetGroupByChatID(ctx, msg.ChatID); err == nil && strings.TrimSpace(group.Title) != "" {
		groupTitle = group.Title
	}
	memberCount := "?"
	if ctx.Err() == nil {
		if count, err := s.bot.Len(&tele.Chat{ID: msg.ChatID}); err == nil {
			memberCount = fmt.Sprintf("%d", count)
		} else {
			s.logger.Warn("load chat member count failed", zap.Error(err), zap.Int64("chat_id", msg.ChatID), zap.Int64("scheduled_message_id", msg.ID))
		}
	}
	return map[string]string{
		"{group_title}":  groupTitle,
		"{member_count}": memberCount,
		"{date}":         now.Format("2006-01-02"),
		"{time}":         now.Format("15:04"),
		"{weekday}":      chineseWeekday(now.Weekday()),
	}
}

func RenderScheduledTemplate(template string, plainVars map[string]string) string {
	return RenderScheduledMessage(template, plainVars).Text
}

func scheduledSendOptions(buttons []byte) *tele.SendOptions {
	options := &tele.SendOptions{
		ParseMode:             tele.ModeMarkdownV2,
		DisableWebPagePreview: true,
	}
	if markup := buildReplyMarkup(buttons); markup != nil {
		options.ReplyMarkup = markup
	}
	return options
}

func buildReplyMarkup(raw []byte) *tele.ReplyMarkup {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var buttons []Button
	if err := json.Unmarshal(raw, &buttons); err != nil || len(buttons) == 0 {
		return nil
	}
	markup := &tele.ReplyMarkup{}
	rows := make([]tele.Row, 0, len(buttons))
	for _, button := range buttons {
		text := strings.TrimSpace(button.Text)
		url := strings.TrimSpace(button.URL)
		if text == "" || url == "" {
			continue
		}
		rows = append(rows, markup.Row(tele.Btn{Text: text, URL: url}))
	}
	if len(rows) == 0 {
		return nil
	}
	markup.Inline(rows...)
	return markup
}

func (s *Scheduler) persistScheduledMessageSent(ctx context.Context, id int64, messageID int64, rendered string, started time.Time) error {
	preview := truncateRunes(rendered, 500)
	duration := int32(time.Since(started).Milliseconds())
	return s.queries.Transact(ctx, func(q *store.Queries) error {
		if err := q.MarkScheduledMessageSent(ctx, id, messageID); err != nil {
			return err
		}
		_, err := q.InsertScheduledMessageRun(ctx, store.InsertScheduledMessageRunParams{
			ScheduledMessageID: id,
			Success:            true,
			TgMessageID:        &messageID,
			RenderedPreview:    &preview,
			Error:              nil,
			DurationMs:         &duration,
		})
		return err
	})
}

func (s *Scheduler) insertRun(ctx context.Context, id int64, success bool, messageID *int64, rendered *string, errText *string, started time.Time) {
	preview := truncateRunes("", 500)
	if rendered != nil {
		preview = truncateRunes(*rendered, 500)
	}
	duration := int32(time.Since(started).Milliseconds())
	if _, err := s.queries.InsertScheduledMessageRun(ctx, store.InsertScheduledMessageRunParams{
		ScheduledMessageID: id,
		Success:            success,
		TgMessageID:        messageID,
		RenderedPreview:    &preview,
		Error:              errText,
		DurationMs:         &duration,
	}); err != nil {
		s.logger.Warn("insert scheduled message run failed", zap.Error(err), zap.Int64("scheduled_message_id", id))
	}
}

func (s *Scheduler) sendThrottled(ctx context.Context, chat *tele.Chat, what interface{}, opts ...interface{}) (*tele.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.sendLimiter != nil {
		if err := s.sendLimiter.WaitChat(ctx, chat.ID); err != nil {
			return nil, err
		}
		if err := s.sendLimiter.WaitGlobal(ctx); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sent, err := s.bot.Send(chat, what, opts...)
	if err == nil {
		return sent, nil
	}
	retryAfter, ok := floodRetryAfter(err)
	if !ok {
		return nil, err
	}
	if err := sleepContext(ctx, time.Duration(retryAfter+1)*time.Second); err != nil {
		return nil, err
	}
	return s.bot.Send(chat, what, opts...)
}

func floodRetryAfter(err error) (int, bool) {
	// Telebot returns FloodError by value (and external implementations may
	// return a pointer); preserve the bounded retry_after for either form.
	var flood tele.FloodError
	if errors.As(err, &flood) {
		return flood.RetryAfter, true
	}
	var floodPtr *tele.FloodError
	if errors.As(err, &floodPtr) && floodPtr != nil {
		return floodPtr.RetryAfter, true
	}
	var teleErr *tele.Error
	if errors.As(err, &teleErr) && teleErr != nil && teleErr.Code == 429 {
		return 0, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseHHMM(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid daily time %q", value)
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func chineseWeekday(weekday time.Weekday) string {
	names := []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}
	return names[int(weekday)]
}

var mdv2Specials = []rune{'_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!'}

func mdv2Escape(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 2)
	for _, ch := range value {
		if isMDV2Special(ch) {
			b.WriteRune('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func isMDV2Special(ch rune) bool {
	for _, special := range mdv2Specials {
		if ch == special {
			return true
		}
	}
	return false
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
