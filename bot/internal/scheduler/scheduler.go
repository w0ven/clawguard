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

	mu      sync.Mutex
	entries map[int64][]cron.EntryID
}

func New(logger *zap.Logger, queries *store.Queries, bot *tele.Bot, sendLimiter interface {
	WaitChat(context.Context, int64) error
	WaitGlobal(context.Context) error
}) *Scheduler {
	return &Scheduler{
		cron:        cron.New(cron.WithLocation(time.UTC), cron.WithSeconds()),
		queries:     queries,
		bot:         bot,
		sendLimiter: sendLimiter,
		logger:      logger,
		entries:     make(map[int64][]cron.EntryID),
	}
}

func (s *Scheduler) Start() {
	s.cron.Start()
}

func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
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
	switch msg.ScheduleType {
	case "interval":
		if msg.IntervalMinutes == nil || *msg.IntervalMinutes < 1 {
			return fmt.Errorf("invalid interval minutes")
		}
		id := s.cron.Schedule(cron.Every(time.Duration(*msg.IntervalMinutes)*time.Minute), cron.FuncJob(func() {
			_ = s.run(context.Background(), msg.ID, false)
		}))
		ids = append(ids, id)
	case "daily":
		if len(msg.DailyTimes) == 0 {
			return fmt.Errorf("daily times required")
		}
		for _, value := range msg.DailyTimes {
			hour, minute, err := parseHHMM(value)
			if err != nil {
				return err
			}
			spec := fmt.Sprintf("0 %d %d * * *", minute, hour)
			id, err := s.cron.AddFunc(spec, func(id int64) func() {
				return func() { _ = s.run(context.Background(), id, false) }
			}(msg.ID))
			if err != nil {
				return err
			}
			ids = append(ids, id)
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

func (s *Scheduler) RunNow(ctx context.Context, id int64) error {
	return s.run(ctx, id, true)
}

func (s *Scheduler) run(ctx context.Context, id int64, manual bool) error {
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
	if !msg.Enabled || msg.Status != "active" {
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

	rendered := s.Render(msg)
	options := &tele.SendOptions{
		ParseMode:             tele.ModeMarkdownV2,
		DisableWebPagePreview: true,
	}
	if markup := buildReplyMarkup(msg.Buttons); markup != nil {
		options.ReplyMarkup = markup
	}

	chat := &tele.Chat{ID: msg.ChatID}
	var sent *tele.Message
	var sendErr error
	for attempt, delay := range []time.Duration{0, 10 * time.Second, 30 * time.Second} {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		sent, sendErr = s.sendThrottled(ctx, chat, rendered, options)
		if sendErr == nil {
			break
		}
		s.logger.Warn("send scheduled message failed", zap.Error(sendErr), zap.Int64("scheduled_message_id", msg.ID), zap.Int("attempt", attempt+1))
	}

	if sendErr != nil {
		errText := sendErr.Error()
		if !manual {
			if markErr := s.queries.MarkScheduledMessageFailed(ctx, msg.ID, errText); markErr != nil {
				s.logger.Warn("mark scheduled message failed state failed", zap.Error(markErr), zap.Int64("scheduled_message_id", msg.ID), zap.Int64("chat_id", msg.ChatID))
			} else {
				s.Remove(msg.ID)
				s.logger.Warn("scheduled message disabled after send failures", zap.Error(sendErr), zap.Int64("scheduled_message_id", msg.ID), zap.Int64("chat_id", msg.ChatID))
			}
		}
		s.insertRun(ctx, msg.ID, false, nil, &rendered, &errText, started)
		return sendErr
	}

	messageID := int64(sent.ID)
	if !manual {
		if err := s.persistScheduledMessageSent(ctx, msg.ID, messageID, rendered, started); err != nil {
			s.logger.Warn("persist scheduled message success failed", zap.Error(err), zap.Int64("scheduled_message_id", msg.ID))
			return err
		}
	} else {
		s.insertRun(ctx, msg.ID, true, &messageID, &rendered, nil, started)
	}

	if msg.AutoDeleteSeconds > 0 {
		go s.deleteLater(msg.ChatID, sent.ID, time.Duration(msg.AutoDeleteSeconds)*time.Second)
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
	return RenderScheduledTemplate(msg.Content, s.renderVars(msg))
}

func (s *Scheduler) renderVars(msg store.ScheduledMessage) map[string]string {
	loc, err := time.LoadLocation(shanghaiTimezone)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	groupTitle := fmt.Sprintf("%d", msg.ChatID)
	if group, err := s.queries.GetGroupByChatID(context.Background(), msg.ChatID); err == nil && strings.TrimSpace(group.Title) != "" {
		groupTitle = group.Title
	}
	memberCount := "?"
	if count, err := s.bot.Len(&tele.Chat{ID: msg.ChatID}); err == nil {
		memberCount = fmt.Sprintf("%d", count)
	} else {
		s.logger.Warn("load chat member count failed", zap.Error(err), zap.Int64("chat_id", msg.ChatID), zap.Int64("scheduled_message_id", msg.ID))
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
	rendered := template
	for key, value := range plainVars {
		rendered = strings.ReplaceAll(rendered, key, mdv2Escape(value))
	}
	return rendered
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

func (s *Scheduler) deleteLater(chatID int64, messageID int, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	<-timer.C
	if err := s.bot.Delete(&tele.Message{ID: messageID, Chat: &tele.Chat{ID: chatID}}); err != nil {
		s.logger.Warn("auto delete scheduled message failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int("message_id", messageID))
	}
}

func (s *Scheduler) sendThrottled(ctx context.Context, chat *tele.Chat, what interface{}, opts ...interface{}) (*tele.Message, error) {
	if s.sendLimiter != nil {
		if err := s.sendLimiter.WaitChat(ctx, chat.ID); err != nil {
			return nil, err
		}
		if err := s.sendLimiter.WaitGlobal(ctx); err != nil {
			return nil, err
		}
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
	var flood *tele.FloodError
	if errors.As(err, &flood) && flood != nil {
		return flood.RetryAfter, true
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
