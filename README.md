# Magnus SDK for Go

**English** · [Español](README.es.md)

The official Go client for **[Magnus Core](https://core.iamagnus.com)**:
governed AI agents behind an OpenAI-compatible API. The model understands and
writes; the agent's rules decide what happens and which actions wait for a
confirmation, and every turn leaves a trace.

```bash
go get github.com/ABZ-LABS/magnus-go-sdk
```

Go 1.22+. Standard library only. If `go get` fails, the module can be fetched
straight from GitHub or from a local copy: see
[Installing without the Go module proxy](#installing-without-the-go-module-proxy).

```go
package main

import (
	"context"
	"fmt"
	"log"

	magnus "github.com/ABZ-LABS/magnus-go-sdk"
)

func main() {
	client := magnus.New("https://app.iamagnus.com", "magnus_sys_...")
	ctx := context.Background()

	agents, err := client.ListAgentsContext(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// One thread per end user: `user` continues it, not resent history.
	chat := client.Conversation(agents[0].ID, "jane@company.com")

	answer, err := chat.SendContext(ctx, "Hi, what can you do?", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)

	followUp, _ := chat.SendContext(ctx, "And the price?", nil)
	fmt.Println(followUp)
}
```

## Installing without the Go module proxy

`go get` normally downloads through `proxy.golang.org` and checks the result
against `sum.golang.org`. Use this when either is out of reach. The import path
stays `github.com/ABZ-LABS/magnus-go-sdk` in every case.

**From GitHub.** `GOPRIVATE` makes Go skip both and clone the tag with `git`:

```bash
GOPRIVATE=github.com/ABZ-LABS/magnus-go-sdk go get github.com/ABZ-LABS/magnus-go-sdk@v0.2.0
```

`go env -w GOPRIVATE=github.com/ABZ-LABS/magnus-go-sdk` makes it permanent on
that machine. From then on `go.sum` pins the hash of what was downloaded. Pin a
tag, as above: `@main` follows the latest commit, which is not a release.

**Without network access to GitHub** (a closed CI, a customer's network). Copy
the module into the project and point the import path at the copy:

```bash
git clone --branch v0.2.0 https://github.com/ABZ-LABS/magnus-go-sdk third_party/magnus-go-sdk
go mod edit -replace github.com/ABZ-LABS/magnus-go-sdk=./third_party/magnus-go-sdk
go mod tidy
```

The module has no dependencies outside the standard library, so nothing else
is downloaded. `go mod vendor` on a machine with access is the other common
route.

## Getting an API key

1. In the Magnus dashboard, open **System API Keys**, under *Integration keys*
   in the sidebar. Organization admins see it.
2. **Create key**, and choose **which agent should answer**. A key is created
   for one agent and always answers as that agent: `ListAgentsContext` returns exactly
   that one, and naming another agent of your organization is refused with
   `model_not_allowed`. Keys for your own agents need a paid plan; the sample
   agents are open on every plan.
3. Under **What will use this key?**, keep **My app or backend**. That choice is
   stamped on every turn the key runs, so prefer one key per integration over
   one shared key.
4. **Copy it right away.** Magnus stores only a hash and shows the key once.

Keys start with `magnus_sys_` (`magnus_gpt_` for a key made for a chat client
such as OpenWebUI).

> **Not to be confused with "LLM API Keys".** That screen holds *your* OpenAI,
> Anthropic or other provider credentials, so that Magnus can call models on your
> behalf. They do not authenticate you against Magnus; using one here gives a
> 401.

## Pointing the client at a deployment

The base URL is configuration, not a constant: nothing about `iamagnus.com` is
built into the library. The hosted service is `https://app.iamagnus.com`, the
same address as the dashboard. Pass the **server root**, without `/v1` — the
client builds `/v1/...` itself, plus `/api/health/simple`, which lives outside
that prefix.

```go
hosted := magnus.New("https://app.iamagnus.com", "magnus_sys_...")
local := magnus.New("http://localhost:5001", "magnus_sys_...")

// In a deployment, read both from the environment:
client := magnus.New(os.Getenv("MAGNUS_BASE_URL"), os.Getenv("MAGNUS_API_KEY"))
```

A trailing slash is trimmed. `New` cannot return an error without breaking every
call site, so an empty URL is reported by the first call as `ErrInvalidRequest`,
naming what is missing, instead of as an unreadable transport error.

### Checking the URL and the key separately

```go
client.Health(ctx)            // no key involved: proves the URL is right
client.ListAgentsContext(ctx) // uses the key: proves the credential
```

If `Health` works and `ListAgentsContext` returns `ErrAuthentication`, the key is
the problem, not the URL — and the other way round.

## What is different from OpenAI

**The key picks the agent.** `model` does not choose who answers; the key's
agent does. Naming another agent of the organization is refused
(`model_not_allowed`), and any other value, such as `gpt-4o`, is ignored, so an
OpenAI client works unchanged.

**A thread is the end user, not the history.** The server reads only the last
user message and keeps the conversation's memory and state server-side, so
resending history restores nothing. There is one live thread per (API key,
`user`, agent): the same `user` continues it, and it ends after 30 idle
minutes. **Always pass `user`**: without it, everyone calling through the key
shares one thread. Each response reports the session the server ran on, but
sending a session id back cannot select, resume or reset a thread.

**The agent owns the turn.** `tools`, `tool_choice`, `functions`,
`function_call`, `response_format` and `n > 1` are *refused*, not ignored:
tools are configured per agent and the response format is the agent's decision.
`temperature`, `max_tokens`, `top_p`, `stop`, `seed` and `presence_penalty` are
accepted and ignored — the agent owns them too. Some OpenAI clients send
`tool_choice: "auto"` or `response_format: {"type": "text"}` by default; those
count as set and are refused, so strip them.

**Some limits answer 200.** When an end user, the organization or its plan runs
out of turns, the turn returns HTTP 200 with a sentence instead of an answer,
`usage_source: "estimated"` and no trace id, not a 429. The list is in
[CONTRACT.md](CONTRACT.md#limits-that-answer-200).

**A person can take over.** When the agent hands a conversation to someone on
your team, or they take it from the dashboard, the agent stops answering until
the team hands it back. Every turn still returns 200 — first the agent's
hand-off message, then a fixed notice — and `chat.Handoff` is `true` for as
long as a person is in charge. What the person writes is not the answer to any
turn, so this client fetches it — something an OpenAI client cannot do:

```go
if chat.Handoff {
	// Polls every 5 s (interval 0) and returns once the agent is back.
	err := chat.FollowContext(ctx, 0, func(m magnus.OperatorMessage) error {
		show(m.Content) // Author is always "human", never a name
		return nil
	})
}
```

`chat.UpdatesContext(ctx)` returns what is new without waiting, for your own
loop. To avoid showing a reply twice across restarts, store `chat.LastUpdateID`
and set it back on the new conversation.

**A streamed turn can fail after HTTP 200.** Once the first chunk is out the
status line cannot be taken back, so a failure arrives *inside* the stream. This
client surfaces it on `stream.Err()` as a `*StreamError` rather than handing back
a truncated answer as a success.

## Streaming

```go
stream, err := client.StreamChat(ctx, agentID,
	[]magnus.ChatMessage{magnus.UserMessage("Tell me more")}, nil)
if err != nil {
	log.Fatal(err)
}
defer stream.Close()

for stream.Next() {
	fmt.Print(stream.Delta())
}
if err := stream.Err(); err != nil {
	log.Fatal(err) // includes a turn that failed mid-stream
}

fmt.Println(stream.Text(), stream.SessionID(), stream.Magnus().UsageSource)
```

Two shapes are normal and both are handled: token by token, and a single delta
for a turn the server delivers whole.

`&magnus.ChatOpts{IncludeUsage: true}` adds the final chunk carrying `Usage()`.

## Errors

Every failure carries the server's error envelope. Match with `errors.Is`, read
the detail with `errors.As`:

```go
_, err := client.ChatContext(ctx, agentID, messages, nil)

switch {
case errors.Is(err, magnus.ErrRateLimit):
	var apiErr *magnus.APIError
	errors.As(err, &apiErr)
	if seconds, ok := apiErr.RetryAfter(); ok {
		time.Sleep(time.Duration(seconds) * time.Second)
	}
case errors.Is(err, magnus.ErrUnsupportedParameter):
	var apiErr *magnus.APIError
	errors.As(err, &apiErr)
	log.Printf("Magnus refuses %q", apiErr.Param)
case errors.Is(err, magnus.ErrAuthentication):
	log.Fatal("the API key is not accepted")
}
```

| Sentinel | Status |
|---|---|
| `ErrInvalidRequest` | 400, including `code: model_not_allowed` (the key belongs to another agent) |
| `ErrUnsupportedParameter` | 400, `code: unsupported_parameter` |
| `ErrAuthentication` | 401 |
| `ErrPermissionDenied` | 403, the key's organization is missing or deactivated |
| `ErrNotFound` | 404 |
| `ErrConflict` | 409, a turn with this `Idempotency-Key` is still running |
| `ErrRateLimit` | 429 |
| `ErrServer` | 5xx, including a mid-stream `server_error` |
| `ErrConnection` / `ErrTimeout` | never reached Magnus, or gave up waiting |

## Retries and idempotency

A turn advances the conversation and can run tools with side effects, so
retrying one blindly can duplicate them. This client therefore retries:

- **GET** always, on 429/5xx and transport failures;
- **POST** only when you supplied an `IdempotencyKey`, because the server then
  replays its first response instead of running the turn again;
- **never a stream** — a streamed body cannot be replayed.

`Retry-After` is honoured; otherwise the backoff is exponential with jitter.

```go
resp, err := client.ChatContext(ctx, agentID, messages, &magnus.ChatOpts{
	IdempotencyKey: uuid.NewString(), // any unique string; here github.com/google/uuid
})
```

Use a fresh UUID for each turn: the server matches the key across the whole
organization for 24 hours, without looking at the body or the end user.

## Metering

`resp.Usage` holds real provider token counts when
`resp.Magnus.UsageSource == "measured"`. `"estimated"` means the turn never
reached an LLM, which includes the limits that answer 200, and the numbers are a
`len(text)/4` heuristic. **Do not bill on an estimate.**

`client.RateLimit()` reports the last seen budget for the key.

## Multi-tenancy

`magnus.WithUser("jane@company.com")`, `client.SetUser`, or per call.
It sets the OpenAI `user` field, and it is what keeps your end users apart:
each value is one person, with their own thread and memory, and a call without
it lands in the one thread shared by everyone on the key. The part before an
`@` becomes the name the agent sees. Values are scoped to the key, so a new or
rotated key starts every person over.

## Verifying a deployment

`magnus-livecheck` runs the fifteen checks in [CONTRACT.md](CONTRACT.md)
against a real deployment and exits non-zero unless all of them pass:

```bash
go install github.com/ABZ-LABS/magnus-go-sdk/cmd/magnus-livecheck@latest

export MAGNUS_BASE_URL=https://app.iamagnus.com
export MAGNUS_API_KEY=magnus_sys_...   # a key created for a test agent

magnus-livecheck
```

Checks 5, 8, 9, 10 and 13 run real turns, which cost tokens and are recorded
like any other conversation. A key answers only as its own agent, so create the
key for a test agent.

## API

| | |
|---|---|
| `New(baseURL, apiKey, ...Option)` | `WithUser`, `WithTimeout`, `WithMaxRetries`, `WithAuthScheme`, `WithHTTPClient` |
| `Health(ctx)` | unauthenticated reachability probe |
| `ListAgentsContext(ctx)` / `GetAgentContext(ctx, id)` | agents; an unknown id is `nil, nil` |
| `ChatContext(ctx, agent, messages, *ChatOpts)` | one buffered turn |
| `StreamChat(ctx, agent, messages, *ChatOpts)` | one streamed turn |
| `SendMessageContext(ctx, agent, text, *SendMessageOpts)` | text in, text out |
| `Conversation(agent, user)` / `Resume(agent, user, sessionID)` | a thread for one end user; after each turn `LastTraceID`, `LastUsageSource` and `Handoff`; the team's replies with `UpdatesContext` and `FollowContext` |
| `ConversationUpdatesContext(ctx, agent, user, after)` | one page of the team's replies, raw |
| `RateLimit()` | last seen budget |

`ListAgents`, `GetAgent`, `Chat` and `SendMessage` are the same calls with a
background context. `ChatOpts.ExtraBody` forwards server fields newer than this
library. Every wire detail is in [CONTRACT.md](CONTRACT.md).

## Development

```bash
go test -race ./...
```

The suite runs against `mockmagnus`, a fake Magnus that implements
[CONTRACT.md](CONTRACT.md) over real sockets, so SSE framing and chunked
transfer are exercised for real. Releases are described in
[RELEASING.md](RELEASING.md).

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
