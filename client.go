// Package magnus is a Go client for the Magnus /v1 API (OpenAI-compatible
// surface).
//
// Magnus exposes its agents as OpenAI "models". The envelope is OpenAI's; the
// semantics are not, and the differences are what this package exists to hide:
//
//   - History is not state. The server reads only the last user message and
//     keeps conversation state server-side, so resending history does not
//     restore a thread. A session id does. Prefer Client.Conversation.
//   - The agent owns the turn. tools, response_format and friends are refused
//     rather than ignored, because a silently dropped response_format is worse
//     than a 400.
//   - A streamed turn can fail after HTTP 200. See ChatStream.
//
// See CONTRACT.md for the wire format this client is written against.
package magnus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version, reported in the User-Agent.
const Version = "0.1.0"

// DefaultTimeout is above the 60s read timeout common in reverse proxies.
// Matching it exactly means the client gives up at the same instant the proxy
// does, turning a clean server-side timeout into an ambiguous client error.
const DefaultTimeout = 90 * time.Second

var uuidRe = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
)

// AuthScheme selects the header the API key travels in. Both are accepted by
// the server; it only matters behind a proxy that strips one of them.
type AuthScheme string

const (
	AuthBearer AuthScheme = "bearer"
	AuthAPIKey AuthScheme = "x-api-key"
)

// Client talks to a Magnus deployment.
type Client struct {
	// BaseURL is the server root, e.g. https://app.iamagnus.com. Not the /v1
	// prefix — the health probe lives outside it.
	BaseURL string
	// User is the end-user identifier for multi-tenant attribution, sent as the
	// OpenAI "user" field. Overridable per call.
	User string
	// MaxRetries is the number of extra attempts for 429/5xx and transport
	// failures.
	MaxRetries int
	// AuthScheme selects the credential header. Defaults to AuthBearer.
	AuthScheme AuthScheme
	// HTTPClient is used for every request. Its Timeout bounds each attempt.
	HTTPClient *http.Client

	apiKey string

	// configErr is a construction mistake reported from every call.
	//
	// New cannot return an error without breaking every call site, and panicking
	// is the wrong trade for a library, so the mistake travels to the first
	// request instead of turning into a confusing "unsupported protocol scheme".
	configErr error

	// rateLimitRemaining and rateLimitReset hold the last seen budget, for
	// callers that pace themselves.
	rateLimitRemaining int
	rateLimitReset     string
	sawRateLimit       bool
}

// Option configures a Client.
type Option func(*Client)

// WithUser sets the end-user identifier for multi-tenant attribution.
func WithUser(user string) Option { return func(c *Client) { c.User = user } }

// WithTimeout bounds each attempt.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.HTTPClient.Timeout = d }
}

// WithMaxRetries sets the number of extra attempts for 429/5xx and transport
// failures.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.MaxRetries = n
	}
}

// WithAuthScheme selects the header the key travels in.
func WithAuthScheme(scheme AuthScheme) Option {
	return func(c *Client) { c.AuthScheme = scheme }
}

// WithHTTPClient supplies the transport, for proxies, tracing or tests.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) { c.HTTPClient = client }
}

// New creates a client. baseURL is the server root; apiKey is a System API Key
// or User API Key from the Magnus dashboard.
func New(baseURL, apiKey string, opts ...Option) *Client {
	client := &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		MaxRetries: 2,
		AuthScheme: AuthBearer,
		HTTPClient: &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(client)
	}
	if client.BaseURL == "" {
		client.configErr = fmt.Errorf(
			"%w: BaseURL is required — pass the Magnus server root, e.g. "+
				"https://app.iamagnus.com (not the /v1 prefix)", ErrInvalidRequest)
	}
	return client
}

// SetUser sets the end-user identifier for multi-tenant mode.
func (c *Client) SetUser(user string) { c.User = user }

// RateLimit reports the last seen budget for this key and whether any response
// carried one.
func (c *Client) RateLimit() (remaining int, reset string, ok bool) {
	return c.rateLimitRemaining, c.rateLimitReset, c.sawRateLimit
}

// ---------------------------------------------------------------- plumbing

