# Contrato de cable de `/v1` de Magnus

[English](CONTRACT.md) · **Español**

El formato de cable que habla la API de Magnus Core, y la única fuente de verdad
para los SDKs de Go, Node y Python.

Cada SDK trae un servidor simulado que implementa este documento, y su suite
unitaria corre contra ese simulador. Si la API cambia, primero cambia este
archivo, después los tres simuladores, y las suites fallan hasta que los
clientes se ponen al día. Esta es una traducción de [CONTRACT.md](CONTRACT.md);
ante una diferencia, manda la versión en inglés.

## URL base y montaje

- El cliente se configura con la **raíz del servidor**:
  `https://app.iamagnus.com` para el servicio alojado (el mismo origen del
  dashboard, que reenvía tanto `/v1` como `/api`), o la raíz de otro despliegue
  de Magnus. No el prefijo `/v1`: la prueba de salud vive fuera de él.
- Los endpoints de chat viven bajo `/v1`. La prueba de salud vive bajo `/api`.

## Autenticación

`Authorization: Bearer <key>` (por defecto; el prefijo `Bearer ` distingue
mayúsculas) o `X-API-Key: <key>`.

Credenciales aceptadas:

- **System API Key**, creada en el dashboard. Una key se crea para **un
  agente** y siempre responde como ese agente. Las keys creadas antes de que
  existiera esa vinculación valen para toda la organización.
- User API Key.
- JWT de Magnus de tipo **access token**. Un refresh token se rechaza.

| Situación | Estado | `code` |
|---|---|---|
| sin credencial | `401` | `null` |
| una credencial que no se reconoce (incluida una System key de una organización desactivada) | `401` | `invalid_api_key` |
| una credencial reconocida sin organización | `403` | `no_organization` |
| una credencial reconocida cuya organización está desactivada | `403` | `organization_deactivated` |
| no se pudo leer el estado de la organización | `503` | `server_error` |

## `GET /api/health/simple` — sin autenticación

```json
{"status": "ok", "timestamp": "<iso8601>", "version": {...}}
```

Solo alcance. Prueba que el host y el prefijo de ruta son correctos *antes* de
que intervenga una key; por eso un fallo de autenticación y una URL base
equivocada dejan de parecerse.

## `GET /v1/models`

```json
{"object": "list", "data": [
  {"id": "magnus_standard", "object": "model", "created": 1767225600,
   "owned_by": "magnus", "permission": [], "root": "magnus_standard", "parent": null}
]}
```

Los modelos son **agentes** de Magnus (personas), no LLMs. Una key vinculada a
un agente lista exactamente ese agente. Una key de toda la organización o un
JWT lista los agentes activos de la organización más los globales de la
plataforma.

## `GET /v1/models/{id}`

El objeto del modelo, o `404` con `code: "model_not_found"` y `param: "model"`.
Con una key vinculada, todo id distinto de su agente es `404`. `magnus` es un
alias de `magnus_standard` cuando ese agente es visible para quien llama.

## `POST /v1/chat/completions`

Envía un cuerpo JSON con `Content-Type: application/json`.

### Petición

