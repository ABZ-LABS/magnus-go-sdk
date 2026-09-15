package magnus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	magnus "github.com/MeGrimlock/magnus-go-sdk"
	"github.com/MeGrimlock/magnus-go-sdk/mockmagnus"
)

func envelope(message, errType, param, code string) map[string]any {
	body := map[string]any{"message": message, "type": errType}
	if param == "" {
		body["param"] = nil
	} else {
		body["param"] = param
	}
	if code == "" {
		body["code"] = nil
	} else {
		body["code"] = code
	}
	return map[string]any{"error": body}
}

var rateLimited = envelope(
	"Rate limit reached for this API key.", "rate_limit_error", "", "rate_limit_exceeded",
)

// The error envelope is the SDK's most-used surface after Send. Losing it turns
// "you sent a parameter Magnus refuses" and "your key expired" into one string.
func TestStatusesMapToSentinels(t *testing.T) {
	cases := []struct {
		status   int
		sentinel error
	}{
		{400, magnus.ErrInvalidRequest},
		{401, magnus.ErrAuthentication},
		{403, magnus.ErrPermissionDenied},
		{404, magnus.ErrNotFound},
		{409, magnus.ErrConflict},
		{429, magnus.ErrRateLimit},
		{500, magnus.ErrServer},
		{503, magnus.ErrServer},
	}
	for _, tc := range cases {
		server := mockmagnus.Start()
		server.Force(tc.status, envelope("x", "invalid_request_error", "", ""), nil)
		client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

		_, err := client.ListAgentsContext(context.Background())
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.sentinel)
		}
		server.Close()
	}
}

func TestEveryEnvelopeFieldSurvives(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(400, envelope("bad", "invalid_request_error", "n", "c"), nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	var apiErr *magnus.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Message != "bad" || apiErr.Type != "invalid_request_error" ||
		apiErr.Param != "n" || apiErr.Code != "c" {
		t.Errorf("got %+v", apiErr)
	}
}

func TestErrorStringLeadsWithWhatToActOn(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(400, envelope(
		"no tools", "invalid_request_error", "tools", "unsupported_parameter",
	), nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	text := err.Error()
	for _, want := range []string{"400", "unsupported_parameter", "tools"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q missing from %q", want, text)
		}
	}
}

func TestRetryAfterIsReadAsSeconds(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(429, rateLimited, map[string]string{"Retry-After": "12"})
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	var apiErr *magnus.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v", err)
	}
	seconds, ok := apiErr.RetryAfter()
	if !ok || seconds != 12 {
		t.Errorf("RetryAfter() = %d, %v", seconds, ok)
	}
}

func TestADateFormRetryAfterIsReportedAsAbsent(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(429, rateLimited, map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"})
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	var apiErr *magnus.APIError
	_ = errors.As(err, &apiErr)
	if _, ok := apiErr.RetryAfter(); ok {
		t.Error("a date-form Retry-After should not parse as seconds")
	}
}

// A proxy error page, or HTML from the wrong host. "502" alone locates nothing.
func TestANonEnvelopeBodyKeepsAReadableSlice(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(502, "<html>nginx bad gateway</html>", nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(0))

	_, err := client.ListAgentsContext(context.Background())
	if !strings.Contains(err.Error(), "nginx") {
		t.Errorf("err = %v, want a slice of the body", err)
	}
}

func TestADeadHostIsAConnectionError(t *testing.T) {
	// Port 1 on loopback: nothing listens, and it fails fast.
	client := magnus.New("http://127.0.0.1:1", "k",
		magnus.WithMaxRetries(0), magnus.WithTimeout(2*time.Second))

	_, err := client.Health(context.Background())
	if !errors.Is(err, magnus.ErrConnection) {
		t.Fatalf("err = %v, want ErrConnection", err)
	}
}

// ------------------------------------------------------------- idempotency

func TestIdempotencyKeyIsSentAsAHeader(t *testing.T) {
	server, client := newFixture(t)

	_, _ = client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")},
		&magnus.ChatOpts{IdempotencyKey: "key-1"})

	if got := server.LastRequest().Headers.Get("Idempotency-Key"); got != "key-1" {
		t.Errorf("Idempotency-Key = %q", got)
	}
}

