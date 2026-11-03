# Durable desktop reading state

`uistate` owns the Inbox's bookkeeping, review walkthrough drafts, each page's
filter and saved Pull requests tabs, and whether the first-run tour was seen
in the shared private application database
(`internal/appdb`). It imports no legacy
browser storage. Original coding-session transcripts and submitted GitHub
reviews are outside this store.

| Document | Contents |
| --- | --- |
| `ui/inbox` | local day; per-PR seen/cleared records; time-based snoozes; head-based snoozes |
| `ui/filters` | each page's filter query and the saved Pull requests tabs (`{pages: {<page>: query}, tabs: [{id, name, view, query}]}`) |
| `ui/tour` | whether this computer has finished or skipped the app's first-run tour (`{seen}`) |
| `ui/walkthrough` | per repository/PR/review-version seen stops, draft comments per stop, and overall comment |
| `ui/review` | per repository/PR, the comments and summary of the reader's own review until it is sent, and the reviews sent from the app |

A filter page is `inbox`, `inbox:<inbox id>` or `pulls`, at most 64 of them,
and a page without a filter is left out (the app drops the least recently
changed beyond 60). A query is one line of at most 500 characters; the daemon
never parses it. A tab has a 32-hex `id` (unique), a trimmed `name` of 1 to 40
characters, a `view` (`open` or `history`, Closed) and a query; at most 20 tabs. A stored filters document in another shape
reads as none (at its revision), and the next save replaces it.

A walkthrough key is `owner/repository#number@vversion` (optionally prefixed
with an Enterprise host). The latest 40 saved review versions are retained.
A review key is `owner/repository#number`, and the latest 200 pull requests'
reviews are retained. A review is `{comments, note}`: at most 100 comments,
each `{id, target: {kind, id, title}, body, place, head, created}` with a
unique `id` (1–64 of `A-Za-z0-9_-`), `kind` `general`, `section`, `risk` or
`file` (all but `general` with an `id`), a non-blank body, `place` null or
`{path, line, side, start_line?}` (side `RIGHT` or `LEFT`, `start_line`
before `line`), and `head` empty or the hex commit the place is on. An optional `sent` holds
at most 20 reviews sent from the app, each `{url, event, at, note, comments}`
with an `https://` URL, `event` `APPROVE`, `COMMENT` or `REQUEST_CHANGES`, and
at most 100 `{title, where, body}` comments.
The save timestamp is advanced monotonically within the transaction so an
unchanged or backwards wall clock cannot prune the draft just saved.
Inbox rollover/clearing semantics remain in the frontend queue model, which
knows whether its GitHub index read is complete and which actions clear work.

## HTTP

The daemon mounts this provider inside its Host, session/bearer, Origin and
JSON-content checks. These routes grant no filesystem access.

- `GET /api/ui-state/inbox`
- `PUT /api/ui-state/inbox`
- `GET /api/ui-state/filters`
- `PUT /api/ui-state/filters`
- `GET /api/ui-state/tour`
- `PUT /api/ui-state/tour`
- `GET /api/ui-state/walkthrough?key=<URL-encoded review key>`
- `PUT /api/ui-state/walkthrough?key=<URL-encoded review key>`
- `GET /api/ui-state/review?key=<URL-encoded pull request key>`
- `PUT /api/ui-state/review?key=<URL-encoded pull request key>`

Reads return `{revision, value}`; an absent record is `{revision: 0, value:
null}`. Reads never create a database. Writes require `{revision, mutation_id,
value}` and return the saved record. A stale revision is 409
`stale_ui_state` with the current revision/value. Revisions are per record,
so editing another review cannot stale this one. All walkthrough and review
retention and revision changes commit atomically through `appdb.Update`.

Each window's record client has a random identifier and increasing batch
sequence, encoded as `mutation_id: "<client>:<sequence>"`. The store retains the
highest committed sequence for every client while the record is retained,
without evicting acknowledgements after keystrokes from another window.
Retries at or below that sequence acknowledge the latest record before
revision comparison, without replacing it. Client high-water marks are private
bookkeeping and are not in HTTP responses. The window keeps the same client
and sequence while retrying an unacknowledged batch.

Requests are limited to 512 KiB; values to 384 KiB, with bounded collection,
key and note sizes. Invalid keys/types/values are 400. An unreadable stored
record or database is 500 with a fixed message; it is never overwritten with
defaults. Required fields and string/map values reject null and missing
values. Every retained walkthrough or review record is validated before reading, writing or pruning;
a corrupt sibling fails the operation and leaves original bytes untouched.
Callback errors and stale writes leave prior bytes untouched.

## Client behavior and verification

`oss/app/src/ui-state.tsx` owns per-window shared snapshots, asynchronous
hydration, sequential pending changes and explicit Retry after a failed
read/write. Changes made during a read are replayed on what the daemon returns.
An in-flight refresh cannot race a save. The walkthrough and Ask panel share
one record; append/Undo replay against current text, preserving unrelated
comments and later edits. Focus refreshes an idle record from another window.

Tests cover database reopening, concurrent CAS and independent reviews,
retention with repeated/backwards clocks, corruption preservation, input
limits, authentication/Origin/namespace boundaries and a new daemon origin.
Frontend tests cover delayed reads, concurrent edits, failed reads/writes and
commit-then-response-loss retries with intervening edits. Browser tests clear
localStorage and open fresh contexts to verify snoozes and draft comments.

Run in Docker from the repository root:

```sh
just oss-go test -race ./internal/uistate ./internal/uiserver
just local_app::check
just local_app::e2e
```
