# Desktop application database

`appdb` owns the one private per-user application database:
`$XDG_STATE_HOME/baum/app.sqlite`, defaulting to
`~/.local/state/baum/app.sqlite`. The daemon and local CLI commands use the
same path. It contains durable application records and disposable caches;
configuration is still read from `config.toml`.

## Contents

| Tables / document bucket and key | Owner and contents |
| --- | --- |
| `repositories`, `clones`, `picked`, `aliases`, `preferences`, `prompt_overrides` | `repos`: picked repositories, clones, settings and global preferences |
| `groups`, `versions` | `reviewgroups`: group definitions and combined review versions |
| `capture_session`, `decision`, `decision_file`, `capture_attempt`, feature/PR/link/search tables | `decisionlog`: decisions, coverage, call ledger and feature history |
| `jobs`, `job_heads` | `jobs`: queue/history, progress, accounting and fix outcomes, one row per job, kept when archived 120 days after the job ended; and the archived runs of each pull request or group head, which keep automatic generation from running it again. `jobs` replaces the `state/jobs` document, which the queue moves into it on first open |
| `attribution/commits/<repository>/<sha>` | `commitattribution`: permanent session-owned post-commit and pre-push observations, with event provenance |
| `state/automatic` | `automatic`: observed heads, pending triggers, retries and notices |
| `state/inboxes` | `inboxes`: saved filters and their revision |
| `state/analytics-actions` | `analytics`: accepted GitHub writes, retained 90 days |
| `state/plan-usage-history` | `planusage`: plan-window samples, retained 90 days |
| `ui/inbox`, `ui/walkthrough`, `ui/review` | `uistate`: inbox day/snoozes, per-version walkthrough drafts and per-PR pending review comments, with revisions and retry receipts |
| `state/agent-requests` | `agentrequests`: Codex approval preference |
| `analytics_sessions`, `analytics_index` | `analytics`: derived per-session statistics without conversation text, one row per session file, and the index's format version and price thresholds. They replace the `cache/analytics-index` document, which the index moves into them on first open |
| `cache/discovery/<hash>` | `discovery`: snapshots, GitHub's pull request rows, the incremental cursor, conditional responses and author permissions, partitioned by host, credential identity and repository; the credential's API budgets and team memberships under `<hash>.budget` |
| `cache/pricing` | `pricing`: signed public pricing list, reverified before use |

Small documents are JSON-encoded values in `app_documents(bucket,key,value)`,
not separate JSON files. `Read` reads through a read-only handle and returns
`fs.ErrNotExist` for a missing database or row. `Update` takes an immediate
SQLite write transaction around reading and replacing a row; callback
failures roll back.

## Write-ahead log and shared handles

The database uses SQLite's write-ahead log (WAL). A write-open switches it
(`PRAGMA journal_mode=WAL`, best effort: a database another connection holds
switches at a later write-open), and the mode is kept in the file, so every
later connection uses it, older baum binaries and read-only opens included.
It switches only where the log works: the first switch in a directory is
preceded, once per process, by a scratch database there that is switched,
written, read back and removed (`sqlitepool.WALSupported`). File systems that
cannot memory-map the log's `-shm` index (some FUSE, network and VM shares)
keep the rollback journal.
Readers never wait for the writer, and the writer never waits for readers;
writers still wait up to ten seconds for each other. A commit appends to
`app.sqlite-wal`; checkpoints copy it into `app.sqlite`, automatically every
1000 pages and when the last connection closes, and cut the log back to
`JournalSizeLimit` (16 MiB). A shared handle closes connections idle for a
minute, so the last one closing in a quiet process checkpoints too. Synchronous mode stays SQLite's default (`FULL`):
a commit is on disk before it returns. A writer killed mid-commit leaves no
hot journal: readers ignore a log's uncommitted end, so read-only connections
never need to roll anything back. A read-only connection needs `app.sqlite-shm`
beside the database, which SQLite creates in the private directory.

`Read`, `Update`, `Write` and `Acquire` (used by the job journal and commit
attribution) share one handle per database and mode for the life of a
process once [`sqlitepool`](../sqlitepool) is enabled, which the `baum`
command does at startup: the schema checks and connection setup run once, not
once per call. A shared handle is reused only while the path still names the
file it opened, with the same mode, and is closed after five idle minutes;
`sqlitepool.CloseUnder` closes those under a directory about to be removed.
Tests and other programs leave the pool off and open a handle per call, as
before. `Open` always returns a handle of its own, which relational stores
keep for their lifetime. A transaction on a shared handle is rolled back on
every way out but its commit, a panic included (`Transact`, `Abandon`); a
connection whose rollback fails is closed, not returned, so it cannot keep
the write lock. The schema checks run when a handle opens: a schema upgrade
after the first baseline must make running processes reopen theirs.
Inboxes use it to compare their revision across independent store instances.
The queue retains its own lifetime lock (`<database path>.jobs.lock.sqlite`) beside the
database so only one process executes jobs; the main database is never locked
for the lifetime of the daemon or queue.

Relational packages initialize their own current tables in this database.
Per-clone review documents, attachments and chats remain in
`<common Git directory>/baum/reviews/versions.sqlite`; this database's
`versions` table contains only group reviews and cannot collide with them.

## Privacy and independent recovery

The application directory must be private and owned by the user (0700 on
Unix; current-user-only DACL on Windows), as must the regular database file
(0600). SQLite's `-wal` and `-shm` files inherit that privacy. Existing public files and
symbolic links are refused. Opening an existing file never opens and closes
an ordinary file descriptor: on Unix that could release SQLite's process
locks. SQLite URI paths are escaped, foreign keys are enabled, and writers
wait up to ten seconds for each other. Writable connections begin SQL
transactions immediately, so a read-then-write operation reserves its writer
before reading rather than failing while upgrading a read lock.

OS keyring credentials, optional `config.toml`, daemon launch credentials,
status/start/stop files, lifetime locks, hook spool and event records,
`live-sessions.json`, diagnostic logs, fix transcripts and agent run directories
stay independent. These are needed before the application database opens,
for failure recovery, or contain actual working files rather than app records.
Native shell preferences are managed by the desktop shell.

## Database baseline and release readiness

The first baseline has `application_id = 0x6261756d` and `user_version = 1`.
New empty files are initialized transactionally. Unrecognized nonempty
unversioned schemas and newer versions are refused; no downgrade or silent
replacement is attempted. Pre-release databases/JSON files are not imported.
Future schema changes must add a deliberate, tested upgrade from this baseline.

Before releasing, run in the repository's Docker development environment:

```sh
just oss-go test -race ./internal/appdb ./internal/sqlitepool ./internal/repos ./internal/reviewgroups ./internal/decisionlog ./internal/jobs ./internal/automatic ./internal/inboxes ./internal/analytics ./internal/planusage ./internal/discovery ./internal/agentrequests ./pricing
```

The baseline suite opens relational and document stores together, writes real
records, closes/reopens them, checks SQLite integrity, verifies concurrent
updates/rollback and missing-read behavior, checks database/log privacy, the
switch to WAL, read-only opens beside a connected writer, readers not blocking
the writer and shared handles seeing other writers, and refuses
newer/unrecognized schemas without overwriting their data.
The release gate also runs the full OSS checks and local application browser
suite; tests use private temporary databases and no personal data.
