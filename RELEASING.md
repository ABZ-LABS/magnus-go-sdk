# Releasing

**English** · [Español](RELEASING.es.md)

A Go module is released by pushing a semver tag: the Go module proxy fetches it
from GitHub, and [pkg.go.dev](https://pkg.go.dev/github.com/ABZ-LABS/magnus-go-sdk)
indexes it. There is nothing to upload. The repository must be public.

## Each release

1. Set `Version` in `client.go`.
2. Run the livecheck against the production API with a test agent. All
   fourteen checks must pass:

   ```bash
   go run ./cmd/magnus-livecheck -agent <test-agent>
   ```

3. Commit, then tag and push:

   ```bash
   git tag v0.1.0
   git push origin main v0.1.0
   ```

4. Ask the proxy for it, so it shows up on pkg.go.dev without waiting:

   ```bash
   GOPROXY=proxy.golang.org go list -m github.com/ABZ-LABS/magnus-go-sdk@v0.1.0
   ```

CI flags a tag that does not match `Version`, but by then the proxy may already
have it, so check before tagging. A published tag is permanent: the proxy keeps
it even if the tag is deleted, so a mistake is fixed with the next version,
never by moving a tag.

The module path is `github.com/ABZ-LABS/magnus-go-sdk`. Moving the repository
to another owner changes the path, which is a breaking change for every user.

## The version appears in the install instructions

The README's *Installing without the Go module proxy* section pins a tag. When
the version changes, update it there, in `README.es.md`, and in the Magnus
dashboard (`sdk_links_section.dart` in the front end).

If `CONTRACT.md` changed, it changes identically in the Python and Node SDKs, and
so does its translation, `CONTRACT.es.md`.