| Campo | Notas |
|---|---|
| `model` | Ver [Qué agente responde](#qué-agente-responde). |
| `messages` | **obligatorio**, lista. Solo se lee el *último* mensaje con `role: "user"` y texto no vacío. |
| `user` | **La clave de la conversación.** El mismo valor continúa el hilo de esa persona; ver [Hilos](#hilos). |
| `session_id` | Extensión de Magnus. Debe ser un **UUID** o da `400`. También se acepta como el header `X-Magnus-Session-Id`. No elige un hilo; ver [Hilos](#hilos). |
| `stream` | `true` → `text/event-stream` |
| `stream_options.include_usage` | `true` → un fragmento final extra con `usage` |

`content` es un string o una lista de partes
(`[{"type": "text", "text": "..."}, {"type": "image_url", ...}]`); las partes de
texto se unen. Un mensaje cuyas únicas partes son imágenes no tiene texto, y
eso es un `400`.

### Qué agente responde

- **Una key vinculada a un agente** siempre corre ese agente. Un `model` que
  nombra a *otro* agente de la organización se rechaza con `400` /
  `code: "model_not_allowed"` / `param: "model"`. Cualquier otro valor
  (`gpt-4o`, `magnus`, un string vacío) es una etiqueta de un cliente de OpenAI
  y se ignora.
- **Una key de toda la organización o un JWT**: un `model` omitido, `magnus` o
  `magnus_standard` corre el agente por defecto de la organización; otro id de
  agente corre ese agente. Un id que no corresponde a ningún agente no se
  rechaza: el turno corre en `magnus_standard`.

El `model` de la respuesta repite lo que nombró la petición, no necesariamente
quién respondió.

### Hilos

**El historial no es estado.** El servidor guarda la memoria y el estado de la
conversación de su lado y lee solo el último mensaje del usuario, así que
reenviar el historial no restaura nada.

Hay **un hilo vivo por (API key, `user`, agente)**. El mismo `user` lo
continúa; un `user` distinto es otra persona con otro hilo. Sin `user`, todos
los que llaman con la key son la misma persona y comparten un hilo. `user`
depende de la key: una key nueva o rotada hace empezar de cero a cada persona,
sin hilo y sin memoria.

Un hilo termina tras **30 minutos sin actividad** (un ajuste del servidor); el
turno siguiente abre uno nuevo.

`session_id` se valida y se devuelve, pero **no puede elegir, retomar ni
reiniciar un hilo**: el servidor continúa el hilo vivo de esa identidad y aun
así informa `session_source: "explicit"`. Solo tiene efecto cuando esa
identidad todavía no tiene un hilo con el agente.

### Rechazados con `400` / `code: "unsupported_parameter"`

`tools`, `tool_choice`, `functions`, `function_call`, `response_format` cuando
no están vacíos, y `n` cuando no es `1`. `param` nombra el campo. Magnus corre
su propio pipeline de agente: las herramientas se configuran por agente y el
formato de la respuesta lo decide el agente.

Los valores vacíos (`{"tools": []}`, `{"response_format": {}}`, `{"n": 1}`)
pasan. Algunos clientes de OpenAI mandan valores por defecto que cuentan como
no vacíos —`tool_choice: "auto"` o `"none"`, `response_format: {"type": "text"}`—
y se rechazan; quítalos.

### Ignorados en silencio

`temperature`, `max_tokens`, `top_p`, `stop`, `seed`, `presence_penalty`: los
maneja el pipeline. Enviarlos no es un error.

### Header `Idempotency-Key`

Un turno hace avanzar la conversación y puede correr herramientas con efectos,
así que un reintento tras un timeout tiene que repetir la respuesta, no volver
a correr el turno.

- primera llamada → corre, la respuesta se guarda
- repetición → la respuesta guardada, sin correr un turno
- todavía en curso → `409` / `code: "request_in_progress"`
- `5xx` → se libera la key, un reintento corre otra vez
- un turno que falló con un `4xx` → queda guardado (es determinista)
- **incompatible con `stream: true`**: un cuerpo en streaming no se puede
  repetir, así que la key se libera y un reintento vuelve a correr el turno.
  Una key que ya tiene una respuesta guardada devuelve ese JSON, aun con
  `stream: true`.

La key vale para **toda la organización durante 24 horas** y se compara solo
por el header: no por el cuerpo, la API key ni `user`. Usa un UUID nuevo por
turno lógico y nunca reutilices uno entre usuarios finales.

### Respuesta completa

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

`magnus.session_id` es el hilo en el que el servidor **realmente corrió**. El
pipeline puede rotar la sesión a mitad del turno, así que puede diferir de lo
enviado. En un rechazo o un error repite el id al que resolvió la petición.

`usage_source` distingue los conteos reales de tokens del proveedor
(`measured`) del cálculo de respaldo `len(text) // 4` que usan los turnos que
nunca llegaron a un LLM (`estimated`), lo que incluye los rechazos de abajo.
Quien mida o facture a partir de `usage` tiene que poder distinguirlos.

`handoff` es `true` mientras una persona del equipo está a cargo de la
conversación: en el turno en que el agente deriva, cuyo mensaje es del propio
agente, y en todos los turnos siguientes, hasta que el panel le devuelva la
conversación al agente o pasen 24 horas. En esos turnos siguientes el agente no
corre: el mensaje es un aviso fijo, `usage_source` es `"estimated"` y
`trace_id` es `null`. Un servidor anterior a este campo no lo manda; un
`handoff` ausente se lee como `false`. Las respuestas que escribe la persona se
piden con [`GET /v1/conversations/updates`](#get-v1conversationsupdates).

### Límites que responden `200`

Algunos límites no responden `429`. El turno devuelve `200` con una frase como
mensaje del asistente, `usage_source: "estimated"` y `trace_id: null`:

- turnos por usuario final por hora (100 por defecto; sin `user`, toda la key
  comparte ese cupo);
- turnos por organización por hora, según el plan;
- el presupuesto mensual de LLM;
- turnos simultáneos por organización;
- la plataforma descartando carga.

### Respuesta en streaming (`stream: true`)

`text/event-stream`, tramas `data: <json>\n\n`, terminado por
`data: [DONE]\n\n`.

1. un fragmento de apertura, `delta: {"role": "assistant"}`
2. cero o más fragmentos `delta: {"content": "..."}`
3. un fragmento de cierre, `finish_reason: "stop"`
4. opcionalmente, si se pidió `include_usage`, un fragmento con `choices: []` y
   `usage`
5. `[DONE]`

Dos formas son normales y un cliente tiene que aceptar ambas: un turno que se
transmitió token por token, y un turno entregado como **un solo** delta de
contenido (el servidor puede mandar un turno entero en lugar de transmitirlo).

**Las extensiones de Magnus viajan en el fragmento que las lleve**: el único
fragmento de contenido cuando el turno llega entero, el fragmento de cierre
cuando se transmitió token por token. Combínalas a medida que llegan; no
esperes una posición fija.

#### El fallo que parece un éxito

Cuando un turno falla *después* de abrirse el stream, el estado HTTP ya es
`200` y no se puede cambiar. Por eso el fallo llega **dentro** del stream: un
fragmento de cierre que lleva a la vez `finish_reason: "stop"` y un objeto
`error`.

```json
{"id": "...", "object": "chat.completion.chunk", "choices": [
   {"index": 0, "delta": {}, "finish_reason": "stop"}],
 "error": {"message": "Internal server error.", "type": "server_error",
           "param": null, "code": null}}
```

`type` es `server_error`, o `invalid_request_error` para un error de turno por
debajo de `500`; `code` es `null` o el código de error del turno. Después de un
error no llegan ni el fragmento de uso ni las extensiones de Magnus.

Un cliente que lo ignore le entrega a quien lo llamó una respuesta truncada o
vacía como si el turno hubiera salido bien. **Todo SDK tiene que lanzar un
error aquí.**

## `GET /v1/conversations/updates`

Lo que la app todavía no vio de la conversación de un usuario final: las
respuestas que una persona del equipo escribió en el panel, y si una persona
está a cargo de la conversación ahora. Un turno de chat no puede llevarlas —se
escriben mientras el usuario final no está preguntando nada—, así que el
cliente las pide. Es un endpoint propio de Magnus, no parte de la superficie de
OpenAI. Mismas credenciales que el chat.

| Query | Notas |
|---|---|
| `user` | El mismo valor que mandan los turnos de chat. Sin él, el hilo único y compartido de la key. |
| `after` | El `id` del último mensaje que el cliente ya tiene. Sin él, las respuestas de las últimas 24 horas (lo máximo que dura una derivación). |
| `model` | De qué agente informar la derivación, como en un turno de chat. Una key atada a un agente lo ignora. |

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

- `data` va del más viejo al más nuevo, como mucho 50 por página; con
  `has_more: true`, se pide de nuevo con el último `id` como `after`. Dos
  mensajes escritos en el mismo instante igual se paginan en un orden fijo.
- `author` es siempre `"human"`: el operador nunca se nombra.
- `handoff` es el mismo indicador que un turno de chat lleva en
  `magnus.handoff`. Cuando pasa a `false` el agente vuelve a responder; un
  cliente que consultaba respuestas puede dejar de hacerlo.
- Un `after` que no es un mensaje de este usuario final es un `400` con
  `code: "invalid_cursor"` y `param: "after"`.
- No corre ningún turno ni modelo. Tiene su propio cupo (ver
  [Límite de peticiones](#límite-de-peticiones)): consultar cada pocos segundos
  mientras `handoff` sea `true`, y no en otro caso.

## Errores

```json
{"error": {"message": "...", "type": "...", "param": "..."|null, "code": "..."|null}}
```

| Estado | `type` | `code` |
|---|---|---|
| 400 | `invalid_request_error` | `unsupported_parameter`, `model_not_allowed`, `invalid_cursor`, un código de error del turno, o `null` |
| 401 | `invalid_request_error` | `invalid_api_key` o `null` |
| 403 | `invalid_request_error` | `no_organization`, `organization_deactivated` |
| 404 | `invalid_request_error` | `model_not_found` |
| 409 | `invalid_request_error` | `request_in_progress` |
| 429 | `rate_limit_error` | `rate_limit_exceeded` |
| 5xx | `server_error` | `internal`, `server_error`, un código de error del pipeline, o `null` |

## Límite de peticiones

`POST /v1/chat/completions` está limitado a **120 peticiones por ventana fija de
una hora**, contada desde la primera petición: por System API Key, y por usuario
para JWTs y User API Keys. Las repeticiones idempotentes cuentan.

`GET /v1/conversations/updates` tiene un cupo propio: **7200 peticiones por
ventana** por key, así consultar nunca le come turnos al chat.

Las respuestas que pasaron la autenticación llevan `X-RateLimit-Remaining` y
`X-RateLimit-Reset` (una fecha ISO-8601). Un `429` lleva además `Retry-After` en
segundos cuando se conoce la hora de reinicio. Un `401` o `403` no lleva
ninguno.

## Política de reintentos que implementan los SDKs

Se reintentan: `429` y `5xx`, más los errores de transporte; en **GET
siempre**, y en `POST` solo cuando quien llama pasó un `Idempotency-Key`,
porque un turno reintentado sin él corre el pipeline dos veces y puede duplicar
efectos.

Se respeta `Retry-After` cuando viene; si no, espera exponencial con variación
aleatoria. Un `4xx` distinto de `429` nunca se reintenta.

## Entradas reservadas

- Un último mensaje del usuario que empieza con `### Task:` se saltea el
  agente: la lista de mensajes va a una llamada directa a un LLM, sin hilo, y
  `usage_source` es `"none"`. Los front ends de chat los mandan para títulos y
  etiquetas.
- Un mensaje que empieza con `/behavior` es un comando de depuración.
- Mientras una persona tomó una conversación, `/v1` responde `200` con un
  aviso fijo y `magnus.handoff: true` (ver
  [Respuesta completa](#respuesta-completa)). Sólo el equipo se la devuelve al
  agente: `reset`, `/bot` y `/auto` son mensajes comunes.

## Límites conocidos

- Un cuerpo que no es JSON devuelve `500`, no `400`.
- Navegadores: el preflight de CORS del origen alojado no permite los headers
  `Idempotency-Key` ni `X-Magnus-Session-Id`. Llama a `/v1` desde un servidor.

---

# Apéndice: la lista de chequeos en vivo

Cada SDK trae un `livecheck` que corre estos quince chequeos contra un
despliegue real y sale con un código distinto de cero ante el primer fallo.
Están numerados para que una luz verde signifique lo mismo en Go, Node y
Python, y para que un fallo se pueda informar como "falló el chequeo 9" sin
pegar un log.

| # | Chequeo | Prueba |
|---|---|---|
| 1 | `GET /api/health/simple` responde `status: ok` | URL base y prefijo de ruta |
| 2 | `GET /v1/models` devuelve al menos un agente | la key es aceptada |
| 3 | `GET /v1/models/{id}` devuelve ese agente | consulta de un modelo |
| 4 | un id de modelo inventado vuelve como ausente, no como error | el 404 se maneja como respuesta |
| 5 | un turno completo devuelve texto no vacío | el pipeline del agente corre |
| 6 | la respuesta lleva `magnus.session_id` y `session_source` | las extensiones sobreviven |
| 7 | `usage.total_tokens > 0` y `usage_source` presente | la medición está conectada |
| 8 | dos turnos en una `Conversation` quedan en una sesión | la continuidad, lo que el historial no logra |
| 9 | un turno en streaming entrega texto y cierra limpio | parseo de SSE, las dos formas de stream |
| 10 | un turno en streaming con `include_usage` informa el uso | el fragmento final de uso |
| 11 | mandar `tools` falla con `unsupported_parameter` nombrando `tools` | el sobre de error tipado |
| 12 | un `session_id` que no es UUID se rechaza | validación del lado del cliente |
| 13 | un `Idempotency-Key` usado dos veces devuelve el mismo id de respuesta | repetición, no un segundo turno |
| 14 | se vio `X-RateLimit-Remaining` en una respuesta | el cupo es observable |
| 15 | `GET /v1/conversations/updates` responde una página con `data` y `handoff` | las respuestas del equipo llegan al SDK |

Los chequeos 5, 8, 9, 10 y 13 **corren turnos reales** contra el agente
objetivo, lo que gasta tokens y registra conversaciones reales. Córrelos con
una key creada para un agente de prueba: una key responde solo como su propio
agente.
