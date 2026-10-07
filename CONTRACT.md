# Magnus `/v1` wire contract

**English** · [Español](CONTRACT.es.md)

The wire format the Magnus Core API speaks, and the single source of truth for
the Go, Node and Python SDKs.

Every SDK ships a mock server implementing this document, and its unit suite
runs against that mock. If the API changes, this file changes first, then the
three mocks, and the suites fail until the clients catch up.

## Base URL and mounting

- The client is configured with the **server root**: `https://app.iamagnus.com`
  for the hosted service (the dashboard's own origin, which forwards both `/v1`
  and `/api`), or the root of another Magnus deployment. Not the `/v1` prefix:
  the health probe lives outside it.
- Chat endpoints live under `/v1`. The health probe lives under `/api`.

## Authentication

`Authorization: Bearer <key>` (default; the `Bearer ` prefix is case-sensitive)
or `X-API-Key: <key>`.

Accepted credentials:

- **System API Key**, created in the dashboard. A key is created for **one
  agent** and always answers as that agent. Keys created before agent binding
  existed are org-wide.
- User API Key.
- Magnus **access-token** JWT. A refresh token is refused.

| Situation | Status | `code` |
|---|---|---|
| no credential | `401` | `null` |
| a credential that is not recognised (including a System key of a deactivated organization) | `401` | `invalid_api_key` |
| a recognised credential with no organization | `403` | `no_organization` |
| a recognised credential whose organization is deactivated | `403` | `organization_deactivated` |
| the organization's status could not be read | `503` | `server_error` |

## `GET /api/health/simple` — unauthenticated

```json
{"status": "ok", "timestamp": "<iso8601>", "version": {...}}
```

Reachability only. It proves the host and the path prefix are right *before* a
key is involved, which is why an auth failure and a wrong base URL stop looking
alike.

## `GET /v1/models`

```json
{"object": "list", "data": [
  {"id": "magnus_standard", "object": "model", "created": 1767225600,
   "owned_by": "magnus", "permission": [], "root": "magnus_standard", "parent": null}
]}
```

Models are Magnus **agents** (personas), not LLMs. A key bound to an agent
lists exactly that agent. An org-wide key or a JWT lists the organization's
active agents plus the platform's global ones.

## `GET /v1/models/{id}`

The model object, or `404` with `code: "model_not_found"` and `param: "model"`.
With a bound key, every id other than its agent is `404`. `magnus` is an alias
for `magnus_standard` when that agent is visible to the caller.

## `POST /v1/chat/completions`

Send a JSON body with `Content-Type: application/json`.

### Request