func (c *Client) newRequest(
	ctx context.Context, method, path string, body []byte, headers map[string]string,
) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("%w: building %s %s: %v", ErrConnection, method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "iamagnus-go/"+Version)
	if c.AuthScheme == AuthAPIKey {
		req.Header.Set("X-API-Key", c.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	// Set after the credential so a caller's extra header cannot drop it.
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req, nil
}

func (c *Client) noteRateLimit(resp *http.Response) {
	if raw := resp.Header.Get("X-RateLimit-Remaining"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			c.rateLimitRemaining = parsed
			c.sawRateLimit = true
		}
	}
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		c.rateLimitReset = reset
	}
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff is how long to wait before the next attempt. The server's own
// Retry-After wins when it sent one — guessing shorter just burns the next
// window too.
func backoff(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if raw := resp.Header.Get("Retry-After"); raw != "" {
			if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	base := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	// Jittered. Without jitter, every client that hit the same 429 retries in
	// the same instant.
	return time.Duration(float64(base) * (0.5 + rand.Float64()/2))
}

func wrapTransport(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fmt.Errorf(
			"%w: %s: %v (the turn may still have run; retry only with an Idempotency-Key)",
			ErrTimeout, what, err,
		)
	}
	return fmt.Errorf("%w: %s: %v", ErrConnection, what, err)
}

func isTimeout(err error) bool {
	var timeout interface{ Timeout() bool }
	return errors.As(err, &timeout) && timeout.Timeout()
}

// do runs one request, retrying when asked. The caller owns resp.Body.
func (c *Client) do(
	ctx context.Context, method, path string, body []byte,
	headers map[string]string, retry bool,
) (*http.Response, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	attempts := 1
	if retry {
		attempts = c.MaxRetries + 1
	}
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		req, err := c.newRequest(ctx, method, path, body, headers)
		if err != nil {
			return nil, err
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = wrapTransport(method+" "+c.BaseURL+path, err)
			if attempt < attempts-1 {
				if waitErr := sleepCtx(ctx, backoff(attempt, nil)); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, lastErr
		}

		c.noteRateLimit(resp)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		if retry && isRetryableStatus(resp.StatusCode) && attempt < attempts-1 {
			wait := backoff(attempt, resp)
			// Drained and closed, or the connection is not reused.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if waitErr := sleepCtx(ctx, wait); waitErr != nil {
				return nil, waitErr
			}
			continue
		}

		payload, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, errorFromResponse(resp.StatusCode, payload, resp.Header)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: %s %s: no attempt was made", ErrConnection, method, path)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrConnection, ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (c *Client) doJSON(
	ctx context.Context, method, path string, body []byte,
	headers map[string]string, retry bool, out any,
) error {
	resp, err := c.do(ctx, method, path, body, headers, retry)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return wrapTransport("reading "+path, err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%w: %s returned a body that is not the documented JSON: %v",
			ErrConnection, path, err)
	}
	return nil
}

// ------------------------------------------------------------------ health

// Health is a reachability probe: GET /api/health/simple, no key needed.
//
// Run it first when a setup is not working: it separates "wrong base URL" from
// "bad key", which otherwise both surface as a failure on the first chat call.
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, "/api/health/simple", nil, nil, true, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ------------------------------------------------------------------ models

