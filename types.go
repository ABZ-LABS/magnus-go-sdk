package magnus

// Wire types for the Magnus /v1 API. See CONTRACT.md.

// Agent is a persona, presented as an OpenAI "model".
type Agent struct {
	ID         string        `json:"id"`
	Object     string        `json:"object"`
	Created    int64         `json:"created,omitempty"`
	OwnedBy    string        `json:"owned_by,omitempty"`
	Permission []interface{} `json:"permission,omitempty"`
	Root       string        `json:"root,omitempty"`
	Parent     *string       `json:"parent,omitempty"`
}

type modelsListResponse struct {
	Object string  `json:"object"`
	Data   []Agent `json:"data"`
}

// ChatMessage is one message in a conversation.
//
// Content is `any` because the wire form is either a string or a list of
// multimodal parts. Use TextContent for the ordinary case.
type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// TextContent is a plain user message.
func TextContent(role, text string) ChatMessage {
	return ChatMessage{Role: role, Content: text}
}

// UserMessage is the message an ordinary turn sends.
func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: "user", Content: text}
}

// TextPart is the text half of a multimodal message.
func TextPart(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// ImagePart is an image in a multimodal message. Magnus reads no text from it;
// an image-only turn is a 400.
func ImagePart(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

// Usage is the token accounting for a turn.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Extensions are Magnus's non-standard additions to the OpenAI response.
type Extensions struct {
	// SessionID is the conversation the server actually ran on — carry this,
	// not what you sent.
	SessionID string `json:"session_id"`
	// SessionSource is "explicit", "derived" or "new".
	SessionSource string `json:"session_source"`
	// TraceID and TurnID identify this turn in the Magnus per-turn traces.
	TraceID string `json:"trace_id"`
	TurnID  string `json:"turn_id"`
	// UsageSource is "measured" (real provider token counts) or "estimated"
	// (the len/4 character heuristic, for turns that never reached an LLM).
	// Anyone metering or billing off Usage has to be able to tell them apart.
	UsageSource string `json:"usage_source"`
	// Handoff is true while a person from the team owns the conversation: the
	// turn where the agent hands off, and every turn after it, whose reply is a
	// fixed notice. A server older than the field omits it, which reads false.
	Handoff bool `json:"handoff"`
}

// ChatChoice is one choice in a chat response. Magnus always returns exactly
// one: a turn advances conversation state, so alternatives cannot exist for it.
type ChatChoice struct {
	Index   int `json:"index"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// ChatResponse is the answer to POST /v1/chat/completions.
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   *Usage       `json:"usage,omitempty"`
	// SessionID is echoed at the top level for OpenAI-shaped clients; Magnus
	// carries the same value plus more under Magnus.
	SessionID string      `json:"session_id,omitempty"`
	Magnus    *Extensions `json:"magnus,omitempty"`
}

// Text is the assistant's answer, or "" when the turn produced no choice.
func (r *ChatResponse) Text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}

// chatRequest is the wire body. Built through buildRequest so the unsupported
// parameters have no field to be set through by accident.
type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []ChatMessage  `json:"messages"`
	User          string         `json:"user,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}
