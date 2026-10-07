// Package livecheck runs the CONTRACT.md checklist against a real Magnus.
//
// It exits non-zero unless all fifteen checks pass. Maintainers run it before
// every release; run it yourself to verify a deployment and a key.
// Checks 5, 8, 9, 10 and 13 run real turns against the target agent - they cost
// tokens and are recorded like any other conversation.
package livecheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	magnus "github.com/ABZ-LABS/magnus-go-sdk"
)

// Written as escapes rather than literals so the source stays plain ASCII.
const (
	esc    = "\u001b"
	green  = esc + "[32m"
	red    = esc + "[31m"
	yellow = esc + "[33m"
	dim    = esc + "[2m"
	reset  = esc + "[0m"
)

// Options configure a run.
type Options struct {
	BaseURL string
	APIKey  string
	// Agent is the persona to test against. Empty means the first one listed.
	Agent   string
	Prompt  string
	Timeout time.Duration
	// Color turns the ANSI markers off for CI logs and tests.
	Color bool
	// Out receives the report. Defaults to os.Stdout in the command.
	Out io.Writer
}

type check struct {
	number int
	name   string
	// run returns a problem description, or "" when the check passed.
	run func() string
}

// Run executes the checklist and returns the process exit code: 0 when every
// check passed, 1 otherwise.
func Run(ctx context.Context, opts Options) int {
	paint := func(text, color string) string {
		if !opts.Color {
			return text
		}
		return color + text + reset
	}
	say := func(format string, args ...any) {
		fmt.Fprintf(opts.Out, format+"\n", args...)
	}

	prompt := opts.Prompt
	if prompt == "" {
		prompt = "Hi, what can you do?"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 90 * time.Second
	}
	client := magnus.New(opts.BaseURL, opts.APIKey,
		magnus.WithTimeout(timeout), magnus.WithMaxRetries(1))

	var (
		agentID     = opts.Agent
		response    *magnus.ChatResponse
		usageSource string
		streamShape string
		continuity  string
		budget      = -1
	)

	checks := []check{
		{1, "health probe answers", func() string {
			body, err := client.Health(ctx)
			if err != nil {
				return err.Error()
			}
			if body["status"] != "ok" {
				return fmt.Sprintf("health said %v, expected \"ok\"", body["status"])
			}
			return ""
		}},
		{2, "the key can list agents", func() string {
			agents, err := client.ListAgentsContext(ctx)
			if err != nil {
				return err.Error()
			}
			if len(agents) == 0 {
				return "no agents visible to this key (wrong org, or none configured)"
			}
			ids := make([]string, 0, len(agents))
			for _, agent := range agents {
				ids = append(ids, agent.ID)
			}
			if agentID == "" {
				agentID = ids[0]
				return ""
			}
			for _, id := range ids {
				if id == agentID {
					return ""
				}
			}
			return fmt.Sprintf("agent %q is not in this key's list: %s",
				agentID, strings.Join(ids, ", "))
		}},
		{3, "a single agent can be retrieved", func() string {
			agent, err := client.GetAgentContext(ctx, agentID)
			if err != nil {
				return err.Error()
			}
			if agent == nil || agent.ID != agentID {
				return fmt.Sprintf("GET /v1/models/%s did not return that agent", agentID)
			}
			return ""
		}},
		{4, "an unknown agent is absent, not an error", func() string {
			agent, err := client.GetAgentContext(ctx, "no_such_agent_livecheck")
			if err != nil {
				return err.Error()
			}
			if agent != nil {
				return "an invented model id came back as if it existed"
			}
			return ""
		}},
		{5, "a buffered turn returns text", func() string {
			resp, err := client.ChatContext(ctx, agentID,
				[]magnus.ChatMessage{magnus.UserMessage(prompt)}, nil)
			if err != nil {
				return err.Error()
			}
			response = resp
			if resp.Text() == "" {
				return "the turn returned no text"
			}
			return ""
		}},
		{6, "the magnus extensions survive", func() string {
			if response == nil || response.Magnus == nil {
				return "response carried no magnus block"
			}
			if response.Magnus.SessionID == "" {
				return "response carried no magnus.session_id"
			}
			if response.Magnus.SessionSource == "" {
				return "response carried no magnus.session_source"
			}
			return ""
		}},
		{7, "usage is reported with its provenance", func() string {
			if response == nil || response.Usage == nil || response.Usage.TotalTokens == 0 {
				return "usage.total_tokens was zero or absent"
			}
			source := ""
			if response.Magnus != nil {
				source = response.Magnus.UsageSource
			}
			if source != "measured" && source != "estimated" {
				return fmt.Sprintf("magnus.usage_source was %q", source)
			}
			usageSource = source
			return ""
		}},
		{8, "a conversation keeps its session", func() string {
			chat := client.Conversation(agentID, "")
			if _, err := chat.SendContext(ctx, prompt, nil); err != nil {
				return err.Error()
			}
			first := chat.SessionID
			if first == "" {
				return "the first turn produced no session id"
			}
			if _, err := chat.SendContext(ctx, "¿Y algo más?", nil); err != nil {
				return err.Error()
			}
			if chat.SessionID == "" {
				return "the session was lost on the second turn"
			}
			continuity = fmt.Sprintf("%s... -> %s...", short(first), short(chat.SessionID))
			return ""
		}},
		{9, "a streamed turn parses and closes", func() string {
			stream, err := client.StreamChat(ctx, agentID,
				[]magnus.ChatMessage{magnus.UserMessage(prompt)}, nil)
			if err != nil {
				return err.Error()
			}
			defer stream.Close()
			pieces := 0
			for stream.Next() {
				pieces++
			}
			if err := stream.Err(); err != nil {
				return err.Error()
			}
			if stream.Text() == "" {
				return "the streamed turn produced no text"
			}
			if stream.SessionID() == "" {
				return "the stream carried no session id"
			}
			if pieces > 1 {
				streamShape = "token by token"
			} else {
				streamShape = "single delta"
			}
			return ""
		}},
		{10, "include_usage reports usage", func() string {
			stream, err := client.StreamChat(ctx, agentID,
				[]magnus.ChatMessage{magnus.UserMessage(prompt)},
				&magnus.ChatOpts{IncludeUsage: true})
			if err != nil {
				return err.Error()
			}
			defer stream.Close()
			if _, err := stream.Collect(); err != nil {
				return err.Error()
			}
			if stream.Usage() == nil || stream.Usage().TotalTokens == 0 {
				return "include_usage produced no usage"
			}
			return ""
		}},
		{11, "'tools' is refused by name", func() string {
			// Sent via ExtraBody: this check is about the *server's* refusal, and
			// the typed surface deliberately has no way to pass tools.
			_, err := client.ChatContext(ctx, agentID,
				[]magnus.ChatMessage{magnus.UserMessage(prompt)},
				&magnus.ChatOpts{ExtraBody: map[string]any{
					"tools": []any{map[string]any{
						"type":     "function",
						"function": map[string]any{"name": "f", "parameters": map[string]any{}},
					}},
				}})
			if err == nil {
				return "the server accepted 'tools', which it is documented to refuse"
			}
			if !errors.Is(err, magnus.ErrUnsupportedParameter) {
				return fmt.Sprintf("expected an unsupported_parameter error, got %v", err)
			}
			var apiErr *magnus.APIError
			if errors.As(err, &apiErr) && apiErr.Param != "tools" {
				return fmt.Sprintf("refused, but named param=%q instead of \"tools\"", apiErr.Param)
			}
			return ""
		}},
		{12, "a non-UUID session_id is refused locally", func() string {
			_, err := client.ChatContext(ctx, agentID,
				[]magnus.ChatMessage{magnus.UserMessage(prompt)},
				&magnus.ChatOpts{SessionID: "not-a-uuid"})
			if err == nil {
				return "a non-UUID SessionID was not refused"
			}
			if !errors.Is(err, magnus.ErrInvalidRequest) {
				return fmt.Sprintf("refused, but as %v", err)
			}
			return ""
		}},
		{13, "one idempotency key runs one turn", func() string {
			key := fmt.Sprintf("livecheck-%d", time.Now().UnixNano())
			messages := []magnus.ChatMessage{magnus.UserMessage(prompt)}
			opts := &magnus.ChatOpts{IdempotencyKey: key}
			first, err := client.ChatContext(ctx, agentID, messages, opts)
			if err != nil {
				return err.Error()
			}
			second, err := client.ChatContext(ctx, agentID, messages, opts)
			if err != nil {
				return err.Error()
			}
			if first.ID != second.ID {
				return fmt.Sprintf("the same Idempotency-Key ran two turns (%s vs %s)",
					first.ID, second.ID)
			}
			return ""
		}},
		{14, "the rate-limit budget is observable", func() string {
			remaining, _, ok := client.RateLimit()
			if !ok {
				return "no X-RateLimit-Remaining header was seen on any response"
			}
			budget = remaining
			return ""
		}},
		{15, "the team's replies can be fetched", func() string {
			// A server older than the endpoint answers 404, and the SDK's
			// UpdatesContext/FollowContext would fail for every user of this release.
			user := fmt.Sprintf("livecheck-%d", time.Now().UnixNano())
			page, err := client.ConversationUpdatesContext(ctx, agentID, user, "")
			if err != nil {
				return fmt.Sprintf("GET /v1/conversations/updates failed: %v", err)
			}
			if page.Object != "list" {
				return fmt.Sprintf("GET /v1/conversations/updates answered object %q, not a list", page.Object)
			}
			return ""
		}},
	}

	say("magnus-go-sdk %s -> %s", magnus.Version, opts.BaseURL)
	say("%s\n", paint("checks 5, 8, 9, 10 and 13 run real turns and cost tokens.", yellow))

	failures := 0
	for _, c := range checks {
		started := time.Now()
		problem := c.run()
		elapsed := fmt.Sprintf("%dms", time.Since(started).Milliseconds())
		mark := paint("PASS", green)
		if problem != "" {
			mark = paint("FAIL", red)
			failures++
		}
		say("  %s %2d. %s %s", mark, c.number, c.name, paint(elapsed, dim))
		if problem != "" {
			say("        %s", paint(problem, red))
		}
	}

	say("")
	if agentID != "" {
		say("  agent          %s", agentID)
	}
	if streamShape != "" {
		say("  stream shape   %s", streamShape)
	}
	if usageSource != "" {
		note := ""
		if usageSource != "measured" {
			note = "  (character heuristic - do not bill on this)"
		}
		say("  usage source   %s%s", usageSource, note)
	}
	if continuity != "" {
		say("  session        %s", continuity)
	}
	if budget >= 0 {
		say("  budget left    %d", budget)
	}
	say("")

	if failures > 0 {
		say("%s", paint(fmt.Sprintf(
			"%d of %d checks failed - this deployment does not meet the contract.", failures, len(checks)), red))
		return 1
	}
	say("%s", paint(fmt.Sprintf(
		"all %d checks passed - this deployment meets the contract.", len(checks)), green))
	return 0
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
