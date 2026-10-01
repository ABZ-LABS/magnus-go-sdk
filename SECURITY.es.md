# Seguridad

[English](SECURITY.md) · **Español**

## Reportar una vulnerabilidad

Repórtala en privado mediante el [formulario de avisos de seguridad de
GitHub](https://github.com/ABZ-LABS/magnus-go-sdk/security/advisories/new). Por favor, no
abras un issue público para eso.

Recibirás un acuse de recibo en pocos días. Si corresponde una corrección, se
publica antes que el aviso.

## Alcance

Esta biblioteca es un cliente. Guarda una API key en memoria y la envía a la URL
base que recibió, por el transporte que esa URL indique: usa `https://` para
todo lo que no sea un servidor local. Nunca escribe la key en disco ni en logs.

Una vulnerabilidad de la propia API de Magnus va en el mismo formulario: indica
qué despliegue y qué endpoint.
