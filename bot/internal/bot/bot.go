package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/casclient"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/store"
)

type Service struct {
	cfg              config.Config
	logger           *zap.Logger
	queries          *store.Queries
	redis            redis.Cmdable
	casClient        *casclient.Client
	aiProviders      ai.ProviderRegistry
	aiModels         ai.ModelRegistry
	aiResolver       *ai.Resolver
	aiModerator      *ai.Moderator
	bot              *tele.Bot
	verifyBtn        tele.Btn
	verifyMathBtn    tele.Btn
	verifyRandBtn    tele.Btn
	startedAt        time.Time
	lastUpdateAt     atomic.Value
	lastAIOKAt       atomic.Value
	lastAIFailAt     atomic.Value
	lastAIError      atomic.Value
	bioCheckInFlight sync.Map
}

type buttonPayload struct {
	ButtonUnique string    `json:"button_unique"`
	CreatedAt    time.Time `json:"created_at"`
}

type mathPayload struct {
	Answer     int    `json:"answer"`
	Expression string `json:"expression,omitempty"`
}

type randomPayload struct {
	CorrectEmoji string `json:"correct_emoji"`
}

type verifyCallbackPayload struct {
	UserID int64
	Value  string
}

// computeProfileCheckTimeout gives the AI fallback chain enough budget to complete
// one full pass: per-model timeout * (primary + fallbacks), capped at 25s.
func computeProfileCheckTimeout(policy config.GuardPolicy) time.Duration {
	const (
		minTimeout = 5 * time.Second
		maxTimeout = 25 * time.Second
	)

	perCall := time.Duration(policy.AI.TimeoutMs) * time.Millisecond
	if perCall <= 0 {
		perCall = 10 * time.Second
	}

	fallbackCount := len(policy.AI.FallbackModelRefs)
	if fallbackCount == 0 {
		fallbackCount = len(policy.AI.FallbackChain)
	}

	total := perCall * time.Duration(fallbackCount+1)
	if total < minTimeout {
		return minTimeout
	}
	if total > maxTimeout {
		return maxTimeout
	}
	return total
}

func New(cfg config.Config, logger *zap.Logger, queries *store.Queries, rdb redis.Cmdable, providers ai.ProviderRegistry, models ai.ModelRegistry, resolver *ai.Resolver) (*Service, error) {
	verifyBtn := tele.Btn{Unique: "verify_human"}
	verifyMathBtn := tele.Btn{Unique: "verify_math"}
	verifyRandBtn := tele.Btn{Unique: "verify_random"}

	b, err := tele.NewBot(tele.Settings{
		Token:       cfg.BotToken,
		Synchronous: true,
	})
	if err != nil {
		return nil, fmt.Errorf("new telebot: %w", err)
	}

	svc := &Service{
		cfg:           cfg,
		logger:        logger,
		queries:       queries,
		redis:         rdb,
		casClient:     casclient.New(nil, rdb),
		bot:           b,
		verifyBtn:     verifyBtn,
		verifyMathBtn: verifyMathBtn,
		verifyRandBtn: verifyRandBtn,
		startedAt:     time.Now().UTC(),
	}

	svc.aiProviders = providers
	svc.aiModels = models
	svc.aiResolver = resolver
	svc.aiModerator = ai.NewModerator(logger, rdb, queries, providers, models, resolver, svc)

	svc.registerHandlers()

	return svc, nil
}

func (s *Service) RegisterWebhook(ctx context.Context) error {
	if err := s.bot.SetWebhook(&tele.Webhook{
		Endpoint:       &tele.WebhookEndpoint{PublicURL: s.cfg.WebhookURL()},
		SecretToken:    s.cfg.WebhookSecret,
		AllowedUpdates: []string{"message", "edited_message", "callback_query", "chat_member", "my_chat_member"},
	}); err != nil {
		return fmt.Errorf("set webhook: %w", err)
	}

	s.logger.Info("telegram webhook registered", zap.String("url", s.cfg.WebhookURL()))
	if err := s.setupCommandMenu(); err != nil {
		s.logger.Warn("setup command menu failed", zap.Error(err))
	}
	return nil
}

func (s *Service) setupCommandMenu() error {
	groupCommands := []tele.Command{
		{Text: "start", Description: "机器人介绍"},
		{Text: "help", Description: "命令列表"},
		{Text: "status", Description: "本群今日统计"},
		{Text: "trust", Description: "查看用户信任分（回复消息或 @用户）"},
		{Text: "config", Description: "打开管理面板"},
		{Text: "warn", Description: "警告用户 /warn @user 原因"},
		{Text: "unban", Description: "解封用户 /unban @user"},
		{Text: "spam", Description: "封禁用户 /spam @user"},
		{Text: "cas", Description: "查询 CAS 黑名单状态"},
		{Text: "warn_status", Description: "查看警告记录"},
	}
	// 群内管理员菜单
	if _, err := s.bot.Raw("setMyCommands", map[string]any{
		"commands": groupCommands,
		"scope":    map[string]string{"type": "all_chat_administrators"},
	}); err != nil {
		return fmt.Errorf("set group commands: %w", err)
	}
	// 私聊菜单
	privateCommands := []tele.Command{
		{Text: "start", Description: "机器人介绍"},
		{Text: "help", Description: "命令列表"},
		{Text: "status", Description: "查看服务状态"},
		{Text: "trust", Description: "查询用户信任分 /trust <user_id>"},
		{Text: "config", Description: "打开管理面板"},
	}
	if _, err := s.bot.Raw("setMyCommands", map[string]any{
		"commands": privateCommands,
		"scope":    map[string]string{"type": "all_private_chats"},
	}); err != nil {
		return fmt.Errorf("set private commands: %w", err)
	}
	return nil
}

func (s *Service) ProcessUpdate(update tele.Update) error {
	s.bot.ProcessUpdate(update)
	return nil
}

