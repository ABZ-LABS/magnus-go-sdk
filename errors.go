package magnus

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Sentinels for errors.Is. The server distinguishes these cases; a caller that
// only ever saw "magnus api: 400 Bad Request" could not act on any of them.
var (
	// ErrAuthentication is a 401: no key, or a key Magnus does not accept.
	ErrAuthentication = errors.New("magnus: authentication failed")
	// ErrPermissionDenied is a 403: the key is valid but not for this.
	ErrPermissionDenied = errors.New("magnus: permission denied")
	// ErrNotFound is a 404: no such agent, or no access to it.
	ErrNotFound = errors.New("magnus: not found")
	// ErrInvalidRequest is a 400: the request cannot be honoured as written.
	ErrInvalidRequest = errors.New("magnus: invalid request")
	// ErrUnsupportedParameter is a 400 whose code is "unsupported_parameter".
	//
	// Magnus runs its own agent pipeline: tools are configured per agent and the
	// response format is the agent's decision, so tools, tool_choice, functions,
	// function_call, response_format and n > 1 are refused rather than silently
	// ignored. APIError.Param names the offender.
	ErrUnsupportedParameter = errors.New("magnus: unsupported parameter")
	// ErrConflict is a 409: a turn with this Idempotency-Key is still running.
	ErrConflict = errors.New("magnus: request already in progress")
	// ErrRateLimit is a 429: the key's window is exhausted. See APIError.RetryAfter.
	ErrRateLimit = errors.New("magnus: rate limited")
	// ErrServer is a 5xx: Magnus failed to process the turn.
	ErrServer = errors.New("magnus: server error")
	// ErrConnection means the request never reached Magnus, or the connection
	// died mid-flight.
	ErrConnection = errors.New("magnus: connection failed")
	// ErrTimeout means the request was still open when the timeout expired.
	//
	// A timeout is not an answer: the turn may well have run. Retrying it
	// without an Idempotency-Key can run the pipeline a second time and
	// duplicate whatever side effects its tools have.
	ErrTimeout = errors.New("magnus: request timed out")
)

// APIError is a failure Magnus described in its error envelope:
//
//	{"error": {"message": ..., "type": ..., "param": ..., "code": ...}}
//
// Every field survives onto the error, so "you sent a parameter Magnus refuses"
// and "your key expired" stop looking identical.
type APIError struct {
	Status  int
	Message string
	Type    string
	Code    string
	Param   string
	Header  http.Header
	// Body is the raw response, for a failure that was not an envelope at all.
	Body string
}

func (e *APIError) Error() string {
	parts := []string{strconv.Itoa(e.Status)}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Param != "" {
		parts = append(parts, "param="+e.Param)
	}
	return fmt.Sprintf("[%s] %s", strings.Join(parts, " "), e.Message)
}

// Is lets errors.Is match an APIError against the sentinels above.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnsupportedParameter:
		return e.Code == "unsupported_parameter"
	case ErrInvalidRequest:
		return e.Status == http.StatusBadRequest
	case ErrAuthentication:
		return e.Status == http.StatusUnauthorized
	case ErrPermissionDenied:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrConflict:
		return e.Status == http.StatusConflict
	case ErrRateLimit:
		return e.Status == http.StatusTooManyRequests
	case ErrServer:
		return e.Status >= 500
	}
	return false
}

// RetryAfter is the number of seconds the server asked us to wait, and whether
// it said at all.
func (e *APIError) RetryAfter() (int, bool) {
	raw := e.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		// The HTTP-date form. Honouring it would need a clock the caller has no
		// reason to trust; the backoff handles it instead.
		return 0, false
	}
	return seconds, true
}

// StreamError is a turn that failed after the stream had already opened.
//
// The status line went out as 200 with the first chunk and cannot be taken
// back, so the failure arrives inside the stream instead. Whatever was streamed
// before it is kept on PartialText — it is what the reader has already seen —
// but the turn did not succeed.
type StreamError struct {
	Message     string
	Type        string
	Code        string
	Param       string
	PartialText string
}

func (e *StreamError) Error() string {
	label := e.Code
	if label == "" {
		label = e.Type
	}
	if label == "" {
		label = "error"
	}
	return fmt.Sprintf("[stream %s] %s", label, e.Message)
}

// Is lets a mid-stream server failure match ErrServer, as its buffered
// counterpart does.
func (e *StreamError) Is(target error) bool {
	return target == ErrServer && e.Type == "server_error"
}

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// errorFromResponse builds an APIError from a failed HTTP response body.
func errorFromResponse(status int, body []byte, header http.Header) *APIError {
	apiErr := &APIError{Status: status, Header: header, Body: string(body)}

	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		apiErr.Message = envelope.Error.Message
		apiErr.Type = envelope.Error.Type
		apiErr.Param = envelope.Error.Param
		apiErr.Code = envelope.Error.Code
		return apiErr
	}

	// Not an envelope at all — a proxy error page, or HTML from the wrong host.
	// Keep a slice of it: "502" alone never located anything.
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	apiErr.Message = fmt.Sprintf("Magnus returned %d.", status)
	if snippet != "" {
		apiErr.Message += " " + snippet
	}
	return apiErr
}
