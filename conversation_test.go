package magnus_test

import (
	"context"
	"errors"
	"testing"

	magnus "github.com/ABZ-LABS/magnus-go-sdk"
	"github.com/ABZ-LABS/magnus-go-sdk/mockmagnus"
)

func TestConversationOpensASessionOnTheFirstTurn(t *testing.T) {
	_, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")

	if chat.SessionID != "" {
		t.Fatalf("session id before the first turn: %q", chat.SessionID)
	}
	if _, err := chat.SendContext(context.Background(), "Hola", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if chat.SessionID == "" {
		t.Error("no session id after the first turn")
	}
	if chat.SessionSource != "new" {
		t.Errorf("session source = %q, want new", chat.SessionSource)
	}
}

func TestConversationCarriesTheSessionForward(t *testing.T) {
	server, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")

	_, _ = chat.SendContext(context.Background(), "Hola", nil)
	first := chat.SessionID
	_, _ = chat.SendContext(context.Background(), "¿Y el precio?", nil)

	if got := server.LastRequest().Body["session_id"]; got != first {
		t.Errorf("session_id = %v, want %q", got, first)
	}
}

// The pipeline rolls the session on persona/user/org change mid-turn. Echoing
// the request value back would pin the thread to a conversation the server has
// already abandoned.
func TestTheSessionTheServerRanOnWins(t *testing.T) {
	server, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")
	_, _ = chat.SendContext(context.Background(), "Hola", nil)

	const rolled = "99999999-8888-7777-6666-555555555555"
	server.RolledSessionID = rolled
	_, _ = chat.SendContext(context.Background(), "Otra cosa", nil)

	if chat.SessionID != rolled {
		t.Errorf("session = %q, want %q", chat.SessionID, rolled)
	}
}

func TestConversationExposesCorrelationIDs(t *testing.T) {
	server, client := newFixture(t)
	server.TraceID, server.TurnID = "trace-42", "turn-42"

	chat := client.Conversation("magnus_standard", "")
	_, _ = chat.SendContext(context.Background(), "Hola", nil)

	if chat.LastTraceID != "trace-42" || chat.LastTurnID != "turn-42" {
		t.Errorf("trace=%q turn=%q", chat.LastTraceID, chat.LastTurnID)
	}
}

func TestConversationKeepsUsageAndItsProvenance(t *testing.T) {
	server, client := newFixture(t)
	server.UsageSource = "estimated"
	server.Usage = map[string]int{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3}

	chat := client.Conversation("magnus_standard", "")
	_, _ = chat.SendContext(context.Background(), "Hola", nil)

	if chat.LastUsage == nil || chat.LastUsage.TotalTokens != 3 {
		t.Errorf("usage = %+v", chat.LastUsage)
	}
	if chat.LastUsageSource != "estimated" {
		t.Errorf("usage source = %q", chat.LastUsageSource)
	}
}

func TestResetStartsANewConversation(t *testing.T) {
	server, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")

	_, _ = chat.SendContext(context.Background(), "Hola", nil)
	chat.Reset()
	_, _ = chat.SendContext(context.Background(), "Empecemos de nuevo", nil)

	if _, present := server.LastRequest().Body["session_id"]; present {
		t.Error("session_id was sent after Reset")
	}
}

func TestConversationInheritsTheClientUser(t *testing.T) {
	server, client := newFixture(t)
	client.SetUser("juan@empresa.com")

	_, _ = client.Conversation("magnus_standard", "").SendContext(
		context.Background(), "Hola", nil)

	if got := server.LastRequest().Body["user"]; got != "juan@empresa.com" {
		t.Errorf("user = %v", got)
	}
}

func TestResumeContinuesAKnownSession(t *testing.T) {
	server, client := newFixture(t)
	const sid = "11111111-2222-3333-4444-555555555555"

	chat, err := client.Resume("magnus_standard", "", sid)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	_, _ = chat.SendContext(context.Background(), "Seguimos", nil)

	if got := server.LastRequest().Body["session_id"]; got != sid {
		t.Errorf("session_id = %v, want %q", got, sid)
	}
}

func TestResumeRefusesANonUUID(t *testing.T) {
	_, client := newFixture(t)

	if _, err := client.Resume("magnus_standard", "", "nope"); !errors.Is(
		err, magnus.ErrInvalidRequest,
	) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestAStreamedTurnAdvancesTheSession(t *testing.T) {
	_, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")

	stream, err := chat.StreamContext(context.Background(), "Hola", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	stream.Close()

	if chat.SessionID == "" || chat.SessionID != stream.SessionID() {
		t.Errorf("conversation=%q stream=%q", chat.SessionID, stream.SessionID())
	}
}

// It ran. Dropping the session would silently fork the thread.
func TestAFailedStreamedTurnStillAdvancedTheConversation(t *testing.T) {
	server, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")
	_, _ = chat.SendContext(context.Background(), "Hola", nil)
	known := chat.SessionID

	server.StreamMode = mockmagnus.StreamFailure
	stream, err := chat.StreamContext(context.Background(), "Y esto", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Collect(); err == nil {
		t.Fatal("expected a stream error")
	}
	stream.Close()

	if chat.SessionID != known {
		t.Errorf("session = %q, want it kept at %q", chat.SessionID, known)
	}
}

// A person from the team can take a conversation over from the agent. The
// server keeps answering 200 — the agent's hand-off, then a fixed notice — so
// without the flag a caller cannot tell a person is in charge.
func TestConversationHandoffFollowsTheServer(t *testing.T) {
	server, client := newFixture(t)
	chat := client.Conversation("magnus_standard", "")

	_, _ = chat.SendContext(context.Background(), "Hola", nil)
	if chat.Handoff {
		t.Fatal("a conversation starts with the agent")
	}
	server.Handoff = true
	_, _ = chat.SendContext(context.Background(), "Quiero hablar con una persona", nil)
	if !chat.Handoff {
		t.Error("handoff not reported")
	}
	server.Handoff = false
	_, _ = chat.SendContext(context.Background(), "Hola de nuevo", nil)
	if chat.Handoff {
		t.Error("handoff outlived the server saying so")
	}
}

func TestAStreamedTurnReportsTheHandoff(t *testing.T) {
	server, client := newFixture(t)
	server.Handoff = true
	chat := client.Conversation("magnus_standard", "")

	stream, err := chat.StreamContext(context.Background(), "Hola", nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	stream.Close()

	if !chat.Handoff {
		t.Error("handoff not reported on a streamed turn")
	}
}

func TestAServerWithoutTheHandoffFieldIsNotAHandoff(t *testing.T) {
	server, client := newFixture(t)
	server.HandoffOmitted = true
	chat := client.Conversation("magnus_standard", "")

	_, _ = chat.SendContext(context.Background(), "Hola", nil)
	if chat.Handoff {
		t.Error("a missing field read as a handoff")
	}
}
