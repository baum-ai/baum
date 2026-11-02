# inboxcount

Counts the pull requests in the app's Inbox for the desktop app's dock badge,
tray text and tray icon, served as `{"waiting":N}` on
`GET /api/index?view=waiting`.

The count follows the app's queue (`upNext` and `kindOf` in
`app/src/up-next.tsx`) over the daemon's own state, read on each request:

- the cached review index, one pull request per repository and number across
  clones (the clone with a saved review, then a readable store, then a clone,
  speaks for it);
- the job queue, since a queued or running fix leaves the author nothing to do;
- the review groups, since a stacked pull request with an open member under it
  is not ready to merge;
- the ui-state `inbox` record, whose snoozes (until a time, or while the head
  stays the one it was held on) hide a pull request.

An open pull request counts when the user's review is requested, or when it is
theirs and has changes requested, a conflict, failing checks or unresolved
threads, is ready to merge, or is behind a base they can push to.

Saved inboxes left out of the app's badge are not applied: matching them needs
the inbox filter language, which only the app has. Moving the whole queue into
the daemon, so the app and the badge share one implementation, is tracked
separately. Until then, a change to the app's queue rules belongs here too.