// ListAgentsContext returns the agents (personas) this key can reach.
func (c *Client) ListAgentsContext(ctx context.Context) ([]Agent, error) {
	var out modelsListResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/models", nil, nil, true, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListAgents is ListAgentsContext with a background context.
func (c *Client) ListAgents() ([]Agent, error) {
	return c.ListAgentsContext(context.Background())
}

// GetAgentContext returns one agent, or nil when it does not exist for this key.
//
// A 404 here means "no such persona", which is an answer, not a failure.
func (c *Client) GetAgentContext(ctx context.Context, agentID string) (*Agent, error) {
	var agent Agent
	path := "/v1/models/" + url.PathEscape(agentID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, true, &agent); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &agent, nil
}

// GetAgent is GetAgentContext with a background context.
func (c *Client) GetAgent(agentID string) (*Agent, error) {
	return c.GetAgentContext(context.Background(), agentID)
}

// -------------------------------------------------------------------- chat

// ChatOpts carries the optional parameters of a turn.
type ChatOpts struct {
	// User overrides Client.User for this turn.
	User string
	// SessionID continues an existing conversation. Must be a UUID.
	SessionID string
	// IdempotencyKey makes the turn safe to retry: the server replays its first
	// response instead of running the pipeline again. It is also what allows
	// this client to retry a failed POST at all.
	IdempotencyKey string
	// IncludeUsage asks a streamed turn for the extra final chunk carrying usage.
	IncludeUsage bool
	// ExtraBody carries server fields newer than this SDK, merged into the
	// request as-is. Nothing here is validated — that is the point, and why it
	// is not the ordinary path.
	ExtraBody map[string]any
}

func (c *Client) buildRequest(
	agentID string, messages []ChatMessage, opts *ChatOpts, stream bool,
) ([]byte, error) {
	if opts == nil {
		opts = &ChatOpts{}
	}
	user := opts.User
	if user == "" {
		user = c.User
	}
	body := chatRequest{
		Model:    agentID,
		Messages: messages,
		User:     user,
		Stream:   stream,
	}
	if opts.SessionID != "" {
		if !uuidRe.MatchString(opts.SessionID) {
			// The server 400s on this; failing locally names the caller's own bug
			// instead of costing a round trip.
			return nil, fmt.Errorf(
				"%w: SessionID must be a UUID, got %q — Magnus rejects anything else "+
					"rather than silently starting a new conversation",
				ErrInvalidRequest, opts.SessionID,
			)
		}
		body.SessionID = opts.SessionID
	}
	if stream && opts.IncludeUsage {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	if len(opts.ExtraBody) == 0 {
		return json.Marshal(body)
	}

	// Merged through a map so an extra field can sit alongside the typed ones.
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range opts.ExtraBody {
		merged[key] = value
	}
	return json.Marshal(merged)
}

// ChatContext runs one turn and returns the whole OpenAI-shaped response.
//
// Response.Text() is the answer; Response.Magnus.SessionID is the conversation
// to carry into the next turn.
func (c *Client) ChatContext(
	ctx context.Context, agentID string, messages []ChatMessage, opts *ChatOpts,
) (*ChatResponse, error) {
	if opts == nil {
		opts = &ChatOpts{}
	}
	body, err := c.buildRequest(agentID, messages, opts, false)
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if opts.IdempotencyKey != "" {
		headers = map[string]string{"Idempotency-Key": opts.IdempotencyKey}
	}
	var out ChatResponse
	// Without an Idempotency-Key a retry runs the pipeline a second time.
	retry := opts.IdempotencyKey != ""
	if err := c.doJSON(
		ctx, http.MethodPost, "/v1/chat/completions", body, headers, retry, &out,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

// Chat runs one turn with a background context.
func (c *Client) Chat(agentID string, messages []ChatMessage, user string) (*ChatResponse, error) {
	return c.ChatContext(context.Background(), agentID, messages, &ChatOpts{User: user})
}

// StreamChat runs one turn, delivered as it is generated.
//
// The caller must Close the returned stream. No IdempotencyKey is accepted: a
// streamed body cannot be replayed, so the server releases the key and a retry
// re-runs the turn. Use ChatContext when a turn must not run twice.
func (c *Client) StreamChat(
	ctx context.Context, agentID string, messages []ChatMessage, opts *ChatOpts,
) (*ChatStream, error) {
	return c.streamChat(ctx, agentID, messages, opts, nil)
}

func (c *Client) streamChat(
	ctx context.Context, agentID string, messages []ChatMessage, opts *ChatOpts,
	onFinish func(*ChatStream),
) (*ChatStream, error) {
	body, err := c.buildRequest(agentID, messages, opts, true)
	if err != nil {
		return nil, err
	}
	// A stream has no idempotency key, so a retry would silently run the turn a
	// second time.
	resp, err := c.do(ctx, http.MethodPost, "/v1/chat/completions", body,
		map[string]string{"Accept": "text/event-stream"}, false)
	if err != nil {
		return nil, err
	}
	return newChatStream(resp.Body, onFinish), nil
}

// SendMessageOpts are the optional arguments of SendMessage.
type SendMessageOpts struct {
	// User overrides Client.User for this turn.
	User string
	// SessionID continues an existing conversation.
	SessionID string
	// IdempotencyKey makes the turn safe to retry.
	IdempotencyKey string
	// History is accepted for OpenAI-shaped call sites, but it does not restore
	// a thread — the server reads only the last user message. Use Conversation
	// for continuity.
	History []ChatMessage
}

// SendMessageContext sends one user message and returns the answer's text.
func (c *Client) SendMessageContext(
	ctx context.Context, agentID, content string, opts *SendMessageOpts,
) (string, error) {
	if opts == nil {
		opts = &SendMessageOpts{}
	}
	messages := make([]ChatMessage, 0, len(opts.History)+1)
	messages = append(messages, opts.History...)
	messages = append(messages, UserMessage(content))

	resp, err := c.ChatContext(ctx, agentID, messages, &ChatOpts{
		User:           opts.User,
		SessionID:      opts.SessionID,
		IdempotencyKey: opts.IdempotencyKey,
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%w: Magnus returned no choices for this turn", ErrServer)
	}
	return resp.Text(), nil
}

// SendMessage sends one user message with a background context.
func (c *Client) SendMessage(agentID, content string, opts *SendMessageOpts) (string, error) {
	return c.SendMessageContext(context.Background(), agentID, content, opts)
}

// ------------------------------------------------------------ conversation

// Conversation is a thread with an agent for one end user.
//
// The server reads only the last user message and keeps the conversation's
// memory and state server-side. The thread is (API key, user, agent), not the
// history a client resends; SessionID reports which session the server ran on.
type Conversation struct {
	client  *Client
	agentID string
	user    string

	// SessionID is the conversation the server last ran on.
	SessionID string
	// SessionSource is how the server decided it: "explicit", "derived" or "new".
	SessionSource string
	// LastTraceID and LastTurnID identify the last turn in the Magnus traces.
	LastTraceID string
	LastTurnID  string
	// LastUsage and LastUsageSource are the last turn's token accounting.
	LastUsage       *Usage
	LastUsageSource string
	// Handoff is true while a person from the team owns the conversation.
	Handoff bool
	// LastUpdateID is the id of the last reply from the team UpdatesContext
	// returned. An app that must not show a reply twice across restarts stores
	// it and sets it back on a new conversation.
	LastUpdateID string
}

// ConversationUpdatesContext returns one page of GET /v1/conversations/updates.
//
// The replies a person from the team wrote to user in the dashboard after the
// message after (or within the last 24 hours), and Handoff: whether a person
// owns the conversation now. A chat turn cannot carry these — they are written
// while the end user is not asking anything. Prefer Conversation.UpdatesContext
// and Conversation.FollowContext, which keep the cursor.
func (c *Client) ConversationUpdatesContext(
	ctx context.Context, agentID, user, after string,
) (*ConversationUpdates, error) {
	if user == "" {
		user = c.User
	}
	params := url.Values{"model": {agentID}}
	if user != "" {
		params.Set("user", user)
	}
	if after != "" {
		params.Set("after", after)
	}
	var out ConversationUpdates
	path := "/v1/conversations/updates?" + params.Encode()
	if err := c.doJSON(ctx, http.MethodGet, path, nil, nil, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Conversation opens a thread with an agent for one end user.
//
// Prefer it over SendMessage with History: the server keeps memory server-side
// and reads only the last user message. A thread is the end user, not resent
// history: there is one live thread per (API key, user, agent), and it ends
// after 30 idle minutes. Pass a user, or everyone calling through the key
// shares one thread.
//
// The conversation sends back the session id the server reports, but the
// server does not let a session id select, resume or reset a thread.
func (c *Client) Conversation(agentID, user string) *Conversation {
	if user == "" {
		user = c.User
	}
	return &Conversation{client: c, agentID: agentID, user: user}
}

// Resume opens a conversation that starts out holding sessionID.
//
// The server continues the end user's live thread whatever the session id, so
// this only matters when that user has no live thread with the agent.
func (c *Client) Resume(agentID, user, sessionID string) (*Conversation, error) {
	if !uuidRe.MatchString(sessionID) {
		return nil, fmt.Errorf("%w: sessionID must be a UUID, got %q",
			ErrInvalidRequest, sessionID)
	}
	conv := c.Conversation(agentID, user)
	conv.SessionID = sessionID
	return conv, nil
}

// AgentID is the agent this thread talks to.
func (cv *Conversation) AgentID() string { return cv.agentID }

// SendContext runs one turn and returns the answer's text.
func (cv *Conversation) SendContext(
	ctx context.Context, content string, opts *ChatOpts,
) (string, error) {
	merged := ChatOpts{User: cv.user, SessionID: cv.SessionID}
	if opts != nil {
		merged.IdempotencyKey = opts.IdempotencyKey
		merged.ExtraBody = opts.ExtraBody
		if opts.User != "" {
			merged.User = opts.User
		}
	}
	resp, err := cv.client.ChatContext(ctx, cv.agentID, []ChatMessage{UserMessage(content)}, &merged)
	if err != nil {
		return "", err
	}
	cv.adopt(resp.Magnus, resp.SessionID, resp.Usage)
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%w: Magnus returned no choices for this turn", ErrServer)
	}
	return resp.Text(), nil
}

// Send runs one turn with a background context.
func (cv *Conversation) Send(content string) (string, error) {
	return cv.SendContext(context.Background(), content, nil)
}

// StreamContext runs one turn, delivered as it is generated.
//
// The session is adopted when the stream finishes — including when it fails,
// since a turn that failed mid-stream still ran and still moved the
// conversation. The caller must Close the stream.
func (cv *Conversation) StreamContext(
	ctx context.Context, content string, opts *ChatOpts,
) (*ChatStream, error) {
	merged := ChatOpts{User: cv.user, SessionID: cv.SessionID}
	if opts != nil {
		merged.IncludeUsage = opts.IncludeUsage
		merged.ExtraBody = opts.ExtraBody
		if opts.User != "" {
			merged.User = opts.User
		}
	}
	return cv.client.streamChat(ctx, cv.agentID, []ChatMessage{UserMessage(content)}, &merged,
		func(stream *ChatStream) {
			extensions := stream.Magnus()
			cv.adopt(&extensions, stream.SessionID(), stream.Usage())
		})
}

// UpdatesContext returns the replies a person from the team wrote since the
// last call, oldest first, and refreshes Handoff. The operator is never named.
// The first call, with no LastUpdateID, returns the last 24 hours.
func (cv *Conversation) UpdatesContext(ctx context.Context) ([]OperatorMessage, error) {
	var messages []OperatorMessage
	for {
		page, err := cv.client.ConversationUpdatesContext(ctx, cv.agentID, cv.user, cv.LastUpdateID)
		if err != nil {
			return messages, err
		}
		messages = append(messages, page.Data...)
		if n := len(page.Data); n > 0 {
			cv.LastUpdateID = page.Data[n-1].ID
		}
		cv.Handoff = page.Handoff
		if !page.HasMore || len(page.Data) == 0 {
			return messages, nil
		}
	}
}

// FollowContext calls fn with each reply from the team as it arrives, polling
// every interval (5 s when zero), and returns nil once the conversation is back
// with the agent — at once when nobody had taken over. It stops with the error
// fn returns, or when ctx is done.
func (cv *Conversation) FollowContext(
	ctx context.Context, interval time.Duration, fn func(OperatorMessage) error,
) error {
	if interval == 0 {
		interval = 5 * time.Second
	}
	for {
		messages, err := cv.UpdatesContext(ctx)
		if err != nil {
			return err
		}
		for _, message := range messages {
			if err := fn(message); err != nil {
				return err
			}
		}
		if !cv.Handoff {
			return nil
		}
		if err := sleepCtx(ctx, interval); err != nil {
			return err
		}
	}
}

// Reset forgets the session id this conversation holds.
//
// The server still continues the end user's live thread: a new thread starts
// after 30 idle minutes, or with a different user.
func (cv *Conversation) Reset() {
	cv.SessionID = ""
	cv.SessionSource = ""
}

func (cv *Conversation) adopt(ext *Extensions, fallbackSessionID string, usage *Usage) {
	// The pipeline may roll the session mid-turn (persona, user or org change),
	// so the id the server says it ran on wins over the one sent.
	if ext != nil && ext.SessionID != "" {
		cv.SessionID = ext.SessionID
	} else if fallbackSessionID != "" {
		cv.SessionID = fallbackSessionID
	}
	if ext != nil {
		if ext.SessionSource != "" {
			cv.SessionSource = ext.SessionSource
		}
		cv.LastTraceID = ext.TraceID
		cv.LastTurnID = ext.TurnID
		cv.LastUsageSource = ext.UsageSource
	}
	// Every turn says it again; a turn without the field is not a handoff.
	cv.Handoff = ext != nil && ext.Handoff
	if usage != nil {
		cv.LastUsage = usage
	}
}
