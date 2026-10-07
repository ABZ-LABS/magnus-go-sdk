// Package mockmagnus is a fake Magnus that implements CONTRACT.md.
//
// The unit suite runs against this rather than a stubbed RoundTripper, so the
// tests exercise real sockets, real chunked transfer and real SSE framing — the
// three things a stubbed transport cannot reproduce.
//
// Every behaviour here is specified in CONTRACT.md. When the contract changes,
// change CONTRACT.md first, then this package, and let the suite fail.
package mockmagnus

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

var uuidRe = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
)

// DefaultAgents are the personas the mock exposes.
var DefaultAgents = []string{"magnus_standard", "porteria", "inmobiliaria"}

// StreamMode selects how a streamed turn is delivered.
type StreamMode string

const (
	// StreamTokens is the live path: word by word.
	StreamTokens StreamMode = "tokens"
	// StreamSingle is the buffered path: one delta, because the server
	// delivered this turn whole.
	StreamSingle StreamMode = "single"
	// StreamFailure is a turn that failed after the stream opened.
	StreamFailure StreamMode = "error"
)

// Request is one recorded call.
type Request struct {
	Method  string
	Path    string
	Body    map[string]any
	Headers http.Header
}

type forced struct {
	status  int
	body    any
	headers map[string]string
}

// Server is a Magnus deployment that exists for the length of one test.
type Server struct {
	mu sync.Mutex

	Agents []string
	// APIKey empty accepts any non-empty credential; set it to enforce one.
	APIKey      string
	Requests    []Request
	TurnsRun    int
	Reply       string
	TraceID     string
	TurnID      string
	UsageSource string
	// Handoff stands for a conversation a person from the team has taken over;
	// HandoffOmitted for a server older than the field.
	Handoff        bool
	HandoffOmitted bool
	// OperatorMessages is what GET /v1/conversations/updates serves, oldest
	// first; UpdatesPage its page size (50 when zero); BeforeUpdates runs before
	// each such request, so a test can change state between polls.
	OperatorMessages   []map[string]any
	UpdatesPage        int
	BeforeUpdates      func(*Server)
	Usage              map[string]int
	RateLimitRemaining int
	RateLimitReset     string
	// RolledSessionID makes the pipeline "roll" the session mid-turn, so a
	// client that echoes the request value instead of the response one is caught.
	RolledSessionID   string
	UnsupportedParams []string
	StreamMode        StreamMode
	StreamError       map[string]any

	idempotency map[string]*idemEntry
	forcedQueue []forced
	httpServer  *httptest.Server
}

type idemEntry struct {
	inFlight bool
	status   int
	body     any
}

// Start brings up a mock on a loopback port. Close it when the test ends.
func Start() *Server {
	server := &Server{
		Agents:             append([]string(nil), DefaultAgents...),
		Reply:              "Hola, soy Magnus.",
		TraceID:            "trace-1",
		TurnID:             "turn-1",
		UsageSource:        "measured",
		Usage:              map[string]int{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
		RateLimitRemaining: 119,
		RateLimitReset:     "2026-09-09T12:01:00+00:00",
		UnsupportedParams: []string{
			"tools", "tool_choice", "functions", "function_call", "response_format",
		},
		StreamMode: StreamTokens,
		StreamError: map[string]any{
			"message": "Internal server error.", "type": "server_error",
			"param": nil, "code": nil,
		},
		idempotency: map[string]*idemEntry{},
	}
	server.httpServer = httptest.NewServer(http.HandlerFunc(server.handle))
	return server
}

// URL is the mock's base URL.
func (s *Server) URL() string { return s.httpServer.URL }

// Close shuts the mock down.
func (s *Server) Close() { s.httpServer.Close() }

// Force queues a response served once, before normal handling.
func (s *Server) Force(status int, body any, headers map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forcedQueue = append(s.forcedQueue, forced{status, body, headers})
}

// SetInFlight marks an idempotency key as a turn that is still running.
func (s *Server) SetInFlight(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idempotency[key] = &idemEntry{inFlight: true}
}

// Recorded returns a copy of the recorded requests.
func (s *Server) Recorded() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.Requests...)
}

