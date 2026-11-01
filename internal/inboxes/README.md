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
| `repositories` | `owner/name` (or `host/owner/name`), at most 50; empty means every repository that matches |
| `involvement` | any of `assigned`, `requested`, `authored`; empty means any |
| `no_drafts` | leave out draft pull requests |
| `review` | any of `approved`, `changes_requested`, `none` (no decision yet); empty means any |
| `checks` | any of `passing`, `failing`, `running`; empty means any |
| `merge` | any of `ready`, `conflict`; empty means any |
| `no_bots` | leave out pull requests by bot authors (`author_bot` in discovery) |
| `badge` | show a count badge in the sidebar |

At most 20 inboxes. The array order is the sidebar order. Owners and
repositories are compared and de-duplicated ignoring case; the first spelling
given is kept. The four lists of pull request states are de-duplicated in
order, and an entry that leaves one out saves it empty; inboxes stored before
they existed read them as empty and `no_drafts` as false.

`owners` through `merge` are `Criteria`, which the app's quick filters share
(the `ui/filters` document of [`uistate`](../uistate/README.md), validated by
`Criteria.Clean`). A list's values combine with or and the parts with and, so
with both owners and repositories listed a pull request's repository must be
in both. A GitHub Enterprise repository is `host/owner/name`; its owner is the
segment before the name.

## Storage

The `state/inboxes` document in the shared private
[`app.sqlite`](../appdb/README.md) application database:

```json
{"revision": 3, "inboxes": [{"id": "...", "name": "Platform", "color": "blue", "what": "any", "owners": ["baum-ai"], "risk": "any", "repositories": ["baum-ai/baum"], "involvement": ["requested"], "no_drafts": true, "review": [], "checks": [], "merge": [], "no_bots": false, "badge": true}]}
```

A missing document reads as revision 0 with no inboxes. A document that does
not parse is **never overwritten**: `GET` and `PUT` answer 500
`inboxes_unreadable`. Fix or remove the document to continue. Writes compare
the revision and replace the document in one immediate SQLite transaction,
including across independent store instances.

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
| 400 | `invalid_inbox` | a field is out of range: an id the daemon did not assign, name length, color, what, risk, owner or repository shape, too many owners or repositories, or an unknown involvement, review, checks or merge value |
| 400 | `too_many_inboxes` | more than 20 inboxes |
| 400 | `duplicate_name` | two names equal ignoring case |
| 400 | `duplicate_id` | the same id twice |
| 409 | `stale_revision` | the revision is not the stored one; the body carries the current list |
| 500 | `inboxes_unreadable` | the stored file cannot be parsed; it is left untouched |
