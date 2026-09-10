package bot

import (
    "context"
    "testing"
    "time"

    tele "gopkg.in/telebot.v3"
)

// Nil storage and bot are deliberate tripwires: ANY assistant storage/transport
// access would panic. The old guard's authoritative Service constructor calls
// this exact NewGroupAssistant(service) path.
func TestRescueNeverConstructsOrRunsLegacyAssistant(t *testing.T) {
    ctx,cancel:=context.WithCancel(context.Background());defer cancel()
    s:=&Service{lifecycleCtx:ctx}
    s.assistant=NewGroupAssistant(s)
    if s.assistant!=nil {t.Fatal("rescue constructed an assistant")}
    drained:=make(chan struct{});go func(){s.wg.Wait();close(drained)}()
    select {case <-drained:case <-time.After(time.Second):t.Fatal("legacy assistant workers started")}
    for _,approved:=range []assistantEligibility{assistantEligibilityEligible,assistantEligibilityBlocked,assistantEligibilityUnknown} {
        for _,edited:=range []bool{false,true} {
            msg:=&tele.Message{ID:1,Chat:&tele.Chat{ID:-123,Type:tele.ChatSuperGroup},Sender:&tele.User{ID:7},Text:"do not learn",Sticker:&tele.Sticker{File:tele.File{FileID:"no-write"}}}
            if err:=s.handleApprovedAssistantMessage(ctx,msg,edited,approved,false,true);err!=nil {t.Fatal(err)}
        }
    }
    if err:=s.assistant.handlePinned(nil);err!=nil {t.Fatal(err)}
    if s.Assistant()!=nil {t.Fatal("assistant admin handle still available")}
}
