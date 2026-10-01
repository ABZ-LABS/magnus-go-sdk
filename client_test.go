package magnus_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	magnus "github.com/ABZ-LABS/magnus-go-sdk"
	"github.com/ABZ-LABS/magnus-go-sdk/mockmagnus"
)

// newFixture starts a mock and a client with retries off: a test that wants
// them asks, and the rest fail fast instead of sleeping through a backoff.
func newFixture(t *testing.T) (*mockmagnus.Server, *magnus.Client) {
	t.Helper()
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	client := magnus.New(server.URL(), "magnus_test_key",
		magnus.WithMaxRetries(0), magnus.WithTimeout(10*time.Second))
	return server, client
}

func TestListAgents(t *testing.T) {
	server, client := newFixture(t)

	agents, err := client.ListAgentsContext(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != len(server.Agents) {
		t.Fatalf("got %d agents, want %d", len(agents), len(server.Agents))
	}
	if agents[0].ID != server.Agents[0] {
		t.Errorf("first agent = %q, want %q", agents[0].ID, server.Agents[0])
	}
	if agents[0].OwnedBy != "magnus" {
		t.Errorf("owned_by = %q, want magnus", agents[0].OwnedBy)
	}
}

func TestGetAgent(t *testing.T) {
	_, client := newFixture(t)

	agent, err := client.GetAgentContext(context.Background(), "porteria")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent == nil || agent.ID != "porteria" {
		t.Fatalf("got %+v, want porteria", agent)
	}
}

func TestGetAgentAliasesMagnus(t *testing.T) {
	_, client := newFixture(t)

	agent, err := client.GetAgentContext(context.Background(), "magnus")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent == nil || agent.ID != "magnus_standard" {
		t.Fatalf("got %+v, want magnus_standard", agent)
	}
}

// A 404 here means "no such persona", which is an answer, not a failure.
func TestGetAgentUnknownIsNilNotError(t *testing.T) {
	_, client := newFixture(t)

	agent, err := client.GetAgentContext(context.Background(), "no_such_agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent != nil {
		t.Fatalf("got %+v, want nil", agent)
	}
}

func TestGetAgentEscapesTheID(t *testing.T) {
	server, client := newFixture(t)

	_, _ = client.GetAgentContext(context.Background(), "weird/id")
	if got := server.LastRequest().Path; got != "/v1/models/weird%2Fid" {
		t.Errorf("path = %q, want /v1/models/weird%%2Fid", got)
	}
}

func TestHealthNeedsNoKey(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	client := magnus.New(server.URL(), "", magnus.WithMaxRetries(0))

	body, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
}

func TestHealthLivesOutsideTheV1Prefix(t *testing.T) {
	server, client := newFixture(t)

	_, _ = client.Health(context.Background())
	if got := server.LastRequest().Path; got != "/api/health/simple" {
		t.Errorf("path = %q, want /api/health/simple", got)
	}
}

func TestBearerAuthByDefault(t *testing.T) {
	server, client := newFixture(t)

	_, _ = client.ListAgentsContext(context.Background())
	if got := server.LastRequest().Headers.Get("Authorization"); got != "Bearer magnus_test_key" {
		t.Errorf("Authorization = %q", got)
	}
}

// Both are accepted by the server; a proxy may strip one or the other.
func TestXAPIKeyAuthScheme(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	client := magnus.New(server.URL(), "k",
		magnus.WithAuthScheme(magnus.AuthAPIKey), magnus.WithMaxRetries(0))

	_, _ = client.ListAgentsContext(context.Background())
	headers := server.LastRequest().Headers
	if got := headers.Get("X-API-Key"); got != "k" {
		t.Errorf("X-API-Key = %q, want k", got)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be absent, got %q", got)
	}
}

func TestMissingKeyIsAuthenticationError(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	client := magnus.New(server.URL(), "", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	if !errors.Is(err, magnus.ErrAuthentication) {
		t.Fatalf("err = %v, want ErrAuthentication", err)
	}
}

func TestWrongKeyIsDistinguishedFromMissing(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.APIKey = "the_right_key"
	client := magnus.New(server.URL(), "wrong", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	var apiErr *magnus.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized || apiErr.Code != "invalid_api_key" {
		t.Errorf("got status=%d code=%q", apiErr.Status, apiErr.Code)
	}
}

func TestChatReturnsTheFullResponse(t *testing.T) {
	server, client := newFixture(t)
	server.Reply = "Buenas."

	resp, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text() != "Buenas." {
		t.Errorf("text = %q, want Buenas.", resp.Text())
	}
	if resp.Object != "chat.completion" {
		t.Errorf("object = %q", resp.Object)
	}
}

func TestSendMessageReturnsOnlyTheText(t *testing.T) {
	server, client := newFixture(t)
	server.Reply = "Listo."

	text, err := client.SendMessageContext(context.Background(), "magnus_standard", "Hola", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if text != "Listo." {
		t.Errorf("text = %q, want Listo.", text)
	}
}

func TestUserIsSentForMultiTenantAttribution(t *testing.T) {
	server, client := newFixture(t)
	client.SetUser("juan@empresa.com")

	_, _ = client.SendMessageContext(context.Background(), "magnus_standard", "Hola", nil)
	if got := server.LastRequest().Body["user"]; got != "juan@empresa.com" {
		t.Errorf("user = %v", got)
	}
}

func TestPerCallUserOverridesTheClientDefault(t *testing.T) {
	server, client := newFixture(t)
	client.SetUser("default@x.com")

	_, _ = client.SendMessageContext(context.Background(), "magnus_standard", "Hola",
		&magnus.SendMessageOpts{User: "otro@x.com"})
	if got := server.LastRequest().Body["user"]; got != "otro@x.com" {
		t.Errorf("user = %v", got)
	}
}

// The server flattens the text parts; the client must not mangle them first.
func TestMultimodalContentPassesThrough(t *testing.T) {
	server, client := newFixture(t)

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{{Role: "user", Content: []any{
			magnus.TextPart("¿Qué es esto?"),
			magnus.ImagePart("https://x/y.png"),
		}}}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	messages := server.LastRequest().Body["messages"].([]any)
	parts := messages[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want 2", len(parts))
	}
	if parts[0].(map[string]any)["text"] != "¿Qué es esto?" {
		t.Errorf("text part = %v", parts[0])
	}
}

// An image-only turn has no text for the pipeline to read.
func TestImageOnlyTurnIsATyped400(t *testing.T) {
	_, client := newFixture(t)

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{{Role: "user", Content: []any{magnus.ImagePart("x")}}}, nil)
	if !errors.Is(err, magnus.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	var apiErr *magnus.APIError
	if errors.As(err, &apiErr) && apiErr.Param != "messages" {
		t.Errorf("param = %q, want messages", apiErr.Param)
	}
}

// `estimated` is a character heuristic — nobody should bill on it unknowingly.
func TestUsageSourceIsExposed(t *testing.T) {
	server, client := newFixture(t)
	server.UsageSource = "estimated"

	resp, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Magnus == nil || resp.Magnus.UsageSource != "estimated" {
		t.Errorf("magnus = %+v", resp.Magnus)
	}
}

func TestExtraBodyIsForwarded(t *testing.T) {
	server, client := newFixture(t)

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")},
		&magnus.ChatOpts{ExtraBody: map[string]any{"some_new_field": 42}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := server.LastRequest().Body["some_new_field"]; got != float64(42) {
		t.Errorf("some_new_field = %v", got)
	}
}

// Magnus refuses these rather than ignoring them; the SDK must say which.
func TestUnsupportedParametersAreNamed(t *testing.T) {
	cases := map[string]any{
		"tools":           []any{map[string]any{"type": "function"}},
		"tool_choice":     "auto",
		"functions":       []any{map[string]any{"name": "f"}},
		"function_call":   "auto",
		"response_format": map[string]any{"type": "json_object"},
	}
	for param, value := range cases {
		t.Run(param, func(t *testing.T) {
			_, client := newFixture(t)
			_, err := client.ChatContext(context.Background(), "magnus_standard",
				[]magnus.ChatMessage{magnus.UserMessage("Hola")},
				&magnus.ChatOpts{ExtraBody: map[string]any{param: value}})

			if !errors.Is(err, magnus.ErrUnsupportedParameter) {
				t.Fatalf("err = %v, want ErrUnsupportedParameter", err)
			}
			if !errors.Is(err, magnus.ErrInvalidRequest) {
				t.Errorf("an unsupported parameter is also an invalid request")
			}
			var apiErr *magnus.APIError
			if errors.As(err, &apiErr) && apiErr.Param != param {
				t.Errorf("param = %q, want %q", apiErr.Param, param)
			}
		})
	}
}

// `{"tools": []}` says nothing, so refusing it would break clients for free.
func TestEmptyValuesOfRefusedParametersAreAccepted(t *testing.T) {
	for _, extra := range []map[string]any{
		{"n": 1}, {"tools": []any{}}, {"response_format": map[string]any{}},
	} {
		_, client := newFixture(t)
		resp, err := client.ChatContext(context.Background(), "magnus_standard",
			[]magnus.ChatMessage{magnus.UserMessage("Hola")},
			&magnus.ChatOpts{ExtraBody: extra})
		if err != nil {
			t.Fatalf("extra=%v: %v", extra, err)
		}
		if resp.Text() == "" {
			t.Errorf("extra=%v: empty answer", extra)
		}
	}
}

func TestSessionIDIsSent(t *testing.T) {
	server, client := newFixture(t)
	const sid = "11111111-2222-3333-4444-555555555555"

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, &magnus.ChatOpts{SessionID: sid})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := server.LastRequest().Body["session_id"]; got != sid {
		t.Errorf("session_id = %v", got)
	}
}

// The server 400s on this; failing locally names the caller's own bug.
func TestNonUUIDSessionIDFailsBeforeTheRoundTrip(t *testing.T) {
	server, client := newFixture(t)

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")},
		&magnus.ChatOpts{SessionID: "not-a-uuid"})
	if !errors.Is(err, magnus.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if len(server.Recorded()) != 0 {
		t.Errorf("the request was sent anyway: %+v", server.Recorded())
	}
}

// ------------------------------------------------------- base URL wiring

// The base URL is configuration, not a constant: the SDK is pointed at
// production, staging or a local server by its caller, and nothing about
// iamagnus.com is compiled in.
func TestTheBaseURLIsWhereTheRequestsGo(t *testing.T) {
	server, client := newFixture(t)

	if client.BaseURL != server.URL() {
		t.Fatalf("BaseURL = %q, want %q", client.BaseURL, server.URL())
	}
	if _, err := client.ListAgentsContext(context.Background()); err != nil {
		t.Fatalf("ListAgents against the configured host: %v", err)
	}
	if len(server.Recorded()) == 0 {
		t.Error("the configured host received nothing")
	}
}

// A trailing slash is the most common way to end up requesting //v1/models.
func TestATrailingSlashIsTrimmed(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	client := magnus.New(server.URL()+"/", "k", magnus.WithMaxRetries(0))

	if _, err := client.ListAgentsContext(context.Background()); err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if got := server.LastRequest().Path; got != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", got)
	}
}

// Python raises and Node throws at construction; Go cannot without changing
// the signature, so the mistake has to survive to the first call rather than
// becoming an opaque transport error.
func TestAnEmptyBaseURLIsNamedNotGuessedAt(t *testing.T) {
	client := magnus.New("", "k")

	_, err := client.ListAgentsContext(context.Background())
	if !errors.Is(err, magnus.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "BaseURL is required") {
		t.Errorf("err = %v, should name the missing setting", err)
	}
}
