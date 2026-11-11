# inboxqueue

Builds the app's Inbox from the daemon's own state: every open pull request
with what it waits for, which saved inboxes hold it, how many pull requests
wait on the person, and how many each inbox lists. The app reads it instead of
matching every inbox's query against every pull request itself, and the
desktop app's dock badge, tray text and tray icon read the total.

It follows the app's queue (`upNext` and `kindOf` in `app/src/up-next.tsx`)
over the daemon's state, read on each request:

- the cached review index, one pull request per repository and number across
  clones (the clone with a saved review, then a readable store, then a clone,
  speaks for it), so a pull request held by several clones counts once;
- the job queue, since a queued or running fix leaves the author nothing to do;
- the review groups, since a stacked pull request with an open member under it
  is not ready to merge;
- the ui-state `inbox` record, whose snoozes (until a time, or while the head
  stays the one it was held on) hide a pull request, and whose archives
  (`archived`) hide one for good;
- the saved inboxes (`internal/inboxes`).

## Kinds

Each open pull request is one item with a kind, which is also what an inbox's
`waiting=` filter reads:

- `review`, or `clone` when it needs a clone: the user's review is requested,
  or it is assigned to them and they did not write it;
- for their own: `fix` (changes requested, a conflict, failing checks or
  unresolved threads), `merge` (ready, not a draft, checks not running, and not
  stacked on an open member of its stack) or `update` (behind a base they can
  push to);
- `fix` with `fixing` set: their own that an agent is fixing now, which is not
  in the queue;
- `other`: someone else's that waits on nothing from them;
- `""`: their own that waits on others.

## Counts

The total (`count`, and `{"waiting":N}` on `GET /api/index?view=waiting`) is
the number of distinct items in the queue, neither snoozed nor archived: `review`, `clone`, and
`fix`, `merge` and `update` without `fixing`. It is the same number as the
total on the app's sidebar Inbox heading, and this package is where that
definition is kept. Saved inboxes play no part: the total includes pull
requests that match no inbox, counts one in several inboxes once, and ignores
each inbox's badge switch. With nothing waiting it is zero.

An inbox's own `count` is what its sidebar row shows: the items it holds,
neither snoozed nor archived, that are in the queue or someone else's (`other`).

## Matching

An inbox holds the items its query matches with `internal/prfilter`, after its
older parts (what, owners, risk, repositories, bots) are folded into it as the
app folds them. A query that does not read narrows nothing; an empty one holds
every open pull request. Items whose kind is `""` match `waiting=` with no
kind, so `waiting=` never holds them.

## API

`GET /api/inbox` answers:

```json
{
  "count": 2,
  "items": [{ "key": "acme/app#12", "kind": "review", "inboxes": ["<id>"] },
            { "key": "acme/app#13", "kind": "fix", "fixing": true, "snoozed": true, "inboxes": [] }],
  "inboxes": [{ "id": "<id>", "query": "<folded query>", "count": 1 }]
}
```

`items` are in the index's order, and each item's `inboxes` in the saved
order. `inboxes` is `null` when the saved inboxes cannot be read; no item is
in any inbox then.

`GET /api/inbox?query=Q` answers what a query not saved yet would hold, for a
filter being typed or an inbox being edited: `{"query": Q, "problem": "",
"matches": ["<key>", …]}`. `problem` says why a query does not read (it then
matches every item), or that it is longer than an inbox may save. A query over
8 KiB is refused with 413.

The host applies its launch authentication first. A change to the app's queue
rules, or to which pull requests an inbox holds, belongs here too.
