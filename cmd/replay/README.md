# replay

`replay` runs baum's review generation again on frozen inputs, called cases,
and saves what it generates. It needs no GitHub access. Use it to try a prompt
or pipeline change on real pull requests repeatedly and compare the outputs.

It is developer tooling, not part of the `baum` binary. It runs the production
pipeline (`internal/generate`): only assembly differs, because the bundle and
the checkout come from the case instead of GitHub and a clone. Backend
selection, budgets, prompts, repair, validation, redaction and pricing are what
the desktop app uses.

## What a dataset holds

`--dataset` names a directory with:

- `cases/<id>/bundle.json`: the exact bytes generation reads (the assembled,
  masked and trimmed bundle). Every other file of the case is checked against
  it.
- `cases/<id>/case.json`: `bundle_sha256` (the SHA-256 of the bundle file),
  `head_tree` and `merge_base_tree`. A recorded value that differs from the
  data is an error.
- `cases/<id>/attachments/`: the images the bundle lists, by file name, each
  with the hash the bundle records.
- `checkouts.git`: a bare repository whose refs `refs/cases/<id>/head` and
  `refs/cases/<id>/merge-base` point at commits whose trees are the `head/` and
  `merge-base/` directories the review agent can read.
- `index/cases.jsonl` (optional, needed for the filters): one JSON object per
  line with `id`, `repository`, `number`, `revision`, `split` (`calibration` or
  `held_out`), `representative`, `primary`, `size` (`xs`, `s`, `m`, `l`, `xl`),
  `session_vendors` (`claude_code`, `codex`), `attachments` and `threads` (counts),
  `previous_version` (the number of the previous review the bundle carries, or
  null) and `captured_at`. One pull request can have several cases: one per
  reviewed revision, and the same revision captured twice, of which `primary`
  marks one.

Nothing is written into the dataset, and no dataset content belongs in git.
Dataset paths are never in the repository: pass them with `--dataset` or
`BAUM_REPLAY_DATASET`.

## Running

`replay` runs the developer's installed `claude` or `codex` command with their
own login, as the desktop app does, so run it where those are installed and
signed in. A container has neither. Build the binary in the `oss-test`
container and run it on the host. From the repository root:

```sh
just oss-go build -ldflags "-X github.com/baum-ai/baum/internal/replay.Commit=$(git rev-parse HEAD)" -o bin/replay ./cmd/replay
oss/bin/replay --dataset <dir> --split calibration --limit 3 --max-cost-usd 5
```

`oss/bin/` is git-ignored. The `-X` flag records the tool's commit in the
manifest; without it the manifest has the commit Go recorded, if any, or the
working directory's. `--help` lists every flag.

```sh
replay --dataset <dir> [--out <dir>] [selection] [settings] [guards]
```

Selection (cases are sorted by id; `--shuffle-seed` then shuffles
deterministically, and `--limit` cuts the list):