// LastRequest is the most recent recorded call.
func (s *Server) LastRequest() Request {
	recorded := s.Recorded()
	if len(recorded) == 0 {
		return Request{}
	}
	return recorded[len(recorded)-1]
}

// CountRequests counts recorded calls matching a predicate.
func (s *Server) CountRequests(match func(Request) bool) int {
	count := 0
	for _, req := range s.Recorded() {
		if match(req) {
			count++
		}
	}
	return count
}

// ------------------------------------------------------------------ handling

func errorBody(message, errType, param, code string) map[string]any {
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

func modelObject(id string) map[string]any {
	return map[string]any{
		"id": id, "object": "model", "created": 1757000000,
		"owned_by": "magnus", "permission": []any{}, "root": id, "parent": nil,
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any, headers map[string]string) {
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) authorized(r *http.Request) bool {
	presented := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		presented = strings.TrimPrefix(auth, "Bearer ")
	} else if key := r.Header.Get("X-API-Key"); key != "" {
		presented = key
	}
	if presented == "" {
		return false
	}
	s.mu.Lock()
	expected := s.APIKey
	s.mu.Unlock()
	return expected == "" || presented == expected
}

func (s *Server) takeForced() (forced, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.forcedQueue) == 0 {
		return forced{}, false
	}
	next := s.forcedQueue[0]
	s.forcedQueue = s.forcedQueue[1:]
	return next, true
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	s.mu.Lock()
	s.Requests = append(s.Requests, Request{
		Method: r.Method, Path: r.URL.RequestURI(), Body: body, Headers: r.Header.Clone(),
	})
	s.mu.Unlock()

	if r.Method == http.MethodGet && r.URL.Path == "/api/health/simple" {
		s.writeJSON(w, 200, map[string]any{
			"status": "ok", "timestamp": "2026-09-09T12:00:00+00:00",
			"version": map[string]any{"version": "mock"},
		}, nil)
		return
	}

	if !s.authorized(r) {
		s.writeJSON(w, 401, errorBody(
			"Incorrect API key provided.", "invalid_request_error", "", "invalid_api_key",
		), nil)
		return
	}

	if next, ok := s.takeForced(); ok {
		s.writeJSON(w, next.status, next.body, next.headers)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		s.mu.Lock()
		data := make([]map[string]any, 0, len(s.Agents))
		for _, agent := range s.Agents {
			data = append(data, modelObject(agent))
		}
		s.mu.Unlock()
		s.writeJSON(w, 200, map[string]any{"object": "list", "data": data}, nil)

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/models/")
		s.mu.Lock()
		agents := append([]string(nil), s.Agents...)
		s.mu.Unlock()
		if id == "magnus" && contains(agents, "magnus_standard") {
			id = "magnus_standard"
		}
		if contains(agents, id) {
			s.writeJSON(w, 200, modelObject(id), nil)
			return
		}
		s.writeJSON(w, 404, errorBody(
			fmt.Sprintf("The model '%s' does not exist or you do not have access.", id),
			"invalid_request_error", "model", "model_not_found",
		), nil)

	case r.Method == http.MethodGet && r.URL.Path == "/v1/conversations/updates":
		s.updates(w, r)

	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		s.chat(w, r, body)

	default:
		s.writeJSON(w, 404, errorBody("Not found.", "invalid_request_error", "", ""), nil)
	}
}

// updates serves the operator's replies after a cursor, and the handoff state.
func (s *Server) updates(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.BeforeUpdates != nil {
		s.BeforeUpdates(s)
	}
	messages := append([]map[string]any(nil), s.OperatorMessages...)
	handoff, size := s.Handoff, s.UpdatesPage
	s.mu.Unlock()
	if size <= 0 {
		size = 50
	}
	start := 0
	if after := r.URL.Query().Get("after"); after != "" {
		start = -1
		for i, message := range messages {
			if message["id"] == after {
				start = i + 1
				break
			}
		}
		if start < 0 {
			s.writeJSON(w, 400, errorBody(
				"after is not a message of this user.",
				"invalid_request_error", "after", "invalid_cursor",
			), nil)
			return
		}
	}
	end := start + size
	if end > len(messages) {
		end = len(messages)
	}
	s.writeJSON(w, 200, map[string]any{
		"object": "list", "handoff": handoff,
		"data": messages[start:end], "has_more": end < len(messages),
	}, nil)
}

