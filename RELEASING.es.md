# Publicar una versión

[English](RELEASING.md) · **Español**

Un módulo de Go se publica subiendo un tag semver: el proxy de módulos de Go lo
descarga de GitHub y [pkg.go.dev](https://pkg.go.dev/github.com/ABZ-LABS/magnus-go-sdk)
lo indexa. No hay nada que subir. El repositorio tiene que ser público.

## En cada versión

1. Pon la versión en `Version` de `client.go`.
2. Corre el livecheck contra la API de producción con un agente de prueba.
   Tienen que pasar los catorce chequeos:

   ```bash
   go run ./cmd/magnus-livecheck -agent <agente-de-prueba>
   ```

3. Haz el commit, crea el tag y súbelo:

   ```bash
   git tag v0.1.0
   git push origin main v0.1.0
   ```

4. Pídeselo al proxy, para que aparezca en pkg.go.dev sin esperar:

   ```bash
   GOPROXY=proxy.golang.org go list -m github.com/ABZ-LABS/magnus-go-sdk@v0.1.0
   ```

CI marca un tag que no coincide con `Version`, pero para entonces el proxy puede
tenerlo ya, así que revisa antes de crear el tag. Un tag publicado es
permanente: el proxy lo conserva aunque se borre el tag, así que un error se
corrige con la versión siguiente, nunca moviendo un tag.

La ruta del módulo es `github.com/ABZ-LABS/magnus-go-sdk`. Mover el
repositorio a otro owner cambia la ruta, y eso rompe a todos los usuarios.

## La versión aparece en las instrucciones de instalación

La sección *Instalar sin el proxy de módulos de Go* del README fija un tag.
Cuando cambie la versión, actualízalo ahí, en `README.md`, y en el dashboard de
Magnus (`sdk_links_section.dart` en el front end).

Si `CONTRACT.md` cambió, cambia igual en los SDKs de Python y Node, junto con su
traducción `CONTRACT.es.md`.
