# seeddata

Writes a large, invented and deterministic dataset into a home directory,
so the desktop app can be profiled against the same data on every machine
(`just desktop::isolated <name> --seed-data`, see the
[desktop README](../../desktop/README.md)). The real `baum daemon` serves it
with no GitHub and no model. `cmd/seeddata` is its command line; it is
developer tooling, not part of the `baum` binary.

## What it writes

With the defaults (seed 1), in about 50 seconds:

| Part | Where | Default |
| --- | --- | --- |
| Repositories | git clones under `~/src/<owner>/<name>`, origin `https://github.com/<owner>/<name>`, registered and picked in `app.sqlite` | 50, over 6 owners |
| Open pull requests | a branch each, one commit per version, and a row in the discovery cache | 1,200: most under 40 changed lines, a tail up to about 2,200, and one of exactly 5,000 (4,000 lines in four new files and 500 lines of a file replaced) |
| Saved reviews | each clone's `.git/baum/reviews/versions.sqlite`, finished by `review.Complete` as generated reviews are | about 75% of pull requests reviewed, about 1,650 versions; sections per directory, risks, CI evidence, decisions, and a GitHub conversation with a thread on reviewed ones |
| Draft review comments | `ui/review` in `app.sqlite` | 12 pull requests |
| Inboxes | `state/inboxes` in `app.sqlite`, with fixed ids | 20, the most the daemon keeps: the default, filters on what waits, drafts, checks, bots, one per owner and per repository |
| Claude Code sessions | `~/.claude/projects/<cwd>/<id>.jsonl` | 8; the longest has 3,000 prompts, each with a tool call, its result and a reply |
| Codex sessions | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` | 4; the longest has 3,000 prompts |
| Session hook events | `~/.local/state/baum/daemon/events` | a start and an end per session |

It also writes `config.toml` with the offline credential, turns automatic
reviews off (so nothing asks a model), marks the tour seen and the desktop
app's first-run setup done (`desktop.json`), and leaves
`~/.local/state/baum/seeddata.json`: the seed, the counts and a manifest.

Sizes are options (`seeddata.Options`, or the command's flags:
`-repositories`, `-pull-requests`, `-large-diff`, `-inboxes`,
`-claude-sessions`, `-codex-sessions`, `-long-transcript`,
`-draft-comments`). It refuses a home that already has baum data. The
marker is written last, and a generation that fails removes what it wrote
(whole folders it created, and only its own clones and transcripts inside
folders that were there before), so a home holds the whole dataset or none.

## How the daemon serves it offline

The inbox lists what discovery cached from GitHub. Discovery's cache in
`app.sqlite` is partitioned by credential, so the dataset names a fixed,
fake token (`github_token` in `config.toml`, `seeddata.Token`) and writes
each repository's snapshot under it with `discovery.Service.Seed`. That
saves the snapshot as a cache of the format before 3, which discovery
already serves until a full sweep replaces it, with the next poll due in
2100: a background poll answers from the cache without a request. Rows
show as stale, as an inbox does after any failed refresh, because no poll
has succeeded. A refresh the app forces does try GitHub; in the isolated
container a seeded home is offline (an unreachable proxy for everything
but loopback), so it fails at once and the cache stays. Nothing in the
daemon changes for ordinary users: `Seed` is only called here.

The review page reads diffs from the clones (`git diff` between each
version's merge base and head), which exist locally, and GitHub's pull
request panels (checks, comment text) say they could not be read.

## Determinism

Names, file contents, commits, review documents, discovery snapshots,
inboxes, draft comments and transcripts come from a PCG generator seeded
with `Options.Seed` (`math/rand/v2`, stable across Go releases), never from
map order, and every time is counted back from `Options.Epoch`
(2026-11-01T12:00Z), never from the clock. Commits are written with
`git fast-import` with fixed authors and dates, so their SHAs repeat. The
same seed and home path give the same `Result.Digest`, a SHA-256 over the
manifest (commit SHAs, each review version's document, each discovery
snapshot, the inboxes and each transcript's bytes).

Not byte for byte: SQLite files' page layout, the hook record's receive
times, and the session events' times when `SessionsAt` is set. The
Sessions page lists an ended session only for a day after its end, so
`isolated.sh` passes `-sessions-at now`, and a session's start is recorded
at most twelve hours before its end, since the tracker ignores events over a
day old; transcripts themselves keep their epoch times. The transcript page
renders at most 4 MiB of a transcript, so the 3,000-prompt sessions show
their first prompts and say the rest were left out. Absolute paths (clones, transcripts' `cwd`) follow the home.

## Tests

```sh
just oss-go test ./internal/seeddata
```

`TestSameSeedSameData` writes a small dataset twice with one seed and once
with another, `TestTheDaemonReadsIt` reads it back through the registry,
discovery, the review index and the inboxes, and `TestDefaultsMeetTheCriteria`
writes the default dataset and checks it meets the profiling criteria
(skipped with `-short`). Monorepo CI writes the full dataset for generator
changes and full sweeps. `TestAFailedGenerationLeavesNoBaumData` makes the
last step fail and checks nothing of the dataset is left.