// A turn advances the conversation and can run tools; running it twice is a bug.
func TestIdempotencyReplaysInsteadOfRunningASecondTurn(t *testing.T) {
	server, client := newFixture(t)
	messages := []magnus.ChatMessage{magnus.UserMessage("Hola")}
	opts := &magnus.ChatOpts{IdempotencyKey: "key-1"}

	first, err := client.ChatContext(context.Background(), "magnus_standard", messages, opts)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := client.ChatContext(context.Background(), "magnus_standard", messages, opts)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ID != second.ID || first.Text() != second.Text() {
		t.Errorf("the replay differed: %+v vs %+v", first, second)
	}
	if server.TurnsRun != 1 {
		t.Errorf("%d turns ran, want 1", server.TurnsRun)
	}
}

func TestAnInFlightTurnIsAConflict(t *testing.T) {
	server, client := newFixture(t)
	server.SetInFlight("key-1")

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")},
		&magnus.ChatOpts{IdempotencyKey: "key-1"})

	if !errors.Is(err, magnus.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	var apiErr *magnus.APIError
	if errors.As(err, &apiErr) && apiErr.Code != "request_in_progress" {
		t.Errorf("code = %q", apiErr.Code)
	}
}

func TestNoKeyMeansNothingIsReserved(t *testing.T) {
	server, client := newFixture(t)

	_, _ = client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)

	if got := server.LastRequest().Headers.Get("Idempotency-Key"); got != "" {
		t.Errorf("Idempotency-Key = %q, want absent", got)
	}
}

// ------------------------------------------------------------------ retries

func TestAGetRetriesThroughA429(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(429, rateLimited, map[string]string{"Retry-After": "0"})
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(2))

	agents, err := client.ListAgentsContext(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) == 0 {
		t.Error("the retry returned nothing")
	}
}

func TestRetriesGiveUpWithTheTypedError(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	for i := 0; i < 3; i++ {
		server.Force(429, rateLimited, map[string]string{"Retry-After": "0"})
	}
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(2))

	_, err := client.ListAgentsContext(context.Background())
	if !errors.Is(err, magnus.ErrRateLimit) {
		t.Fatalf("err = %v, want ErrRateLimit", err)
	}
}

// Without an Idempotency-Key a retry runs the pipeline a second time.
func TestABarePostIsNeverRetried(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(503, envelope("upstream down", "server_error", "", ""), nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(3))

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)
	if !errors.Is(err, magnus.ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
	posts := server.CountRequests(func(r mockmagnus.Request) bool { return r.Method == "POST" })
	if posts != 1 {
		t.Errorf("%d POSTs, want 1", posts)
	}
}

// Safe: the server replays rather than re-running.
func TestAPostWithAnIdempotencyKeyIsRetried(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(503, envelope("upstream down", "server_error", "", ""), nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(2))

	resp, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")},
		&magnus.ChatOpts{IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text() == "" {
		t.Error("empty answer after the retry")
	}
	posts := server.CountRequests(func(r mockmagnus.Request) bool { return r.Method == "POST" })
	if posts != 2 {
		t.Errorf("%d POSTs, want 2", posts)
	}
}

func TestA4xxIsNotRetried(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(400, envelope("bad", "invalid_request_error", "messages", ""), nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(3))

	_, _ = client.ListAgentsContext(context.Background())
	gets := server.CountRequests(func(r mockmagnus.Request) bool {
		return r.Path == "/v1/models"
	})
	if gets != 1 {
		t.Errorf("%d GETs, want 1", gets)
	}
}

func TestTheRateLimitBudgetIsTracked(t *testing.T) {
	server, client := newFixture(t)
	server.RateLimitRemaining = 42

	_, err := client.ChatContext(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	remaining, reset, ok := client.RateLimit()
	if !ok || remaining != 42 {
		t.Errorf("remaining = %d, ok = %v", remaining, ok)
	}
	if reset != server.RateLimitReset {
		t.Errorf("reset = %q", reset)
	}
}

func TestContextCancellationStopsARetryLoop(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	for i := 0; i < 5; i++ {
		server.Force(503, envelope("down", "server_error", "", ""), nil)
	}
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(5))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	if _, err := client.ListAgentsContext(ctx); err == nil {
		t.Fatal("expected the cancelled context to end the loop")
	}
}
