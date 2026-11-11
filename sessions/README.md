# Session ingestion

This directory owns transcript readers, session mapping, accounting and local
collection for Claude Code, Codex, Cursor and Entire. The appliance imports
these packages too; its small adapters select the hosted interpretation and
keep persistence and repository-service transport outside this module.

Readers interpret recorded formats; mappers produce a graph and an independent
accounting report. Keep record accounting balanced and preserve graph ids,
ordering, relationships, file evidence, usage and errors. A balanced report
alone does not establish that a graph is valid.

Codex adds record and item types between releases. The Codex reader keeps a
line whose record class, `response_item` type or `event_msg` type it does not
know, and the mapper discards it as `unknown/...` in the report, so one new
type does not fail the whole session. A malformed line of a known type still
fails it. An Entire checkpoint whose embedded Codex transcript has an unknown
record falls back to its compact transcript instead.

## Mapping cache

`sessions.Map` keeps the graphs it mapped in a process-wide cache. Every
caller shares it: `Load`, the live transcript view and the decision log. A
daemon that reads the same transcripts on each refresh therefore decodes each
one only once until it changes.

* **Key:** the source and, for each file, its name, kind, path, size and
  modification time. Generated content, such as a Cursor tree, is keyed by its
  SHA-256 instead.
* **Invalidation:** a file that grows or is rewritten gets a new key. Keeping
  the new mapping drops the one of the same session's files as they were. A
  file that cannot be statted is mapped without the cache.
* **Failures:** a mapping that failed because of the file's content is cached
  as well, so an unchanged broken transcript is not read again. A file that
  could not be opened or read is tried again on the next call.
* **Discovery warnings:** these come from discovery, not the mapper. They are
  not cached, and each call adds its own.
* **Size:** least recently used entries are evicted once the estimated memory
  of the graphs held passes `DefaultMapCacheBytes` (64 MiB).
  `SetMapCacheLimit` changes the limit; zero or less turns the cache off. The
  estimate (`graphBytes`) adds up the graphs' rows, ids, text and JSON; on a
  586 MB Claude Code session tree it gave 88 MB where the heap profile showed
  93 MB. A graph takes about a sixth of its transcript's size, so the limit
  used to count transcript bytes overstated the memory held about six times.
  The `sessions.map_cache.bytes` gauge is the estimate and
  `sessions.map_cache.transcript_bytes` the transcripts behind it.
* **Sharing:** cached graphs are shared between callers, so callers must not
  change a graph `Map` or `Load` returns.
* **Holds:** `HoldMappings` raises the limit to `HeldMapCacheBytes` (256 MiB)
  until it is released. The daemon's background pass holds it while it
  refreshes several pull requests of a clone, so each session is decoded once
  in the pass.

## Incremental decoding

Claude Code transcripts and Codex rollouts are append-only JSONL, and an agent
appends to the transcript of a running session all the time. Each append
gives the session a new mapping-cache key, so below that cache `Map` keeps,
per transcript file, its decoded lines and the byte offset they end at
(`claudetranscript.Reading`, `codextranscript.Reading`). When the file grows,
only the bytes after that offset are decoded, and the mapper then runs again
over all the lines, so the `ErrUnbalanced` accounting check is unchanged.
Cursor sessions arrive as generated content and are always decoded whole.

* **Per file:** a Claude Code subagent transcript and a Codex child or fork
  source rollout each keep their own offset. Claude Code's duplicate uuid and
  tool use id checks, and the Codex envelope ordinal check, which depends on
  whether the first line was a paginated `session_meta`, keep their state
  across reads, so they reject what a whole read would.
* **Torn lines:** a final line without a newline is not counted as read; the
  next read starts at its first byte. Until then it is decoded on each read as
  a whole read decodes a last line (a partial write sets `TornFinalLine`).
* **Whole reads:** a file is read from its start when it is another file than
  last time (a different inode: replaced), shorter than the offset (shrunk), or
  its first 4 KiB or the 4 KiB before the offset differ from what they were
  (rewritten). A file that could not be decoded loses its kept lines.
