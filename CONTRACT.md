# Magnus `/v1` wire contract

The wire format the Magnus Core API speaks, and the single source of truth for
the Go, Node and Python SDKs.

Every SDK ships a mock server implementing exactly this document, and its unit
suite runs against that mock. If the API changes, this file changes first,
then the three mocks, and the suites fail until the clients catch up.

## Base URL and mounting

- The client is configured with the **server root** — `https://api.iamagnus.com`
  for the hosted service, or the root of another Magnus deployment. Not the `/v1`
  prefix: the health probe lives outside it.
- Chat endpoints live under `/v1`. The health probe lives under `/api`.

## Authentication

`Authorization: Bearer <key>` (default) or `X-API-Key: <key>`.

Accepted credentials: Magnus JWT, System API Key, User API Key. A missing or
unparseable credential is `401` with `code: null` / `code: "invalid_api_key"`.

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
  {"id": "magnus_standard", "object": "model", "created": 0,
   "owned_by": "magnus", "permission": [], "root": "magnus_standard", "parent": null}
]}
```

Scoped to the key's organisation. Models are Magnus **personas**, not LLMs.

## `GET /v1/models/{id}`

The model object, or `404` with `code: "model_not_found"`.
`magnus` is an alias for `magnus_standard`.

## `POST /v1/chat/completions`

### Request

| Field | Notes |
|---|---|
| `model` | persona id; defaults to `magnus_standard` |
| `messages` | **required**, list. Only the *last* message with `role: "user"` and non-empty text is read. |
| `user` | end-user identifier for multi-tenant attribution |
| `session_id` | Magnus extension. Must be a **UUID** or `400`. Also accepted as the `X-Magnus-Session-Id` header. |
| `stream` | `true` → `text/event-stream` |
| `stream_options.include_usage` | `true` → extra final chunk carrying `usage` |

`content` is either a string or a list of parts
(`[{"type": "text", "text": "..."}, {"type": "image_url", ...}]`); the text parts
are concatenated. A message whose only parts are images yields no text.

**History is not state.** The server keeps conversation state server-side and
reads only the last user message, so resending history does not restore a
thread — `session_id` does. Without one, continuity is derived from the caller's
last message within a server-configured time window, and is lost silently when
that window passes.

### Refused with `400` / `code: "unsupported_parameter"`

`tools`, `tool_choice`, `functions`, `function_call`, `response_format` when
non-empty, and `n` when not `1`. Magnus runs its own agent pipeline: tools are
configured per agent and the response format is the agent's decision.

Empty values (`{"tools": []}`, `{"response_format": {}}`, `{"n": 1}`) pass.

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
- `4xx` → cached (deterministic)
- **incompatible with `stream: true`** — a streamed body cannot be replayed, so
  the key is released and a retry re-runs the turn.

### Buffered response

```json
{
  "id": "chatcmpl-...", "object": "chat.completion", "created": 0,
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
    "usage_source": "measured" | "estimated"
  }
}
```

`magnus.session_id` is the conversation the server **actually ran on**. The
pipeline may roll the session mid-turn, so it can differ from what was sent, and
it — not the request value — is what the next turn must carry.

`usage_source` distinguishes real provider token counts (`measured`) from the
`len(text) // 4` fallback used by turns that never reached an LLM (`estimated`).
Anyone metering or billing off `usage` has to be able to tell them apart.

### Streamed response (`stream: true`)

`text/event-stream`, frames `data: <json>\n\n`, terminated by `data: [DONE]\n\n`.

1. an opening chunk, `delta: {"role": "assistant"}`
2. zero or more `delta: {"content": "..."}` chunks
3. a closing chunk, `finish_reason: "stop"`
4. optionally, when `include_usage` was asked for, a chunk with `choices: []`
   and `usage`
5. `[DONE]`

Two shapes are normal and a client must accept both: a turn that streamed token
by token, and a turn delivered as **one** delta (the server may send a turn whole instead of
streaming it).

**The Magnus extensions ride on whichever chunk carries them** — the first
content chunk on the buffered path, the closing chunk on the live path. Merge
them as they arrive; do not expect a fixed position.

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

A client that ignores it hands a truncated or empty answer to its caller as
though the turn had succeeded. **Every SDK must raise here.**

## Errors

```json
{"error": {"message": "...", "type": "...", "param": "..."|null, "code": "..."|null}}
```

| Status | `type` | `code` |
|---|---|---|
| 400 | `invalid_request_error` | `unsupported_parameter`, or `null` |
| 401 | `invalid_request_error` | `invalid_api_key` or `null` |
| 404 | `invalid_request_error` | `model_not_found` |
| 409 | `invalid_request_error` | `request_in_progress` |
| 429 | `rate_limit_error` | `rate_limit_exceeded` |
| 5xx | `server_error` | pipeline error code or `null` |

## Rate limiting

`POST /v1/chat/completions` is limited per API key (120 per window). Every
response carries `X-RateLimit-Remaining` and `X-RateLimit-Reset`; a `429` also
carries `Retry-After` in seconds when the reset time is known.

## Retry policy the SDKs implement

Retried: `429` and `5xx`, plus transport errors — on **GET always**, and on
`POST` only when the caller supplied an `Idempotency-Key`, because a bare retried
turn runs the pipeline twice and can duplicate side effects.

`Retry-After` is honoured when present; otherwise exponential backoff with
jitter. `4xx` other than `429` is never retried.

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
| 8 | two turns on one `Conversation` reuse the session | continuity, the thing history cannot do |
| 9 | a streamed turn yields text and closes cleanly | SSE parsing, both stream shapes |
| 10 | a streamed turn with `include_usage` reports usage | the final usage chunk |
| 11 | sending `tools` fails with `unsupported_parameter` naming `tools` | the typed error envelope |
| 12 | a non-UUID `session_id` is refused | client-side validation |
| 13 | one `Idempotency-Key` used twice returns the identical response id | replay, not a second turn |
| 14 | `X-RateLimit-Remaining` was seen on a response | the budget is observable |

Checks 5, 8, 9, 10 and 13 **run real turns** against the target agent, which
costs tokens and records real conversations. Point the livecheck at a test
agent, or accept the handful of turns.