| Field | Notes |
|---|---|
| `model` | See [Which agent answers](#which-agent-answers). |
| `messages` | **required**, list. Only the *last* message with `role: "user"` and non-empty text is read. |
| `user` | **The conversation key.** The same value continues that person's thread; see [Threads](#threads). |
| `session_id` | Magnus extension. Must be a **UUID** or `400`. Also accepted as the `X-Magnus-Session-Id` header. It does not select a thread; see [Threads](#threads). |
| `stream` | `true` → `text/event-stream` |
| `stream_options.include_usage` | `true` → extra final chunk carrying `usage` |

`content` is either a string or a list of parts
(`[{"type": "text", "text": "..."}, {"type": "image_url", ...}]`); the text parts
are joined. A message whose only parts are images has no text, which is a
`400`.

### Which agent answers

- **A key bound to an agent** always runs that agent. A `model` naming a
  *different* agent of the organization is refused with `400` /
  `code: "model_not_allowed"` / `param: "model"`. Any other value (`gpt-4o`,
  `magnus`, an empty string) is a label from an OpenAI client and is ignored.
- **An org-wide key or a JWT**: an omitted `model`, `magnus` or
  `magnus_standard` runs the organization's default agent; another agent id
  runs that agent. An id that matches no agent is not rejected: the turn runs
  on `magnus_standard`.

The response's `model` echoes what the request named, not necessarily who
answered.

### Threads

**History is not state.** The server keeps the conversation's memory and state
server-side and reads only the last user message, so resending history does not
restore anything.

There is **one live thread per (API key, `user`, agent)**. The same `user`
continues it; a different `user` is a different person with a different thread.
Without `user`, everyone calling through the key is the same person and shares
one thread. `user` is scoped to the key: a new or rotated key starts every
person over, with no thread and no memory.

A thread ends after **30 idle minutes** (a server setting); the next turn
starts a new one.

`session_id` is validated and echoed, but it **cannot select, resume or reset a
thread**: the server continues the identity's live thread and still reports
`session_source: "explicit"`. It only takes effect when that identity has no
thread with the agent yet.

### Refused with `400` / `code: "unsupported_parameter"`

`tools`, `tool_choice`, `functions`, `function_call`, `response_format` when
non-empty, and `n` when not `1`. `param` names the field. Magnus runs its own
agent pipeline: tools are configured per agent and the response format is the
agent's decision.

Empty values (`{"tools": []}`, `{"response_format": {}}`, `{"n": 1}`) pass. Some
OpenAI clients send defaults that count as non-empty —
`tool_choice: "auto"` or `"none"`, `response_format: {"type": "text"}` — and are
refused; strip them.

### Silently ignored

`temperature`, `max_tokens`, `top_p`, `stop`, `seed`, `presence_penalty` — the
pipeline owns them. Sending them is not an error.

### `Idempotency-Key` header

A turn advances the conversation and can run tools with side effects, so a retry
after a timeout must replay rather than re-run.

- first call → runs, response is stored
- repeat → the stored response, without running a turn
- still in flight → `409` / `code: "request_in_progress"`
- `5xx` → key released, a retry runs again
- a turn that failed with a `4xx` → cached (deterministic)
- **incompatible with `stream: true`** — a streamed body cannot be replayed, so
  the key is released and a retry re-runs the turn. A key that already holds a
  stored response returns that JSON, even with `stream: true`.

The key is scoped to the **organization for 24 hours** and matched on the
header alone — not on the body, the API key or `user`. Use a fresh UUID per
logical turn and never reuse one across end users.

### Buffered response

```json
{
  "id": "chatcmpl-...", "object": "chat.completion", "created": 1767225600,
  "model": "magnus_standard",
  "choices": [{"index": 0,
               "message": {"role": "assistant", "content": "..."},
               "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
  "session_id": "...",
  "magnus": {
    "session_id": "...",
    "session_source": "explicit" | "derived" | "new",
    "trace_id": "..." | null,
    "turn_id": "..." | null,
    "usage_source": "measured" | "estimated",
    "handoff": true | false
  }
}
```

`magnus.session_id` is the thread the server **actually ran on**. The pipeline
may roll the session mid-turn, so it can differ from what was sent. On a
refusal or an error it echoes the id the request resolved to.

`usage_source` distinguishes real provider token counts (`measured`) from the
`len(text) // 4` fallback used by turns that never reached an LLM (`estimated`),
which includes the refusals below. Anyone metering or billing off `usage` has to
be able to tell them apart.

`handoff` is `true` while a person from the team owns the conversation: on the
turn where the agent hands off, whose message is the agent's own, and on every
turn after it, until the dashboard hands the conversation back to the agent or
24 hours pass. On those later turns the agent does not run: the message is a
fixed notice, `usage_source` is `"estimated"` and `trace_id` is `null`. A
server older than this field omits it; read a missing `handoff` as `false`.
The replies the person writes are fetched with
[`GET /v1/conversations/updates`](#get-v1conversationsupdates).

### Limits that answer `200`

Some limits do not answer `429`. The turn returns `200` with a sentence as the
assistant message, `usage_source: "estimated"` and `trace_id: null`:

- turns per end user per hour (100 by default; without `user`, the whole key
  shares this budget);
- turns per organization per hour, set by the plan;
- the monthly LLM budget;
- concurrent turns per organization;
- the platform shedding load.

### Streamed response (`stream: true`)

`text/event-stream`, frames `data: <json>\n\n`, terminated by `data: [DONE]\n\n`.

1. an opening chunk, `delta: {"role": "assistant"}`
2. zero or more `delta: {"content": "..."}` chunks
3. a closing chunk, `finish_reason: "stop"`
4. optionally, when `include_usage` was asked for, a chunk with `choices: []`
   and `usage`
5. `[DONE]`

Two shapes are normal and a client must accept both: a turn that streamed token
by token, and a turn delivered as **one** content delta (the server may send a
turn whole instead of streaming it).

**The Magnus extensions ride on whichever chunk carries them**: the single
content chunk when the turn arrives whole, the closing chunk when it streamed
token by token. Merge them as they arrive; do not expect a fixed position.

#### The failure that looks like success

When a turn fails *after* the stream opened, the HTTP status is already `200`
and cannot be taken back. The failure therefore arrives **inside** the stream:
a closing chunk that carries both `finish_reason: "stop"` and an `error` object.

```json
{"id": "...", "object": "chat.completion.chunk", "choices": [
   {"index": 0, "delta": {}, "finish_reason": "stop"}],
 "error": {"message": "Internal server error.", "type": "server_error",
           "param": null, "code": null}}
```

`type` is `server_error`, or `invalid_request_error` for a turn error below
`500`; `code` is `null` or the turn's error code. No usage chunk and no Magnus
extensions follow an error.

A client that ignores it hands a truncated or empty answer to its caller as
though the turn had succeeded. **Every SDK must raise here.**

## `GET /v1/conversations/updates`

What the app has not seen of one end user's conversation: the replies a person
from the team wrote in the dashboard, and whether a person owns the
conversation now. A chat turn cannot carry them — they are written while the
end user is not asking anything — so a client fetches them. This is Magnus's own
endpoint, not part of the OpenAI surface. Same credentials as the chat.

| Query | Notes |
|---|---|
| `user` | The same value the chat turns send. Without it, the key's one shared thread. |
| `after` | The `id` of the last message the client already has. Without it, the replies of the last 24 hours (the longest a handoff lasts). |
| `model` | Which agent's handoff to report, as in a chat turn. A key bound to an agent ignores it. |

```json
{
  "object": "list",
  "handoff": true,
  "data": [
    {"id": "...", "object": "conversation.message", "author": "human",
     "content": "...", "created": 1767225600}
  ],
  "has_more": false
}
```

- `data` is oldest first, at most 50 per page; with `has_more: true`, ask again
  with the last `id` as `after`. Two messages written in the same instant still
  page in a fixed order.
- `author` is always `"human"`: the operator is never named.
- `handoff` is the same flag a chat turn carries in `magnus.handoff`. When it
  turns `false` the agent answers again; a client polling for replies can stop.
- An `after` that is not a message of this end user is a `400` with
  `code: "invalid_cursor"` and `param: "after"`.
- It runs no turn and no model. It has its own rate bucket (see
  [Rate limiting](#rate-limiting)): poll every few seconds while `handoff` is
  `true`, and not otherwise.

## Errors

```json
{"error": {"message": "...", "type": "...", "param": "..."|null, "code": "..."|null}}
```

| Status | `type` | `code` |
|---|---|---|
| 400 | `invalid_request_error` | `unsupported_parameter`, `model_not_allowed`, `invalid_cursor`, a turn error code, or `null` |
| 401 | `invalid_request_error` | `invalid_api_key` or `null` |
| 403 | `invalid_request_error` | `no_organization`, `organization_deactivated` |
| 404 | `invalid_request_error` | `model_not_found` |
| 409 | `invalid_request_error` | `request_in_progress` |
| 429 | `rate_limit_error` | `rate_limit_exceeded` |
| 5xx | `server_error` | `internal`, `server_error`, a pipeline error code, or `null` |

## Rate limiting

`POST /v1/chat/completions` is limited to **120 requests per fixed one-hour
window**, counted from the first request: per System API Key, and per user for
JWTs and User API Keys. Idempotent replays count.

`GET /v1/conversations/updates` has a bucket of its own: **7200 requests per
window** per key, so polling never eats into the chat's turns.

Responses that passed authentication carry `X-RateLimit-Remaining` and
`X-RateLimit-Reset` (an ISO-8601 timestamp). A `429` also carries `Retry-After`
in seconds when the reset time is known. A `401` or `403` carries neither.

## Retry policy the SDKs implement

Retried: `429` and `5xx`, plus transport errors — on **GET always**, and on
`POST` only when the caller supplied an `Idempotency-Key`, because a bare retried
turn runs the pipeline twice and can duplicate side effects.

`Retry-After` is honoured when present; otherwise exponential backoff with
jitter. `4xx` other than `429` is never retried.

## Reserved inputs

- A last user message that starts with `### Task:` skips the agent: the message
  list goes to a plain LLM call, with no thread, and `usage_source` is `"none"`.
  Chat front ends send these for titles and tags.
- A message that starts with `/behavior` is a debug command.
- While a person has taken over a conversation, `/v1` answers `200` with a
  fixed notice and `magnus.handoff: true` (see
  [Buffered response](#buffered-response)). Only the team hands it back to the
  agent: `reset`, `/bot` and `/auto` are ordinary messages.

## Known limits

- A body that is not JSON returns `500`, not `400`.
- Browsers: the hosted origin's CORS preflight does not allow the
  `Idempotency-Key` or `X-Magnus-Session-Id` headers. Call `/v1` from a
  server.

---

# Appendix: the live checklist

Each SDK ships a `livecheck` that runs these fourteen checks against a real
deployment and exits non-zero on the first failure. They are numbered so that a
green light means the same thing in Go, Node and Python, and so a failure can be
reported as "check 9 failed" without pasting a log.

| # | Check | Proves |
|---|---|---|
| 1 | `GET /api/health/simple` answers `status: ok` | base URL and path prefix |
| 2 | `GET /v1/models` returns at least one agent | the key is accepted |
| 3 | `GET /v1/models/{id}` returns that agent | single-model lookup |
| 4 | an invented model id comes back absent, not as an error | 404 is handled as an answer |
| 5 | a buffered turn returns non-empty text | the agent pipeline runs |
| 6 | the response carries `magnus.session_id` and `session_source` | the extensions survive |
| 7 | `usage.total_tokens > 0` and `usage_source` is set | metering is wired |
| 8 | two turns on one `Conversation` stay on one session | continuity, the thing history cannot do |
| 9 | a streamed turn yields text and closes cleanly | SSE parsing, both stream shapes |
| 10 | a streamed turn with `include_usage` reports usage | the final usage chunk |
| 11 | sending `tools` fails with `unsupported_parameter` naming `tools` | the typed error envelope |
| 12 | a non-UUID `session_id` is refused | client-side validation |
| 13 | one `Idempotency-Key` used twice returns the identical response id | replay, not a second turn |
| 14 | `X-RateLimit-Remaining` was seen on a response | the budget is observable |

Checks 5, 8, 9, 10 and 13 **run real turns** against the target agent, which
costs tokens and records real conversations. Run them with a key created for a
test agent: a key answers only as its own agent.
