# Durable desktop reading state

`uistate` owns the Inbox's bookkeeping and review walkthrough drafts in the
shared private application database (`internal/appdb`). It imports no legacy
browser storage. Original coding-session transcripts and submitted GitHub
reviews are outside this store.

| Document | Contents |
| --- | --- |
| `ui/inbox` | local day; per-PR seen/cleared records; time-based snoozes; head-based snoozes |
| `ui/walkthrough` | per repository/PR/review-version seen stops, draft comments per stop, and overall comment |

A walkthrough key is `owner/repository#number@vversion` (optionally prefixed
with an Enterprise host). The latest 40 saved review versions are retained.
The save timestamp is advanced monotonically within the transaction so an
unchanged or backwards wall clock cannot prune the draft just saved.
Inbox rollover/clearing semantics remain in the frontend queue model, which
knows whether its GitHub index read is complete and which actions clear work.

## HTTP

The daemon mounts this provider inside its Host, session/bearer, Origin and
JSON-content checks. These routes grant no filesystem access.

- `GET /api/ui-state/inbox`
- `PUT /api/ui-state/inbox`
- `GET /api/ui-state/walkthrough?key=<URL-encoded review key>`
- `PUT /api/ui-state/walkthrough?key=<URL-encoded review key>`

Reads return `{revision, value}`; an absent record is `{revision: 0, value:
null}`. Reads never create a database. Writes require `{revision, mutation_id,
value}` and return the saved record. A stale revision is 409
`stale_ui_state` with the current revision/value. Revisions are per record,
so editing another review cannot stale this one. All walkthrough retention
and revision changes commit atomically through `appdb.Update`.

The last 128 mutation IDs of each retained record acknowledge write retries,
including a response lost after commit and another window's subsequent edit.
Acknowledgement happens before revision comparison and returns the latest
record, without replacing it. IDs are private bookkeeping and are not in HTTP
responses. Clients keep the same ID while retrying an unacknowledged batch.

Requests are limited to 512 KiB; values to 384 KiB, with bounded collection,
key and note sizes. Invalid keys/types/values are 400. An unreadable stored
record or database is 500 with a fixed message; it is never overwritten with
defaults. Callback errors and stale writes leave prior bytes untouched.

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