| Flag | Meaning |
| --- | --- |
| `--case <id>` | Run this case; repeatable. Not combined with filters. |
| `--split calibration\|held_out` | Keep one side of the split. |
| `--representative`, `--primary` | Keep the cases the index marks so. |
| `--size xs,s,m,l,xl` | Keep these size buckets; repeatable. |
| `--with-sessions claude_code\|codex\|none` | Keep cases whose bundle has sessions of that agent, or none. |
| `--with-images`, `--with-threads`, `--with-previous` | Keep cases that have images, review threads or a previous version. Each is yes or no: `--with-images` or `--with-images=true` keeps cases with images, `--with-images=false` those without, and leaving it out keeps both. |
| `--detector schema\|api\|any\|none` | Keep cases a structural detector matches, either one (`any`) or neither (`none`); see [Detectors](#detectors). |
| `--case-file <path>` | Case ids from a file, one per line; blank lines and `#` comments are ignored. Same as repeating `--case`, and not combined with filters. |
| `--chain` | Replay each pull request's revisions in order; see [Chains](#chains). |
| `--limit N`, `--shuffle-seed N` | Cut the selection, after a seeded shuffle if given. |

Settings. The defaults are the desktop app's built-in ones: Claude Code, the
agent's own model and effort, a staged review, ten minutes per case. Replay
does not read the developer's baum configuration or the desktop's preferences,
so a run does not depend on them; the backend is `claude` unless named.

| Flag | Meaning |
| --- | --- |
| `--backend claude\|codex`, `--model`, `--effort` | The agent. Empty model and effort are the agent's own. `--model` takes what the agent takes, such as an alias like `sonnet`. |
| `--staged` (default), `--no-staged` | Write the review in stages, or in one pass. `--no-staged` wins over `--staged`. |
| `--reuse` | See [Previous versions](#previous-versions). |
| `--prompt-override <id>=<file>` | Replace a staged prompt with the file's text; repeatable. The ids are `review-staged-shared`, `review-staged-outline`, `review-staged-details` and `review-staged-context`. The text replaces the built-in text before the `Submitting` heading, which stays baum's, exactly as a custom prompt does in the app. Needs a staged run. |
| `--time-limit 10m` | Time limit of one case. |
| `--claude-config-dir`, `--codex-home`, `--agent-env KEY=VALUE` | The config fields the app sets for its agents (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `agent_env`); repeatable for `--agent-env`. Without them the agent runs with the ambient login. |
| `--no-transcript` | Do not keep the agent's transcript. |

The agent is started with the app's environment filter: a fixed allowlist of
variables passes, and provider API keys and tokens in the environment are
dropped (even when `--agent-env` names them), so a run uses the developer's plan
login, not an API account.

Guards and control:

| Flag | Meaning |
| --- | --- |
| `--concurrency N` | Cases run at once; default 1. |
| `--max-cost-usd X` | Stop starting cases once the priced cost so far, cases a `--resume` skips included, reaches X. A case that made calls but has no cost (the backend reported no usage, or the model is not priced) also stops the run, with the reason printed, because the guard cannot be enforced. Cases in flight finish, so with concurrency N the total can pass X by up to N cases. Default: no guard. |
| `--max-cases N` | Refuse a selection of more than N cases (after `--resume` skips); default 20, 0 is no guard. |
| `--name <run>` | The run's directory name; default is the start time. |
| `--resume` | With `--name`, skip the cases whose outcome there succeeded and run the rest. It refuses when the saved backend, model, effort, generator settings (staged, reuse, prompt version and digest, override hashes) differ from the current ones, unless `--resume-anyway` is given. |
| `--dry-run` | List the cases that would run, checking that each loads, and call no model. It also prints how many cases pass the index filters, how many of them have images, threads and a previous version in each combination, how many each detector matches, and how many pass every filter: counts only. |

Exit status: zero when every selected case succeeded (and for `--help` and
`--dry-run`); non-zero when any case failed or was not run, after the summary is
written, and when the run is interrupted with Ctrl+C. An interrupted case
leaves no files, so `--resume` runs it again.

`--out` is `baum/replay-runs` under the user cache directory by default (or
`BAUM_REPLAY_OUT`), which is outside any repository. Outputs hold private
content and are never committed.

## Detectors

A detector is a small pure function over a case's diff (`detect.go`), so a
selection can pick cases where a specialised view of the review would apply. Both
look at hunks outside tests, docs, generated and vendored files, and at the
lines a hunk adds or removes.

- `schema`: a DDL statement (`create`, `alter` or `drop` of a table, index, view,
  type, trigger, function and so on, or `add column`), a migration, or a `.sql`
  file (which covers `queries/*.sql`).
- `api`: an HTTP route registration (`HandleFunc`, `Get`/`Post`/... with a path,
  a `@router.get` decorator, `.route("/...")`), an agent tool definition
  (`NewTool`, `AddTool`, `input_schema`, `@tool`), or a published JSON schema
  file (a `.json` file under a `schema` or `schemas` directory, `*.schema.json`,
  OpenAPI and Swagger files).

They are heuristics for stratified selection, not a classification. `--detector`
reads each candidate's bundle, so it takes a moment on a whole dataset.

## Chains

With `--chain`, a pull request's revisions are replayed in order, and each reads
the document the previous replayed revision produced as its previous version
instead of the one frozen in its bundle. A generator's identifiers can then be
compared across versions of its own output, which no captured case allows: the
frozen previous versions are the app's output from other prompts and code.

```sh
replay --dataset <dir> --split calibration --chain --with-previous --limit 4 --model sonnet --max-cost-usd 6
```

- **Which cases.** The filters choose pull requests: a pull request is taken when
  it has two or more primary cases (one per revision) and at least one of them
  passes every filter, and then all its primary cases run. `--with-previous` thus
  selects pull requests with a later revision. `--limit` counts cases and takes
  whole chains, skipping one that no longer fits. `--case` and `--case-file`
  name the revisions exactly (they are grouped by pull request and ordered).
- **Order.** A revision comes after those with a lower previous version number
  in their bundle (the version of the review the app held when it captured the
  case), and among equal numbers after the one captured earlier; the case id
  settles the rest. The bundle's own number is used first because it is how the
  app counted versions, where capture times can tie or differ by machine; the
  capture time breaks ties.
- **Handoff.** The first revision keeps its bundle, frozen previous version
  included. For each later one, the generator turns the previous revision's
  document into the previous version as the app does (`review.PreviousVersionOf`)
  and moves its hunk anchors to where the hunks sit in this revision's diff
  (`review.FitPreviousHunkAnchors`, which assembly runs). The case's bundle is
  encoded again with it, so the digest in `outcome.json` is that of the bundle the
  model read. The version number is the previous revision's position in the
  chain. Everything else in the bundle is the capture's. With `--reuse`, details
  are reused as in the app, including when the images are unchanged, because the
  carried version knows which images it was offered.
- **Failure.** A revision that fails ends its pull request's chain: the later
  ones are skipped (reason `chain`) and `chain.json` says why. Other pull requests
  go on. `--resume` keeps the leading revisions that succeeded and replays the
  rest, the first of them reading the stored document of the last kept.
- **Generators.** A generator that can chain implements `Chainer` (the review
  generator does). The runner and driver do not know the document's format.

## Metrics

`replay metrics <run-dir>` reads a finished run, calls no model, prints the
metrics and writes `metrics.json` into the run directory (`--no-write` prints
only).

```sh
replay metrics ~/.cache/baum/replay-runs/20261114T101500Z
replay metrics <new-run> --compare <baseline-run>
```

Every generator has the runner's metrics, per case and aggregated (count, sum,
mean, median, p90, min, max) over all cases, by size bucket and by the agent of the
case's sessions (`claude_code`, `codex`, `both`, `none`; the manifest records
each case's size and sessions): status and failure reasons, `wall_seconds`,
`calls`, `tokens`, `cost_usd` (cases with no cost are left out), `repairs`,
`failed_parts` (the outcome's notes, failed sections for a review),
`validation_findings` and `document_bytes`.

A generator adds its own through the `Meter` interface, registered under the
generator's name with `RegisterMeter` (the manifest names the generator, so
`replay metrics` finds it). For the review generator, per document:

- `sections`, `sections_without_hunks`;
- `risks`, `risks_active`, `risks_active_<tier>` (`critical`, `high`, `medium`,
  `low`), `risks_mitigated`;
- `evidence` and `evidence_<status>`;
- `hunks`, `hunks_covered`, `hunks_unassigned`, `hunks_covered_ratio`;
- `risk_lifecycle_violations`: risks whose state contradicts itself (active with a
  mitigated version, mitigated without one, listed in two sections in different
  states, or an unknown state).

For a chain run, each pair of consecutive revisions that both have a document is
measured (`pairs` in `metrics.json`; the run also wrote the pair's record next to
`chain.json`):

- `sections_matched`, `section_ids_retained`, `section_id_retention`: each section
  of the earlier version that shares hunks with a section of the later one is
  matched to the later section it shares most hunks with (the same ID wins a tie);
  retention is the share of matches that kept the ID;
- `sections_dropped`, `sections_added`, `sections_previous`, `sections_next`;
- `risks_carried`, `risks_newly_mitigated`, `risks_introduced`, `risks_vanished`
  and the next version's `risk_lifecycle_violations`.

`section_id_retention_pooled` (derived, over all matched sections of all pairs) is
the figure to follow; the per-pair statistics show the spread. A pair with no
shared hunks has no retention figure.

`--compare <other-run>` aligns the two runs by case id and prints, for the cases
both completed, each metric's change (this run minus the other): the sums, the
number of cases that changed, and the mean, median, p10 and p90 of the per-case
difference and the median ratio. Pass the baseline to `--compare` and the new run
first. It also lists cases only one run has and status changes, and counts the
cases whose generator metrics (all but cost, time and the runner's counts) are
equal in both runs: when that is all of them, the analysis the metrics see is
unchanged. It writes `compare.json`.

## What is written

```text
<out>/<run>/manifest.json         settings, backend and version, the generator's settings (prompt
                                  version and digest, override hashes, staged, reuse), caps, the
                                  tool's git commit, the case ids and the SHA-256 of the dataset index
<out>/<run>/summary.json          one row per case, totals, failures by reason
<out>/<run>/cases/<id>/document.json    the generated document (successful cases)
<out>/<run>/cases/<id>/outcome.json     status, reason, wall time, calls, tokens, usage, cost,
                                        repairs, notes, details, per-call timing, validation
<out>/<run>/cases/<id>/diagnostics.json the pipeline's redacted diagnostics (failures too)
<out>/<run>/cases/<id>/progress.log     the pipeline's progress lines, written as the case runs
<out>/<run>/cases/<id>/transcript.jsonl the agent's steps
<out>/<run>/cases/<id>/<artifact>       extra files a generator wrote (see below)
<out>/<run>/chains/<pr>/chain.json      with --chain: the revisions in order, the
                                        status of each, where its previous version
                                        came from, and why the chain ended early
<out>/<run>/chains/<pr>/NN-<id>         with --chain: links to the cases, in order
<out>/<run>/chains/<pr>/pair-NN.json    with --chain: the metrics of revision NN
                                        against the one before it
<out>/<run>/metrics.json, compare.json  written by replay metrics
```

`transcript.jsonl` is the agent's raw output: it is not masked, and holds
whatever the agent read and wrote, including repository text. Treat it as
private. `document.json` and `diagnostics.json` are masked by the pipeline.

`outcome.json` is written last, so a case without it did not finish. A case that
fails to load or generate is a row with a reason (`case`, `backend`, `budget`,
`validation`, `interrupted`, ...) and the run goes on. The table printed at the
end has the same columns as `summary.json`. A cost is the price list's cost of
the reported usage; it is empty when the backend reported no usage or the model
is not priced, and totals leave such cases out. `manifest.json` also records
`chain` and each case's size, split and session agents.

## Agent sessions

Claude Code and Codex keep their own session files. A replay writes them to
the developer's own session stores, exactly as the app's review runs do, and
they are kept out of baum's session discovery the same way: the prompts start
with the generation-run marker and the run directory has the run-directory
prefix, which discovery skips.

## Previous versions

A case's bundle may carry the previous review of the pull request (`previous`),
which the model reads as it did in the app. In the app, a staged run reuses the
previous version's section details for a section whose hunks are unchanged,
unless the bundle offers an image the previous version was not offered.

By default a replay generates every section (the pipeline's `NoReuse`), so a
prompt change is measured on all of them. `--reuse` keeps the pipeline's
behaviour, with one difference: a saved bundle does not record which images the
previous version was offered, so the pipeline treats any image the bundle offers
as new. With `--reuse` details are therefore reused only for a case whose bundle
offers no image; the app also reuses them when the images are unchanged.

## Adding another kind of output

The runner builds the options common to every generator (assembler, backend,
budgets, accounting, progress) and hands them to a `Generator`:

```go
type Generator interface {
	Name() string
	Describe(ctx context.Context, backend string) (map[string]any, error)
	Generate(ctx context.Context, base generate.Options, c Case) (Generated, error)
	Validate(document []byte, c Case) []string
}
```

`Describe` reports the generator's settings and its prompt versions and digests
for the manifest, which `--resume` compares. `Generate` returns the document
bytes, the agent outcome and format-specific notes and details that the runner
only records. `Validate` checks the document against its case. `ReviewGenerator`
(`review.go`) is the only implementation that knows the review format; set
another one in `Settings.Generator`. The selection, guards, files and summary do
not depend on the document's format.

`Generated.Artifacts` maps names to bytes. The runner writes each beside the
document, in the case's directory (a name may have directories, and cannot leave
the directory or be one of the runner's files, or the case fails), and lists them
in `outcome.json` under `artifacts`; a rerun of the case replaces them. A
generator that produces more than one file, such as a layout next to the review
it was composed from, returns the rest this way.

Two optional interfaces add what depends on the format:

- `Chainer` (`Next(c Case, document []byte, version int) (Case, error)`) makes
  `--chain` available: it returns the case with a generated document as its
  previous version.
- `Meter` (`DocumentMetrics` and `PairMetrics`, each returning numbers by name,
  and optionally `Deriver` for figures over sums) makes `replay metrics`
  report the generator's own measures. Register it with
  `RegisterMeter(<generator name>, meter)` at initialisation; the command and the
  aggregation do not change.

## Tests

The package's tests build a synthetic dataset and run the real pipeline with a
fake backend. `just oss-replay-dataset <dir>` loads every case of a real dataset
through the loader and assembler (no model) and logs counts and case ids only.
