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
* **Invalidation:** a file that grows or is rewritten gets a new key. A file
  that cannot be statted is mapped without the cache.
* **Failures:** a mapping that failed because of the file's content is cached
  as well, so an unchanged broken transcript is not read again. A file that
  could not be opened or read is tried again on the next call.
* **Discovery warnings:** these come from discovery, not the mapper. They are
  not cached, and each call adds its own.
* **Size:** least recently used entries are evicted once the transcript bytes
  held pass `DefaultMapCacheBytes` (64 MiB). `SetMapCacheLimit` changes the
  limit; zero or less turns the cache off.
* **Sharing:** cached graphs are shared between callers, so callers must not
  change a graph `Map` or `Load` returns.
* **Holds:** `HoldMappings` raises the limit to `HeldMapCacheBytes` (256 MiB)
  until it is released. The daemon's background pass holds it while it
  refreshes several pull requests of a clone, so each session is decoded once
  in the pass.

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