func (s *Service) SendHTMLPrivateMessage(telegramID int64, text string) error {
	if telegramID == 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := s.bot.Send(&tele.User{ID: telegramID}, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
	return err
}

func (s *Service) registerHandlers() {
	s.bot.Handle(tele.OnUserJoined, func(c tele.Context) error {
		return s.handleUserJoined(c)
	})

	s.bot.Handle(tele.OnMyChatMember, func(c tele.Context) error {
		return s.handleMyChatMemberUpdate(c)
	})

	s.bot.Handle(tele.OnChatMember, func(c tele.Context) error {
		return s.handleChatMemberUpdate(c)
	})

	s.bot.Handle(&s.verifyBtn, func(c tele.Context) error {
		return s.handleVerifyButton(c)
	})

	s.bot.Handle(&s.verifyMathBtn, func(c tele.Context) error {
		return s.handleVerifyMath(c)
	})

	s.bot.Handle(&s.verifyRandBtn, func(c tele.Context) error {
		return s.handleVerifyRandom(c)
	})

	s.bot.Handle(tele.OnText, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnPhoto, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnVideo, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnDocument, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnContact, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnSticker, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnAnimation, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnVoice, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnAudio, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle(tele.OnVideoNote, func(c tele.Context) error {
		return s.handleIncomingMessage(c)
	})
	s.bot.Handle("/cas", func(c tele.Context) error {
		return s.handleCASCommand(c)
	})
	s.bot.Handle("/start", func(c tele.Context) error {
		return s.handleStartCommand(c)
	})
	s.bot.Handle("/help", func(c tele.Context) error {
		return s.handleHelpCommand(c)
	})
	s.bot.Handle("/warn_status", func(c tele.Context) error {
		return s.handleWarnStatusCommand(c)
	})
	s.bot.Handle("/status", func(c tele.Context) error {
		return s.handleStatusCommand(c)
	})
	s.bot.Handle("/trust", func(c tele.Context) error {
		return s.handleTrustCommand(c)
	})
	s.bot.Handle("/config", func(c tele.Context) error {
		return s.handleConfigCommand(c)
	})
	s.bot.Handle("/warn", func(c tele.Context) error {
		return s.handleWarnCommand(c)
	})
	s.bot.Handle("/unban", func(c tele.Context) error {
		return s.handleUnbanCommand(c)
	})
	s.bot.Handle("/spam", func(c tele.Context) error {
		return s.handleSpamCommand(c)
	})
}

func (s *Service) handleUserJoined(c tele.Context) error {
	s.MarkUpdateSeen()
	chat := c.Chat()
	msg := c.Message()
	user := c.Sender()
	if chat == nil || msg == nil || user == nil {
		return nil
	}

	return s.startVerification(chat, user, msg)
}

func (s *Service) handleChatMemberUpdate(c tele.Context) error {
	s.MarkUpdateSeen()
	update := c.ChatMember()
	if update == nil || update.Chat == nil || update.NewChatMember == nil || update.OldChatMember == nil {
		return nil
	}

	member := update.NewChatMember
	if member.User == nil {
		return nil
	}

	if s.bot.Me != nil && member.User.ID == s.bot.Me.ID {
		return s.handleBotChatMemberUpdate(update)
	}

	if member.User.IsBot {
		return nil
	}

	if !isJoinTransition(update.OldChatMember, member) {
		return nil
	}

	return s.startVerification(update.Chat, member.User, nil)
}

func (s *Service) handleBotChatMemberUpdate(update *tele.ChatMemberUpdate) error {
	if update == nil || update.Chat == nil || update.NewChatMember == nil || update.OldChatMember == nil {
		return nil
	}
	if !isJoinTransition(update.OldChatMember, update.NewChatMember) {
		return nil
	}

	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, update.Chat.ID)
	if err != nil {
		s.logger.Warn("check authorized group failed, allow bot stay", zap.Error(err), zap.Int64("chat_id", update.Chat.ID))
		return nil
	}
	if authorized {
		if _, err := s.queries.UpsertGroup(ctx, store.UpsertGroupParams{
			ChatID:      update.Chat.ID,
			Title:       update.Chat.Title,
			Type:        string(update.Chat.Type),
			MemberCount: 0,
		}); err != nil {
			s.logger.Warn("upsert group on bot join failed", zap.Error(err), zap.Int64("chat_id", update.Chat.ID))
		}
		return nil
	}

	chatID := update.Chat.ID
	s.WriteRuntimeAudit(ctx, "group_auth", &chatID, "unauthorized_group_leave", map[string]any{
		"chat_id": chatID,
		"title":   update.Chat.Title,
	}, map[string]any{
		"left": true,
	})
	if err := s.LeaveChat(chatID); err != nil {
		s.logger.Warn("leave unauthorized group failed", zap.Error(err), zap.Int64("chat_id", chatID))
	}
	return nil
}

func (s *Service) startVerification(chat *tele.Chat, user *tele.User, joinEventMessage *tele.Message) error {
	if chat == nil || user == nil {
		return nil
	}

	flowStartedAt := time.Now()
	ctx := context.Background()
	policyStartedAt := time.Now()
	policy, err := config.LoadPolicy(ctx, s.queries, chat.ID)
	if err != nil {
		s.logger.Warn("load guard policy failed, using defaults", zap.Error(err), zap.Int64("chat_id", chat.ID))
		policy = config.DefaultPolicy
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "load_policy"),
		zap.Duration("elapsed", time.Since(policyStartedAt)),
	)
	if !policy.Verify.Enabled {
		s.logger.Info("verification disabled by policy, skip flow", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	}

	member := tele.ChatMember{
		User:   user,
		Rights: tele.NoRights(),
	}
	restrictStartedAt := time.Now()
	if err := s.bot.Restrict(chat, &member); err != nil {
		s.logger.Error("restrict new member", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "restrict"),
		zap.Duration("elapsed", time.Since(restrictStartedAt)),
	)

	// 去重锁：避免 OnUserJoined 和 OnChatMember 对同一 join 事件双触发
	if s.redis != nil {
		lockKey := fmt.Sprintf("clawguard:verify:lock:%d:%d", chat.ID, user.ID)
		set, lockErr := s.redis.SetNX(ctx, lockKey, "1", 10*time.Second).Result()
		if lockErr == nil && !set {
			s.logger.Debug("skip duplicate join event", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			return nil
		}
	}

	s.startAsyncVerificationChecks(chat, user, policy)

	if policy.Verify.DeleteJoinMessage && joinEventMessage != nil {
		go s.deleteJoinEventMessageAsync(chat, user, joinEventMessage)
	}

	verifyStartedAt := time.Now()
	if err := s.startVerificationPrompt(ctx, chat, user, policy); err != nil {
		return err
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "send_verification_prompt"),
		zap.Duration("elapsed", time.Since(verifyStartedAt)),
	)
	s.logger.Info("verification flow started",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.Duration("elapsed", time.Since(flowStartedAt)),
	)
	return nil
}

func (s *Service) startVerificationPrompt(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	method := policy.Verify.Method
	switch method {
	case "", "button":
		return s.startButtonVerification(ctx, chat, user, policy)
	case "math":
		return s.startMathVerification(ctx, chat, user, policy)
	case "math_image":
		return s.sendMathImageChallenge(ctx, chat, user, policy)
	case "random":
		return s.startRandomVerification(ctx, chat, user, policy)
	case "turnstile":
		return s.startTurnstileVerification(ctx, chat, user, policy)
	default:
		s.logger.Warn(
			"unknown verification method, fallback to button",
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
			zap.String("method", method),
		)
		return s.startButtonVerification(ctx, chat, user, policy)
	}
}

func (s *Service) startAsyncVerificationChecks(chat *tele.Chat, user *tele.User, policy config.GuardPolicy) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		totalStartedAt := time.Now()
		s.upsertJoinSideEffects(ctx, chat, user)

		if policy.AntiSpam.CASEnabled && s.casClient != nil {
			casStartedAt := time.Now()
			casCtx, casCancel := context.WithTimeout(ctx, 3*time.Second)
			banned, result, err := s.casClient.IsBanned(casCtx, user.ID)
			casCancel()
			s.logger.Info("verification step completed",
				zap.Int64("chat_id", chat.ID),
				zap.Int64("user_id", user.ID),
				zap.String("step", "cas_lookup"),
				zap.Duration("elapsed", time.Since(casStartedAt)),
			)
			if err != nil {
				s.logger.Warn("cas lookup failed, skip", zap.Error(err), zap.Int64("user_id", user.ID))
			} else if banned {
				if err := s.handleAsyncVerificationMatch(ctx, asyncVerificationMatch{
					chat:         chat,
					user:         user,
					policy:       policy,
					rule:         "cas_banned",
					matched:      stringifyCASResult(result),
					messageText:  stringifyCASResult(result),
					feedbackText: "CAS 黑名单",
					feedbackKind: "cas",
					logLabel:     "cas matched user",
					decisionMode: "cas",
				}); err != nil {
					s.logger.Error("handle cas match failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
				}
				s.logger.Info("verification async checks finished",
					zap.Int64("chat_id", chat.ID),
					zap.Int64("user_id", user.ID),
					zap.Duration("elapsed", time.Since(totalStartedAt)),
				)
				return
			}
		}

		profileStartedAt := time.Now()
		profileCtx, profileCancel := context.WithTimeout(ctx, computeProfileCheckTimeout(policy))
		matched, aiOutput, err := s.checkProfile(profileCtx, chat, user, policy)
		profileCancel()
		s.logger.Info("verification step completed",
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
			zap.String("step", "profile_check"),
			zap.Duration("elapsed", time.Since(profileStartedAt)),
		)
		if err != nil {
			s.logger.Warn("profile check failed, skip", zap.Error(err), zap.Int64("user_id", user.ID))
		} else if matched != "" {
			payload, _ := json.Marshal(map[string]string{"matched": matched})
			decisionMode := "join_keyword"
			if strings.EqualFold(strings.TrimSpace(policy.Verify.ProfileCheckMode), "ai") {
				decisionMode = "join_ai"
			}
			if err := s.handleAsyncVerificationMatch(ctx, asyncVerificationMatch{
				chat:         chat,
				user:         user,
				policy:       policy,
				rule:         "profile_match",
				matched:      stringPtr(matched),
				messageText:  stringPtr(string(payload)),
				feedbackText: "个人简介违规：" + matched,
				feedbackKind: "profile",
				logLabel:     "profile matched user",
				aiOutput:     aiOutput,
				decisionMode: decisionMode,
			}); err != nil {
				s.logger.Error("handle profile match failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			}
		}

		s.logger.Info("verification async checks finished",
			zap.Int64("chat_id", chat.ID),
			zap.Int64("user_id", user.ID),
			zap.Duration("elapsed", time.Since(totalStartedAt)),
		)
	}()
}

func (s *Service) upsertJoinSideEffects(ctx context.Context, chat *tele.Chat, user *tele.User) {
	groupStartedAt := time.Now()
	if _, err := s.queries.UpsertGroup(ctx, store.UpsertGroupParams{
		ChatID:      chat.ID,
		Title:       chat.Title,
		Type:        string(chat.Type),
		MemberCount: 0,
	}); err != nil {
		s.logger.Error("upsert group", zap.Error(err), zap.Int64("chat_id", chat.ID))
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "upsert_group"),
		zap.Duration("elapsed", time.Since(groupStartedAt)),
	)

	trustStartedAt := time.Now()
	if _, err := s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
		ChatID:          chat.ID,
		UserID:          user.ID,
		Username:        userFieldPtr(user.Username),
		FirstName:       userFieldPtr(user.FirstName),
		LastName:        userFieldPtr(user.LastName),
		JoinedAt:        time.Now(),
		Status:          "new",
		Score:           0.5,
		MessagesChecked: 0,
		MessagesClean:   0,
	}); err != nil {
		s.logger.Warn("upsert user trust on join failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "upsert_user_trust"),
		zap.Duration("elapsed", time.Since(trustStartedAt)),
	)
}

