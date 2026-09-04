package bot

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/openclaw/clawguard/internal/adkiller"
	"github.com/openclaw/clawguard/internal/ai"
	"github.com/openclaw/clawguard/internal/casclient"
	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"github.com/openclaw/clawguard/internal/store"
)

type Service struct {
	cfg                config.Config
	logger             *zap.Logger
	queries            *store.Queries
	redis              redis.Cmdable
	casClient          *casclient.Client
	aiProviders        ai.ProviderRegistry
	aiModels           ai.ModelRegistry
	aiResolver         *ai.Resolver
	aiModerator        *ai.Moderator
	adkiller           *adkiller.Client
	bot                *tele.Bot
	sender             telegramSender
	sendLimiter        *SendLimiter
	lifecycleCtx       context.Context
	lifecycleCancel    context.CancelFunc
	wg                 sync.WaitGroup
	verifyBtn          tele.Btn
	verifyMathBtn      tele.Btn
	verifyRandBtn      tele.Btn
	startedAt          time.Time
	lastUpdateAt       atomic.Value
	lastAIOKAt         atomic.Value
	lastAIFailAt       atomic.Value
	lastAIError        atomic.Value
	bioCheckInFlight   sync.Map
	userActionLocks    sync.Map
	messageDeleteLocks sync.Map
	joinEventLocks     sync.Map
	updateExecutions   sync.Map
	membershipSessions sync.Map
	authorizedGroups   sync.Map
	authorizedGroupAt  sync.Map
	policySnapshots    sync.Map
	policySnapshotAt   sync.Map
	chatAdmins         sync.Map
	systemState        atomic.Value
	systemStateAt      atomic.Int64
	runtimeGuardsOnce  sync.Once
	joinProtector      *joinProtector
	cleanupBreaker     *telegramCleanupBreaker
}

type buttonPayload struct {
	ButtonUnique  string    `json:"button_unique"`
	CreatedAt     time.Time `json:"created_at"`
	Nonce         string    `json:"nonce"`
	Signature     string    `json:"signature"`
	MessageID     int64     `json:"message_id"`
	ExpiresAtUnix int64     `json:"expires_at_unix"`
}

type mathPayload struct {
	Answer     int    `json:"answer"`
	Expression string `json:"expression,omitempty"`
}

type randomPayload struct {
	CorrectEmoji string `json:"correct_emoji"`
}

const verificationFailActionPayloadKey = "_failure_action"

type verifyCallbackPayload struct {
	UserID int64
	Value  string
}

type signedButtonCallbackPayload struct {
	Nonce     string
	Signature string
}

const (
	aiModerationMinTotalTimeout    = 5 * time.Second
	aiModerationMaxTotalTimeout    = 60 * time.Second
	aiModerationDefaultCallTimeout = 10 * time.Second
)

// computeAIModerationTimeout gives the AI fallback chain enough budget for
// every configured retry round. Moderator treats MaxRetries as extra rounds, so
// total rounds are MaxRetries+1.
func computeAIModerationTimeout(policy config.AIPolicy) time.Duration {
	return computeAIReviewTimeout(policy)
}

func computeProfileCheckTimeout(policy config.GuardPolicy) time.Duration {
	return computeAIReviewTimeout(policy.AI)
}

func computeAIReviewTimeout(policy config.AIPolicy) time.Duration {
	perCall := time.Duration(policy.TimeoutMs) * time.Millisecond
	if perCall <= 0 {
		perCall = aiModerationDefaultCallTimeout
	}

	unitCount := configuredAIModelCount(policy) * aiRetryRoundCount(policy.MaxRetries)
	units := time.Duration(unitCount)
	if perCall > aiModerationMaxTotalTimeout/units {
		return aiModerationMaxTotalTimeout
	}
	total := perCall * units

	if total < aiModerationMinTotalTimeout {
		return aiModerationMinTotalTimeout
	}
	if total > aiModerationMaxTotalTimeout {
		return aiModerationMaxTotalTimeout
	}
	return total
}

func configuredAIModelCount(policy config.AIPolicy) int {
	seen := map[string]struct{}{}
	add := func(ref string) {
		ref = normalizeConfiguredAIModelRef(ref)
		if ref != "" {
			seen[ref] = struct{}{}
		}
	}

	if ref := normalizeConfiguredAIModelRef(policy.PrimaryModelRef); ref != "" {
		seen[ref] = struct{}{}
	} else if ref := ai.NewModelRef(policy.PrimaryProvider, policy.PrimaryModel); ref != "" {
		seen[ref.String()] = struct{}{}
	} else {
		seen["__default_primary__"] = struct{}{}
	}
	for _, ref := range policy.FallbackModelRefs {
		add(ref)
	}
	for _, ref := range policy.FallbackChain {
		add(ref)
	}
	if len(seen) == 0 {
		return 1
	}
	return len(seen)
}

func normalizeConfiguredAIModelRef(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if provider, model, ok := ai.ModelRef(raw).Parse(); ok {
		return ai.NewModelRef(provider, model).String()
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return ai.NewModelRef(parts[0], parts[1]).String()
}

func aiRetryRoundCount(maxRetries int) int {
	if maxRetries < 0 {
		return 1
	}
	return maxRetries + 1
}

func New(ctx context.Context, cfg config.Config, logger *zap.Logger, queries *store.Queries, rdb redis.Cmdable, providers ai.ProviderRegistry, models ai.ModelRegistry, resolver *ai.Resolver) (*Service, error) {
	logger = redact.ZapLogger(logger)

	verifyBtn := tele.Btn{Unique: "verify_human"}
	verifyMathBtn := tele.Btn{Unique: "verify_math"}
	verifyRandBtn := tele.Btn{Unique: "verify_random"}

	b, err := tele.NewBot(tele.Settings{
		Token:       cfg.BotToken,
		Synchronous: true,
		OnError: func(err error, c tele.Context) {
			if c != nil && c.Message() != nil && c.Message().Chat != nil {
				logger.Error("telegram handler error", zap.Error(err), zap.Int64("chat_id", c.Message().Chat.ID), zap.Int("message_id", c.Message().ID))
				return
			}
			logger.Error("telegram handler error", zap.Error(err))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("new telebot: %w", err)
	}

	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	svc := &Service{
		cfg:             cfg,
		logger:          logger,
		queries:         queries,
		redis:           rdb,
		casClient:       casclient.New(nil, rdb),
		bot:             b,
		sender:          b,
		sendLimiter:     NewSendLimiter(),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		verifyBtn:       verifyBtn,
		verifyMathBtn:   verifyMathBtn,
		verifyRandBtn:   verifyRandBtn,
		joinProtector:   newJoinProtector(),
		cleanupBreaker:  newTelegramCleanupBreaker(),
		startedAt:       time.Now().UTC(),
	}

	svc.aiProviders = providers
	svc.aiModels = models
	svc.aiResolver = resolver
	svc.aiModerator = ai.NewModerator(ctx, logger, rdb, queries, providers, models, resolver, svc)
	svc.adkiller = adkiller.NewClient(adkiller.DefaultBaseURL, logger)
	if err := svc.ReloadAdKillerSecret(ctx); err != nil {
		logger.Warn("load adkiller secret failed", zap.Error(err))
	}
	if err := svc.PrimeRuntimeSnapshots(ctx); err != nil {
		return nil, fmt.Errorf("prime runtime snapshots: %w", err)
	}

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

	s.logger.Info("telegram webhook registered", zap.String("public_base_url", strings.TrimRight(s.cfg.PublicBaseURL, "/")))
	if err := s.setupCommandMenu(ctx); err != nil {
		s.logger.Warn("setup command menu failed", zap.Error(err))
	}
	return nil
}

func (s *Service) setupCommandMenu(ctx context.Context) error {
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
	if err := setDefaultMiniAppMenuButton(ctx, s.cfg.BotToken, strings.TrimRight(s.cfg.PublicBaseURL, "/")+"/miniapp"); err != nil {
		return fmt.Errorf("set mini app menu button: %w", err)
	}
	return nil
}

func (s *Service) Stop() {
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if s.logger != nil {
			s.logger.Warn("bot service stop timeout waiting delayed jobs")
		}
	}
}

func (s *Service) runDelayed(delay time.Duration, fn func()) {
	ctx := s.lifecycleCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			if ctx.Err() == nil {
				fn()
			}
		case <-ctx.Done():
		}
	}()
}