* **What is kept:** only files changed in the last hour, since older ones are
  unlikely to grow, within `DefaultTailCacheBytes` (64 MiB) of estimated line
  memory (`Reading.Bytes`). A file read for the first time only pushes out
  files unread for 30 minutes; if it still does not fit, it is not kept. A
  session tree larger than the cache, read file by file on every refresh,
  then keeps what fits instead of pushing out each file before it is read
  again. A kept file that grew pushes out the least recently read.
  `SetTailCacheLimit` changes the limit; zero or less turns it off, and every
  transcript is then decoded whole each time.
* **Slim lines:** the kept lines leave out the copies of each record the
  mapper never reads: `Line.Raw` for Claude Code (only the shape survey reads
  it, and `Map` does not survey); for Codex `Line.Raw`, `Line.Payload` except
  on `event_msg` lines (whose tool completions become tool output), and the
  `Raw` of response items, events and turn contexts. The graphs are the same
  as from a whole read.

Measured on this repository's largest Claude Code session (a 164 MB main
transcript and 154 subagent transcripts, 586 MB in all) after appending 1 MB:
the kept lines covered 349 MB of transcript in 75 MB of heap (estimate 67 MB),
and the map took 2.0 s instead of 4.3 s, decoding 22,676 lines instead of
75,577; the files that did not fit were decoded whole. A 10.7 MB Codex rollout
mapped in 60 ms instead of 219 ms; its kept lines took 19 MB (estimate 18 MB),
since Codex lines keep typed payloads about as large as the records.

The graphs themselves are not slimmed. Their callers read more than session
evidence does: commit linkage reads the output of commit commands' tool
results (`linkage/sessioncorrelate`), the transcript views render tool
output and assistant content, decision capture reads the transcript text,
and analytics reads every usage row.

`workstats` counts `sessions.map.decoded_bytes` and
`sessions.map.decoded_lines` (what was actually decoded, appended bytes for a
continued read), `sessions.tail.reads.full` and `.appended`, and
`sessions.tail.restarts.shrunk`, `.replaced` and `.rewritten`. The
`sessions.tail_cache.bytes` (estimated memory), `.transcript_bytes` and
`.entries` gauges give what is kept.

## Decode bound

A decode holds a whole transcript and its decoded lines, and transcripts reach
over 150 MB, so decoding is bounded two ways.

