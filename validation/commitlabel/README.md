# Commit labels for validation

`Compute` relates a validation item's cited tool calls to a PR's reviewed head,
without a model. It is shared by local and hosted code and reads Git only
through `Repository.ValidationCommit`. It does not load sessions or assemble
review documents; hosts supply its evidence and the later bundle integration
consumes its labels.

## Inputs and rule

Supply the complete session-tree timeline for the target repository, including
nested agents. `Call.ID` is the stored tool-use ID. A call interval starts at
the tool-use timestamp and ends at its tool-result timestamp. An edit interval
covers the entire owning tool call, not merely when it started: file-touch
rows have no timestamp of their own. Supply only file edits, never reads.
Do not replace a missing result timestamp with a start timestamp. An unowned
edit or missing interval remains in the input as unknown evidence. Explicitly
scoped edits in another repository are ignored; an edit with unknown repository
scope prevents a label. Hosts must resolve path/repository ownership before
claiming an edit belongs elsewhere.

Commit references use the existing `created` role and `observed_at` from
captured commit output. `starting`, `checked_out` and `mentioned` refs are not
creation evidence and are ignored. The host supplies the complete PR commit
list oldest first, ending at the reviewed head. That order determines N commits
behind; author/committer dates never substitute for the session's event times.

The latest cited call is chosen by start timestamp, independent of citation
order. Missing IDs/timestamps or a tie at the latest timestamp produce
`unknown`; ties among older calls do not hide a later call. Every edit interval
is compared inclusively with the span covering the whole chosen run and each
commit observation. Overlaps and coincident edit timestamps block a match.
A cited call that edits files itself produces `unknown`.

## Labels

- `head`: no edits in this session separate the run from a session-created
  commit, and Git verifies that commit is the reviewed head or has the same
  root tree. This covers unchanged trees after amends, rebases and squashes.
- `earlier`: the same no-session-edits rule matches a commit in the PR;
  `commit_sha` is canonical and `commits_behind` comes from PR order.
- `uncommitted`: a known session-created commit precedes recorded edits before
  the run, and no session-created PR commit matches that run. Unknown creation
  times or missing objects prevent this conclusion.
- `unknown`: no usable creation evidence, missing Git objects/mirror, ambiguous
  timing/scope/identities, or a matching commit outside the PR with a different
  tree. Lack of PR membership alone never means uncommitted.

Verified head/tree-equivalent matches take priority, then the PR commit nearest
head. A direct head match wins over an equivalent old SHA; equivalent old SHAs
tie-break lexically so input order cannot change the result. Every label has
a method and human-readable reason, including unknowns.

## Limits and Git adapters

The claim is always **no edits in this session**. Other people's edits,
unrecorded shell writes and external working-tree changes are invisible. Tree
equality compares two verified commit objects; it never proves that the run's
working tree was identical. Hosts must not treat the label as proof of passing
tests or verified requirements.

Both the candidate commit and reviewed head must resolve through Git, even
when their input SHA strings are equal. Unique hex prefixes of at least seven
digits are resolved to actual commit objects; string-prefix equality cannot
establish a match. Missing or ambiguous objects produce unknown. A host may
cache immutable object lookups during one labeling batch.
Conflicting trees returned for the same canonical commit also produce unknown.

The local `internal/gitlocal.Repo` adapter reads its private object repository,
never fetches and never resolves hex-looking branch names as commit evidence.
The hosted adapter is bound to one repository and storage epoch; it preserves
the mirror's retained-ref reachability gate. Neither adapter grants session access.

Run `go test -race ./validation/commitlabel` in the development container.
