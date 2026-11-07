# inboxcount

Counts what waits in the app's Inbox for the desktop app's dock badge, tray
text and tray icon, served as `{"waiting":N}` on
`GET /api/index?view=waiting`. It is the same number as the total on the
app's sidebar Inbox heading, and this package is where that definition is
kept.

The total is the number of distinct, unsnoozed actionable pull requests in the
queue. It follows the app's queue (`upNext` and `kindOf` in
`app/src/up-next.tsx`) over the daemon's own state, read on each request:

- the cached review index, one pull request per repository and number across
  clones (the clone with a saved review, then a readable store, then a clone,
  speaks for it), so a pull request held by several clones counts once;
- the job queue, since a queued or running fix leaves the author nothing to do;
- the review groups, since a stacked pull request with an open member under it
  is not ready to merge;
- the ui-state `inbox` record, whose snoozes (until a time, or while the head
  stays the one it was held on) hide a pull request.

An open pull request counts when the user's review is requested, or when it is
theirs and has changes requested, a conflict, failing checks or unresolved
threads, is ready to merge, or is behind a base they can push to. With nothing
waiting the count is zero.

Saved inboxes play no part: the total includes pull requests that match no
inbox, counts one in several inboxes once, and ignores each inbox's badge
switch (that switch only decides whether the inbox's own sidebar row shows a
count). The daemon therefore needs neither the inbox list nor the filter
language, and overlap and unmatched pull requests do not arise here. The app
takes the same total from its queue. A change to the app's queue rules belongs
here too.