func (s *Service) deleteDelayedMessage(msg tele.Editable) error {
	sender := s.sender
	if sender != nil {
		return sender.Delete(msg)
	}
	return s.bot.Delete(msg)
}

func (s *Service) ProcessUpdate(update tele.Update) (err error) {
	execution := newTelegramUpdateExecution()
	actual, loaded := s.updateExecutions.LoadOrStore(update.ID, execution)
	if loaded {
		return actual.(*telegramUpdateExecution).wait()
	}
	claim := telegramUpdateClaim{}
	finalizeClaim := false
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("telegram update panic: %v", r)
			if s.logger != nil {
				s.logger.Error("telegram update panic recovered", zap.Any("panic", r), zap.Int("update_id", update.ID))
			}
		}
		if finalizeClaim {
			if err != nil {
				claim.release(context.Background())
			} else {
				claim.complete(context.Background())
			}
		}
		execution.finish(err)
		s.updateExecutions.Delete(update.ID)
	}()

	var process bool
	var claimErr error
	claim, process, claimErr = s.claimTelegramUpdate(context.Background(), update.ID)
	if claimErr != nil {
		return claimErr
	}
	if !process {
		return nil
	}
	finalizeClaim = true

	s.bot.ProcessUpdate(update)
	return execution.result()
}

func (s *Service) SendHTMLPrivateMessage(telegramID int64, text string) error {
	if telegramID == 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	_, err := s.sendThrottled(context.Background(), &tele.User{ID: telegramID}, text, &tele.SendOptions{ParseMode: tele.ModeHTML})
	return err
}