var operatorSeq atomic.Int64

// NewOperatorMessage is one reply a person from the team wrote, as the server
// serves it.
func NewOperatorMessage(content string) map[string]any {
	return map[string]any{
		"id": fmt.Sprintf("msg-%d", operatorSeq.Add(1)), "object": "conversation.message", "author": "human",
		"content": content, "created": 1767225600,
	}
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if body == nil {
		s.writeJSON(w, 400, errorBody(
			"Request body must be a JSON object.", "invalid_request_error", "body", "",
		), nil)
		return
	}

	s.mu.Lock()
	unsupported := append([]string(nil), s.UnsupportedParams...)
	s.mu.Unlock()

	// Refused rather than ignored — the pipeline owns these.
	for _, param := range unsupported {
		value, present := body[param]
		if !present || value == nil || isEmptyValue(value) {
			continue
		}
		s.writeJSON(w, 400, errorBody(
			fmt.Sprintf("'%s' is not supported by this endpoint.", param),
			"invalid_request_error", param, "unsupported_parameter",
		), nil)
		return
	}
	if n, present := body["n"]; present && n != nil {
		if count, ok := n.(float64); ok && count != 1 {
			s.writeJSON(w, 400, errorBody(
				"'n' must be 1.", "invalid_request_error", "n", "unsupported_parameter",
			), nil)
			return
		}
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		s.writeJSON(w, 400, errorBody(
			"messages is required and must be a list.", "invalid_request_error", "messages", "",
		), nil)
		return
	}

	text := lastUserText(messages)
	if text == "" {
		s.writeJSON(w, 400, errorBody(
			"No user message with text content found.", "invalid_request_error", "messages", "",
		), nil)
		return
	}

	requested, _ := body["session_id"].(string)
	if requested == "" {
		requested = r.Header.Get("X-Magnus-Session-Id")
	}
	source := "new"
	if requested != "" {
		if !uuidRe.MatchString(requested) {
			s.writeJSON(w, 400, errorBody(
				"session_id must be a UUID.", "invalid_request_error", "session_id", "",
			), nil)
			return
		}
		source = "explicit"
	}

	s.mu.Lock()
	effective := s.RolledSessionID
	if effective != "" {
		source = "new"
	} else if requested != "" {
		effective = requested
	} else {
		effective = "11111111-2222-3333-4444-555555555555"
	}
	reply := s.Reply
	magnus := map[string]any{
		"session_id":     effective,
		"session_source": source,
		"trace_id":       s.TraceID,
		"turn_id":        s.TurnID,
		"usage_source":   s.UsageSource,
	}
	if !s.HandoffOmitted {
		magnus["handoff"] = s.Handoff
	}
	extensions := map[string]any{
		"session_id": effective,
		"magnus":     magnus,
	}
	usage := map[string]int{}
	for key, value := range s.Usage {
		usage[key] = value
	}
	streamMode := s.StreamMode
	streamError := s.StreamError
	rateRemaining := s.RateLimitRemaining
	rateReset := s.RateLimitReset
	s.mu.Unlock()

	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" {
		s.mu.Lock()
		entry, present := s.idempotency[idemKey]
		s.mu.Unlock()
		if present && entry.inFlight {
			s.writeJSON(w, 409, errorBody(
				"A request with this Idempotency-Key is still in progress.",
				"invalid_request_error", "Idempotency-Key", "request_in_progress",
			), nil)
			return
		}
		if present {
			s.writeJSON(w, entry.status, entry.body, nil)
			return
		}
	}

	s.mu.Lock()
	s.TurnsRun++
	s.mu.Unlock()

	if stream, _ := body["stream"].(bool); stream {
		includeUsage := false
		if opts, ok := body["stream_options"].(map[string]any); ok {
			includeUsage, _ = opts["include_usage"].(bool)
		}
		s.writeSSE(w, buildStream(streamMode, streamError, reply, extensions, usage, includeUsage))
		return
	}

	response := map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion", "created": 1757000000,
		"model": body["model"],
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": reply},
			"finish_reason": "stop",
		}},
		"usage": usage,
	}
	for key, value := range extensions {
		response[key] = value
	}
	if idemKey != "" {
		s.mu.Lock()
		s.idempotency[idemKey] = &idemEntry{status: 200, body: response}
		s.mu.Unlock()
	}
	s.writeJSON(w, 200, response, map[string]string{
		"X-RateLimit-Remaining": fmt.Sprintf("%d", rateRemaining),
		"X-RateLimit-Reset":     rateReset,
	})
}

