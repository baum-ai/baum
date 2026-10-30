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
| Codex | Existing tool inputs, results and commit evidence | Normalize code-mode shell commands, attach child reports and detail metadata |
| Cursor | Existing payload decoder and error wording | Validate uploaded tree nodes and retain hosted error wording |
| Portable and compact tools | Existing tool aliases and patch-line limits | Lowercase `grep`/`bash` aliases and patch scanning past long lines |
| Entire reader | Existing checkpoint parsing | Bound session entries, reject repeated sources, discover tasks and read Pi `toolCall` blocks |
| Entire mapper | Recorded source hash and existing metadata token handling | Hash mapped source bytes, graft Claude tasks, reject negative aggregate usage and fill Cursor models |
| Reports | Existing local fields | Include hosted task and rejected-usage accounting, even when zero |
| Collection | Local Git environment, Windows path folding and modification-time filtering | Repository-service snapshots and case-sensitive Claude project-directory matching |
| Archives | Existing local errors and byte filesystem | Preserve hosted sentinel errors and directory discovery for task transcripts |

These differences preserve current product contracts. They are not upgrade
paths, and neither profile changes the local application's database.

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
