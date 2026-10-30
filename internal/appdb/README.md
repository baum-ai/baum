# Desktop application database

`appdb` owns the one private per-user application database:
`$XDG_STATE_HOME/baum/app.sqlite`, defaulting to
`~/.local/state/baum/app.sqlite`. The daemon and local CLI commands use the
same path. It contains durable application records and disposable caches;
configuration is still read from `config.toml`.

## Contents

| Tables / document bucket and key | Owner and contents |
| --- | --- |
| `repositories`, `clones`, `picked`, `aliases`, `preferences` | `repos`: picked repositories, clones, settings and global preferences |
| `groups`, `versions` | `reviewgroups`: group definitions and combined review versions |
| `capture_session`, `decision`, `decision_file`, `capture_attempt`, feature/PR/link/search tables | `decisionlog`: decisions, coverage, call ledger and feature history |
| `state/jobs` | `jobs`: queue/history, progress, accounting and fix outcomes |
| `state/automatic` | `automatic`: observed heads, pending triggers, retries and notices |
| `state/inboxes` | `inboxes`: saved filters and their revision |
| `state/analytics-actions` | `analytics`: accepted GitHub writes, retained 90 days |
| `state/plan-usage-history` | `planusage`: plan-window samples, retained 90 days |
| `state/agent-requests` | `agentrequests`: Codex approval preference |
| `cache/analytics-index` | `analytics`: derived per-session statistics without conversation text |
| `cache/discovery/<hash>` | `discovery`: snapshots/conditional responses and API budgets, partitioned by host, credential identity and repository |
| `cache/pricing` | `pricing`: signed public pricing list, reverified before use |

Small documents are JSON-encoded values in `app_documents(bucket,key,value)`,
not separate JSON files. `Read` opens read-only and returns `fs.ErrNotExist`
for a missing database or row. `Update` takes an immediate SQLite write
transaction around reading and replacing a row; callback failures roll back.
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
(0600). SQLite journals inherit that privacy. Existing public files and
symbolic links are refused. Opening an existing file never opens and closes
an ordinary file descriptor: on Unix that could release SQLite's process
locks. SQLite URI paths are escaped, foreign keys are enabled, and writers
wait up to ten seconds for each other.

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
just oss-go test -race ./internal/appdb ./internal/repos ./internal/reviewgroups ./internal/decisionlog ./internal/jobs ./internal/automatic ./internal/inboxes ./internal/analytics ./internal/planusage ./internal/discovery ./internal/agentrequests ./pricing
```

The baseline suite opens relational and document stores together, writes real
records, closes/reopens them, checks SQLite integrity, verifies concurrent
updates/rollback and missing-read behavior, checks database/journal privacy,
and refuses newer/unrecognized schemas without overwriting their data.
The release gate also runs the full OSS checks and local application browser
suite; tests use private temporary databases and no personal data.