func (s *Server) writeSSE(w http.ResponseWriter, frames []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		_, _ = io.WriteString(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func buildStream(
	mode StreamMode, streamError map[string]any, text string,
	extensions map[string]any, usage map[string]int, includeUsage bool,
) []string {
	chunk := func(choices []any, extra map[string]any, usagePayload map[string]int) string {
		payload := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": 1757000000, "model": "magnus_standard", "choices": choices,
		}
		if usagePayload != nil {
			payload["usage"] = usagePayload
		}
		for key, value := range extra {
			payload[key] = value
		}
		encoded, _ := json.Marshal(payload)
		return "data: " + string(encoded) + "\n\n"
	}

	var out []string

	if mode == StreamSingle {
		// The buffered path: one delta, extensions riding on it.
		out = append(out, chunk([]any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"role": "assistant", "content": text},
			"finish_reason": nil,
		}}, extensions, nil))
		out = append(out, chunk([]any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}}, nil, nil))
	} else {
		// The live path: an opening chunk before any token, then tokens, then a
		// closing chunk carrying the extensions.
		out = append(out, chunk([]any{map[string]any{
			"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil,
		}}, nil, nil))

		if mode == StreamFailure {
			head := text
			if len(head) > 5 {
				head = head[:5]
			}
			out = append(out, chunk([]any{map[string]any{
				"index": 0, "delta": map[string]any{"content": head}, "finish_reason": nil,
			}}, nil, nil))
			out = append(out, chunk([]any{map[string]any{
				"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
			}}, map[string]any{"error": streamError}, nil))
			return append(out, "data: [DONE]\n\n")
		}

		for _, piece := range tokenize(text) {
			out = append(out, chunk([]any{map[string]any{
				"index": 0, "delta": map[string]any{"content": piece}, "finish_reason": nil,
			}}, nil, nil))
		}
		out = append(out, chunk([]any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}}, extensions, nil))
	}

	if includeUsage {
		out = append(out, chunk([]any{}, nil, usage))
	}
	return append(out, "data: [DONE]\n\n")
}

// lastUserText mirrors the server: only the last user message is read, and
// multimodal parts are flattened to their text.
func lastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message, ok := messages[i].(map[string]any)
		if !ok || message["role"] != "user" {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				return content
			}
		case []any:
			var builder strings.Builder
			for _, raw := range content {
				part, ok := raw.(map[string]any)
				if !ok || part["type"] != "text" {
					continue
				}
				if text, ok := part["text"].(string); ok {
					builder.WriteString(text)
				}
			}
			if strings.TrimSpace(builder.String()) != "" {
				return builder.String()
			}
		}
	}
	return ""
}

// tokenize splits into the kind of pieces the server streams: words, spaces kept.
func tokenize(text string) []string {
	words := strings.Split(text, " ")
	pieces := make([]string, 0, len(words))
	for i, word := range words {
		if i == 0 {
			pieces = append(pieces, word)
		} else {
			pieces = append(pieces, " "+word)
		}
	}
	return pieces
}

func isEmptyValue(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
