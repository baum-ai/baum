# discovery

Keeps a private cache of each picked repository's open pull requests (the
local app's inbox), read from GitHub with the user's token. It makes no git
fetch, no generation and no write to GitHub. `Cached` never makes a request;
`Poll`, `PollNow` and `Probe` do, on the credential's shared budget.

The cache lives in `app.sqlite` under `cache/discovery/<hash>`, partitioned by
host, credential identity and repository. The credential's record (`<hash>.budget`)
holds its REST and GraphQL budgets and its team memberships, which every
repository shares.

**Decoded once per change.** A service keeps each cache document it read or
wrote, and decodes the fields readers use (the snapshot, the probe's
fingerprint, the budgets) once, on first use. `Cached`, `Probe` and the budget
share that decoded copy until the document changes; `Cached` gives each caller
its own pull request and observation slices, and nothing may write through
their pointers. Polls, `Observe`, `Merged` and `Unresolved` decode a copy of
their own to change. The kept copy is trusted while `app.sqlite` and its
write-ahead log (`app.sqlite-wal`) keep the identity, size and modification
time they had when it was read. A commit appends to the log and leaves
`app.sqlite` as it was until a checkpoint copies the log into it, so the log's
stat is what moves with each write. When they changed, or were modified within the last three seconds
(`racyWindow`), the document is read again and decoded only if its bytes
differ, so another process or service writing the cache is seen at the next
read. The database is only stat'ed, never opened as an ordinary file.

**Unchanged polls are not written.** A successful or failed poll that changed
nothing but its times (fetched, next poll, rate, backoff interval and last
sweep) keeps them in memory instead of rewriting the document, as long as the
disk copy is less than `unsavedLimit` (an hour) old. `Cached`, `Revision` and
the next poll see the new times; any other change (a pull request, an
observation, a permission answer) writes the whole document with them, in one
transaction as before. A restart, or another process, reads the older times on
disk, which only brings a poll or sweep forward. A save whose bytes equal the
disk copy's is skipped too.

`Revision` stamps what `Cached` would read, without opening the database: the
partition, the identity epoch, whether the last poll validated the cache, and
how many times this service changed it. Two equal revisions read the same
snapshot, except that `Stale` turns true once `NextPoll` passes. The count
lives in memory, so it holds only because the daemon's service is the cache's
one writer: the daemon runs once, and nothing else writes `cache/discovery`.
Live sessions and the review index use it to read a cache again only after it
changed. `Updated`, when set, is called after each write of a repository's
cache, an identity reset and each change to live freshness. A write of the
budget record alone (a probe saves only that) is not reported, since `Cached`
does not read it.

## A poll