func (s *Service) deleteJoinEventMessageAsync(chat *tele.Chat, user *tele.User, joinEventMessage *tele.Message) {
	startedAt := time.Now()
	if err := s.bot.Delete(joinEventMessage); err != nil {
		s.logger.Warn("delete join event message", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int("message_id", joinEventMessage.ID))
		return
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "delete_join_message"),
		zap.Duration("elapsed", time.Since(startedAt)),
	)
}

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
	aiOutput     *ai.CheckOutput
	decisionMode string
}

type asyncVerificationMatchOps struct {
	banUser                   func(*tele.Chat, *tele.User) error
	awaitPendingVerification  func(context.Context, int64, int64, time.Duration) (store.PendingVerification, error)
	deleteVerificationMessage func(*tele.Chat, *int64)
	deletePendingVerification func(context.Context, int64, int64) error
	insertViolation           func(context.Context, store.InsertViolationParams) error
	sendCASFeedback           func()
	sendProfileFeedback       func()
	recordAIDecision          func(context.Context, *tele.Chat, *tele.User, string, string, *ai.CheckOutput) error
	updateTrustBanned         func(context.Context, int64, int64, string)
	logger                    *zap.Logger
}

func (s *Service) handleAsyncVerificationMatch(ctx context.Context, match asyncVerificationMatch) error {
	if match.feedbackKind == "profile" && match.decisionMode == "" {
		match.decisionMode = "join_keyword"
		if strings.EqualFold(strings.TrimSpace(match.policy.Verify.ProfileCheckMode), "ai") {
			match.decisionMode = "join_ai"
		}
	}
	return performAsyncVerificationMatch(ctx, asyncVerificationMatchOps{
		banUser:                  s.banUser,
		awaitPendingVerification: s.awaitPendingVerification,
		deleteVerificationMessage: func(chat *tele.Chat, messageID *int64) {
			s.deleteVerificationMessage(chat, messageID)
		},
		deletePendingVerification: func(ctx context.Context, chatID, userID int64) error {
			return s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
				ChatID: chatID,
				UserID: userID,
			})
		},
		insertViolation: func(ctx context.Context, params store.InsertViolationParams) error {
			_, err := s.queries.InsertViolation(ctx, params)
			return err
		},
		sendCASFeedback: func() {
			s.sendActionFeedback(match.chat, nil, match.policy.Feedback.CASHit, map[string]string{
				"user":         feedbackUserLabel(match.user, match.policy.Feedback.CASHit.ParseMode),
				"user_mention": feedbackUserMention(match.user, match.policy.Feedback.CASHit.ParseMode),
				"group":        match.chat.Title,
				"reason":       match.feedbackText,
			})
		},
		sendProfileFeedback: func() {
			s.sendActionFeedback(match.chat, nil, match.policy.Feedback.VerifyFail, map[string]string{
				"user":         feedbackUserLabel(match.user, match.policy.Feedback.VerifyFail.ParseMode),
				"user_mention": feedbackUserMention(match.user, match.policy.Feedback.VerifyFail.ParseMode),
				"reason":       match.feedbackText,
			})
		},
		recordAIDecision: func(ctx context.Context, chat *tele.Chat, user *tele.User, matched string, mode string, aiOutput *ai.CheckOutput) error {
			return s.recordProfileViolationDecision(ctx, chat, user, nil, matched, mode, aiOutput)
		},
		updateTrustBanned: func(ctx context.Context, chatID, userID int64, notes string) {
			fakeMsg := &tele.Message{
				Chat:   match.chat,
				Sender: match.user,
			}
			s.resetTrustAfterViolation(ctx, fakeMsg, "ban", stringPtr(notes))
		},
		logger: s.logger,
	}, match)
}

func performAsyncVerificationMatch(ctx context.Context, ops asyncVerificationMatchOps, match asyncVerificationMatch) error {
	if err := ops.banUser(match.chat, match.user); err != nil {
		ops.logger.Error(match.logLabel, zap.Error(err), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID))
		return err
	}

	pending, err := ops.awaitPendingVerification(ctx, match.chat.ID, match.user.ID, 2*time.Second)
	if err != nil && err != pgx.ErrNoRows {
		ops.logger.Warn("load pending verification for async cleanup failed", zap.Error(err), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID))
	}
	if err == nil {
		ops.deleteVerificationMessage(match.chat, pending.JoinMessageID)
		if delErr := ops.deletePendingVerification(ctx, match.chat.ID, match.user.ID); delErr != nil {
			ops.logger.Warn("delete pending verification after async match failed", zap.Error(delErr), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID))
		}
	}

	if err := ops.insertViolation(ctx, store.InsertViolationParams{
		ChatID:      match.chat.ID,
		UserID:      match.user.ID,
		Username:    stringPtr(match.user.Username),
		Rule:        match.rule,
		Matched:     match.matched,
		Action:      "ban",
		MessageText: match.messageText,
	}); err != nil {
		ops.logger.Warn("insert async verification violation failed", zap.Error(err), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID), zap.String("rule", match.rule))
	}
	if ops.recordAIDecision != nil && match.decisionMode != "" && match.decisionMode != "cas" && match.matched != nil {
		if err := ops.recordAIDecision(ctx, match.chat, match.user, *match.matched, match.decisionMode, match.aiOutput); err != nil {
			ops.logger.Warn("record profile violation ai_decision failed", zap.Error(err), zap.Int64("chat_id", match.chat.ID), zap.Int64("user_id", match.user.ID))
		}
	}
	if ops.updateTrustBanned != nil {
		reason := match.rule
		if match.matched != nil {
			reason = match.rule + ": " + *match.matched
		}
		ops.updateTrustBanned(ctx, match.chat.ID, match.user.ID, reason)
	}

	switch match.feedbackKind {
	case "cas":
		ops.sendCASFeedback()
	default:
		ops.sendProfileFeedback()
	}

	return nil
}

