# Durable session commit observations

The live tracker identifies the agent session whose process ancestry owns a
Git post-commit or pre-push hook. This store retains those observations in the
private application database, under `attribution/commits/<repository>/<sha>`.
Records keep canonical repository and agent identity, session, exact SHA,
commit/push kind, hook event id and UTC observation time. There is no TTL.

A post-commit observation establishes commit creation. A pre-push observation
records context, including pushes that later fail remotely, and never proves
that its session authored a commit. Checkout history is not written here.

Writes merge idempotently under the application database write transaction.
Independent writers cannot overwrite each other's observations. Reads are
indexed by repository and SHA and never create a database. The tracker retains
failed writes in its recovery file and retries them, independently of live
session expiry. Existing saved hook work is backfilled at startup.

The shared local resolver adds creation references alongside transcript
references without changing cached session graphs. It preserves links when a
transcript is absent, marking conversation and usage as unavailable. The same
resolver supplies review session cards, review bundles and feature attribution.

Run the focused checks from the monorepo root, in Docker:

```sh
just oss-go test -race ./internal/commitattribution ./internal/livesessions ./internal/locallink
```
