# Security

**English** · [Español](SECURITY.es.md)

## Reporting a vulnerability

Report it privately through GitHub's [security advisory
form](https://github.com/ABZ-LABS/magnus-go-sdk/security/advisories/new). Please do not
open a public issue for one.

Expect an acknowledgement within a few days. If a fix is warranted, it ships
before the advisory is published.

## Scope

This library is a client. It holds an API key in memory and sends it to the
base URL it was given, over whatever transport that URL names — use `https://`
for anything but a local server. It never writes the key to disk or logs.

A vulnerability in the Magnus API itself belongs in the same form: say which
deployment and which endpoint.