func (s *Service) awaitPendingVerification(ctx context.Context, chatID, userID int64, wait time.Duration) (store.PendingVerification, error) {
	deadlineCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		pending, err := s.queries.GetPendingVerification(deadlineCtx, store.GetPendingVerificationParams{
			ChatID: chatID,
			UserID: userID,
		})
		if err == nil {
			return pending, nil
		}
		if err != pgx.ErrNoRows {
			return store.PendingVerification{}, err
		}

		select {
		case <-deadlineCtx.Done():
			return store.PendingVerification{}, pgx.ErrNoRows
		case <-ticker.C:
		}
	}
}

func (s *Service) startButtonVerification(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	markup := &tele.ReplyMarkup{}
	button := markup.Data("我是人类 ✅", s.verifyBtn.Unique, strconv.FormatInt(user.ID, 10))
	markup.Inline(markup.Row(button))

	prompt := fmt.Sprintf(`<a href="tg://user?id=%d">%s</a> 你好，请在 %s 内<b>先阅读下面文字 3 秒</b>后再点击按钮`, user.ID, htmlEscape(displayName(user)), formatTimeout(policy.Verify.TimeoutSeconds))
	sent, err := s.bot.Send(chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	})
	if err != nil {
		s.logger.Error("send verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	payload, err := json.Marshal(buttonPayload{
		ButtonUnique: s.verifyBtn.Unique,
		CreatedAt:    time.Now(),
	})
	if err != nil {
		return err
	}

	return s.storePendingVerification(ctx, chat.ID, user, "button", payload, sent.ID, policy.Verify.TimeoutSeconds)
}

