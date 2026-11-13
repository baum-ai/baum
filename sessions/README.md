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
* **Memory bound:** what `sessions` keeps after decoding is at most 64 MiB of
  estimated graph memory (256 MiB while `HoldMappings` holds it) plus 64 MiB
  of estimated kept transcript lines (`DefaultTailCacheBytes`, see
  "Incremental decoding"): 128 MiB, or 320 MiB during a background pass.
  The Entire checkpoint cache adds at most another 64 MiB of estimated graph
  memory (see "Entire checkpoint reuse").
  Decoding in progress is bounded separately (see "Decode bound").
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
Cursor sessions arrive as generated content and are always decoded whole, and
so is a compressed Codex rollout (see below).

* **Per file:** a Claude Code subagent transcript and a Codex child or fork
  source rollout each keep their own offset. Claude Code's duplicate uuid and
  tool use id checks, and the Codex envelope ordinal check, which depends on
  whether the first line was a paginated `session_meta`, keep their state
  across reads, so they reject what a whole read would.
* **Torn lines:** a final line without a newline is not counted as read; the
  next read starts at its first byte. Until then it is decoded on each read as
  a whole read decodes a last line (a partial write sets `TornFinalLine`).
* **Whole reads:** a file is read from its start when it is another file than
  last time (a different inode: replaced), shorter than the offset (shrunk),
  or rewritten: it kept its size but its modification time moved, or the
  CRC-32C of its bytes before the offset differs from that of the bytes the
  kept lines were decoded from. That checksum is updated from the bytes each
  read consumed, never from a second read, and checked again whenever the
  size or modification time changed; checking reads the prefix but decodes
  nothing (25–35 ms for a 164 MB transcript in the page cache). A file whose
  size and modification time did not change is taken as unchanged. A read
  stops at the size the file had when it was opened. A file that could not be
  decoded loses its kept lines.
* **Compressed rollouts:** a `.jsonl.zst` rollout cannot be continued from an
  offset or checksummed by prefix: a Reading consumes decoded bytes, and a
  zstd stream cannot be entered in the middle. Its entry keeps the lines and
  the final unread line of the last whole decode. While the file's inode, size
  and modification time are unchanged `Map` uses them without reading the file
  (counted as `sessions.tail.reads.reused`); any change decodes the whole
  stream again as a rewrite. Offsets, `pending` and the transcript-bytes gauge
  are decoded bytes, and the decode budget counts such a rollout by its decoded
  size (the frame's content size, or eight times the compressed size when the
  frame has none), while the mapping-cache key uses its size and modification
  time on disk.
* **What is kept:** files of any age may keep their decoded lines. Age alone
  does not force unchanged transcripts to decode again. The same byte budget
  and idle eviction bound retention: `DefaultTailCacheBytes` (64 MiB) of
  estimated line memory (`Reading.Bytes`). Files unread for 30 minutes are
  released on the next read of any file. A new file that does not fit does
  not evict recently read files, so trees larger than the budget keep what
  fits instead of thrashing. A kept file that grows evicts the least recently
  read. `SetTailCacheLimit` changes the limit; zero or less turns it off.
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
  kept. An entry costs the estimated memory of the graphs it keeps (the same
  `graphBytes` estimate as the mapping cache, plus 1 KiB, which is also the
  cost of an error); least recently used entries
  go first once `DefaultCheckpointCacheBytes` (64 MiB) is held, and an
  outcome larger than that is not kept. The `sessions.checkpoint_cache.bytes`
  gauge is that estimate. `SetCheckpointCacheLimit` changes
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
Codex discovery indexes the whole Codex store (every rollout's first record),
because a session's children and fork sources can be anywhere in it. The
collector keeps what it read in a `codex.Cache` keyed by rollout path, size and
modification time, so an unchanged rollout is not opened again and a deleted
one is dropped by the next scan; `FindOptions.CodexCache` selects the cache,
and by default the process-wide `codex.Shared` is used. Discovery returns the
same sessions with or without it. Within a `Cache.BeginPass`/`EndPass` pair
the store is scanned once however many clones look it up, and so does not see a
rollout added during the pass. The parent and child lookups are built once per
scan, not once per session.
Codex compresses old rollouts to `.jsonl.zst` (a zstd stream, decoded while
reading) and removes the plain file, keeping the modification time. baum reads
them like plain rollouts: discovery, `FindStores` and the partial index list a
compressed rollout unless a plain file of the same name is beside it, in which
case the plain one wins and the compressed copy is ignored, so a rollout is
never a duplicate thread. The session's file keeps its real `.zst` path but its
plain name (`rollout-<ts>-<uuid>.jsonl`), so graph source refs and native
session identity do not change when Codex compresses a rollout. The walk lists
every rollout once and decides between the two copies from that listing, without
a stat per file. If the plain file is gone by the time it is read, because
Codex compressed it after the walk, the compressed copy is read instead. Discovery reads only a rollout's first record.
A compressed rollout whose zstd data fails there (corrupt, truncated, or
with a window above the decoder's 64 MiB bound; rollouts of any length decode,
since a stream holds only its window) is skipped, like one without a complete
first record but with its own reason ("compressed rollout is corrupt or
truncated"; `ScanReport.UnreadablePaths`), and the rest of the store is indexed.
A stream that fails later is read as a plain rollout cut off at that point: the
records decoded before the failure are kept and the rest is a torn final line,
without an error. An error reading the file itself is not a cut: it fails the
read, as it does for a plain rollout, and is not cached. Decoders are pooled,
since each holds a few MiB.
If a scan does fail, discovery falls back to a partial index of `sessions/`,
read through the same cache.
Cursor is the exception to "never a transcript": it keeps every conversation
in one database, so discovery reads the store's conversation index and builds
the tree of each repository conversation for its payload. `Options.Since` bounds that: a conversation last updated
before it is skipped on the index's update time, before its tree is read.
`Options.SessionIDs` keeps only the sessions with those native session IDs; the
others are never mapped, so a caller that needs a few sessions does not decode
every transcript of the repository. An Entire session is dropped when the
agent's own store holds its ID, as without the option.
`Options.StartedBefore` drops sessions the agent's own store says began after
it. Claude Code's start comes from its first records and Codex's from its
`session_meta`; a session with no known start is kept.

## Reusing sessions a caller already read

Each session `Load` returns carries a `Key` naming what its graph was mapped
from. For a session from the agent's own store, the key is a digest of the
mapping cache's key. A file modified in the last 2 seconds, or in the future,
gives its session no key at all: a rewrite at the same size within one
modification-time tick would look unchanged. For an Entire session, the key is
the clone, the checkpoint's tree and the session's id. A tree names its
content, so the key never goes stale. The clone is part of the key, as in the
checkpoint cache's key, so that one clone's reads never answer for another's.
`Discovery.LoadReusing` takes a callback that says which keys the caller
already holds what it needs from. Those sessions are not mapped. They come back
`Unmapped`, with no graph, and `Transcript` says whether their graph has a
transcript, so `Fuller` still compares them. Everything else is what `Load`
returns. `locallink` uses this to map only the sessions that changed.

Choosing an Entire session's snapshot needs only each checkpoint's session ids,
dates, warnings and whether each mapped and has a transcript. The process
remembers these by clone and checkpoint tree (8,192 checkpoints). They are
small, so they outlive the whole-read cache's evictions. After the first read,
a checkpoint is read whole again only when a session chosen from it has to be
mapped, and that read goes through the whole-read cache. If the read now fails,
the choice is made again from what the read found, as a load that remembered
nothing would make it.

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