* **Parallelism:** `Load` maps, and reads Entire checkpoints, up to
  `min(GOMAXPROCS, 8)` sessions at once. A context marked with
  `WithBackground` (the daemon's background pass sets it) allows
  `BackgroundParallel` (2) instead; interactive loads keep the larger bound.
* **Bytes:** a process-wide weighted semaphore of `DefaultDecodeBudgetBytes`
  (256 MiB) is acquired with a session's transcript size before it is decoded,
  less the bytes its kept lines cover (see "Incremental decoding"),
  and with a fixed estimate for each checkpoint read. A session larger than
  the budget is clamped to it, so it runs alone rather than waiting forever.
  Waiting ends when the context is cancelled. Cache hits, of mapped sessions
  and of checkpoint reads, take no budget.

## Entire checkpoint reuse

Entire checkpoints are named by git objects, which never change, so a process
reads each one once:

* **Listings.** `entire.Listing` remembers, per clone, what `List`,
  `Touched`, `Sessions` and `Located` learned: the branch's checkpoints by
  the branch commit, the branch's dates by the branch commit and the clone's
  shallow boundary, each ref's tree and date by the ref's commit, and each
  checkpoint's metadata by its tree. When neither the branch nor any ref moved,
  they run no git. When the branch only moved forward, `Touched` reads only
  `<old>..<new>` (after `git merge-base --is-ancestor`) and merges it into the
  dates it had; a rewritten branch is read whole. A ref that is added or moved
  is asked about alone, and what a removed ref named is dropped. Only answers
  git gave in full are kept: an object a partial clone lacks is asked for again
  next time, and never fetched. `sessions` keeps one listing per clone root,
  for up to 32 clones; a listing costs about 100 bytes per checkpoint.
* **Whole reads.** A checkpoint read whole (`git archive`, then parsing and
  mapping) is kept in a process-wide cache keyed by the clone and the
  checkpoint's tree, as the sessions it maps to, or the error parsing or
  mapping it gave. The checkpoint's files themselves, the transcripts, are not
  kept. An entry costs the size of the checkpoint's files, which bounds its
  graphs' memory as the mapping cache's cost does; least recently used entries
  go first once `DefaultCheckpointCacheBytes` (64 MiB) is held, and a
  checkpoint larger than that is not kept. `SetCheckpointCacheLimit` changes
  the limit; zero or less turns the cache off. A checkpoint whose export
  failed, such as one with objects a partial clone never downloaded, is not
  kept, so it is reported again on the next read. Cached graphs are shared
  between callers, as `Map`'s are.
* **One private repository.** `Discovery.Load` and `Discovery.Checkpoints`
  read the stores `Discover` resolved, in the private repository of the clone
  `Discovery.ShareRepo` gives them, or of one they open themselves.

`Load` is `Discover` followed by `Discovery.Load`. `Discover` reads
directories, file metadata and the Entire refs, and never a transcript.
`Discovery.Fingerprint` describes everything `Load` would read, so a caller can
tell whether anything changed before paying for the decode.
`Options.StartedBefore` drops sessions the agent's own store says began after
it. Claude Code's start comes from its first records and Codex's from its
`session_meta`; a session with no known start is kept.

## Product profiles

`traceprocessing.Local` is the zero value and the default of existing public
entry points. `Hosted` is passed explicitly to `BuildWith`, checkpoint parsing
and archive reading. It never changes another build's behavior. Claude's
`Build` still requests its transcript shape survey; `BuildWith` keeps the
independent `Survey` option, including the local mapping path's `Survey: false`.

| Area | Local interpretation | Hosted interpretation |
| --- | --- | --- |
| Claude agent messages | Existing local input classification and tool results | Agent messages are system inputs; waiting calls receive child reports and detail metadata |
| Claude attributes | Avoid duplicate timestamps per key | Advance every change past the preceding timestamp per key |
| Codex | Normalize code-mode shell commands, preserve local result and report interpretation | Normalize code-mode shell commands, attach child reports and detail metadata |
| Cursor | Existing payload decoder and error wording | Validate uploaded tree nodes and retain hosted error wording |
| Portable and compact tools | Existing tool aliases and patch-line limits | Lowercase `grep`/`bash` aliases and patch scanning past long lines |
| Entire reader | Existing checkpoint parsing | Bound session entries, reject repeated sources, discover tasks and read Pi `toolCall` blocks |
| Entire mapper | Recorded source hash and existing metadata token handling | Hash mapped source bytes, graft Claude tasks, reject negative aggregate usage and fill Cursor models |
| Reports | Existing local fields | Include hosted task and rejected-usage accounting, even when zero |
| Collection | Local Git environment, Windows path folding and modification-time filtering | Repository-service snapshots and case-sensitive Claude project-directory matching |
| Archives | Existing local errors and byte filesystem | Preserve hosted sentinel errors and directory discovery for task transcripts |

Both profiles preserve Codex code-mode programs and extract static shell commands
from them, including compact Entire transcripts. This supplies independent
commit-creation evidence locally. Programs without static shell commands remain
unchanged. The local analytics index rebuilds after this evidence change.

These differences preserve current product contracts. They are not upgrade
paths, and neither profile changes the local application's database.

For the local image reader, an ordinary Claude image-only user envelope is a
prompt with human origin even though it has no text. Meta, system-injected,
peer and unknown-source records remain system inputs; child-session asks keep
their agent origin. The hosted interpretation is unchanged.

## Verification

Run commands in the configured development containers from the monorepo root:

```sh
just oss-go test ./sessions/...
just platform::test ./internal/traceprocessing/... ./internal/traceingest/... ./internal/entireingest/...
just platform::update-goldens
```

The standalone OSS module runs the same `go test ./sessions/...` command inside
its development or CI environment. Its tests need no appliance source checkout
or database. Hosted pure cases use `TestHosted` names and retain their own
expected output under `testdata/hosted`; local cases retain their original
fixtures. Captured appliance Entire transcripts and goldens remain in the
appliance test suite; public policy tests use synthetic inputs. Optional corpus
reconciliation tests remain opt-in. Platform corpus storage tests stay at the
persistence boundary.

Golden updates are for intentional behavior changes. A consolidation must
produce no expected-output changes: compare all existing local and hosted
fixture bytes before and after the update recipe, and investigate differences
instead of accepting refreshed files as evidence.
