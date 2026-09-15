# Magnus SDK for Go

The official Go client for **[Magnus Core](https://core.iamagnus.com)**:
governed AI agents behind an OpenAI-compatible API. The model understands and
writes; rules decide what happens, anything irreversible gets confirmed first,
and every turn leaves a trace.

```bash
go get github.com/MeGrimlock/magnus-go-sdk
```

Go 1.22+. Standard library only.

```go
package main

import (
	"context"
	"fmt"
	"log"

	magnus "github.com/MeGrimlock/magnus-go-sdk"
)

func main() {
	client := magnus.New("https://api.iamagnus.com", "magnus_sys_...")
	ctx := context.Background()

	agents, err := client.ListAgentsContext(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// A thread: the session id continues it, not resent history.
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

## Getting an API key

1. In the Magnus dashboard, open **Configuration → System API Keys**.
2. Create a key with a descriptive name and the `api_generic` channel. The
   channel is stamped on every turn the key runs, so prefer one key per
   integration over one shared key.
3. **Copy it right away.** Magnus stores only a hash and shows the key once.

Keys start with `magnus_sys_`.

> **Not to be confused with "LLM API Keys".** That screen holds *your* OpenAI,
> Anthropic or other provider credentials, so that Magnus can call models on your
> behalf. They do not authenticate you against Magnus; using one here gives a
> 401.

## Pointing the client at a deployment

The base URL is configuration, not a constant: nothing about `iamagnus.com` is
built into the library. Pass the **server root**, without `/v1` — the client
builds `/v1/...` itself, plus `/api/health/simple`, which lives outside that
prefix.

```go
hosted := magnus.New("https://api.iamagnus.com", "magnus_sys_...")
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

## Three things that are not OpenAI

**History is not state.** The server reads only the last user message and keeps
the conversation's memory and state server-side. Resending history does not
restore a thread — a session id does. Use `Conversation`; without one, continuity
falls back to a time window and is lost silently when it expires.

**The agent owns the turn.** `tools`, `tool_choice`, `functions`,
`function_call`, `response_format` and `n > 1` are *refused*, not ignored:
tools are configured per agent and the response format is the agent's decision.
`temperature`, `max_tokens`, `top_p`, `stop`, `seed` and `presence_penalty` are
accepted and ignored — the agent owns them too.

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
| `ErrInvalidRequest` | 400 |
| `ErrUnsupportedParameter` | 400, `code: unsupported_parameter` |
| `ErrAuthentication` | 401 |
| `ErrPermissionDenied` | 403 |
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
	IdempotencyKey: uuid.NewString(), // any unique string
})
```

## Metering

`resp.Usage` holds real provider token counts when
`resp.Magnus.UsageSource == "measured"`. `"estimated"` means the turn never
reached an LLM and the numbers are a `len(text)/4` heuristic. **Do not bill on
an estimate.**

`client.RateLimit()` reports the last seen budget for the key.

## Multi-tenancy

`magnus.WithUser("jane@company.com")`, or `client.SetUser`, or per call. It sets
the OpenAI `user` field, which Magnus uses to attribute the turn to an end user
inside the API key's organization.

## Verifying a deployment

`magnus-livecheck` runs the fourteen checks in [CONTRACT.md](CONTRACT.md)
against a real deployment and exits non-zero unless all of them pass:

```bash
go install github.com/MeGrimlock/magnus-go-sdk/cmd/magnus-livecheck@latest

export MAGNUS_BASE_URL=https://api.iamagnus.com
export MAGNUS_API_KEY=magnus_sys_...
export MAGNUS_AGENT=magnus_standard   # optional: defaults to the first agent listed

magnus-livecheck
```

Checks 5, 8, 9, 10 and 13 run real turns, which cost tokens and are recorded
like any other conversation — point it at a test agent with `-agent`.

## API

| | |
|---|---|
| `New(baseURL, apiKey, ...Option)` | `WithUser`, `WithTimeout`, `WithMaxRetries`, `WithAuthScheme`, `WithHTTPClient` |
| `Health(ctx)` | unauthenticated reachability probe |
| `ListAgentsContext(ctx)` / `GetAgentContext(ctx, id)` | agents; an unknown id is `nil, nil` |
| `ChatContext(ctx, agent, messages, *ChatOpts)` | one buffered turn |
| `StreamChat(ctx, agent, messages, *ChatOpts)` | one streamed turn |
| `SendMessageContext(ctx, agent, text, *SendMessageOpts)` | text in, text out |
| `Conversation(agent, user)` / `Resume(agent, user, sessionID)` | a thread |
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
