package magnus

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// streamChunk is one SSE frame of a streamed turn.
type streamChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage     *Usage      `json:"usage"`
	SessionID string      `json:"session_id"`
	Magnus    *Extensions `json:"magnus"`
	Error     *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ChatStream is a turn arriving as it is generated.
//
// Drive it with Next, read each piece with Delta, and check Err when Next
// returns false. Once it is exhausted, Text, Usage, SessionID and Magnus hold
// the same values a buffered response carries.
//
//	stream, err := client.StreamChat(ctx, agent, msgs, nil)
//	if err != nil { return err }
//	defer stream.Close()
//	for stream.Next() {
//	    fmt.Print(stream.Delta())
//	}
//	if err := stream.Err(); err != nil { return err }
//
// Two shapes are normal and both are handled: a turn that streamed token by
// token, and a turn delivered as one delta because the server did
// not stream it.
type ChatStream struct {
	body    io.ReadCloser
	scanner *bufio.Scanner

	delta string
	text  string
	err   error
	done  bool

	usage        *Usage
	sessionID    string
	magnus       Extensions
	id           string
	model        string
	finishReason string

	onFinish func(*ChatStream)
	finished bool
}

func newChatStream(body io.ReadCloser, onFinish func(*ChatStream)) *ChatStream {
	scanner := bufio.NewScanner(body)
	// A turn delivered whole can be one long frame; the 64KB default would
	// truncate it into a parse failure that looks like a dropped token.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &ChatStream{body: body, scanner: scanner, onFinish: onFinish}
}

// Next advances to the next text delta, returning false when the turn is over
// or has failed. Check Err afterwards.
func (s *ChatStream) Next() bool {
	if s.done {
		return false
	}
	for s.scanner.Scan() {
		line := strings.TrimSpace(s.scanner.Text())
		// Blank lines separate frames and ":" lines are keep-alives.
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			s.finish()
			return false
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A frame we cannot parse is not worth failing a turn over, but
			// silently dropping it would hide a server-side format change.
			continue
		}

		if chunk.ID != "" {
			s.id = chunk.ID
		}
		if chunk.Model != "" {
			s.model = chunk.Model
		}
		// Extensions can be on any chunk — the first content chunk when the turn
		// was buffered, the closing chunk when it streamed — so they are merged
		// as they arrive rather than read from a fixed position.
		if chunk.SessionID != "" {
			s.sessionID = chunk.SessionID
		}
		if chunk.Magnus != nil {
			mergeExtensions(&s.magnus, chunk.Magnus)
			if chunk.Magnus.SessionID != "" {
				s.sessionID = chunk.Magnus.SessionID
			}
		}
		if chunk.Usage != nil {
			s.usage = chunk.Usage
		}

		// A turn that failed after the stream opened. Reported as an error, not
		// as a normal end: the alternative is handing back a truncated answer as
		// a success.
		if chunk.Error != nil {
			s.err = &StreamError{
				Message:     chunk.Error.Message,
				Type:        chunk.Error.Type,
				Code:        chunk.Error.Code,
				Param:       chunk.Error.Param,
				PartialText: s.text,
			}
			s.finish()
			return false
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				s.finishReason = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				s.delta = choice.Delta.Content
				s.text += choice.Delta.Content
				return true
			}
		}
	}

	if err := s.scanner.Err(); err != nil {
		s.err = wrapTransport("reading the stream", err)
	}
	s.finish()
	return false
}

// Delta is the piece of text the last Next produced.
func (s *ChatStream) Delta() string { return s.delta }

// Text is everything delivered so far, and the whole answer once the stream ends.
func (s *ChatStream) Text() string { return s.text }

// Err is why the stream stopped, or nil if it ended normally.
func (s *ChatStream) Err() error { return s.err }

// Usage is the token accounting, present only when include_usage was requested.
func (s *ChatStream) Usage() *Usage { return s.usage }

// SessionID is the conversation the server ran on.
func (s *ChatStream) SessionID() string { return s.sessionID }

// Magnus is the extension block merged across every chunk that carried part of it.
func (s *ChatStream) Magnus() Extensions { return s.magnus }

// FinishReason is the reason the turn ended, once it has.
func (s *ChatStream) FinishReason() string { return s.finishReason }

// Collect drains the stream and returns the whole answer.
func (s *ChatStream) Collect() (string, error) {
	for s.Next() {
	}
	return s.text, s.err
}

// Close releases the connection. Safe to call more than once, and safe to call
// on a stream that was abandoned part-way.
func (s *ChatStream) Close() error {
	s.finish()
	return nil
}

func (s *ChatStream) finish() {
	if s.finished {
		return
	}
	s.finished = true
	s.done = true
	_ = s.body.Close()
	if s.onFinish != nil {
		s.onFinish(s)
	}
}

// mergeExtensions fills in fields the incoming chunk actually carried, so a
// later chunk cannot blank what an earlier one established.
func mergeExtensions(dst *Extensions, src *Extensions) {
	if src.SessionID != "" {
		dst.SessionID = src.SessionID
	}
	if src.SessionSource != "" {
		dst.SessionSource = src.SessionSource
	}
	if src.TraceID != "" {
		dst.TraceID = src.TraceID
	}
	if src.TurnID != "" {
		dst.TurnID = src.TurnID
	}
	if src.UsageSource != "" {
		dst.UsageSource = src.UsageSource
	}
}
