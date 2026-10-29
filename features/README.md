# features

The rules for grouping a repository's pull requests and agent sessions into
features, without storage: the model calls of the assigner, summarizer and
consolidation, and the pure rules a host applies around them. A host reads
and writes its own store and calls a model through `decisions.Model`.

## Prompts

Each model call package (`assign`, `summary`, `consolidate`) takes its
prompt versions from the host as a `Prompts` value, so a host keeps its own
versions and their names. A released prompt is never edited; a change is a
new version. Each package also embeds published versions, `Public`:

| Package | Published version | Traits |
| --- | --- | --- |
| `assign` | `feature-assign-public-v1`, its batch version `-batch` and seeded form `-batch-2` | may skip a small change; lists each candidate's digest and pull requests |
| `summary` | `feature-summary-public-v1` | the feature call reads the feature's current text; owns its pull request prompt |
| `consolidate` | `feature-consolidate-public-v1` | the merge call sees each feature's digest and pull request summaries |

A version's name is part of what a host keys stored results on (for example
`summary.ContentHash`), so published and hosted results never mix.

## Assigner

`assign` renders one pull request or session and its candidate features,
named `F1`, `F2`, … so the model never copies an id, every field truncated to
its bound. A reply chooses a candidate, starts a new feature with a title of
at most 120 and a description of at most 1000 characters, or, under a
version that skips, leaves a small change in no feature. Anything else is
invalid.

### Batch

A batch version assigns up to 50 pull requests in one call under the rules of
its single-item version: the single-item prompt's definition of a feature and
its `# Choosing` rules, cut out unchanged, set in a frame that describes the
batch input and reply. A batch version's seeded form adds one note on
candidates with no pull requests (proposed features); a rebuild uses it for
calls that offer them. A reply answers every item exactly once; a new feature
is started once and joined only by a later item. A rendered batch over
768 KiB is refused (`ErrBatchTooLarge`) for the caller to split.

## Summarizer

`summary` renders one feature (its pull requests, their reviews, its current
decisions and paths) within a byte bound, leaving out superseded decisions,
review findings, review decisions, pull request descriptions, current
decisions, paths and pull requests, in that order. A reply gives a title,
description, digest and at most three key decisions, each a rendered current
decision. A pull request without a summary gets one from its own call.

## Consolidation

`consolidate` renders a repository's features for a merge call, which groups
duplicates, and one oversized feature's pull requests for a split call, which
partitions them. A reply places every member at most once (merge) or exactly
once (split).

## Decision attribution

`SessionLinks.Attributor` gives each current decision of a session exactly
one feature, or none when the session has none:

1. Pull requests of one feature linked to the session: that feature.
2. Of several features: the pull request whose paths contain most of the
   files the decision cites (`decisions.CitedPaths`: exactly, or as the one
   path the file is a `/`-suffix of); ties to the more dominant feature, then
   the earlier pull request.
3. No cited file in any of them: the dominant feature, the one with most of
   the session's linked commits, ties to the earliest opened pull request.
4. No feature pull request: the feature the session is in on its own.

`Attributor.PullRequest` is the pull request of that feature a decision is
shown under: the one whose paths contain most of its cited files, ties and
no match to the earliest opened.

## Status

`DeriveStatus`: in progress while any pull request is open or there is none
yet, shipped once one merged and none is open, abandoned when all closed
unmerged.

## Large update

`LargeUpdate`: a pull request is assigned again when, with `A` the paths it
was assigned with and `B` those it touches now, at least the given share of
`A ∪ B` and at least the given number of paths are in only one of them.