Most polls are **incremental**. A poll reads the window of pull requests
updated since the cursor, newest first and of any state, 25 to a page (see
[Time limits](#time-limits-and-concurrency) for smaller pages). It
stops after the first page that reaches below the cursor less a two-minute
overlap. The overlap re-reads a pull request updated in the cursor's second,
or one that GitHub recorded a little late. An open pull request replaces its
cached row; a closed or merged one leaves the cache. If GitHub reads the same
pull request twice in one poll, the older copy never replaces the newer. The
cursor is GitHub's `updatedAt`, never this computer's clock. An update time
recorded more than the overlap behind a newer one waits for the next sweep, at
most `sweepInterval` away. A window longer than `windowPageLimit` (20) pages,
as after days asleep, gives way to a fresh sweep.

The window also reads GitHub's count of open pull requests and the newest
one's number. When the cache disagrees with either, the same poll runs a full
sweep. That happens when a pull request was deleted, or when a change was
missed for any other reason.

Some changes leave a pull request's `updatedAt` as it was. An incremental poll
therefore re-reads by number, in aliased queries of a page each, at most 50:

- pull requests whose mergeability GitHub is still working out while the poll
  settles after a push (`settleInterval`);
- open pull requests on a base that a pull request in the window merged into,
  which GitHub works out again once asked;
- pull requests whose checks are still running.

A finished check does not show in the probe either, so while any open pull
request has checks running the polls stay 30 seconds apart rather than backing
off: the checks' result shows within about a minute. Checks that never report
stop holding the interval once the pull requests have stayed as they are for
`checksHold` (an hour).

A **full sweep** reads every open pull request, the most recently updated
first. It runs on the first poll, after a cache-format upgrade, every
`sweepInterval` (10 minutes) and when the window's count disagrees.

A sweep cut short by a timeout or an error keeps the pages it read and saves
its GraphQL cursor; the next poll goes on from there. Until a first sweep
finishes, each poll also reads the window from GitHub's latest update of any
pull request when that sweep began, which also becomes the cursor. Once a sweep
finishes, the cached pull requests it did not see, and those of an older
format's snapshot, are asked about by number, a page to a query. One is dropped
only when GitHub says it closed, merged or no longer exists, however the pages
shifted meanwhile.

**Partial results.** Pages read before a failed request are merged into the
cache and shown, stale, with the error. Nothing is dropped because a failed
poll did not see it. The cursor moves only once the poll has read its whole
window and the re-reads by number it called for. A poll that read a new head
and then failed still settles. Authors not yet asked about since an upgrade
keep the older snapshot's permission.

**Upgrades.** A cache of an older format (before 3) keeps its snapshot, which
stays shown. The first sweep updates it pull request by pull request; it is
replaced only when the sweep finishes. Its conditional responses are dropped
and not reused.

## Teams, permissions and budgets

- `/user/teams` is read once per credential every `teamsInterval` (10
  minutes), and every repository's poll uses that answer. A 401, 403 or 404
  (no `read:org`) is kept as "membership unknown" for as long. A failed request
  keeps the previous answer.
- An author's write permission (`/collaborators/<login>/permission`) is kept
  per repository for `permissionTTL` (1 hour), and a 403 or 404 (unknown) is
  kept as long as any other answer. A rate limit or a failed request is not
  kept; the next poll asks again.
- The REST and GraphQL budgets are kept in memory per credential and shared by
  every poll and probe under way. Each request checks them first, so a rate
  limit one poll meets stops the next request of the others. Within one rate
  window the lower remaining count stands. They are saved with each poll and
  merged with the saved record at the start of the next, so a restart honours
  them. `GraphQLBudget` reads the GraphQL budget without a request, so the
  background refresh can leave discovery and generation their share.

## Time limits and concurrency

A poll has no fixed deadline: a sweep of a busy repository takes minutes. Its
callers pass a `WithIdleTimeout` context instead, which ends the poll once
GitHub has not answered one of its requests for 45 seconds (the background
service and the app's refresh). The timeout pauses while the poll waits for its
repository's lock or for another poll's read of `/user/teams`. A probe has a
fixed 45-second deadline. A poll or probe that runs out of time returns
`ErrTimeout`, and what it read is saved.

GitHub gives up on a GraphQL query after about ten seconds and answers 502, 503
or 504. A query asks for 25 pull requests (`inboxPageSize`); one that gets a
gateway error is asked again, up to four times, for half as many (at least 5),
and the smaller size stays for the rest of the poll. A sweep that still fails
keeps its cursor. An identity reset (`ResetIdentity`)
during a poll returns `github.ErrIdentityChanged` and discards what the poll
read. A credential that changes under a poll's requests fails them, and what
was read before stays in the old credential's partition.

Polls of different repositories run at the same time. Each repository has its
own lock. `Poll`, `Observe`, `Merged` and `Unresolved` take that lock, so a
call for one repository does not wait for another repository's GitHub
requests. The exceptions are brief: a poll may wait for another's read of
`/user/teams`, and for a cache write. `Probe` takes no repository lock. A
repository's lock is still held across its own poll's GitHub requests. Every
cache and budget write goes through one service-wide write lock, so
`app.sqlite` has no more concurrent writers from discovery than before.

## Probe

`Probe` asks in one GraphQL query per 50 repositories for each repository's
newest open pull request, open count and latest update. It compares them with
the fingerprint the last successful poll left. A repository that differs is
polled at once.

## Tests

```sh
just oss-go test -race ./internal/discovery ./internal/background
```
