package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
	tele "gopkg.in/telebot.v3"
)

func realExemptUser(u *tele.User) bool {
	return u != nil && u.ID > 0 && !u.IsBot && u.ID != 1087968824 && u.ID != 136817688 && u.ID != 777000
}
func parseExemptCommand(msg *tele.Message, remove bool) (int64, *time.Time, string, error) {
	fields := strings.Fields(msg.Text)
	if len(fields) == 0 {
		return 0, nil, "", errors.New("命令无效")
	}
	args := fields[1:]
	var id int64
	if msg.ReplyTo != nil {
		if msg.ReplyTo.SenderChat != nil || !realExemptUser(msg.ReplyTo.Sender) {
			return 0, nil, "", errors.New("回复目标不是可识别的真人用户")
		}
		id = msg.ReplyTo.Sender.ID
	} else {
		if len(args) == 0 {
			return 0, nil, "", errors.New("请回复真人消息或指定数字用户 ID")
		}
		for _, ch := range args[0] {
			if ch < '0' || ch > '9' {
				return 0, nil, "", errors.New("用户 ID 无效")
			}
		}
		var err error
		id, err = strconv.ParseInt(args[0], 10, 64)
		if err != nil || id <= 0 || id == 1087968824 || id == 136817688 || id == 777000 {
			return 0, nil, "", errors.New("用户 ID 无效")
		}
		args = args[1:]
	}
	if remove {
		if len(args) > 0 {
			return 0, nil, "", errors.New("撤销不接受其他参数")
		}
		return id, nil, "", nil
	}
	var until *time.Time
	durationLike := regexp.MustCompile(`^[+-]?[0-9].*$`)
	parseDuration := func(token string) (*time.Time, error) {
		t := strings.ToLower(token)
		if len(t) < 2 {
			return nil, errors.New("时长无效")
		}
		unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[t[len(t)-1]]
		n, err := strconv.ParseInt(t[:len(t)-1], 10, 64)
		if err != nil || unit == 0 || n <= 0 || n > int64((365*24*time.Hour)/unit) {
			return nil, errors.New("时长无效，使用 1m/1h/1d/1w，最多 365 天")
		}
		v := time.Now().Add(time.Duration(n) * unit)
		return &v, nil
	}
	if len(args) > 0 && args[0] != "--" && args[0] != "--note" && durationLike.MatchString(args[0]) && strings.IndexFunc(args[0], func(r rune) bool { return (r < '0' || r > '9') }) >= 0 {
		var e error
		until, e = parseDuration(args[0])
		if e != nil {
			return 0, nil, "", e
		}
		args = args[1:]
	}
	explicitNote := len(args) > 0 && (args[0] == "--" || args[0] == "--note")
	if explicitNote {
		args = args[1:]
	} else {
		for _, token := range args {
			if token == "--" || token == "--note" || (durationLike.MatchString(token) && strings.IndexFunc(token, func(r rune) bool { return r < '0' || r > '9' }) >= 0) {
				return 0, nil, "", errors.New("时长必须位于备注之前；歧义备注请用 --note")
			}
		}
	}

	note := strings.Join(args, " ")
	if len(note) > 500 {
		return 0, nil, "", errors.New("备注过长")
	}
	return id, until, note, nil
}

func (s *Service) handleGroupExemptCommand(c tele.Context, command string) error {
	msg := c.Message()
	chat := c.Chat()
	if msg == nil || chat == nil || (chat.Type != tele.ChatGroup && chat.Type != tele.ChatSuperGroup) {
		return c.Send("请在群内使用")
	}
	if msg.SenderChat != nil || !realExemptUser(msg.Sender) {
		return c.Send("无法验证操作者身份")
	}
	// Always ask Telegram directly; Web roles and cached group roles are not authority.
	member, err := s.bot.ChatMemberOf(chat, msg.Sender)
	if err != nil || member == nil || member.User == nil || member.User.ID != msg.Sender.ID || (member.Role != tele.Administrator && member.Role != tele.Creator) {
		return c.Send("仅本群管理员可用，或无法验证管理员身份")
	}
	ctx := context.Background()
	if command == "exemptlist" {
		fields := strings.Fields(msg.Text)
		page := 1
		if len(fields) > 2 {
			return c.Send("用法：/exemptlist [页码]")
		}
		if len(fields) == 2 {
			page, err = strconv.Atoi(fields[1])
			if err != nil || page < 1 || page > 100000 {
				return c.Send("页码无效")
			}
		}
		items, err := s.queries.ListGroupExemptUsers(ctx, chat.ID)
		if err != nil {
			return c.Send("名单加载失败")
		}
		start := (page - 1) * 20
		if start >= len(items) {
			return c.Send("本页无豁免用户")
		}
		end := start + 20
		if end > len(items) {
			end = len(items)
		}
		var out strings.Builder
		fmt.Fprintf(&out, "本群豁免（第 %d 页）：\n", page)
		for _, u := range items[start:end] {
			expiry := "永久"
			if u.ExpiresAt != nil {
				expiry = u.ExpiresAt.UTC().Format(time.RFC3339)
				if !u.ExpiresAt.After(time.Now()) {
					expiry += "（已过期）"
				}
			}
			fmt.Fprintf(&out, "%d · %s\n", u.UserID, expiry)
		}
		return c.Send(out.String())
	}
	id, until, note, err := parseExemptCommand(msg, command == "unexempt")
	if err != nil {
		return c.Send(err.Error() + "；用法：/exempt ID [1d] [备注] 或回复消息 /exempt [1d] [备注]；歧义备注用 --note；/unexempt ID 或回复消息")
	}
	if command == "exempt" {
		knownBot, lookupErr := s.ExemptionTargetIsKnownBot(ctx, chat.ID, id)
		if lookupErr != nil {
			return c.Send("无法核实用户身份，请重试")
		}
		if knownBot {
			return c.Send("机器人不能加入真人豁免，请使用独立机器人白名单")
		}
	}
	err = s.queries.Transact(ctx, func(q *store.Queries) error {
		if err := q.LockGroupExemptUser(ctx, chat.ID, id); err != nil {
			return err
		}
		before, e := q.GetGroupExemptUser(ctx, chat.ID, id)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if command == "unexempt" && errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		old := []byte("null")
		if e == nil {
			old, _ = json.Marshal(before)
		}
		after := []byte("null")
		if command == "exempt" {
			record, e := q.UpsertGroupExemptUser(ctx, chat.ID, id, note, until)
			if e != nil {
				return e
			}
			after, _ = json.Marshal(record)
		} else if _, e = q.DeleteGroupExemptUser(ctx, chat.ID, id); e != nil {
			return e
		}
		actor, _ := json.Marshal(map[string]any{"source": "telegram", "telegram_user_id": msg.Sender.ID, "command": command})
		diff := string(actor)
		_, e = q.InsertAuditEntry(ctx, store.InsertAuditEntryParams{Scope: "group", ChatID: &chat.ID, AdminID: 0, Action: "command_" + command, Before: old, After: after, Diff: &diff})
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Send("本群不存在该豁免")
	}
	if err != nil {
		return c.Send("豁免操作失败")
	}
	return c.Send(fmt.Sprintf("本群用户 %d 豁免已%s；已有禁言或封禁不会自动解除", id, map[bool]string{true: "撤销", false: "保存"}[command == "unexempt"]))
}