func (s *Service) startMathVerification(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	question, answer, options := buildMathChallenge()

	markup := &tele.ReplyMarkup{}
	row := make([]tele.Btn, 0, len(options))
	for _, option := range options {
		row = append(row, markup.Data(strconv.Itoa(option), s.verifyMathBtn.Unique, formatVerifyCallbackData(user.ID, strconv.Itoa(option))))
	}
	markup.Inline(row)

	prompt := fmt.Sprintf(`<a href="tg://user?id=%d">%s</a> 你好，请在 %s 内回答：%s`, user.ID, htmlEscape(displayName(user)), formatTimeout(policy.Verify.TimeoutSeconds), htmlEscape(question))
	sent, err := s.bot.Send(chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	})
	if err != nil {
		s.logger.Error("send math verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	payload, err := json.Marshal(mathPayload{Answer: answer})
	if err != nil {
		return err
	}

	return s.storePendingVerification(ctx, chat.ID, user, "math", payload, sent.ID, policy.Verify.TimeoutSeconds)
}

func (s *Service) startRandomVerification(ctx context.Context, chat *tele.Chat, user *tele.User, policy config.GuardPolicy) error {
	options, answer := buildRandomEmojiChallenge()

	markup := &tele.ReplyMarkup{}
	row := make([]tele.Btn, 0, len(options))
	for _, option := range options {
		row = append(row, markup.Data(option, s.verifyRandBtn.Unique, formatVerifyCallbackData(user.ID, option)))
	}
	markup.Inline(row)

	prompt := fmt.Sprintf(`%s 你好，请在 %s 内点击 %s ✅`, mentionHTML(user), formatTimeout(policy.Verify.TimeoutSeconds), htmlEscape(answer))
	sent, err := s.bot.Send(chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
		ReplyMarkup:           markup,
	})
	if err != nil {
		s.logger.Error("send random verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	payload, err := json.Marshal(randomPayload{CorrectEmoji: answer})
	if err != nil {
		return err
	}

	return s.storePendingVerification(ctx, chat.ID, user, "random", payload, sent.ID, policy.Verify.TimeoutSeconds)
}

func (s *Service) storePendingVerification(ctx context.Context, chatID int64, user *tele.User, method string, payload []byte, messageID int, timeoutSeconds int) error {
	joinMessageID := int64(messageID)
	expiresAt := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)

	if _, err := s.queries.UpsertPendingVerification(ctx, store.UpsertPendingVerificationParams{
		ChatID:        chatID,
		UserID:        user.ID,
		Username:      stringPtr(user.Username),
		FirstName:     stringPtr(user.FirstName),
		Method:        method,
		Payload:       payload,
		JoinMessageID: &joinMessageID,
		ExpiresAt:     expiresAt,
	}); err != nil {
		s.logger.Error("store pending verification", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", user.ID))
		return err
	}

	s.logger.Info(
		"pending verification created",
		zap.Int64("chat_id", chatID),
		zap.Int64("user_id", user.ID),
		zap.String("method", method),
		zap.Time("expires_at", expiresAt),
	)
	return nil
}

func isJoinTransition(oldMember, newMember *tele.ChatMember) bool {
	if oldMember == nil || newMember == nil {
		return false
	}

	if oldMember.Role != tele.Left && oldMember.Role != tele.Kicked {
		return false
	}

	switch newMember.Role {
	case tele.Member:
		return true
	case tele.Restricted:
		return newMember.Rights == tele.NoRestrictions()
	default:
		return false
	}
}

func (s *Service) handleVerifyButton(c tele.Context) error {
	callback := c.Callback()
	chat := c.Chat()
	sender := c.Sender()
	if callback == nil || chat == nil || sender == nil {
		return nil
	}

	expectedUserID, err := strconv.ParseInt(c.Data(), 10, 64)
	if err != nil {
		s.logger.Warn("invalid button callback payload", zap.Error(err))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}

	if expectedUserID != sender.ID {
		return c.Respond(&tele.CallbackResponse{Text: "只能由加入群组的本人点击", ShowAlert: true})
	}

	pending, err := s.queries.GetPendingVerification(context.Background(), store.GetPendingVerificationParams{
		ChatID: chat.ID,
		UserID: sender.ID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.Respond(&tele.CallbackResponse{Text: "验证已失效或已处理", ShowAlert: true})
		}
		s.logger.Error("load pending verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "暂时无法验证，请稍后重试", ShowAlert: true})
	}

	if pending.Method != "button" {
		return c.Respond(&tele.CallbackResponse{Text: "当前验证方式不是按钮验证", ShowAlert: true})
	}

	payload := buttonPayload{}
	if err := json.Unmarshal(pending.Payload, &payload); err != nil {
		s.logger.Warn("decode button payload failed, fallback to pending timestamp", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
	}

	createdAt := pending.CreatedAt
	if !payload.CreatedAt.IsZero() {
		createdAt = payload.CreatedAt
	}
	if time.Since(createdAt) < 3*time.Second {
		return c.Respond(&tele.CallbackResponse{Text: "请再等一下，确认你不是脚本", ShowAlert: true})
	}

	if err := s.completeVerification(context.Background(), chat, sender, pending); err != nil {
		s.logger.Error("complete button verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证失败，请稍后重试", ShowAlert: true})
	}

	s.logger.Info("user verified via button", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
	return c.Respond(&tele.CallbackResponse{Text: "验证通过，欢迎加入", ShowAlert: false})
}

func (s *Service) handleVerifyMath(c tele.Context) error {
	callback := c.Callback()
	chat := c.Chat()
	sender := c.Sender()
	if callback == nil || chat == nil || sender == nil {
		return nil
	}

	payload, err := parseVerifyCallbackData(c.Data())
	if err != nil {
		s.logger.Warn("invalid math callback payload", zap.Error(err))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}

	if payload.UserID != sender.ID {
		return c.Respond(&tele.CallbackResponse{Text: "只能由加入群组的本人作答", ShowAlert: true})
	}

	selectedAnswer, err := strconv.Atoi(payload.Value)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: "答案格式无效", ShowAlert: true})
	}

	pending, err := s.queries.GetPendingVerification(context.Background(), store.GetPendingVerificationParams{
		ChatID: chat.ID,
		UserID: sender.ID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.Respond(&tele.CallbackResponse{Text: "验证已失效或已处理", ShowAlert: true})
		}
		s.logger.Error("load math pending verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "暂时无法验证，请稍后重试", ShowAlert: true})
	}

	if pending.Method != "math" && pending.Method != "math_image" {
		return c.Respond(&tele.CallbackResponse{Text: "当前验证方式不是数学题", ShowAlert: true})
	}

	policy, err := config.LoadPolicy(context.Background(), s.queries, chat.ID)
	if err != nil {
		s.logger.Warn("load policy for verification", zap.Error(err), zap.Int64("chat_id", chat.ID))
		policy = config.DefaultPolicy
	}

	var math mathPayload
	if err := json.Unmarshal(pending.Payload, &math); err != nil {
		s.logger.Error("decode math payload", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证题目已损坏，请稍后重试", ShowAlert: true})
	}

	if selectedAnswer != math.Answer {
		if err := s.failVerificationImmediately(context.Background(), chat, sender, pending, policy.Verify.FailAction, "verify_math_wrong"); err != nil {
			s.logger.Error("kick math verification failure", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
			return c.Respond(&tele.CallbackResponse{Text: "处理失败，请联系管理员", ShowAlert: true})
		}
		s.sendActionFeedback(chat, nil, policy.Feedback.VerifyFail, map[string]string{
			"user":         feedbackUserLabel(sender, policy.Feedback.VerifyFail.ParseMode),
			"user_mention": feedbackUserMention(sender, policy.Feedback.VerifyFail.ParseMode),
			"reason":       "答题错误",
		})
		return c.Respond(&tele.CallbackResponse{Text: "答错了，已移出群组", ShowAlert: true})
	}

	if err := s.completeVerification(context.Background(), chat, sender, pending); err != nil {
		s.logger.Error("complete math verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证失败，请稍后重试", ShowAlert: true})
	}

	s.logger.Info("user verified via math", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
	return c.Respond(&tele.CallbackResponse{Text: "验证通过，欢迎加入", ShowAlert: false})
}

func (s *Service) handleVerifyRandom(c tele.Context) error {
	callback := c.Callback()
	chat := c.Chat()
	sender := c.Sender()
	if callback == nil || chat == nil || sender == nil {
		return nil
	}

	payload, err := parseVerifyCallbackData(c.Data())
	if err != nil {
		s.logger.Warn("invalid random callback payload", zap.Error(err))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}

	if payload.UserID != sender.ID {
		return c.Respond(&tele.CallbackResponse{Text: "只能由加入群组的本人作答", ShowAlert: true})
	}

	pending, err := s.queries.GetPendingVerification(context.Background(), store.GetPendingVerificationParams{
		ChatID: chat.ID,
		UserID: sender.ID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.Respond(&tele.CallbackResponse{Text: "验证已失效或已处理", ShowAlert: true})
		}
		s.logger.Error("load random pending verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "暂时无法验证，请稍后重试", ShowAlert: true})
	}

	if pending.Method != "random" {
		return c.Respond(&tele.CallbackResponse{Text: "当前验证方式不是随机题", ShowAlert: true})
	}

	policy, err := config.LoadPolicy(context.Background(), s.queries, chat.ID)
	if err != nil {
		s.logger.Warn("load policy for verification", zap.Error(err), zap.Int64("chat_id", chat.ID))
		policy = config.DefaultPolicy
	}

	var random randomPayload
	if err := json.Unmarshal(pending.Payload, &random); err != nil {
		s.logger.Error("decode random payload", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证题目已损坏，请稍后重试", ShowAlert: true})
	}

	if payload.Value != random.CorrectEmoji {
		if err := s.failVerificationImmediately(context.Background(), chat, sender, pending, policy.Verify.FailAction, "verify_random_wrong"); err != nil {
			s.logger.Error("kick random verification failure", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
			return c.Respond(&tele.CallbackResponse{Text: "处理失败，请联系管理员", ShowAlert: true})
		}
		s.sendActionFeedback(chat, nil, policy.Feedback.VerifyFail, map[string]string{
			"user":         feedbackUserLabel(sender, policy.Feedback.VerifyFail.ParseMode),
			"user_mention": feedbackUserMention(sender, policy.Feedback.VerifyFail.ParseMode),
			"reason":       "答题错误",
		})
		return c.Respond(&tele.CallbackResponse{Text: "答错了，已移出群组", ShowAlert: true})
	}

	if err := s.completeVerification(context.Background(), chat, sender, pending); err != nil {
		s.logger.Error("complete random verification", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证失败，请稍后重试", ShowAlert: true})
	}

	s.logger.Info("user verified via random", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
	return c.Respond(&tele.CallbackResponse{Text: "验证通过，欢迎加入", ShowAlert: false})
}

func (s *Service) completeVerification(ctx context.Context, chat *tele.Chat, user *tele.User, pending store.PendingVerification) error {
	member := tele.ChatMember{
		User:   user,
		Rights: tele.NoRestrictions(),
	}
	if err := s.bot.Restrict(chat, &member); err != nil {
		return fmt.Errorf("unrestrict member: %w", err)
	}

	s.deleteVerificationMessage(chat, pending.JoinMessageID)

	if err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: chat.ID,
		UserID: user.ID,
	}); err != nil {
		return fmt.Errorf("delete pending verification: %w", err)
	}

	s.sendWelcomeMessage(context.Background(), chat, user)

	// 验证通过反馈（默认关）
	if policy, polErr := config.LoadPolicy(context.Background(), s.queries, chat.ID); polErr == nil {
		s.sendActionFeedback(chat, nil, policy.Feedback.VerifyPass, map[string]string{
			"user":         feedbackUserLabel(user, policy.Feedback.VerifyPass.ParseMode),
			"user_mention": feedbackUserMention(user, policy.Feedback.VerifyPass.ParseMode),
			"group":        chat.Title,
		})
	}

	return nil
}

func (s *Service) HandleVerificationExpiry(ctx context.Context, pending store.PendingVerification) error {
	policy, err := config.LoadPolicy(ctx, s.queries, pending.ChatID)
	if err != nil {
		s.logger.Warn("load guard policy failed during expiry handling, using defaults", zap.Error(err), zap.Int64("chat_id", pending.ChatID))
		policy = config.DefaultPolicy
	}

	chat := &tele.Chat{ID: pending.ChatID}
	user := &tele.User{
		ID:        pending.UserID,
		Username:  derefString(pending.Username),
		FirstName: derefString(pending.FirstName),
	}

	action, err := s.applyVerificationFailAction(chat, user, policy.Verify.FailAction)
	if err != nil {
		return err
	}

	s.deleteVerificationMessage(chat, pending.JoinMessageID)

	if err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: pending.ChatID,
		UserID: pending.UserID,
	}); err != nil {
		return fmt.Errorf("delete expired pending verification: %w", err)
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:   pending.ChatID,
		UserID:   pending.UserID,
		Username: pending.Username,
		Rule:     "verify_timeout",
		Action:   action,
	}); err != nil {
		return fmt.Errorf("insert verification timeout violation: %w", err)
	}

	s.sendActionFeedback(chat, nil, policy.Feedback.VerifyFail, map[string]string{
		"user":         feedbackUserLabel(user, policy.Feedback.VerifyFail.ParseMode),
		"user_mention": feedbackUserMention(user, policy.Feedback.VerifyFail.ParseMode),
		"reason":       "验证超时",
	})

	s.logger.Info(
		"expired verification handled",
		zap.Int64("chat_id", pending.ChatID),
		zap.Int64("user_id", pending.UserID),
		zap.String("method", pending.Method),
		zap.String("action", action),
		zap.Time("expires_at", pending.ExpiresAt),
	)
	return nil
}

func (s *Service) failVerificationImmediately(ctx context.Context, chat *tele.Chat, user *tele.User, pending store.PendingVerification, failAction, rule string) error {
	action, err := s.applyVerificationFailAction(chat, user, failAction)
	if err != nil {
		return err
	}

	s.deleteVerificationMessage(chat, pending.JoinMessageID)

	if err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: pending.ChatID,
		UserID: pending.UserID,
	}); err != nil {
		return fmt.Errorf("delete failed pending verification: %w", err)
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:   pending.ChatID,
		UserID:   pending.UserID,
		Username: pending.Username,
		Rule:     rule,
		Action:   action,
	}); err != nil {
		return fmt.Errorf("insert failed verification violation: %w", err)
	}

	return nil
}

func (s *Service) applyVerificationFailAction(chat *tele.Chat, user *tele.User, failAction string) (string, error) {
	member := &tele.ChatMember{User: user}

	switch failAction {
	case "", "kick":
		if err := s.bot.Ban(chat, member); err != nil {
			return "", fmt.Errorf("kick user via ban: %w", err)
		}
		if err := s.bot.Unban(chat, user); err != nil {
			return "", fmt.Errorf("unban kicked user: %w", err)
		}
		return "kick", nil
	case "ban":
		if err := s.bot.Ban(chat, member); err != nil {
			return "", fmt.Errorf("ban user: %w", err)
		}
		return "ban", nil
	case "mute_permanent":
		if err := s.bot.Restrict(chat, &tele.ChatMember{
			User:            user,
			Rights:          tele.NoRights(),
			RestrictedUntil: 0,
		}); err != nil {
			return "", fmt.Errorf("mute user permanently: %w", err)
		}
		return "mute", nil
	default:
		s.logger.Warn("unknown verify fail action, fallback to kick", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.String("fail_action", failAction))
		if err := s.bot.Ban(chat, member); err != nil {
			return "", fmt.Errorf("kick user via ban: %w", err)
		}
		if err := s.bot.Unban(chat, user); err != nil {
			return "", fmt.Errorf("unban kicked user: %w", err)
		}
		return "kick", nil
	}
}

func (s *Service) deleteVerificationMessage(chat *tele.Chat, messageID *int64) {
	if messageID == nil {
		return
	}

	if err := s.bot.Delete(&tele.Message{
		ID:   int(*messageID),
		Chat: chat,
	}); err != nil {
		s.logger.Warn("delete verification message", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("message_id", *messageID))
	}
}

func buildMathChallenge() (string, int, []int) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	left := 10 + r.Intn(90)
	right := 10 + r.Intn(90)
	operator := "+"
	answer := left + right

	if r.Intn(2) == 1 {
		if right > left {
			left, right = right, left
		}
		operator = "-"
		answer = left - right
	}

	optionsSet := map[int]struct{}{answer: {}}
	for len(optionsSet) < 4 {
		delta := 1 + r.Intn(12)
		candidate := answer + delta
		if r.Intn(2) == 1 {
			candidate = answer - delta
		}
		if candidate < 0 {
			continue
		}
		optionsSet[candidate] = struct{}{}
	}

	options := make([]int, 0, len(optionsSet))
	for option := range optionsSet {
		options = append(options, option)
	}
	r.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})

	return fmt.Sprintf("%d %s %d = ?", left, operator, right), answer, options
}

