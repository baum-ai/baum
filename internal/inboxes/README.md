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
| `no_bots` | leave out pull requests by bot authors (`author_bot` in discovery) |
| `badge` | show a count badge in the sidebar |

At most 20 inboxes. The array order is the sidebar order. Owners and
repositories are compared and de-duplicated ignoring case; the first spelling
given is kept.

## Storage

`inboxes.json` (0600, written through a temporary file and a rename) in the
daemon's state directory, beside `groups.sqlite`:

```json
{"revision": 3, "inboxes": [{"id": "...", "name": "Platform", "color": "blue", "what": "any", "owners": ["baum-ai"], "risk": "any", "repositories": ["baum-ai/baum"], "no_bots": false, "badge": true}]}
```

A missing file reads as revision 0 with no inboxes. A file that does not parse
is **never overwritten**: `GET` answers 500 `inboxes_unreadable` and so does
`PUT`, so nothing is lost silently; fix or remove the file to continue. Writes
are serialized.

## API

- `GET /api/inboxes` answers `{"available": true, "revision": n, "inboxes": [...]}`.
- `PUT /api/inboxes` takes `{"revision": n, "inboxes": [...]}` and replaces the
  whole list, answering the saved list in the shape of `GET`. `revision` is
  the one the client read; any other answers 409 `stale_revision` with the
  current list. Each save increments the revision. An entry without an `id`
  gets one. An `id` in the daemon's shape (32 lower-case hex characters) is
  kept even when the stored list no longer holds it, so an inbox deleted and
  brought back by Undo keeps its links; any other `id` is refused. Other
  methods answer 405.

Errors are `{"code": "...", "message": "..."}` with these codes. A refused save
changes nothing.

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `invalid_request` | the body is not one JSON object of the expected shape, has no `revision`, or is over 256 KB |
| 400 | `invalid_inbox` | a field is out of range: an id the daemon did not assign, name length, color, what, risk, owner or repository shape, or too many owners or repositories |
| 400 | `too_many_inboxes` | more than 20 inboxes |
| 400 | `duplicate_name` | two names equal ignoring case |
| 400 | `duplicate_id` | the same id twice |
| 409 | `stale_revision` | the revision is not the stored one; the body carries the current list |
| 500 | `inboxes_unreadable` | the stored file cannot be parsed; it is left untouched |
