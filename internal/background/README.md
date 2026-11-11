# background

Keeps local GitHub metadata fresh while the daemon runs: discovery polls of
every picked repository, then reads of the saved reviews' evidence. It never
queues or starts generation. The daemon hands each successful snapshot to its
automatic scheduler through `Discovered`.

## A pass

`Run` makes a pass at least every 30 seconds, or sooner when a repository is
due sooner, as one settling after a new head is. A pass (`Sync`):

1. **Probes** (`discovery.Probe`), at most once per interval (a minute). A
   repository whose open pull requests changed since its last poll polls now
   (`PollNow`), past discovery's backoff. A pull request opened or pushed from
   anywhere therefore shows within about a minute. A probe that succeeds after
   one that failed makes every repository whose poll failed meanwhile poll at
   once.
2. **Polls** every due repository, `pollConcurrency` (3) at a time. Each poll
   stops once GitHub has not answered it for `pollTimeout` (45 s), so a busy
   repository's sweep may take minutes. Discovery keeps what it read when time
   runs out. The polls share the credential's GitHub
   budget, which discovery checks before every request. Their results are
   handled one at a time: `Discovered`, the repository's state, and the reads
   each poll makes due.
3. **Reads** the due saved pull requests within `refreshBudget`: those GitHub
   reported changed first, then the oldest. A rate-limited read pauses reads
   on that host (`Paused`, `Limited`), which the PR page's facts share. A read
   that finds nothing new leaves the saved document unwritten, its
   `evidence_refreshed_at` included (`store.Store.RefreshCurrent`), and still
   counts as the pull request's last read, in memory. See
   [Which pull requests are read](#which-pull-requests-are-read).
4. Reconciles retained review refs (`Retain`) once a day per common git
   directory.

## Which pull requests are read

Only pull requests with a saved review in a clone's store (`Saved`) are read.
Discovery already carries the list fields of the other open ones.

- **Open.** Read when discovery shows a change since the last read: a newer
  `updatedAt` (a push, edit, review or comment), another head, or another
  check rollup (a check finishing moves no update time). Otherwise read once
  its evidence is `openReadAge` (2 hours) old.
- **Closed or merged.** A pull request that is not among the open ones is
  read once more, to record its final state, while its saved document still
  says `open` or `draft`. Once the document records `merged` or `closed`, it
  is retired: the background refresh never reads it again. If it is reopened,
  discovery lists it open again and the rules above apply.
- **Failures.** A failed read is tried again after the interval, doubling. After
  `maxReadFailures` (3) failures in a row, it waits for a change: an open pull
  request is read again when its `updatedAt` or head moves, or after
  `stoppedRetry` (a day); a closed one only in the next daemon process. A
  refresh the person asks for (`Retry`, from the app's refresh of a
  repository) and GitHub answering again after it did not retry every failed
  read once. A conflict with a generation that published first is no failure.
- **Restart.** Reads live in memory. In a new process, the reads that fall due
  by age, and final reads, are spread over `restartSpread` (30 minutes) by a
  hash of the pull request, so a restart does not read every saved review at
  once. One GitHub reports changed is read at once.

**Budget and time limits.** The reads make at most `refreshRequestsPerHour`
(1,000) GraphQL requests in any hour, each at least one point of the token's
usual 5,000. They also wait while discovery last read fewer than
`refreshReserve` (1,000) GraphQL points left in the current window
(`Budget`, `discovery.Service.GraphQLBudget`), leaving those to discovery and
generation. Each GitHub request of a read may take `readRequestTimeout` (30 s),
and the whole read `readLimit` (2 minutes), so a huge pull request takes as many
requests as it needs while a stalled one fails. Neither budget sets the
repository's error.

`RefreshNow` (hook events) reads what it is asked to, retired, stopped or past
the budget, honouring only the host's rate pause. Its requests count toward
the hour's budget, and a successful read clears the pull request's failures.

A repository's state (`GET /api/background`) records its last success, its
next refresh and the error of the last pass: `discovery_unavailable`,
`discovery_timeout` (the poll ran out of time), `rate_limited`,
`metadata_unavailable`, `store_unreadable` or `cache_unavailable`. A failed
poll backs off exponentially, for at most 15 minutes. A settling poll is due
when discovery says.

## Tests

```sh
just oss-go test -race ./internal/background
```