func buildRandomEmojiChallenge() ([]string, string) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	pool := []string{"🐶", "🐱", "🐰", "🐻", "🐼", "🦊", "🦁", "🐯", "🐸", "🐙", "🦋", "🐬"}
	options := append([]string(nil), pool...)
	r.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})
	options = options[:4]
	answer := options[r.Intn(len(options))]
	r.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})

	return options, answer
}

func parseVerifyCallbackData(raw string) (verifyCallbackPayload, error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return verifyCallbackPayload{}, fmt.Errorf("invalid callback payload %q", raw)
	}

	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return verifyCallbackPayload{}, err
	}

	return verifyCallbackPayload{
		UserID: userID,
		Value:  parts[1],
	}, nil
}

func formatVerifyCallbackData(userID int64, value string) string {
	return strconv.FormatInt(userID, 10) + ":" + value
}

func formatTimeout(timeoutSeconds int) string {
	if timeoutSeconds <= 0 {
		timeoutSeconds = config.DefaultPolicy.Verify.TimeoutSeconds
	}
	if timeoutSeconds%60 == 0 {
		return fmt.Sprintf("%d 分钟", timeoutSeconds/60)
	}
	return fmt.Sprintf("%d 秒", timeoutSeconds)
}

func displayName(user *tele.User) string {
	if user == nil {
		return "新成员"
	}
	if user.Username != "" {
		return user.Username
	}
	return user.FirstName
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func userFieldPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func htmlEscape(str string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(str)
}

func (s *Service) handleCASCommand(c tele.Context) error {
	msg, err := s.requirePrivateAdmin(c)
	if err != nil || msg == nil {
		return err
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		return c.Send("用法: <code>/cas &lt;user_id&gt;</code>", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	userID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return c.Send("user_id 格式无效", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	banned, result, err := s.casClient.IsBanned(context.Background(), userID)
	if err != nil {
		s.logger.Warn("cas command lookup failed", zap.Error(err), zap.Int64("user_id", userID))
		return c.Send("CAS 查询失败", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	if !banned {
		return c.Send(fmt.Sprintf("<code>%d</code> 未命中 CAS", userID), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	payload, _ := json.Marshal(result)
	return c.Send(fmt.Sprintf("<code>%d</code> 命中 CAS\n<code>%s</code>", userID, htmlEscape(string(payload))), &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleWarnStatusCommand(c tele.Context) error {
	msg, err := s.requirePrivateAdmin(c)
	if err != nil || msg == nil {
		return err
	}

	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		return c.Send("用法: <code>/warn_status &lt;user_id&gt;</code>", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	userID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return c.Send("user_id 格式无效", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	ctx := context.Background()
	chatIDs, err := s.queries.ListWarningChatsByUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("list warning chats: %w", err)
	}
	if len(chatIDs) == 0 {
		return c.Send(fmt.Sprintf("<code>%d</code> 当前没有 active warnings", userID), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	lines := []string{fmt.Sprintf("<code>%d</code> 的 active warnings:", userID)}
	total := 0
	for _, chatID := range chatIDs {
		policy, err := config.LoadPolicy(ctx, s.queries, chatID)
		if err != nil {
			policy = config.DefaultPolicy
		}
		warnings, err := s.queries.GetActiveWarnings(ctx, store.GetActiveWarningsParams{
			ChatID:       chatID,
			UserID:       userID,
			DecaySeconds: int64(policy.Warnings.DecayDays * 86400),
		})
		if err != nil {
			return fmt.Errorf("get active warnings: %w", err)
		}
		if len(warnings) == 0 {
			continue
		}
		total += len(warnings)
		lines = append(lines, fmt.Sprintf("群 <code>%d</code>: %d", chatID, len(warnings)))
	}
	lines = append(lines, fmt.Sprintf("总计: %d", total))
	return c.Send(strings.Join(lines, "\n"), &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleStatusCommand(c tele.Context) error {
	_, chat, err := s.requireGroupAdmin(c)
	if err != nil || chat == nil {
		return err
	}

	stats, err := s.queries.GetTodayChatStats(context.Background(), chat.ID)
	if err != nil {
		return fmt.Errorf("get today chat stats: %w", err)
	}

	lines := []string{
		"【本群今日】",
		fmt.Sprintf("入群: %d  验证通过: %d  验证失败: %d", stats.JoinedCount, stats.VerificationPassedCount, stats.VerificationFailedCount),
		fmt.Sprintf("AI 判定: %d 条 (ad: %d, scam: %d, clean: %d)", stats.AICalls, stats.AIAdCount, stats.AIScamCount, stats.AICleanCount),
		fmt.Sprintf("封禁/移出: %d  警告: %d", stats.BanKickCount, stats.WarnCount),
		fmt.Sprintf("AI 成本: %s", formatCents(stats.AICostCents)),
	}
	return c.Send(strings.Join(lines, "\n"), &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleTrustCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, err := s.resolveCommandTarget(context.Background(), msg, chat, 1)
	if err != nil {
		return c.Send(err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	trust, err := s.queries.GetUserTrust(context.Background(), chat.ID, target.UserID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return c.Send("未找到该用户的信任记录", &tele.SendOptions{ParseMode: tele.ModeHTML})
		}
		return fmt.Errorf("get user trust: %w", err)
	}

	recentViolations, err := s.queries.CountRecentViolationsByUser(context.Background(), chat.ID, target.UserID, 7)
	if err != nil {
		return fmt.Errorf("count recent violations: %w", err)
	}

	lines := []string{
		fmt.Sprintf("用户 %s (%d)", htmlEscape(target.Display), target.UserID),
		fmt.Sprintf("状态: %s  分数: %.2f", trust.Status, trust.Score),
		fmt.Sprintf("已审核消息: %d  清洁消息: %d", trust.MessagesChecked, trust.MessagesClean),
		fmt.Sprintf("入群时间: %s", trust.JoinedAt.In(time.Local).Format("2006-01-02 15:04")),
		fmt.Sprintf("最近违规: %d 次（7天内）", recentViolations),
	}
	return c.Send(strings.Join(lines, "\n"), &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleWarnCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, reason, err := s.resolveWarnTargetAndReason(context.Background(), msg, chat)
	if err != nil {
		return c.Send(err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	policy, err := config.LoadPolicy(context.Background(), s.queries, chat.ID)
	if err != nil {
		policy = config.DefaultPolicy
	}
	policy.Warnings.ActionAtMax = "mute"

	count, escalated, err := s.IncrWarning(context.Background(), chat, &tele.User{
		ID:        target.UserID,
		Username:  target.Username,
		FirstName: target.Display,
	}, reason, policy)
	if err != nil {
		return fmt.Errorf("insert manual warning: %w", err)
	}

	if _, err := s.queries.InsertViolation(context.Background(), store.InsertViolationParams{
		ChatID:      chat.ID,
		UserID:      target.UserID,
		Username:    stringPtr(target.Username),
		Rule:        "manual_warn",
		Matched:     stringPtr(reason),
		Action:      "warn",
		MessageText: stringPtr(reason),
	}); err != nil {
		return fmt.Errorf("insert manual warn violation: %w", err)
	}

	text := fmt.Sprintf("已警告 %s，当前 warnings: %d", htmlEscape(target.Display), count)
	if escalated {
		text += "\n已触发 warn_threshold，执行 mute"
	}

	// AdminAction feedback
	if c.Sender() != nil {
		targetUser := &tele.User{
			ID:        target.UserID,
			Username:  target.Username,
			FirstName: target.Display,
		}
		s.sendActionFeedback(chat, nil, policy.Feedback.AdminAction, map[string]string{
			"admin":         feedbackAdminLabel(c.Sender(), policy.Feedback.AdminAction.ParseMode),
			"admin_mention": feedbackAdminMention(c.Sender(), policy.Feedback.AdminAction.ParseMode),
			"user":          feedbackUserLabel(targetUser, policy.Feedback.AdminAction.ParseMode),
			"user_mention":  feedbackUserMention(targetUser, policy.Feedback.AdminAction.ParseMode),
			"action":        "警告",
			"reason":        reason,
		})
	}

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleUnbanCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, err := s.resolveCommandTarget(context.Background(), msg, chat, 1)
	if err != nil {
		return c.Send(err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	if err := s.bot.Unban(chat, &tele.User{ID: target.UserID}); err != nil {
		return c.Send("解封失败: "+htmlEscape(err.Error()), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	if err := s.queries.DeleteBannedUser(context.Background(), target.UserID); err != nil {
		return c.Send("清理封禁记录失败: "+htmlEscape(err.Error()), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	if err := s.UnmuteChatUser(context.Background(), chat.ID, target.UserID); err != nil {
		return c.Send("解除禁言失败: "+htmlEscape(err.Error()), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	if _, err := s.queries.UpdateUserTrustStatus(context.Background(), store.UpdateUserTrustStatusParams{
		ChatID: chat.ID,
		UserID: target.UserID,
		Status: "new",
		Score:  0.5,
	}); err != nil {
		s.logger.Warn("reset user trust status on unban failed", zap.Error(err), zap.Int64("user_id", target.UserID))
	} else if err := s.queries.ClearUserTrustBanMeta(context.Background(), chat.ID, target.UserID); err != nil {
		s.logger.Warn("clear user trust ban meta on unban failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", target.UserID))
	}

	diff := "manual_unban"
	_, _ = s.queries.InsertAuditEntry(context.Background(), store.InsertAuditEntryParams{
		Scope:   "moderation",
		ChatID:  &chat.ID,
		AdminID: c.Sender().ID,
		Action:  "manual_unban",
		Before: mustJSONBytes(map[string]any{
			"user_id": target.UserID,
		}),
		After: mustJSONBytes(map[string]any{
			"user_id":  target.UserID,
			"unbanned": true,
			"unmuted":  true,
		}),
		Diff: &diff,
	})
	return c.Send("已解封 "+htmlEscape(target.Display), &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleSpamCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, err := s.resolveCommandTarget(context.Background(), msg, chat, 1)
	if err != nil {
		return c.Send(err.Error(), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	ctx := context.Background()

	// 1. Delete the spam message (reply target or resolved user's last message)
	if target.MessageID != 0 {
		_ = s.bot.Delete(&tele.Message{ID: target.MessageID, Chat: chat})
	}

	// 2. Delete the /spam command message itself after 3s
	go func() {
		time.Sleep(3 * time.Second)
		_ = s.bot.Delete(&tele.Message{ID: msg.ID, Chat: chat})
	}()

	// 3. Ban user
	if err := s.bot.Ban(chat, &tele.ChatMember{User: &tele.User{ID: target.UserID}}); err != nil {
		return c.Send("封禁失败: "+htmlEscape(err.Error()), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	// 4. Upsert banned_user record
	reason := "spam"
	bannedBy := c.Sender().ID
	_, _ = s.queries.UpsertBannedUser(ctx, store.UpsertBannedUserParams{
		UserID:   target.UserID,
		BannedBy: &bannedBy,
		Reason:   &reason,
		Source:   "manual",
	})

	// 3. Update trust status to banned
	now := time.Now()
	_, _ = s.queries.UpdateUserTrustStatus(ctx, store.UpdateUserTrustStatusParams{
		ChatID:       chat.ID,
		UserID:       target.UserID,
		Status:       "banned",
		Score:        0,
		BannedAt:     &now,
		BannedReason: buildUserTrustBanReason("spam", "spam", "manual_spam_cmd"),
	})

	// 4. Audit log
	diff := "manual_ban"
	_, _ = s.queries.InsertAuditEntry(ctx, store.InsertAuditEntryParams{
		Scope:   "moderation",
		ChatID:  &chat.ID,
		AdminID: c.Sender().ID,
		Action:  "manual_ban",
		Before:  mustJSONBytes(map[string]any{"user_id": target.UserID}),
		After:   mustJSONBytes(map[string]any{"user_id": target.UserID, "banned": true, "reason": "spam"}),
		Diff:    &diff,
	})

	// 5. Send ban feedback if configured
	policy, _ := config.LoadPolicy(ctx, s.queries, chat.ID)
	if policy.Feedback.Ban.Enabled {
		targetUser := &tele.User{
			ID:        target.UserID,
			Username:  target.Username,
			FirstName: target.Display,
		}
		s.sendActionFeedback(chat, nil, policy.Feedback.Ban, map[string]string{
			"user":         feedbackUserLabel(targetUser, policy.Feedback.Ban.ParseMode),
			"user_mention": feedbackUserMention(targetUser, policy.Feedback.Ban.ParseMode),
			"reason":       "spam",
		})
	}

	// 9. Send confirmation then auto-delete after 5s
	confirm, _ := s.bot.Send(chat, "\U0001f6a8 已将 "+htmlEscape(target.Display)+" 封禁", &tele.SendOptions{ParseMode: tele.ModeHTML})
	if confirm != nil {
		go func() {
			time.Sleep(5 * time.Second)
			_ = s.bot.Delete(&tele.Message{ID: confirm.ID, Chat: chat})
		}()
	}

	return nil
}

type commandTarget struct {
	UserID    int64
	Username  string
	Display   string
	MessageID int // the message to delete (e.g. replied-to spam message)
}

func (s *Service) requireGroupAdmin(c tele.Context) (*tele.Message, *tele.Chat, error) {
	msg := c.Message()
	chat := c.Chat()
	if msg == nil || chat == nil || (chat.Type != tele.ChatGroup && chat.Type != tele.ChatSuperGroup) {
		return msg, nil, c.Send("请在群内使用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	sender := c.Sender()
	if sender == nil {
		return msg, nil, c.Send("仅管理员可用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	if admin, err := s.queries.GetAdminByTelegramID(context.Background(), sender.ID); err == nil {
		if botAdminCanAccessChat(admin, chat.ID) {
			return msg, chat, nil
		}
		return msg, nil, c.Send("该群不在你的管理范围内", &tele.SendOptions{ParseMode: tele.ModeHTML})
	} else if err != pgx.ErrNoRows {
		return msg, chat, err
	}

	isAdmin, err := s.isChatAdmin(context.Background(), chat.ID, sender.ID)
	if err != nil {
		return msg, chat, err
	}
	if !isAdmin {
		return msg, nil, c.Send("仅管理员可用", &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	return msg, chat, nil
}

func botAdminCanAccessChat(admin store.Admin, chatID int64) bool {
	if admin.Role == "owner" {
		return true
	}
	if len(admin.GroupScope) == 0 {
		return true
	}
	var scope []int64
	if err := json.Unmarshal(admin.GroupScope, &scope); err != nil {
		return false
	}
	if len(scope) == 0 {
		return true
	}
	for _, allowed := range scope {
		if allowed == chatID {
			return true
		}
	}
	return false
}

func (s *Service) resolveCommandTarget(ctx context.Context, msg *tele.Message, chat *tele.Chat, argIndex int) (commandTarget, error) {
	if msg == nil || chat == nil {
		return commandTarget{}, fmt.Errorf("缺少消息上下文")
	}
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if msg.ReplyTo != nil && len(fields) <= argIndex {
		return commandTarget{
			UserID:    msg.ReplyTo.Sender.ID,
			Username:  msg.ReplyTo.Sender.Username,
			Display:   formatTargetDisplay(msg.ReplyTo.Sender.ID, msg.ReplyTo.Sender.Username, displayName(msg.ReplyTo.Sender)),
			MessageID: msg.ReplyTo.ID,
		}, nil
	}
	if len(fields) <= argIndex {
		return commandTarget{}, fmt.Errorf("请提供 @user、user_id，或回复对方消息使用")
	}
	return s.resolveTargetSpec(ctx, chat.ID, fields[argIndex])
}

func (s *Service) resolveWarnTargetAndReason(ctx context.Context, msg *tele.Message, chat *tele.Chat) (commandTarget, string, error) {
	if msg == nil || chat == nil {
		return commandTarget{}, "", fmt.Errorf("缺少消息上下文")
	}
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) == 0 {
		return commandTarget{}, "", fmt.Errorf("用法: /warn @user 原因")
	}

	var (
		target commandTarget
		err    error
		reason string
	)

	if msg.ReplyTo != nil {
		if len(fields) > 1 && isUserSpecifier(fields[1]) {
			target, err = s.resolveTargetSpec(ctx, chat.ID, fields[1])
			if err != nil {
				return commandTarget{}, "", err
			}
			reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg.Text), fields[0]+" "+fields[1]))
		} else {
			target = commandTarget{
				UserID:   msg.ReplyTo.Sender.ID,
				Username: msg.ReplyTo.Sender.Username,
				Display:  formatTargetDisplay(msg.ReplyTo.Sender.ID, msg.ReplyTo.Sender.Username, displayName(msg.ReplyTo.Sender)),
			}
			reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg.Text), fields[0]))
		}
	} else {
		if len(fields) < 3 {
			return commandTarget{}, "", fmt.Errorf("用法: /warn @user 原因")
		}
		target, err = s.resolveTargetSpec(ctx, chat.ID, fields[1])
		if err != nil {
			return commandTarget{}, "", err
		}
		reason = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(msg.Text), fields[0]+" "+fields[1]))
	}

	if reason == "" {
		return commandTarget{}, "", fmt.Errorf("请提供警告原因")
	}
	return target, reason, nil
}

func (s *Service) resolveTargetSpec(ctx context.Context, chatID int64, spec string) (commandTarget, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return commandTarget{}, fmt.Errorf("请提供目标用户")
	}
	if userID, err := strconv.ParseInt(spec, 10, 64); err == nil {
		return commandTarget{
			UserID:  userID,
			Display: formatTargetDisplay(userID, "", ""),
		}, nil
	}

	username := strings.TrimPrefix(spec, "@")
	if username == "" {
		return commandTarget{}, fmt.Errorf("用户格式无效")
	}
	resolved, err := s.queries.ResolveChatUserByUsername(ctx, chatID, username)
	if err != nil {
		if err == pgx.ErrNoRows {
			return commandTarget{}, fmt.Errorf("无法根据 @%s 解析用户，请改用 user_id 或回复消息", username)
		}
		return commandTarget{}, err
	}
	return commandTarget{
		UserID:   resolved.UserID,
		Username: derefString(resolved.Username),
		Display:  formatTargetDisplay(resolved.UserID, derefString(resolved.Username), ""),
	}, nil
}

func isUserSpecifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "@") {
		return len(value) > 1
	}
	_, err := strconv.ParseInt(value, 10, 64)
	return err == nil
}

func formatTargetDisplay(userID int64, username, fallback string) string {
	if username != "" {
		return "@" + username
	}
	if fallback != "" {
		return fallback
	}
	return strconv.FormatInt(userID, 10)
}

func formatCents(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d¢", int64(value))
	}
	return fmt.Sprintf("%.2f¢", value)
}

func (s *Service) MarkUpdateSeen() {
	s.lastUpdateAt.Store(time.Now().UTC())
}

func (s *Service) MarkAISuccess() {
	s.lastAIOKAt.Store(time.Now().UTC())
	s.lastAIError.Store("")
}

func (s *Service) MarkAIFailure(err error) {
	s.lastAIFailAt.Store(time.Now().UTC())
	if err == nil {
		s.lastAIError.Store("")
		return
	}
	s.lastAIError.Store(err.Error())
}

func (s *Service) Status() map[string]any {
	status := map[string]any{
		"started_at":             s.startedAt,
		"uptime_seconds":         int64(time.Since(s.startedAt).Seconds()),
		"webhook_last_update_at": nil,
		"webhook_seconds_ago":    nil,
		"ai_last_ok_at":          nil,
		"ai_last_fail_at":        nil,
		"ai_last_error":          "",
	}

	if value := s.lastUpdateAt.Load(); value != nil {
		if t, ok := value.(time.Time); ok && !t.IsZero() {
			status["webhook_last_update_at"] = t
			status["webhook_seconds_ago"] = int64(time.Since(t).Seconds())
		}
	}
	if value := s.lastAIOKAt.Load(); value != nil {
		if t, ok := value.(time.Time); ok && !t.IsZero() {
			status["ai_last_ok_at"] = t
		}
	}
	if value := s.lastAIFailAt.Load(); value != nil {
		if t, ok := value.(time.Time); ok && !t.IsZero() {
			status["ai_last_fail_at"] = t
		}
	}
	if value := s.lastAIError.Load(); value != nil {
		if text, ok := value.(string); ok {
			status["ai_last_error"] = text
		}
	}

	return status
}

func (s *Service) handleMyChatMemberUpdate(c tele.Context) error {
	s.MarkUpdateSeen()
	update := c.ChatMember()
	if update == nil || update.Chat == nil || update.NewChatMember == nil || update.OldChatMember == nil {
		return nil
	}
	return s.handleBotChatMemberUpdate(update)
}