func (s *Service) registerHandlers() {
	s.bot.Handle(tele.OnUserJoined, func(c tele.Context) error {
		return s.runHandler("user_joined", c, s.handleUserJoined)
	})

	s.bot.Handle(tele.OnMyChatMember, func(c tele.Context) error {
		return s.runHandler("my_chat_member", c, s.handleMyChatMemberUpdate)
	})

	s.bot.Handle(tele.OnChatMember, func(c tele.Context) error {
		return s.runHandler("chat_member", c, s.handleChatMemberUpdate)
	})

	s.bot.Handle(&s.verifyBtn, func(c tele.Context) error {
		return s.runHandler("verify_button", c, s.handleVerifyButton)
	})

	s.bot.Handle(&s.verifyMathBtn, func(c tele.Context) error {
		return s.runHandler("verify_math", c, s.handleVerifyMath)
	})

	s.bot.Handle(&s.verifyRandBtn, func(c tele.Context) error {
		return s.runHandler("verify_random", c, s.handleVerifyRandom)
	})

	s.bot.Handle(tele.OnText, func(c tele.Context) error {
		return s.runHandler("text", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnEdited, func(c tele.Context) error {
		return s.runHandler("edited", c, s.handleEditedMessage)
	})
	s.bot.Handle(tele.OnPhoto, func(c tele.Context) error {
		return s.runHandler("photo", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnVideo, func(c tele.Context) error {
		return s.runHandler("video", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnDocument, func(c tele.Context) error {
		return s.runHandler("document", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnContact, func(c tele.Context) error {
		return s.runHandler("contact", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnSticker, func(c tele.Context) error {
		return s.runHandler("sticker", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnAnimation, func(c tele.Context) error {
		return s.runHandler("animation", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnVoice, func(c tele.Context) error {
		return s.runHandler("voice", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnAudio, func(c tele.Context) error {
		return s.runHandler("audio", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnVideoNote, func(c tele.Context) error {
		return s.runHandler("video_note", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnPoll, func(c tele.Context) error {
		return s.runHandler("poll", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnDice, func(c tele.Context) error {
		return s.runHandler("dice", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnLocation, func(c tele.Context) error {
		return s.runHandler("location", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnVenue, func(c tele.Context) error {
		return s.runHandler("venue", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnGame, func(c tele.Context) error {
		return s.runHandler("game", c, s.handleIncomingMessage)
	})
	s.bot.Handle(tele.OnInvoice, func(c tele.Context) error {
		return s.runHandler("invoice", c, s.handleIncomingMessage)
	})
	// TODO: telebot.v3 v3.3.8 exposes Message.Story/ReplyToStory/Giveaway fields,
	// but has no OnStory, OnPaidMedia, OnGiveaway, or OnGiveawayCreated endpoints.
	// Register these handlers after upgrading telebot to a version that dispatches them.
	s.bot.Handle("/cas", func(c tele.Context) error {
		return s.runCommandHandler("cas", c, s.handleCASCommand)
	})
	s.bot.Handle("/start", func(c tele.Context) error {
		return s.runCommandHandler("start", c, s.handleStartCommand)
	})
	s.bot.Handle("/help", func(c tele.Context) error {
		return s.runCommandHandler("help", c, s.handleHelpCommand)
	})
	s.bot.Handle("/warn_status", func(c tele.Context) error {
		return s.runCommandHandler("warn_status", c, s.handleWarnStatusCommand)
	})
	s.bot.Handle("/status", func(c tele.Context) error {
		return s.runCommandHandler("status", c, s.handleStatusCommand)
	})
	s.bot.Handle("/trust", func(c tele.Context) error {
		return s.runCommandHandler("trust", c, s.handleTrustCommand)
	})
	s.bot.Handle("/config", func(c tele.Context) error {
		return s.runCommandHandler("config", c, s.handleConfigCommand)
	})
	s.bot.Handle("/warn", func(c tele.Context) error {
		return s.runCommandHandler("warn", c, s.handleWarnCommand)
	})
	s.bot.Handle("/unban", func(c tele.Context) error {
		return s.runCommandHandler("unban", c, s.handleUnbanCommand)
	})
	s.bot.Handle("/spam", func(c tele.Context) error {
		return s.runCommandHandler("spam", c, s.handleSpamCommand)
	})
}

func (s *Service) runHandler(name string, c tele.Context, handler func(tele.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if s.logger != nil {
				s.logger.Error("telegram handler panic recovered", zap.String("handler", name), zap.Any("panic", r))
			}
			err = fmt.Errorf("telegram handler %s panic: %v", name, r)
		}
		if err != nil {
			s.recordTelegramUpdateError(c, err)
		}
	}()
	return handler(c)
}

func (s *Service) runCommandHandler(name string, c tele.Context, handler func(tele.Context) error) error {
	return s.runHandler(name, c, func(c tele.Context) error {
		handled, err := s.preprocessGroupCommand(c)
		if err != nil || handled {
			return err
		}
		return handler(c)
	})
}

// preprocessGroupCommand applies the same authorization, runtime-state, and
// Deleted Account checks that ordinary group messages pass before Telebot's
// dedicated command handlers can run. Private-chat commands remain unchanged.
func (s *Service) preprocessGroupCommand(c tele.Context) (bool, error) {
	if c == nil || c.Chat() == nil {
		return true, nil
	}
	chat := c.Chat()
	if chat.Type != tele.ChatGroup && chat.Type != tele.ChatSuperGroup {
		return false, nil
	}
	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, chat.ID)
	if err != nil {
		s.logger.Warn("authorized group check failed for command", zap.Error(err), zap.Int64("chat_id", chat.ID))
		return true, nil
	}
	if !authorized {
		return true, nil
	}
	state, err := s.GetSystemState(ctx)
	if err != nil {
		s.logger.Warn("load system state failed for command", zap.Error(err), zap.Int64("chat_id", chat.ID))
		return true, nil
	}
	if state.Frozen {
		return true, nil
	}

	msg := c.Message()
	if msg == nil || msg.Sender == nil || isSenderChatPersona(msg) {
		return false, nil
	}
	policy, err := s.LoadGuardPolicy(ctx, chat.ID)
	if err != nil {
		s.logger.Warn("load guard policy failed for command", zap.Error(err), zap.Int64("chat_id", chat.ID))
		return true, nil
	}
	return s.applyDeletedAccountMessageFilter(ctx, msg, policy, state.ActionsPaused)
}

func (s *Service) handleUserJoined(c tele.Context) error {
	s.MarkUpdateSeen()
	chat := c.Chat()
	msg := c.Message()
	if c.Get(joinServiceMessageHandledContextKey) != nil {
		return nil
	}
	c.Set(joinServiceMessageHandledContextKey, true)

	user := joinedServiceMessageUser(msg)
	if chat == nil || msg == nil || user == nil {
		return nil
	}
	if s.bot != nil && s.bot.Me != nil && user.ID == s.bot.Me.ID {
		return nil
	}

	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, chat.ID)
	if err != nil {
		return fmt.Errorf("check authorized group for join service message: %w", err)
	}
	if !authorized {
		return nil
	}
	state, err := s.GetSystemState(ctx)
	if err != nil {
		return fmt.Errorf("load system state for join service message: %w", err)
	}
	policy, err := s.LoadGuardPolicy(ctx, chat.ID)
	if err != nil {
		return fmt.Errorf("load guard policy for join service message: %w", err)
	}
	if !state.ActionsPaused && policy.Verify.DeleteJoinMessage {
		go s.deleteJoinEventMessageAsync(chat, user, msg)
	}

	return nil
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
	s.invalidateChatAdminCache(context.Background(), update.Chat.ID, member.User.ID)

	if s.bot.Me != nil && member.User.ID == s.bot.Me.ID {
		return s.handleBotChatMemberUpdate(update)
	}

	if isLeaveTransition(update.OldChatMember, member) {
		s.cleanupVerificationStateOnLeave(context.Background(), update.Chat, member.User)
		return nil
	}

	if !isJoinTransition(update.OldChatMember, member) {
		return nil
	}
	generation := membershipGenerationForUpdate(c.Update().ID, update)

	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, update.Chat.ID)
	if err != nil {
		s.logger.Warn("authorized group check failed for chat-member join", zap.Error(err), zap.Int64("chat_id", update.Chat.ID), zap.Int64("user_id", member.User.ID))
		return err
	}
	if !authorized {
		return nil
	}
	policy, err := s.LoadGuardPolicy(ctx, update.Chat.ID)
	if err != nil {
		s.logger.Warn("load policy for chat-member join failed", zap.Error(err), zap.Int64("chat_id", update.Chat.ID), zap.Int64("user_id", member.User.ID))
		return err
	}
	state, err := s.GetSystemState(ctx)
	if err != nil {
		s.logger.Warn("load system state for chat-member join failed", zap.Error(err), zap.Int64("chat_id", update.Chat.ID), zap.Int64("user_id", member.User.ID))
		return err
	}
	actionsPaused := state.ActionsPaused
	if handled, err := s.handleDeletedAccountJoin(ctx, update.Chat, member.User, policy, actionsPaused); err != nil {
		return err
	} else if handled {
		return nil
	}
	if s.handleUngraduatedInviteChatMember(ctx, update, member.User, policy) {
		return nil
	}

	if member.User.IsBot {
		return s.handleOtherBotJoined(update, member.User)
	}

	return s.startVerification(update.Chat, member.User, nil, generation)
}

func (s *Service) handleOtherBotJoined(update *tele.ChatMemberUpdate, user *tele.User) error {
	if update == nil || update.Chat == nil || user == nil {
		return nil
	}
	ctx := context.Background()
	policy, err := s.LoadGuardPolicy(ctx, update.Chat.ID)
	if err != nil {
		s.logger.Warn("load policy for other bot join failed", zap.Error(err), zap.Int64("chat_id", update.Chat.ID), zap.Int64("user_id", user.ID))
		policy = config.DefaultPolicy
	}
	inviter := s.otherBotInviter(update, user)
	if isBotWhitelisted(user, policy.Filter.BotWhitelist) {
		_, err := s.upsertBotTrust(ctx, update.Chat, user, "trusted", 1, "whitelisted bot", nil)
		return err
	}

	action := strings.TrimSpace(strings.ToLower(policy.Filter.OtherBotsAction))
	if action == "" {
		action = config.DefaultPolicy.Filter.OtherBotsAction
	}
	switch action {
	case "off":
		return nil
	case "kick":
		if err := s.kickUser(update.Chat, user); err != nil {
			return err
		}
		_, err := s.upsertBotTrust(ctx, update.Chat, user, "banned", 0, botTrustNotes("auto-kicked: other bot", inviter), stringPtr("auto-kicked: other bot"))
		s.notifyOwnersOtherBot(ctx, update.Chat, user, "kick", "非白名单 bot 已自动移出")
		return err
	case "ban":
		if err := s.banUser(update.Chat, user); err != nil {
			return err
		}
		_, err := s.upsertBotTrust(ctx, update.Chat, user, "banned", 0, botTrustNotes("auto-banned: other bot", inviter), stringPtr("auto-banned: other bot"))
		s.notifyOwnersOtherBot(ctx, update.Chat, user, "ban", "非白名单 bot 已自动封禁")
		return err
	case "audit":
		fallthrough
	default:
		_, err := s.upsertBotTrust(ctx, update.Chat, user, "new", 0.5, botTrustNotes("audit: other bot", inviter), nil)
		s.notifyOwnersOtherBot(ctx, update.Chat, user, "audit", "非白名单 bot 已进入 AI 审核")
		return err
	}
}

func (s *Service) otherBotInviter(update *tele.ChatMemberUpdate, botUser *tele.User) *tele.User {
	if update == nil || update.Sender == nil || botUser == nil {
		return nil
	}
	inviter := update.Sender
	if inviter.IsBot || inviter.ID == botUser.ID {
		return nil
	}
	if s.bot != nil && s.bot.Me != nil && inviter.ID == s.bot.Me.ID {
		return nil
	}
	return inviter
}

type botInviteTrustNotes struct {
	Reason  string              `json:"reason"`
	Inviter *botInviteTrustUser `json:"inviter,omitempty"`
}

type botInviteTrustUser struct {
	UserID    int64  `json:"user_id"`
	IsBot     bool   `json:"is_bot,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Display   string `json:"display,omitempty"`
}

func botTrustNotes(reason string, inviter *tele.User) string {
	if inviter == nil {
		return reason
	}
	raw, err := json.Marshal(botInviteTrustNotes{
		Reason: reason,
		Inviter: &botInviteTrustUser{
			UserID:    inviter.ID,
			IsBot:     inviter.IsBot,
			Username:  inviter.Username,
			FirstName: inviter.FirstName,
			LastName:  inviter.LastName,
			Display:   displayName(inviter),
		},
	})
	if err != nil {
		return reason
	}
	return string(raw)
}

func (s *Service) upsertBotTrust(ctx context.Context, chat *tele.Chat, user *tele.User, status string, score float64, notes string, banReason *string) (store.UserTrust, error) {
	var bannedAt *time.Time
	var bannedReason []byte
	if status == "banned" {
		now := time.Now()
		bannedAt = &now
		reason := notes
		if banReason != nil {
			reason = *banReason
		}
		bannedReason = buildUserTrustBanReason("other_bot", reason, "join")
	}
	return s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
		ChatID:          chat.ID,
		UserID:          user.ID,
		Username:        userFieldPtr(user.Username),
		FirstName:       userFieldPtr(user.FirstName),
		LastName:        userFieldPtr(user.LastName),
		JoinedAt:        time.Now(),
		Status:          status,
		Score:           score,
		MessagesChecked: 0,
		MessagesClean:   0,
		BannedAt:        bannedAt,
		BannedReason:    bannedReason,
		Notes:           stringPtr(notes),
		IsBot:           true,
	})
}

func (s *Service) notifyOwnersOtherBot(ctx context.Context, chat *tele.Chat, user *tele.User, action string, message string) {
	if chat == nil || user == nil {
		return
	}
	chatID := chat.ID
	s.WriteRuntimeAudit(ctx, "other_bot", &chatID, "other_bot_"+action, map[string]any{
		"source": "system:other_bot_join",
		"chat": map[string]any{
			"id":    chat.ID,
			"title": chat.Title,
			"type":  chat.Type,
		},
		"target": map[string]any{
			"user_id":    user.ID,
			"username":   user.Username,
			"first_name": user.FirstName,
			"last_name":  user.LastName,
			"display":    displayName(user),
		},
	}, map[string]any{
		"source":  "system:other_bot_join",
		"action":  action,
		"reason":  message,
		"outcome": "success",
	})
	text := fmt.Sprintf("⚠️ %s：%s（%d）", htmlEscape(message), htmlEscape(userDisplayForOwner(user)), user.ID)
	s.sendBotPermissionWarningToOwners(ctx, chat, text)
}

func userDisplayForOwner(user *tele.User) string {
	if user == nil {
		return "unknown"
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return "@" + strings.TrimPrefix(username, "@")
	}
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName + " " + user.LastName))
	if name != "" {
		return name
	}
	return strconv.FormatInt(user.ID, 10)
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

func (s *Service) startVerification(chat *tele.Chat, user *tele.User, joinEventMessage *tele.Message, membershipGeneration string) error {
	if chat == nil || user == nil {
		return nil
	}

	flowStartedAt := time.Now()
	ctx := context.Background()
	authorized, err := s.IsAuthorizedGroup(ctx, chat.ID)
	if err != nil {
		s.logger.Warn("authorized group check failed for verification, skip flow", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return fmt.Errorf("check authorized group for verification: %w", err)
	}
	if !authorized {
		s.logger.Info("verification skipped for unauthorized group", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	}

	state, err := s.GetSystemState(ctx)
	if err != nil {
		s.logger.Warn("load system state failed for verification, skip flow", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return fmt.Errorf("load system state for verification: %w", err)
	}
	actionsPaused := state.ActionsPaused

	policyStartedAt := time.Now()
	policy, err := s.LoadGuardPolicy(ctx, chat.ID)
	if err != nil {
		s.logger.Error("load guard policy failed without a safe snapshot; stop verification flow", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "load_policy"),
		zap.Duration("elapsed", time.Since(policyStartedAt)),
	)

	if handled, err := s.handleDeletedAccountJoin(ctx, chat, user, policy, actionsPaused); err != nil {
		return err
	} else if handled {
		return nil
	}

	if s.handleUngraduatedInviteServiceMessage(ctx, joinEventMessage, policy) {
		return nil
	}

	if !s.acquireVerificationJoinLock(chat.ID, user.ID) {
		s.logger.Debug("skip duplicate join event", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	}
	if err := s.rememberMembershipSession(ctx, chat.ID, user.ID, membershipGeneration); err != nil && s.logger != nil {
		s.logger.Warn("persist membership session failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.String("generation", membershipGeneration))
	}

	subject := joinProtectionSubject{}
	if policy.JoinProtection.Enabled {
		subject = s.joinProtectionSubjectForUser(ctx, chat.ID, user.ID)
		if subject.Trusted {
			s.releaseVerificationJoinLock(ctx, chat.ID, user.ID)
			s.logger.Info("trusted member bypassed join protection", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			return nil
		}
	}
	decision := joinProtectionDecision{}
	if subject.Candidate {
		decision = s.evaluateJoinProtection(ctx, chat, policy.JoinProtection, time.Now())
	}
	if !actionsPaused {
		latestState, latestStateErr := s.GetSystemState(ctx)
		if latestStateErr != nil {
			s.logger.Warn("reload system state before join telegram action failed; pause action", zap.Error(latestStateErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
			actionsPaused = true
		} else {
			actionsPaused = latestState.ActionsPaused
		}
	}
	if decision.Protect {
		action := subject.Action
		s.upsertJoinSideEffects(ctx, chat, user)
		if !actionsPaused && policy.Verify.DeleteJoinMessage && joinEventMessage != nil {
			go s.deleteJoinEventMessageAsync(chat, user, joinEventMessage)
		}
		if actionsPaused {
			s.logger.Warn("join protection action skipped because telegram actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		} else if err := s.handleJoinProtectionAction(ctx, chat, user, policy.JoinProtection, action, membershipGeneration, time.Now()); err != nil {
			s.logger.Error("handle join protection action failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID), zap.String("action", action))
			s.releaseVerificationJoinLock(ctx, chat.ID, user.ID)
			return err
		}
		if decision.Notify {
			s.notifyJoinProtectionAdmins(ctx, chat, decision, time.Now())
		}
		if decision.Entered {
			s.logger.Warn(
				"join protection entered",
				zap.String("event", "join_protection_entered"),
				zap.Int64("chat_id", chat.ID),
				zap.String("trigger", decision.Trigger),
				zap.Time("protection_until", decision.ProtectionUntil),
			)
		}
		s.scheduleJoinProtectionRecovery(chat, decision)
		return nil
	}

	if !policy.Verify.Enabled {
		s.joinProtector.ReleasePending(chat.ID)
		trust, trustOK := s.upsertJoinSideEffects(ctx, chat, user)
		s.logger.Info("verification disabled by policy, skip flow", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		if trustOK && !actionsPaused {
			s.applyUngraduatedPermissionRestriction(chat, user, policy, trust, "join_without_verification")
		}
		return nil
	}
	if actionsPaused {
		s.joinProtector.ReleasePending(chat.ID)
		s.upsertJoinSideEffects(ctx, chat, user)
		s.logger.Info("verification action skipped because telegram actions are paused", zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return nil
	}

	s.upsertJoinSideEffects(ctx, chat, user)

	member := tele.ChatMember{
		User:   user,
		Rights: tele.NoRights(),
	}
	restrictStartedAt := time.Now()
	if err := s.bot.Restrict(chat, &member); err != nil {
		s.joinProtector.ReleasePending(chat.ID)
		readable := normalizeTelegramActionError("restrict", err)
		s.logger.Error("restrict new member", zap.Error(readable), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		s.sendBotPermissionWarningToChat(chat, "⚠️ 新人入群验证启动失败："+htmlEscape(redact.ErrorString(readable))+"。请检查 bot 是否拥有封禁/禁言成员权限。")
		if cleanupErr := s.handleVerificationPromptFailure(ctx, chat, user, policy, readable); cleanupErr != nil {
			s.releaseVerificationJoinLock(ctx, chat.ID, user.ID)
			return errors.Join(readable, cleanupErr)
		}
		return nil
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "restrict"),
		zap.Duration("elapsed", time.Since(restrictStartedAt)),
	)

	if policy.Verify.DeleteJoinMessage && joinEventMessage != nil {
		go s.deleteJoinEventMessageAsync(chat, user, joinEventMessage)
	}

	verifyStartedAt := time.Now()
	if err := s.startVerificationPrompt(ctx, chat, user, policy); err != nil {
		s.joinProtector.ReleasePending(chat.ID)
		if cleanupErr := s.handleVerificationPromptFailure(ctx, chat, user, policy, err); cleanupErr != nil {
			s.releaseVerificationJoinLock(ctx, chat.ID, user.ID)
			return errors.Join(err, cleanupErr)
		}
		return nil
	}
	s.startAsyncVerificationChecks(chat, user, policy)
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

func int64Ptr(v int64) *int64 {
	return &v
}

func (s *Service) startAsyncVerificationChecks(chat *tele.Chat, user *tele.User, policy config.GuardPolicy) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		totalStartedAt := time.Now()

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
			s.holdProfileReviewAfterError(chat, user, policy, err, "join")
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

func (s *Service) upsertJoinSideEffects(ctx context.Context, chat *tele.Chat, user *tele.User) (store.UserTrust, bool) {
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
	trust, trustErr := s.queries.UpsertUserTrust(ctx, store.UpsertUserTrustParams{
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
		IsBot:           user.IsBot,
	})
	if trustErr != nil {
		s.logger.Warn("upsert user trust on join failed", zap.Error(trustErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	}
	s.logger.Info("verification step completed",
		zap.Int64("chat_id", chat.ID),
		zap.Int64("user_id", user.ID),
		zap.String("step", "upsert_user_trust"),
		zap.Duration("elapsed", time.Since(trustStartedAt)),
	)
	if trustErr != nil {
		return store.UserTrust{}, false
	}
	return trust, true
}

func (s *Service) deleteJoinEventMessageAsync(chat *tele.Chat, user *tele.User, joinEventMessage *tele.Message) {
	startedAt := time.Now()
	if err := s.deleteMessage(joinEventMessage); err != nil {
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
			deleted, err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
				ChatID: chatID,
				UserID: userID,
			})
			if err == nil && deleted > 0 {
				s.ensureRuntimeGuards()
				s.joinProtector.ReleasePending(chatID)
			}
			return err
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
	prompt := fmt.Sprintf(`<a href="tg://user?id=%d">%s</a> 你好，请在 %s 内<b>先阅读下面文字 3 秒</b>后再点击按钮`, user.ID, htmlEscape(displayName(user)), formatTimeout(policy.Verify.TimeoutSeconds))
	sent, err := s.sendThrottled(ctx, chat, prompt, &tele.SendOptions{
		ParseMode:             tele.ModeHTML,
		DisableWebPagePreview: true,
	})
	if err != nil {
		s.logger.Error("send verification prompt", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		return err
	}

	createdAt := time.Now()
	expiresAt := createdAt.Add(time.Duration(policy.Verify.TimeoutSeconds) * time.Second)
	nonce, err := generateShortNonce()
	if err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	signature := s.signVerificationCallback(chat.ID, user.ID, int64(sent.ID), "button", expiresAt.Unix(), nonce)
	callbackData := formatSignedButtonCallbackData(nonce, signature)
	if len("\f"+s.verifyBtn.Unique+"|"+callbackData) > 64 {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return fmt.Errorf("button callback_data too long")
	}

	markup := &tele.ReplyMarkup{}
	button := markup.Data("我是人类 ✅", s.verifyBtn.Unique, callbackData)
	markup.Inline(markup.Row(button))
	if _, err := s.bot.EditReplyMarkup(sent, markup); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}

	payload, err := json.Marshal(buttonPayload{
		ButtonUnique:  s.verifyBtn.Unique,
		CreatedAt:     createdAt,
		Nonce:         nonce,
		Signature:     signature,
		MessageID:     int64(sent.ID),
		ExpiresAtUnix: expiresAt.Unix(),
	})
	if err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}

	if err := s.storePendingVerification(ctx, chat.ID, user, "button", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
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
	sent, err := s.sendThrottled(ctx, chat, prompt, &tele.SendOptions{
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
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}

	if err := s.storePendingVerification(ctx, chat.ID, user, "math", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
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
	sent, err := s.sendThrottled(ctx, chat, prompt, &tele.SendOptions{
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
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}

	if err := s.storePendingVerification(ctx, chat.ID, user, "random", payload, sent.ID, policy.Verify.TimeoutSeconds, policy.Verify.FailAction); err != nil {
		s.deleteVerificationMessage(chat, int64Ptr(int64(sent.ID)))
		return err
	}
	return nil
}

func (s *Service) storePendingVerification(ctx context.Context, chatID int64, user *tele.User, method string, payload []byte, messageID int, timeoutSeconds int, failAction string) error {
	joinMessageID := int64(messageID)
	expiresAt := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	payload, err := withVerificationFailActionSnapshot(payload, failAction)
	if err != nil {
		return fmt.Errorf("snapshot verification fail action: %w", err)
	}

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

func (s *Service) acquireVerificationJoinLock(chatID, userID int64) bool {
	key := fmt.Sprintf("clawguard:verify:lock:%d:%d", chatID, userID)
	_, ok := s.acquireActionDedupeLock(
		key,
		10*time.Second,
		&s.joinEventLocks,
		"verification join",
		zap.Int64("chat_id", chatID),
		zap.Int64("user_id", userID),
	)
	return ok
}

func (s *Service) releaseVerificationJoinLock(ctx context.Context, chatID, userID int64) {
	key := fmt.Sprintf("clawguard:verify:lock:%d:%d", chatID, userID)
	if s.redis != nil {
		if err := s.redis.Del(ctx, key).Err(); err != nil && s.logger != nil {
			s.logger.Warn("release verification join lock via redis failed", zap.Error(err), zap.Int64("chat_id", chatID), zap.Int64("user_id", userID))
		}
	}
	if value, ok := s.joinEventLocks.LoadAndDelete(key); ok {
		if timer, timerOK := value.(*time.Timer); timerOK {
			timer.Stop()
		}
	}
}

func withVerificationFailActionSnapshot(payload []byte, failAction string) ([]byte, error) {
	document := map[string]json.RawMessage{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &document); err != nil {
			return nil, err
		}
	}
	rawAction, err := json.Marshal(normalizeVerificationFailAction(failAction))
	if err != nil {
		return nil, err
	}
	document[verificationFailActionPayloadKey] = rawAction
	return json.Marshal(document)
}

func verificationFailActionSnapshot(payload []byte, fallback string) string {
	document := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &document); err == nil {
		var action string
		if err := json.Unmarshal(document[verificationFailActionPayloadKey], &action); err == nil && strings.TrimSpace(action) != "" {
			return normalizeVerificationFailAction(action)
		}
	}
	return normalizeVerificationFailAction(fallback)
}

func normalizeVerificationFailAction(action string) string {
	switch strings.TrimSpace(strings.ToLower(action)) {
	case "ban":
		return "ban"
	case "mute_permanent":
		return "mute_permanent"
	default:
		return "kick"
	}
}

func (s *Service) cleanupVerificationStateOnLeave(ctx context.Context, chat *tele.Chat, user *tele.User) {
	if chat == nil || user == nil {
		return
	}
	pending, err := s.queries.GetPendingVerification(ctx, store.GetPendingVerificationParams{
		ChatID: chat.ID,
		UserID: user.ID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("load pending verification on leave failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	}
	pendingLoaded := err == nil
	if pendingLoaded {
		s.deleteVerificationMessage(chat, pending.JoinMessageID)
	}
	deleted, deleteErr := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: chat.ID,
		UserID: user.ID,
	})
	if deleteErr != nil {
		s.logger.Warn("delete pending verification on leave failed", zap.Error(deleteErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
	} else if deleted > 0 && (!pendingLoaded || pendingReservesJoinProtectionSlot(pending.Method)) {
		s.ensureRuntimeGuards()
		s.joinProtector.ReleasePending(chat.ID)
	}
	if s.redis != nil {
		if err := cancelJoinProtectionCleanupRedis(ctx, s.redis, chat.ID, user.ID); err != nil {
			s.logger.Warn("cancel redis join protection cleanup on leave failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
		if err := s.redis.Del(ctx, membershipSessionKey(chat.ID, user.ID)).Err(); err != nil {
			s.logger.Warn("delete membership session on leave failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		}
	}
	s.membershipSessions.Delete(membershipSessionMapKey(chat.ID, user.ID))
	s.releaseVerificationJoinLock(ctx, chat.ID, user.ID)
	s.bioCheckInFlight.Delete(user.ID)
}

func isLeaveTransition(oldMember, newMember *tele.ChatMember) bool {
	if oldMember == nil || newMember == nil {
		return false
	}
	if oldMember.Role == tele.Left || oldMember.Role == tele.Kicked {
		return false
	}
	return newMember.Role == tele.Left || newMember.Role == tele.Kicked
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

	callbackPayload, err := parseSignedButtonCallbackData(c.Data())
	if err != nil {
		s.logger.Warn("invalid button callback payload", zap.Error(err))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}

	pending, err := s.queries.GetActivePendingVerification(context.Background(), store.GetPendingVerificationParams{
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
		s.logger.Warn("decode button payload failed", zap.Error(err), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", sender.ID))
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}
	if callback.Message == nil || int64(callback.Message.ID) != payload.MessageID {
		return c.Respond(&tele.CallbackResponse{Text: "验证按钮已失效", ShowAlert: true})
	}
	if payload.Nonce == "" || payload.Signature == "" || payload.ExpiresAtUnix == 0 {
		return c.Respond(&tele.CallbackResponse{Text: "验证参数无效", ShowAlert: true})
	}
	if callbackPayload.Nonce != payload.Nonce || callbackPayload.Signature != payload.Signature {
		return c.Respond(&tele.CallbackResponse{Text: "验证按钮已失效", ShowAlert: true})
	}
	if time.Now().Unix() >= payload.ExpiresAtUnix {
		return c.Respond(&tele.CallbackResponse{Text: "验证已超时", ShowAlert: true})
	}
	expectedSignature := s.signVerificationCallback(chat.ID, sender.ID, payload.MessageID, pending.Method, payload.ExpiresAtUnix, payload.Nonce)
	if !hmac.Equal([]byte(payload.Signature), []byte(expectedSignature)) {
		return c.Respond(&tele.CallbackResponse{Text: "验证按钮已失效", ShowAlert: true})
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

	pending, err := s.queries.GetActivePendingVerification(context.Background(), store.GetPendingVerificationParams{
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

	policy, err := s.LoadGuardPolicy(context.Background(), chat.ID)
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

	pending, err := s.queries.GetActivePendingVerification(context.Background(), store.GetPendingVerificationParams{
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

	policy, err := s.LoadGuardPolicy(context.Background(), chat.ID)
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
	deleted, err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: chat.ID,
		UserID: user.ID,
	})
	if err != nil {
		return fmt.Errorf("delete pending verification: %w", err)
	}
	if deleted == 0 {
		return pgx.ErrNoRows
	}
	policy, polErr := s.LoadGuardPolicy(ctx, chat.ID)
	if polErr != nil {
		s.logger.Warn("load guard policy before verification permission sync failed, using defaults", zap.Error(polErr), zap.Int64("chat_id", chat.ID), zap.Int64("user_id", user.ID))
		policy = config.DefaultPolicy
	}
	if err := s.applyVerificationPassPermissions(ctx, chat, user, policy); err != nil {
		if restoreErr := s.restorePendingVerification(ctx, pending); restoreErr != nil {
			s.logger.Error("restore pending verification after restrict failure", zap.Error(restoreErr), zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
		}
		return fmt.Errorf("sync verification pass permissions: %w", err)
	}
	s.ensureRuntimeGuards()
	s.joinProtector.ReleasePending(chat.ID)

	s.deleteVerificationMessage(chat, pending.JoinMessageID)
	s.sendWelcomeMessage(context.Background(), chat, user)

	// 验证通过反馈（默认关）
	if polErr == nil {
		s.sendActionFeedback(chat, nil, policy.Feedback.VerifyPass, map[string]string{
			"user":         feedbackUserLabel(user, policy.Feedback.VerifyPass.ParseMode),
			"user_mention": feedbackUserMention(user, policy.Feedback.VerifyPass.ParseMode),
			"group":        chat.Title,
		})
	}

	return nil
}

type VerificationExpiryResult struct {
	RetryAt     time.Time
	RetryReason string
}

func (s *Service) HandleVerificationExpiry(ctx context.Context, pending store.PendingVerification) (VerificationExpiryResult, error) {
	current, err := s.pendingVerificationClaimCurrent(ctx, pending)
	if err != nil {
		return VerificationExpiryResult{}, fmt.Errorf("check pending verification claim: %w", err)
	}
	if !current {
		return VerificationExpiryResult{}, nil
	}

	policy, err := s.LoadGuardPolicy(ctx, pending.ChatID)
	if err != nil {
		return verificationExpiryRetry(time.Now().Add(30*time.Second), "guard policy unavailable"), nil
	}
	state, stateErr := s.GetSystemState(ctx)
	if stateErr != nil {
		return verificationExpiryRetry(time.Now().Add(30*time.Second), "system state unavailable"), nil
	}
	if state.ActionsPaused {
		return verificationExpiryRetry(time.Now().Add(30*time.Second), "telegram actions paused"), nil
	}

	chat := &tele.Chat{ID: pending.ChatID}
	user := &tele.User{
		ID:        pending.UserID,
		Username:  derefString(pending.Username),
		FirstName: derefString(pending.FirstName),
	}
	if pending.Method == "join_protection_cleanup" {
		return s.handleJoinProtectionCleanupExpiry(ctx, pending, policy, chat, user)
	}
	s.ensureRuntimeGuards()
	allowed, retryAt := s.allowTelegramCleanup(ctx, pending.ChatID, time.Now())
	if !allowed {
		return verificationExpiryRetry(retryAt, "telegram cleanup cooldown"), nil
	}

	failAction := verificationFailActionSnapshot(pending.Payload, policy.Verify.FailAction)
	action, err := s.applyVerificationFailAction(chat, user, failAction)
	if err != nil {
		if isTerminalTelegramCleanupError(err) {
			action = "already_absent"
		} else {
			retryAt = s.openTelegramCleanupCooldown(ctx, chat, policy.JoinProtection, err)
			return verificationExpiryRetry(retryAt, redact.ErrorString(err)), nil
		}
	}
	lockKey := fmt.Sprintf("clawguard:verify:lock:%d:%d", chat.ID, user.ID)
	if value, ok := s.joinEventLocks.LoadAndDelete(lockKey); ok {
		if timer, timerOK := value.(*time.Timer); timerOK {
			timer.Stop()
		}
	}
	s.markTelegramCleanupSuccess(ctx, pending.ChatID)

	s.deleteVerificationMessage(chat, pending.JoinMessageID)

	deleted, err := s.deleteProcessedPendingVerification(ctx, pending)
	if err != nil {
		return VerificationExpiryResult{}, fmt.Errorf("delete expired pending verification: %w", err)
	}
	if deleted == 0 {
		return VerificationExpiryResult{}, nil
	}
	if pendingReservesJoinProtectionSlot(pending.Method) {
		s.joinProtector.ReleasePending(pending.ChatID)
	}

	if _, err := s.queries.InsertViolation(ctx, store.InsertViolationParams{
		ChatID:   pending.ChatID,
		UserID:   pending.UserID,
		Username: pending.Username,
		Rule:     "verify_timeout",
		Action:   action,
	}); err != nil {
		s.logger.Warn("record verification timeout result failed after cleanup", zap.Error(err), zap.Int64("chat_id", pending.ChatID), zap.Int64("user_id", pending.UserID))
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
	return VerificationExpiryResult{}, nil
}

func verificationExpiryRetry(retryAt time.Time, reason string) VerificationExpiryResult {
	if retryAt.IsZero() || retryAt.Before(time.Now()) {
		retryAt = time.Now().Add(30 * time.Second)
	}
	return VerificationExpiryResult{RetryAt: retryAt, RetryReason: reason}
}

func (s *Service) pendingVerificationClaimCurrent(ctx context.Context, pending store.PendingVerification) (bool, error) {
	if pending.LeaseOwner == nil || strings.TrimSpace(*pending.LeaseOwner) == "" {
		return true, nil
	}
	return s.queries.IsPendingVerificationClaimCurrent(ctx, store.IsPendingVerificationClaimCurrentParams{
		ID:         pending.ID,
		LeaseOwner: *pending.LeaseOwner,
	})
}

func (s *Service) deleteProcessedPendingVerification(ctx context.Context, pending store.PendingVerification) (int64, error) {
	if pending.LeaseOwner != nil && strings.TrimSpace(*pending.LeaseOwner) != "" {
		return s.queries.DeleteClaimedPendingVerification(ctx, store.DeleteClaimedPendingVerificationParams{
			ID:         pending.ID,
			LeaseOwner: *pending.LeaseOwner,
		})
	}
	return s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: pending.ChatID,
		UserID: pending.UserID,
	})
}

func (s *Service) failVerificationImmediately(ctx context.Context, chat *tele.Chat, user *tele.User, pending store.PendingVerification, failAction, rule string) error {
	action, err := s.applyVerificationFailAction(chat, user, failAction)
	if err != nil {
		return err
	}

	s.deleteVerificationMessage(chat, pending.JoinMessageID)

	deleted, err := s.queries.DeletePendingVerification(ctx, store.DeletePendingVerificationParams{
		ChatID: pending.ChatID,
		UserID: pending.UserID,
	})
	if err != nil {
		return fmt.Errorf("delete failed pending verification: %w", err)
	}
	if deleted == 0 {
		return nil
	}
	s.ensureRuntimeGuards()
	s.joinProtector.ReleasePending(pending.ChatID)

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
		if err := runVerificationKick(
			func() error { return s.bot.Ban(chat, member) },
			func() error { return s.bot.Unban(chat, user) },
		); err != nil {
			return "", err
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
		if err := runVerificationKick(
			func() error { return s.bot.Ban(chat, member) },
			func() error { return s.bot.Unban(chat, user) },
		); err != nil {
			return "", err
		}
		return "kick", nil
	}
}

func runVerificationKick(ban, unban func() error) error {
	if err := ban(); err != nil && !isAlreadyBannedTelegramError(err) {
		return fmt.Errorf("kick user via ban: %w", err)
	}
	if err := unban(); err != nil {
		if !isTerminalTelegramCleanupError(err) || isAlreadyBannedTelegramError(err) {
			return fmt.Errorf("unban kicked user: %w", err)
		}
	}
	return nil
}

func (s *Service) deleteVerificationMessage(chat *tele.Chat, messageID *int64) {
	if messageID == nil {
		return
	}

	if err := normalizeTelegramActionError("delete", s.bot.Delete(&tele.Message{
		ID:   int(*messageID),
		Chat: chat,
	})); err != nil {
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

func generateShortNonce() (string, error) {
	raw := make([]byte, 8)
	if _, err := crand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (s *Service) signVerificationCallback(chatID, userID, messageID int64, method string, expiresAtUnix int64, nonce string) string {
	secret := s.cfg.JWTSecret
	if strings.TrimSpace(secret) == "" {
		secret = s.cfg.WebhookSecret
	}
	if strings.TrimSpace(secret) == "" {
		secret = s.cfg.BotToken
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d:%d:%d:%s:%d:%s", chatID, userID, messageID, method, expiresAtUnix, nonce)
	sum := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func formatSignedButtonCallbackData(nonce, signature string) string {
	return "b." + nonce + "." + signature
}

func parseSignedButtonCallbackData(raw string) (signedButtonCallbackPayload, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != "b" || parts[1] == "" || parts[2] == "" {
		return signedButtonCallbackPayload{}, fmt.Errorf("invalid signed button callback payload")
	}
	return signedButtonCallbackPayload{Nonce: parts[1], Signature: parts[2]}, nil
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
		policy, err := s.LoadGuardPolicy(ctx, chatID)
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
		return c.Send(redact.ErrorString(err), &tele.SendOptions{ParseMode: tele.ModeHTML})
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
		return c.Send(redact.ErrorString(err), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	policy, err := s.LoadGuardPolicy(context.Background(), chat.ID)
	if err != nil {
		policy = config.DefaultPolicy
	}
	policy.Warnings.ActionAtMax = "mute"

	count, escalated, err := s.IncrWarning(context.Background(), chat, &tele.User{
		ID:        target.UserID,
		Username:  target.Username,
		FirstName: target.Display,
	}, reason, policy, true, false)
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

	s.writeCommandModerationAudit(context.Background(), "warn", c.Sender(), chat, &tele.User{
		ID:        target.UserID,
		Username:  target.Username,
		FirstName: target.Display,
	}, "warn", reason, target.MessageID, msg.ID, "success", "")

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleUnbanCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, err := s.resolveCommandTarget(context.Background(), msg, chat, 1)
	if err != nil {
		return c.Send(redact.ErrorString(err), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	warnings := make([]string, 0, 2)
	if warning, err := s.SafeUnbanChatUser(context.Background(), chat.ID, target.UserID); err != nil {
		s.writeCommandModerationAudit(context.Background(), "unban", c.Sender(), chat, &tele.User{ID: target.UserID, Username: target.Username, FirstName: target.Display}, "unban", "manual_unban", target.MessageID, msg.ID, "failed", redact.ErrorString(err))
		return c.Send("解封失败: "+htmlEscape(redact.ErrorString(err)), &tele.SendOptions{ParseMode: tele.ModeHTML})
	} else if strings.TrimSpace(warning) != "" {
		warnings = append(warnings, warning)
	}
	if err := s.queries.DeleteBannedUser(context.Background(), target.UserID); err != nil {
		return c.Send("清理封禁记录失败: "+htmlEscape(redact.ErrorString(err)), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}
	if _, err := s.queries.UnbanUserTrust(context.Background(), store.UnbanUserTrustParams{
		ChatID: chat.ID,
		UserID: target.UserID,
		Score:  0.5,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			warnings = append(warnings, "用户并未处于本地封禁状态，已跳过本地信任状态解封")
		} else {
			s.logger.Warn("reset user trust status on unban failed", zap.Error(err), zap.Int64("user_id", target.UserID))
		}
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
			"warnings": warnings,
		}),
		Diff: &diff,
	})
	text := "已尝试解封 " + htmlEscape(target.Display)
	if len(warnings) > 0 {
		text += "\n安全提示：" + htmlEscape(strings.Join(warnings, "；"))
	}
	s.writeCommandModerationAudit(context.Background(), "unban", c.Sender(), chat, &tele.User{
		ID:        target.UserID,
		Username:  target.Username,
		FirstName: target.Display,
	}, "unban", "manual_unban", target.MessageID, msg.ID, "success", "")

	return c.Send(text, &tele.SendOptions{ParseMode: tele.ModeHTML})
}

func (s *Service) handleSpamCommand(c tele.Context) error {
	msg, chat, err := s.requireGroupAdmin(c)
	if err != nil || msg == nil || chat == nil {
		return err
	}

	target, err := s.resolveCommandTarget(context.Background(), msg, chat, 1)
	if err != nil {
		return c.Send(redact.ErrorString(err), &tele.SendOptions{ParseMode: tele.ModeHTML})
	}

	ctx := context.Background()

	// 1. Delete the spam message (reply target or resolved user's last message)
	if target.MessageID != 0 {
		_ = s.bot.Delete(&tele.Message{ID: target.MessageID, Chat: chat})
	}

	// 2. Delete the /spam command message itself after 3s
	s.runDelayed(3*time.Second, func() {
		_ = s.deleteDelayedMessage(&tele.Message{ID: msg.ID, Chat: chat})
	})

	targetUser := &tele.User{
		ID:        target.UserID,
		Username:  target.Username,
		FirstName: target.Display,
	}

	// 3. Ban user. Bots cannot file an official Telegram spam report; revoke_messages
	// is the available Bot API mechanism for removing the target user's chat history.
	if err := s.banUser(chat, targetUser); err != nil {
		s.writeCommandModerationAudit(ctx, "spam", c.Sender(), chat, targetUser, "ban", "spam", target.MessageID, msg.ID, "failed", redact.ErrorString(err))
		return c.Send("封禁失败: "+htmlEscape(redact.ErrorString(err)), &tele.SendOptions{ParseMode: tele.ModeHTML})
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
	s.writeCommandModerationAudit(ctx, "spam", c.Sender(), chat, targetUser, "ban", "spam", target.MessageID, msg.ID, "success", "")

	// 5. Send exactly one user-visible result: configured ban feedback, or a fallback confirmation.
	policy, _ := s.LoadGuardPolicy(ctx, chat.ID)
	sentFeedback := false
	if policy.Feedback.Ban.Enabled {
		s.sendActionFeedback(chat, nil, policy.Feedback.Ban, map[string]string{
			"user":         feedbackUserLabel(targetUser, policy.Feedback.Ban.ParseMode),
			"user_mention": feedbackUserMention(targetUser, policy.Feedback.Ban.ParseMode),
			"reason":       "spam",
		})
		if strings.TrimSpace(policy.Feedback.Ban.Template) != "" {
			sentFeedback = true
		}
	}

	if !sentFeedback {
		confirm, _ := s.sendThrottled(ctx, chat, "\U0001f6a8 已将 "+htmlEscape(target.Display)+" 封禁", &tele.SendOptions{ParseMode: tele.ModeHTML})
		if confirm != nil {
			s.runDelayed(5*time.Second, func() {
				_ = s.deleteDelayedMessage(&tele.Message{ID: confirm.ID, Chat: chat})
			})
		}
	}

	return nil
}

func (s *Service) writeCommandModerationAudit(ctx context.Context, command string, operator *tele.User, chat *tele.Chat, target *tele.User, action string, reason string, referencedMessageID int, commandMessageID int, outcome string, errText string) {
	if chat == nil || target == nil {
		return
	}
	chatID := chat.ID
	adminID := int64(0)
	operatorPayload := map[string]any{}
	if operator != nil {
		adminID = operator.ID
		operatorPayload = map[string]any{
			"user_id":    operator.ID,
			"username":   operator.Username,
			"first_name": operator.FirstName,
			"last_name":  operator.LastName,
			"display":    displayName(operator),
		}
	}
	before := map[string]any{
		"source":   "command:" + command,
		"operator": operatorPayload,
		"chat": map[string]any{
			"id":       chat.ID,
			"title":    chat.Title,
			"username": chat.Username,
			"type":     chat.Type,
		},
		"target": map[string]any{
			"user_id":    target.ID,
			"username":   target.Username,
			"first_name": target.FirstName,
			"last_name":  target.LastName,
			"display":    displayName(target),
		},
	}
	after := map[string]any{
		"source":                "command:" + command,
		"action":                action,
		"reason":                reason,
		"message_id":            commandMessageID,
		"referenced_message_id": referencedMessageID,
		"outcome":               outcome,
		"error":                 errText,
	}
	s.WriteRuntimeAuditWithAdmin(ctx, "moderation", &chatID, adminID, "command_"+command+"_"+action, before, after)
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
		if msg.ReplyTo.Sender == nil {
			return commandTarget{}, fmt.Errorf("不支持处理匿名/频道身份消息，请用 user_id")
		}
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
			if msg.ReplyTo.Sender == nil {
				return commandTarget{}, "", fmt.Errorf("不支持处理匿名/频道身份消息，请用 user_id")
			}
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
	s.lastAIError.Store(redact.ErrorString(err))
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
	ctx := context.Background()
	s.checkBotAdminPermissions(ctx, update)
	return s.handleBotChatMemberUpdate(update)
}

func (s *Service) checkBotAdminPermissions(ctx context.Context, update *tele.ChatMemberUpdate) {
	if update == nil || update.Chat == nil || update.NewChatMember == nil {
		return
	}
	if shouldSkipBotAdminPermissionCheck(update.Chat) {
		return
	}
	authorized := true
	if s.queries != nil {
		var err error
		authorized, err = s.IsAuthorizedGroup(ctx, update.Chat.ID)
		if err != nil {
			s.logger.Warn("check authorized group for bot permissions failed", zap.Error(err), zap.Int64("chat_id", update.Chat.ID))
			return
		}
	}
	if !authorized {
		s.writeUnauthorizedBotPermissionAudit(ctx, update.Chat)
		return
	}
	missing := botAdminPermissionMissingNames(update.Chat, authorized, update.NewChatMember)
	if len(missing) == 0 {
		return
	}
	s.warnMissingBotPermissions(ctx, update.Chat, missing)
}

func botAdminPermissionMissingNames(chat *tele.Chat, authorized bool, member *tele.ChatMember) []string {
	if shouldSkipBotAdminPermissionCheck(chat) || !authorized || member == nil {
		return nil
	}
	if member.Role != tele.Administrator && member.Role != tele.Creator {
		return []string{"管理员身份", "删除消息", "封禁/禁言成员"}
	}
	return requiredBotPermissionNames(member)
}

func shouldSkipBotAdminPermissionCheck(chat *tele.Chat) bool {
	if chat == nil {
		return true
	}
	return chat.Type == tele.ChatPrivate || chat.Type == tele.ChatChannel
}

func (s *Service) writeUnauthorizedBotPermissionAudit(ctx context.Context, chat *tele.Chat) {
	if chat == nil || s.queries == nil {
		return
	}
	chatID := chat.ID
	s.WriteRuntimeAudit(ctx, "bot_permissions", &chatID, "unauthorized_group_skip_permission_warning", map[string]any{
		"chat_id": chat.ID,
		"title":   chat.Title,
		"type":    string(chat.Type),
	}, map[string]any{
		"warned": false,
	})
}

func requiredBotPermissionNames(member *tele.ChatMember) []string {
	if member == nil || member.Role == tele.Creator {
		return nil
	}
	missing := []string{}
	if !member.CanDeleteMessages {
		missing = append(missing, "删除消息")
	}
	if !member.CanRestrictMembers {
		missing = append(missing, "封禁/禁言成员")
	}
	return missing
}

func (s *Service) warnMissingBotPermissions(ctx context.Context, chat *tele.Chat, missing []string) {
	if chat == nil || len(missing) == 0 {
		return
	}
	chatID := chat.ID
	s.WriteRuntimeAudit(ctx, "bot_permissions", &chatID, "missing_required_permissions", map[string]any{
		"chat_id": chat.ID,
		"title":   chat.Title,
	}, map[string]any{
		"missing": missing,
	})
	message := fmt.Sprintf("⚠️ ClawGuard 缺少必要管理权限：%s。请授予 bot 管理员身份、删除消息、封禁/禁言成员权限，否则入群验证和违规处置可能失效。", htmlEscape(strings.Join(missing, "、")))
	s.sendBotPermissionWarningToChat(chat, message)
	s.sendBotPermissionWarningToOwners(ctx, chat, message)
}

func (s *Service) sendBotPermissionWarningToChat(chat *tele.Chat, message string) {
	if chat == nil {
		return
	}
	if _, err := s.sendThrottled(context.Background(), chat, message, &tele.SendOptions{ParseMode: tele.ModeHTML, DisableWebPagePreview: true}); err != nil {
		s.logger.Warn("send bot permission warning to chat failed", zap.Error(err), zap.Int64("chat_id", chat.ID))
	}
}

func (s *Service) sendBotPermissionWarningToOwners(ctx context.Context, chat *tele.Chat, message string) {
	recipients := map[int64]struct{}{}
	if s.queries != nil {
		if owner, err := s.queries.GetFirstOwnerAdmin(ctx); err == nil && owner.TelegramID != 0 {
			recipients[owner.TelegramID] = struct{}{}
		}
	}
	for _, id := range s.cfg.AdminTelegramIDs {
		if id != 0 {
			recipients[id] = struct{}{}
		}
	}
	if len(recipients) == 0 {
		return
	}
	text := fmt.Sprintf("%s\n群：%s", message, formatOwnerWarnChatLabel(chat))
	for id := range recipients {
		if err := s.SendHTMLPrivateMessage(id, text); err != nil {
			s.logger.Warn("send bot permission warning to owner failed", zap.Error(err), zap.Int64("telegram_id", id))
		}
	}
}

func formatOwnerWarnChatLabel(chat *tele.Chat) string {
	if chat == nil {
		return "未命名 [0] ()"
	}
	name := strings.TrimSpace(chat.Title)
	if name == "" && strings.TrimSpace(chat.Username) != "" {
		name = "@" + strings.TrimPrefix(strings.TrimSpace(chat.Username), "@")
	}
	if name == "" {
		name = "未命名"
	}
	return fmt.Sprintf("%s [%d] (%s)", htmlEscape(name), chat.ID, htmlEscape(string(chat.Type)))
}
