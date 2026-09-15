package magnus_test

import (
	"context"
	"errors"
	"testing"

	magnus "github.com/MeGrimlock/magnus-go-sdk"
	"github.com/MeGrimlock/magnus-go-sdk/mockmagnus"
)

func drain(t *testing.T, stream *magnus.ChatStream) []string {
	t.Helper()
	var pieces []string
	for stream.Next() {
		pieces = append(pieces, stream.Delta())
	}
	return pieces
}

func TestStreamDeliversDeltasOneByOne(t *testing.T) {
	server, client := newFixture(t)
	server.Reply = "Hola que tal"

	stream, err := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	defer stream.Close()

	pieces := drain(t, stream)
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	want := []string{"Hola", " que", " tal"}
	if len(pieces) != len(want) {
		t.Fatalf("got %v, want %v", pieces, want)
	}
	for i := range want {
		if pieces[i] != want[i] {
			t.Errorf("piece %d = %q, want %q", i, pieces[i], want[i])
		}
	}
}

func TestStreamExposesTheWholeTextAtTheEnd(t *testing.T) {
	server, client := newFixture(t)
	server.Reply = "Hola que tal"

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()
	drain(t, stream)

	if stream.Text() != "Hola que tal" {
		t.Errorf("text = %q", stream.Text())
	}
	if stream.FinishReason() != "stop" {
		t.Errorf("finish_reason = %q", stream.FinishReason())
	}
}

// The server may deliver a turn whole; it then comes as one delta, not zero.
func TestATurnDeliveredWholeStillArrives(t *testing.T) {
	server, client := newFixture(t)
	server.StreamMode = mockmagnus.StreamSingle
	server.Reply = "Respuesta entera"

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()

	pieces := drain(t, stream)
	if len(pieces) != 1 || pieces[0] != "Respuesta entera" {
		t.Fatalf("got %v, want one whole delta", pieces)
	}
}

// The live path puts them on the closing chunk, the buffered path on the first.
func TestExtensionsAreMergedFromWhicheverChunkCarriesThem(t *testing.T) {
	for _, mode := range []mockmagnus.StreamMode{
		mockmagnus.StreamTokens, mockmagnus.StreamSingle,
	} {
		t.Run(string(mode), func(t *testing.T) {
			server, client := newFixture(t)
			server.StreamMode = mode

			stream, _ := client.StreamChat(context.Background(), "magnus_standard",
				[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
			defer stream.Close()
			drain(t, stream)

			if stream.SessionID() == "" {
				t.Error("no session id")
			}
			ext := stream.Magnus()
			if ext.SessionSource == "" {
				t.Error("no session source")
			}
			if ext.UsageSource != "measured" {
				t.Errorf("usage_source = %q", ext.UsageSource)
			}
		})
	}
}

func TestStreamWithholdsUsageUnlessAskedFor(t *testing.T) {
	_, client := newFixture(t)

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()
	drain(t, stream)

	if stream.Usage() != nil {
		t.Errorf("usage = %+v, want nil", stream.Usage())
	}
}

func TestIncludeUsageYieldsAFinalUsageChunk(t *testing.T) {
	server, client := newFixture(t)
	server.Usage = map[string]int{
		"prompt_tokens": 5, "completion_tokens": 6, "total_tokens": 11,
	}

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, &magnus.ChatOpts{IncludeUsage: true})
	defer stream.Close()
	drain(t, stream)

	if stream.Usage() == nil || stream.Usage().TotalTokens != 11 {
		t.Fatalf("usage = %+v", stream.Usage())
	}
}

func TestIncludeUsageIsRequestedInTheDocumentedShape(t *testing.T) {
	server, client := newFixture(t)

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, &magnus.ChatOpts{IncludeUsage: true})
	defer stream.Close()
	drain(t, stream)

	body := server.LastRequest().Body
	if body["stream"] != true {
		t.Errorf("stream = %v", body["stream"])
	}
	options, ok := body["stream_options"].(map[string]any)
	if !ok || options["include_usage"] != true {
		t.Errorf("stream_options = %v", body["stream_options"])
	}
}

func TestCollectDrainsAndReturnsTheText(t *testing.T) {
	server, client := newFixture(t)
	server.Reply = "Todo junto"

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()

	text, err := stream.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if text != "Todo junto" {
		t.Errorf("text = %q", text)
	}
}

// The status line went out as 200 before the turn failed. A client that reads
// only the deltas hands back a truncated answer as though it were the real one.
// This is the whole reason the stream is parsed rather than concatenated.
func TestAMidStreamErrorIsReportedAsAnError(t *testing.T) {
	server, client := newFixture(t)
	server.StreamMode = mockmagnus.StreamFailure

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()
	drain(t, stream)

	var streamErr *magnus.StreamError
	if !errors.As(stream.Err(), &streamErr) {
		t.Fatalf("err = %v, want *StreamError", stream.Err())
	}
}

func TestTheStreamErrorCarriesTheServerEnvelope(t *testing.T) {
	server, client := newFixture(t)
	server.StreamMode = mockmagnus.StreamFailure
	server.StreamError = map[string]any{
		"message": "El agente no pudo completar el turno.",
		"type":    "server_error", "param": nil, "code": "pipeline_failed",
	}

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()
	drain(t, stream)

	var streamErr *magnus.StreamError
	if !errors.As(stream.Err(), &streamErr) {
		t.Fatalf("err = %v", stream.Err())
	}
	if streamErr.Code != "pipeline_failed" || streamErr.Type != "server_error" {
		t.Errorf("code=%q type=%q", streamErr.Code, streamErr.Type)
	}
	// A mid-stream server failure is still a server failure.
	if !errors.Is(stream.Err(), magnus.ErrServer) {
		t.Error("a server_error mid-stream should match ErrServer")
	}
}

// Contradicting bytes already delivered is worse than truncating.
func TestWhatTheReaderAlreadySawIsKept(t *testing.T) {
	server, client := newFixture(t)
	server.StreamMode = mockmagnus.StreamFailure
	server.Reply = "Hola mundo"

	stream, _ := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("hi")}, nil)
	defer stream.Close()

	seen := ""
	for stream.Next() {
		seen += stream.Delta()
	}

	var streamErr *magnus.StreamError
	if !errors.As(stream.Err(), &streamErr) {
		t.Fatalf("err = %v", stream.Err())
	}
	if streamErr.PartialText != seen {
		t.Errorf("partial = %q, reader saw %q", streamErr.PartialText, seen)
	}
	if streamErr.PartialText != "Hola " {
		t.Errorf("partial = %q, want %q", streamErr.PartialText, "Hola ")
	}
}

// It has no idempotency key, so a retry would silently run the turn again.
func TestAStreamIsNeverRetried(t *testing.T) {
	server := mockmagnus.Start()
	t.Cleanup(server.Close)
	server.Force(503, map[string]any{"error": map[string]any{
		"message": "down", "type": "server_error", "param": nil, "code": nil,
	}}, nil)
	client := magnus.New(server.URL(), "k", magnus.WithMaxRetries(3))

	_, err := client.StreamChat(context.Background(), "magnus_standard",
		[]magnus.ChatMessage{magnus.UserMessage("Hola")}, nil)
	if !errors.Is(err, magnus.ErrServer) {
		t.Fatalf("err = %v, want ErrServer", err)
	}
	posts := server.CountRequests(func(r mockmagnus.Request) bool { return r.Method == "POST" })
	if posts != 1 {
		t.Errorf("%d POSTs, want 1", posts)
	}
}
