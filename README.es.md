# Magnus SDK para Go

[English](README.md) · **Español**

El cliente oficial de Go para **[Magnus Core](https://core.iamagnus.com)**:
agentes de IA gobernados detrás de una API compatible con OpenAI. El modelo
entiende y redacta; las reglas del agente deciden qué pasa y qué acciones
esperan una confirmación, y cada turno deja una traza.

```bash
go get github.com/ABZ-LABS/magnus-go-sdk
```

Go 1.22+. Solo la biblioteca estándar. Si `go get` falla, el módulo se puede
traer directo desde GitHub o desde una copia local: ver
[Instalar sin el proxy de módulos de Go](#instalar-sin-el-proxy-de-módulos-de-go).

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

	// Un hilo por usuario final: lo continúa `user`, no el historial reenviado.
	chat := client.Conversation(agents[0].ID, "jane@company.com")

	answer, err := chat.SendContext(ctx, "Hola, ¿qué puedes hacer?", nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)

	followUp, _ := chat.SendContext(ctx, "¿Y el precio?", nil)
	fmt.Println(followUp)
}
```

## Instalar sin el proxy de módulos de Go

`go get` normalmente descarga a través de `proxy.golang.org` y verifica el
resultado contra `sum.golang.org`. Usa esto cuando alguno de los dos no está
accesible. La ruta de import sigue siendo `github.com/ABZ-LABS/magnus-go-sdk`
en todos los casos.

**Desde GitHub.** `GOPRIVATE` hace que Go se salte los dos y clone el tag con
`git`:

```bash
GOPRIVATE=github.com/ABZ-LABS/magnus-go-sdk go get github.com/ABZ-LABS/magnus-go-sdk@v0.2.0
```

`go env -w GOPRIVATE=github.com/ABZ-LABS/magnus-go-sdk` lo deja permanente en
esa máquina. A partir de ahí `go.sum` fija el hash de lo que se descargó. Fija
un tag, como arriba: `@main` sigue al último commit, que no es una versión
publicada.

**Sin acceso de red a GitHub** (un CI cerrado, la red de un cliente). Copia el
módulo dentro del proyecto y apunta la ruta de import a la copia:

```bash
git clone --branch v0.2.0 https://github.com/ABZ-LABS/magnus-go-sdk third_party/magnus-go-sdk
go mod edit -replace github.com/ABZ-LABS/magnus-go-sdk=./third_party/magnus-go-sdk
go mod tidy
```

El módulo no tiene dependencias fuera de la biblioteca estándar, así que no se
descarga nada más. `go mod vendor` en una máquina con acceso es el otro camino
habitual.

## Obtener una API key

1. En el dashboard de Magnus, abre **System API Keys**, en *Integration keys*
   del menú lateral. La ven los administradores de la organización.
2. **Create key**, y elige **qué agente responde** (*Which agent should
   answer?*). Una key se crea para un agente y siempre responde como ese
   agente: `ListAgentsContext` devuelve exactamente ese, y nombrar otro agente
   de tu organización se rechaza con `model_not_allowed`. Las keys para tus
   propios agentes requieren un plan pago; los agentes de muestra están
   abiertos en todos los planes.
3. En **What will use this key?**, deja **My app or backend**. Esa elección
   queda registrada en cada turno que corre la key, así que conviene una key
   por integración en lugar de una compartida.
4. **Cópiala en el momento.** Magnus guarda solo un hash y muestra la key una
   sola vez.

Las keys empiezan con `magnus_sys_` (`magnus_gpt_` si se crearon para un
cliente de chat como OpenWebUI).

> **No confundir con "LLM API Keys".** Esa pantalla guarda *tus* credenciales
> de OpenAI, Anthropic u otro proveedor, para que Magnus llame a los modelos en
> tu nombre. No te autentican contra Magnus; usar una aquí da 401.

## Apuntar el cliente a un despliegue

La URL base es configuración, no una constante: la biblioteca no trae nada de
`iamagnus.com` incorporado. El servicio alojado es `https://app.iamagnus.com`,
la misma dirección que el dashboard. Pasa la **raíz del servidor**, sin `/v1`:
el cliente arma `/v1/...` por su cuenta, más `/api/health/simple`, que vive
fuera de ese prefijo.

```go
hosted := magnus.New("https://app.iamagnus.com", "magnus_sys_...")
local := magnus.New("http://localhost:5001", "magnus_sys_...")

// En un despliegue, lee las dos del entorno:
client := magnus.New(os.Getenv("MAGNUS_BASE_URL"), os.Getenv("MAGNUS_API_KEY"))
```

Una barra final se recorta. `New` no puede devolver un error sin romper cada
punto de llamada, así que una URL vacía la informa la primera llamada como
`ErrInvalidRequest`, nombrando lo que falta, en lugar de un error de transporte
ilegible.

### Probar la URL y la key por separado

```go
client.Health(ctx)            // sin key: prueba que la URL es correcta
client.ListAgentsContext(ctx) // usa la key: prueba la credencial
```

Si `Health` funciona y `ListAgentsContext` devuelve `ErrAuthentication`, el
problema es la key, no la URL, y viceversa.

## En qué se diferencia de OpenAI

**La key elige el agente.** `model` no decide quién responde; lo decide el
agente de la key. Nombrar otro agente de la organización se rechaza
(`model_not_allowed`), y cualquier otro valor, como `gpt-4o`, se ignora, así
que un cliente de OpenAI funciona sin cambios.

**Un hilo es el usuario final, no el historial.** El servidor lee solo el
último mensaje del usuario y guarda la memoria y el estado de la conversación
de su lado, así que reenviar el historial no restaura nada. Hay un hilo vivo
por (API key, `user`, agente): el mismo `user` lo continúa, y termina tras 30
minutos sin actividad. **Pasa siempre `user`**: sin él, todos los que llaman
con la key comparten un mismo hilo. Cada respuesta informa en qué sesión corrió
el servidor, pero devolverle un id de sesión no permite elegir, retomar ni
reiniciar un hilo.

**El agente es dueño del turno.** `tools`, `tool_choice`, `functions`,
`function_call`, `response_format` y `n > 1` se *rechazan*, no se ignoran: las
herramientas se configuran por agente y el formato de la respuesta lo decide el
agente. `temperature`, `max_tokens`, `top_p`, `stop`, `seed` y
`presence_penalty` se aceptan y se ignoran: también los maneja el agente.
Algunos clientes de OpenAI mandan `tool_choice: "auto"` o
`response_format: {"type": "text"}` por defecto; cuentan como definidos y se
rechazan, así que quítalos.

**Algunos límites responden 200.** Cuando un usuario final, la organización o
su plan se quedan sin turnos, el turno devuelve HTTP 200 con una frase en lugar
de una respuesta, `usage_source: "estimated"` y sin trace id; no un 429. La
lista está en [CONTRACT.es.md](CONTRACT.es.md#límites-que-responden-200).

**Una persona puede tomar la conversación.** Cuando el agente deriva a alguien
de tu equipo, o lo toman desde el panel, el agente deja de responder hasta que
el equipo se la devuelva. Cada turno sigue devolviendo 200 —primero el mensaje
de derivación del agente, después un aviso fijo— y `chat.Handoff` es `true`
mientras una persona esté a cargo. Lo que escribe la persona no es la
respuesta a ningún turno, así que este cliente lo trae —algo que un cliente de
OpenAI no puede hacer—:

```go
if chat.Handoff {
	// Consulta cada 5 s (intervalo 0) y vuelve cuando el agente retoma.
	err := chat.FollowContext(ctx, 0, func(m magnus.OperatorMessage) error {
		show(m.Content) // Author es siempre "human", nunca un nombre
		return nil
	})
}
```

`chat.UpdatesContext(ctx)` devuelve lo nuevo sin esperar, para tu propio
bucle. Para no mostrar una respuesta dos veces entre reinicios, guardá
`chat.LastUpdateID` y volvé a ponerlo en la conversación nueva.

**Un turno en streaming puede fallar después del HTTP 200.** Una vez que salió
el primer fragmento, la línea de estado ya no se puede cambiar, así que el
fallo llega *dentro* del stream. Este cliente lo expone en `stream.Err()` como
un `*StreamError` en lugar de entregar una respuesta truncada como si fuera un
éxito.

## Streaming

```go
stream, err := client.StreamChat(ctx, agentID,
	[]magnus.ChatMessage{magnus.UserMessage("Cuéntame más")}, nil)
if err != nil {
	log.Fatal(err)
}
defer stream.Close()

for stream.Next() {
	fmt.Print(stream.Delta())
}
if err := stream.Err(); err != nil {
	log.Fatal(err) // incluye un turno que falló a mitad del stream
}

fmt.Println(stream.Text(), stream.SessionID(), stream.Magnus().UsageSource)
```

Dos formas son normales y las dos se manejan: token por token, y un único delta
para un turno que el servidor entrega entero.

`&magnus.ChatOpts{IncludeUsage: true}` agrega el fragmento final con `Usage()`.

## Errores

Cada fallo trae el sobre de error del servidor. Compara con `errors.Is` y lee
el detalle con `errors.As`:

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
	log.Printf("Magnus rechaza %q", apiErr.Param)
case errors.Is(err, magnus.ErrAuthentication):
	log.Fatal("la API key no es aceptada")
}
```

| Centinela | Estado |
|---|---|
| `ErrInvalidRequest` | 400, incluido `code: model_not_allowed` (la key es de otro agente) |
| `ErrUnsupportedParameter` | 400, `code: unsupported_parameter` |
| `ErrAuthentication` | 401 |
| `ErrPermissionDenied` | 403, la organización de la key no existe o está desactivada |
| `ErrNotFound` | 404 |
| `ErrConflict` | 409, un turno con este `Idempotency-Key` sigue corriendo |
| `ErrRateLimit` | 429 |
| `ErrServer` | 5xx, incluido un `server_error` a mitad del stream |
| `ErrConnection` / `ErrTimeout` | nunca llegó a Magnus, o dejó de esperar |

## Reintentos e idempotencia

Un turno hace avanzar la conversación y puede correr herramientas con efectos,
así que reintentarlo a ciegas puede duplicarlos. Por eso este cliente
reintenta:

- **GET** siempre, ante 429/5xx y fallos de transporte;
- **POST** solo si pasaste un `IdempotencyKey`, porque entonces el servidor
  repite su primera respuesta en lugar de correr el turno otra vez;
- **nunca un stream**: un cuerpo en streaming no se puede repetir.

Se respeta `Retry-After`; si no viene, la espera crece exponencialmente con
variación aleatoria.

```go
resp, err := client.ChatContext(ctx, agentID, messages, &magnus.ChatOpts{
	IdempotencyKey: uuid.NewString(), // cualquier cadena única; aquí github.com/google/uuid
})
```

Usa un UUID nuevo en cada turno: el servidor compara la key en toda la
organización durante 24 horas, sin mirar el cuerpo ni el usuario final.

## Medición

`resp.Usage` trae los conteos reales de tokens del proveedor cuando
`resp.Magnus.UsageSource == "measured"`. `"estimated"` significa que el turno
nunca llegó a un LLM, lo que incluye los límites que responden 200, y los
números son una heurística de `len(text)/4`. **No factures sobre una
estimación.**

`client.RateLimit()` informa el último cupo visto para la key.

## Multi-tenencia

`magnus.WithUser("jane@company.com")`, `client.SetUser`, o por llamada. Define
el campo `user` de OpenAI, y es lo que separa a tus usuarios finales: cada
valor es una persona, con su propio hilo y su memoria, y una llamada sin él cae
en el único hilo que comparten todos los de la key. Lo que va antes de una `@`
pasa a ser el nombre que ve el agente. Los valores dependen de la key: una key
nueva o rotada hace empezar de cero a cada persona.

## Verificar un despliegue

`magnus-livecheck` corre los quince chequeos de [CONTRACT.es.md](CONTRACT.es.md)
contra un despliegue real y sale con un código distinto de cero salvo que pasen
todos:

```bash
go install github.com/ABZ-LABS/magnus-go-sdk/cmd/magnus-livecheck@latest

export MAGNUS_BASE_URL=https://app.iamagnus.com
export MAGNUS_API_KEY=magnus_sys_...   # una key creada para un agente de prueba

magnus-livecheck
```

Los chequeos 5, 8, 9, 10 y 13 corren turnos reales, que gastan tokens y quedan
registrados como cualquier conversación. Una key responde solo como su propio
agente, así que crea la key para un agente de prueba.

## API

| | |
|---|---|
| `New(baseURL, apiKey, ...Option)` | `WithUser`, `WithTimeout`, `WithMaxRetries`, `WithAuthScheme`, `WithHTTPClient` |
| `Health(ctx)` | prueba de alcance sin autenticación |
| `ListAgentsContext(ctx)` / `GetAgentContext(ctx, id)` | agentes; un id desconocido es `nil, nil` |
| `ChatContext(ctx, agent, messages, *ChatOpts)` | un turno completo |
| `StreamChat(ctx, agent, messages, *ChatOpts)` | un turno en streaming |
| `SendMessageContext(ctx, agent, text, *SendMessageOpts)` | entra texto, sale texto |
| `Conversation(agent, user)` / `Resume(agent, user, sessionID)` | un hilo para un usuario final; después de cada turno `LastTraceID`, `LastUsageSource` y `Handoff`; las respuestas del equipo con `UpdatesContext` y `FollowContext` |
| `ConversationUpdatesContext(ctx, agent, user, after)` | una página de respuestas del equipo, en crudo |
| `RateLimit()` | último cupo visto |

`ListAgents`, `GetAgent`, `Chat` y `SendMessage` son las mismas llamadas con un
contexto de fondo. `ChatOpts.ExtraBody` reenvía campos del servidor más nuevos
que esta biblioteca. Cada detalle del cable está en
[CONTRACT.es.md](CONTRACT.es.md).

## Desarrollo

```bash
go test -race ./...
```

La suite corre contra `mockmagnus`, un Magnus falso que implementa
[CONTRACT.md](CONTRACT.md) sobre sockets reales, así que el framing de SSE y la
transferencia por fragmentos se ejercitan de verdad. Las versiones se describen
en [RELEASING.es.md](RELEASING.es.md).

## Licencia

[Apache License 2.0](LICENSE). Ver [NOTICE](NOTICE) para la atribución.
