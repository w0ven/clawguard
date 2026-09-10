package bot

import (
	"context"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

// The existing named verification buttons keep their handlers. Only this
// assistant's /lm and auto-delete buttons enter the native fallback.
func (s *Service) handleNativeAssistantCallback(c tele.Context) error {
	if !s.NativeAssistantEnabled() {
		return nil
	}
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Message.Chat == nil || cb.Sender == nil {
		return nil
	}
	if !strings.HasPrefix(cb.Data, "lml:") && !strings.HasPrefix(cb.Data, "lmd:") && cb.Data != "adel:1" {
		return nil
	}
	msg := cb.Message
	if msg.Chat.ID >= 0 {
		return nil
	}
	ctx := context.Background()
	state, err := s.GetSystemState(ctx)
	if err != nil || state.Frozen {
		return err
	}
	allowed, err := s.IsAuthorizedGroup(ctx, msg.Chat.ID)
	if err != nil || !allowed {
		return err
	}
	n := s.assistant.native
	policy, err := n.a.Policy(ctx, msg.Chat.ID)
	if err != nil || !policy.ChatEnabled {
		return err
	}
	owned, err := s.queries.NativeDeliveryOwned(ctx, msg.Chat.ID, int32(msg.ThreadID), int64(msg.ID))
	if err != nil || !owned {
		return err
	}
	// The Telegram update owner prevents update replay. Only idempotent source
	// list/delete operations are exposed; callback grants can never invoke LLMs.
	generation, err := s.queries.NativeScopeGeneration(ctx, msg.Chat.ID, int32(msg.ThreadID))
	if err != nil {
		return err
	}
	grant := nativeGrant{GroupID: msg.Chat.ID, TopicID: int32(msg.ThreadID), ActorID: cb.Sender.ID,
		MessageID: int64(msg.ID), CallbackID: cb.ID, Generation: generation, Expires: time.Now().Add(2 * time.Minute).Unix()}
	background, err := n.backgroundGrant(ctx, msg.Chat.ID, int32(msg.ThreadID))
	if err != nil {
		return err
	}
	message, err := nativeMessage(msg)
	if err != nil {
		return err
	}
	_, err = n.request(ctx, "/callbacks", map[string]any{"group_id": msg.Chat.ID, "topic_id": msg.ThreadID,
		"grant": n.sign(grant), "turn": "callback:" + cb.ID, "background_grant": background,
		"is_owner": n.a.assistantIsOwner(ctx, cb.Sender.ID),
		"callback": map[string]any{"id": cb.ID, "from": cb.Sender, "message": message, "data": cb.Data, "chat_instance": cb.ChatInstance}})
	return err
}
