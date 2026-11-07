# Inboxes

An inbox is a saved filter over the desktop app's Up next queue. People define
inboxes and the app lists them under Inbox in the sidebar, each showing the
queue entries that match. **Matching lives in the app**; this package only
stores the definitions in the daemon and serves them at `/api/inboxes`, so
they follow the person across app restarts. Per-inbox notifications are a
later step.

## An inbox

| Field | Values |
| --- | --- |
| `id` | assigned by the daemon (random hex), stable across saves |
| `name` | 1 to 40 characters after trimming, unique ignoring case |
| `color` | `blue`, `green`, `red`, `amber`, `purple`, `grey` |
| `what` | `any`, `review`, `merge`, `agent`, `mine` |
| `owners` | GitHub owner logins (organization or user), at most 50; empty means any |
| `risk` | `any`, `3`, `4` (strings) |
| `repositories` | `owner/name`, at most 50; empty means every repository that matches |
| `query` | a filter query the app parses and matches, trimmed, one line of at most 500 characters; empty (or missing) for none. The app reads `what`, `owners`, `risk`, `repositories` and `no_bots` as part of it and saves them folded in, leaving those fields at their plain values |
| `no_bots` | leave out pull requests by bot authors (`author_bot` in discovery) |
| `badge` | show a count badge in the sidebar |

At least one and at most 20 inboxes. The array order is the sidebar order. Owners and
repositories are compared and de-duplicated ignoring case; the first spelling
given is kept.

## Storage

The `state/inboxes` document in the shared private
[`app.sqlite`](../appdb/README.md) application database:

```json
{"revision": 3, "initialized": true, "inboxes": [{"id": "...", "name": "Platform", "color": "blue", "what": "any", "owners": ["baum-ai"], "risk": "any", "repositories": ["baum-ai/baum"], "no_bots": false, "badge": true}]}
```

`initialized` marks that the default inbox has been dealt with. The first
read (or save) of a document without it initializes it in one immediate
transaction, so independent stores and concurrent clients agree:

- A fresh installation gets one inbox named `Default`: what `any`, no owners,
  risk `any`, no repositories, empty query, `no_bots` false, badge on, color
  `blue`, with a daemon-assigned id. The revision becomes 1.
- An upgrade keeps every stored inbox untouched (ids, fields, order) and
  appends `Default` last. If a name equal to `Default` ignoring case exists, it
  is `Default 2`, `Default 3`, and so on. With 20 inboxes already stored,
  nothing is added or removed and the document is only marked initialized.
- `Default` is an ordinary inbox: it can be renamed, edited or deleted while
  another inbox remains, and is not recreated afterwards.
- A saved list is never empty: the last inbox cannot be deleted.
- An initialized document is only read, never written, by `GET`.

A document that does not parse is **never overwritten**: `GET` and `PUT` answer 500
`inboxes_unreadable`. Fix or remove the document to continue. Writes compare
the revision and replace the document in one immediate SQLite transaction,
including across independent store instances.

## API

- `GET /api/inboxes` answers `{"available": true, "revision": n, "initialized": true, "inboxes": [...]}`.
- `PUT /api/inboxes` takes `{"revision": n, "inboxes": [...]}` and replaces the
  whole list, answering the saved list in the shape of `GET`. `revision` is
  the one the client read; any other answers 409 `stale_revision` with the
  current list, including a client still holding revision 0 on a fresh
  installation. Each save increments the revision. An entry without an `id`
  gets one. An `id` in the daemon's shape (32 lower-case hex characters) is
  kept even when the stored list no longer holds it, so an inbox deleted and
  brought back by Undo keeps its links; any other `id` is refused. Other
  methods answer 405.

Errors are `{"code": "...", "message": "..."}` with these codes. A refused save
changes nothing.

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | the body is not one JSON object of the expected shape, has no `revision`, or is over 256 KB |
| 400 | `invalid_inbox` | a field is out of range: an id the daemon did not assign, name length, color, what, risk, owner or repository shape, too many owners or repositories, or a query that is too long or has more than one line |
| 400 | `too_many_inboxes` | more than 20 inboxes |
| 400 | `last_inbox` | an empty list: at least one inbox must remain |
| 400 | `duplicate_name` | two names equal ignoring case |
| 400 | `duplicate_id` | the same id twice |
| 409 | `stale_revision` | the revision is not the stored one; the body carries the current list |
| 500 | `inboxes_unreadable` | the stored file cannot be parsed; it is left untouched |
