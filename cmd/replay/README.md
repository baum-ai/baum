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
| `--generator`, `--generator-option KEY=VALUE` | What to generate (`review`, the default), by name from the generator registry, and options for generators that take them; see [Adding another kind of output](#adding-another-kind-of-output). |
| `--detector-report` | Write the case ids by kind of detector evidence; see [Detectors](#detectors). |
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

An argument that is not a flag is refused: the flag package would stop reading
flags at it and ignore the rest, so `--with-images false --dry-run` would not be
a dry run. Write a boolean flag's value as `--with-images=false`.

Exit status: zero when every selected case succeeded (and for `--help` and
`--dry-run`); non-zero when any case failed or was not run, after the summary is
written, and when the run is interrupted with Ctrl+C. An interrupted case
leaves no files, so `--resume` runs it again.

`--out` is `baum/replay-runs` under the user cache directory by default (or
`BAUM_REPLAY_OUT`), which is outside any repository. Outputs hold private
content and are never committed.

## baum.review/2 in levels

`--generator v2` writes each case as a baum.review/2 document, as the desktop
does with tile reviews on once the levels land (`generate.Options.V2`,
`agent/orchestrate`): the map, then the section agents and the context agent at
once, each tile once its section is written, and the overview. Every section is
written; nothing is reused unless you pass `--reuse`, which lets the
orchestration reuse a previous version's unchanged sections as the app does (it
needs a previous version, so it matters for chains). For v2 it also turns on
revising changed sections from their previous text, keeping unchanged tiles and
revising the previous overview. Without `--reuse`, a v2 chain's previous
document only feeds risk history and section state, and every section and the
overview are written fresh. `--generator-option concurrency=N` bounds a case's
calls at once (default 4). Prompt overrides take the ids `review-v2-shared`,
`review-map`, `review-section`, `review-context-v2`, `review-tile` and
`review-overview`.

```sh
oss/bin/replay --dataset <dir> --generator v2 --case <id> --name v2-first --max-cost-usd 20
```

A case has no clone and no daemon, so its calls run over the unpacked
`head/` and `merge-base/` trees and `bundle.json`, without baum's MCP server:
what a backend without MCP gets. Claude Code has its sandboxed shell where the
machine can enforce the sandbox (bubblewrap and socat on Linux, macOS);
`progress.log` says when it does not.

The case's `document.json` is the completed document. Beside it:

| File | Holds |
| --- | --- |
| `calls/<level>/<id>/prompt.md`, `input.txt` | A call's instructions (with the tools and workspace sections) and the data after them. The levels are `map`, `sections`, `context`, `tiles` and `overview`; the id is the section's or tile's. |
| `calls/<level>/<id>/reply.json`, `reply-2.json`, `findings-N.txt` | Each pass's reply, masked, and what the host found in it when it sent it back. A library tile's reply is its fill's data. |
| `levels.json` | A `replay.v2/1` record: calls, tokens, repairs, seconds and `cost_usd` per level, the calls that ran, failed, were reused or skipped, the sections among those that ran that kept or revised their previous version's text (`kept`, `revised`), and the document's generation record. |

Validation checks the schema and that every hunk is in a section.
`replay metrics` reports, from the document, `v2_sections`,
`v2_logical_sections`, `v2_sections_failed`, `v2_tiles`, `v2_tiles_ready`,
`v2_tiles_failed`, `v2_tiles_type_<type>`, `v2_blocks`, `v2_points`, `v2_risks`,
`v2_context_missing` and `v2_overview_failed`; from `levels.json`
`v2_repairs`, `v2_sections_kept` and `v2_sections_revised` (sections whose
hunks changed and whose agent kept or revised the previous version's text),
and per level `v2_<level>_calls`, `_tokens`, `_seconds`, `_failed` and
`_cost_usd`.

With `--chain`, each revision after the first reads the baum.review/2 document
the one before it generated as its previous version (`generate.V2.Previous`,
with that document's own `review.version`), as `generate.Publish` hands it to a
run in the app. The bundle of a later revision carries no previous version of
its own for v2. The first revision has no previous document, though its
bundle's frozen previous version, if any, still feeds risk history and section
state when the document is completed. Each pair of consecutive documents
reports how stable the review stayed:

- **Section fate.** `v2_sections_previous`, then each earlier section is counted
  once: `v2_sections_reused` (same id, same hunks, identical body),
  `v2_sections_rewritten` (same id, anything else), `v2_sections_matched` (no same
  id, but a later section matches by code: most shared hunks, then shared file
  paths, as for the review generator) or `v2_sections_dropped`.
  `v2_sections_new` counts later sections that no earlier section paired with.
- **Body text.** For every same-id and matched pair, `v2_body_similarity_sum` adds
  the word-level Jaccard index of the two bodies' text (block titles, text, each
  point's text and `more`; two empty bodies count 1), and `v2_body_pairs` counts
  the pairs, so the mean is the sum over the pairs.
- **Facts.** `v2_facts_previous` counts the facts of the earlier bodies of those
  pairs (bold spans, code spans and numbers such as `24`, `5,000` and `3.5`, each
  once per body) and `v2_facts_kept` those that the later body still has verbatim.
  A number must match whole, not as part of another number, and a span must not
  sit inside a longer word (`4` is not kept by `14`, `id` not by `provide`). A
  bold span and the numbers inside it are separate facts, so both count.
- **Changed pairs.** Reused sections are the same by construction and pull the
  figures above toward 1. `v2_changed_body_similarity_sum`, `v2_changed_body_pairs`,
  `v2_changed_facts_previous` and `v2_changed_facts_kept` are the same four
  figures over the rewritten and matched pairs only; they are the headline
  numbers for how stable a rewrite kept the review.
- **Pairing.** A later section pairs with at most one earlier section: same ids
  first, then the earlier sections without one match among the later sections
  still unpaired. An earlier section that finds none is dropped; a later section
  nothing paired with is new.
- **Summary and overview.** `v2_summary_similarity` is the word-level Jaccard index
  of the two summaries' title, text, `risk_why` and verdict text and points, and
  `v2_overview_similarity` the same over the intents' `asked` and `got`. Each is
  left out when both texts are empty.

`v2_body_similarity_pooled`, `v2_fact_retention_pooled`, `v2_section_reuse_pooled`,
`v2_section_dropped_pooled` and, over changed pairs, `v2_changed_body_similarity_pooled`
and `v2_changed_fact_retention_pooled` are ratios over the sums of a group (kept over
earlier facts, reused or dropped over earlier sections), not the mean of each
pair's.

## Detectors

A detector is a small pure function over a case's diff (`detect.go`), so a
selection can pick cases where a specialised view of the review would apply.
They are heuristics for stratified selection, not ground truth: they miss
changes written in ways they do not know, and some matches are not changes of
that kind. `--dry-run` says so and counts what each match rests on. Both skip
tests, docs, generated and vendored hunks and comment lines, and read the lines a
hunk adds or removes.

- `schema`: a migration or a `.sql` file (which covers `queries/*.sql`), kind
  `schema:path`; or a DDL statement in a changed line, kind `schema:ddl`. A
  statement reads as one (`create table runs (`, `alter table runs add`,
  `drop index if exists x;`), so prose and labels with the same words do not match.
- `api`: a published JSON schema file (a `.json` file under a `schema` or
  `schemas` directory, `*.schema.json`, OpenAPI and Swagger files), kind
  `api:schema-file`; a Rails `routes.rb`, kind `api:routes-file`; a route
  registration, kind `api:route` (`Handle` and `HandleFunc` with a path,
  `Get`/`Post`/... with a path on a receiver named like a router, mux, app,
  server or group but not a client, `.Route`, `.Mount`, `@app.get` decorators,
  Spring mapping annotations); or an agent tool definition, kind `api:tool`
  (`NewTool`, `AddTool`, `@tool`, and `input_schema` beside a tool `name`).

The path kinds rest on a file's name. The text kinds rest on a regular
expression over changed lines and are where false positives and misses are:
`--dry-run` prints how many matches have no path kind (`text alone`), how many
cases have no patch text at all in any hunk (`blind`: the patch text was cut or
never there, so only paths could match) and the count per kind.

`--detector-report` writes, into `<out>/<name>/detectors/`, one file of case ids
per kind (`schema-path.txt`, `schema-ddl.txt`, `api-route.txt`, ...,
`schema-text-only.txt`, `api-text-only.txt`, `blind.txt`, `none.txt`) for the cases
that pass the index filters, so a person can label a sample of each. Ids only; it
never leaves the output directory.

`--detector` reads each candidate's bundle, so it takes a moment on a whole dataset.

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
  it has two or more revisions and at least one of its primary cases passes every
  filter; all its revisions are then replayed, one primary case each (when the
  index marks several primary cases of one revision, the earliest captured wins,
  then the lowest id). `--with-previous` thus selects pull requests with a later
  revision, and a larger sibling revision can come with a small one. `--limit`
  counts cases and takes whole chains; a chain that does not fit is left out, and
  when none fits the error says so. `--max-cases` counts cases too, so it also
  limits whole chains. `--case` and `--case-file` name the revisions exactly (they
  are grouped by pull request and ordered). `--dry-run` prints the number of
  eligible chains and revisions, the chains and their order.
- **Order.** A revision comes after those with a lower previous version number in
  their bundle (the version of the review the app held when it captured the case;
  none counts as 0), and among equal numbers after the one captured earlier
  (capture times are parsed as RFC 3339 instants, so offsets count; one that does
  not parse sorts first); the case id settles the rest. The bundle's own number is
  used first because it is how the app counted versions, where capture times can
  tie or differ by machine. A revision with no number that follows one that has a
  number, or that was captured before the one ordered ahead of it, is listed in
  `chain.json` and the dry run as a guess.
- **Handoff.** For the review (v1) generator, the first revision keeps its
  bundle, frozen previous version included. For each later one the generator
  turns the previous revision's document into the previous version as the app
  does (`review.PreviousVersionOf`, with the document's own `review.version`,
  which the pipeline set above the version it was given), and moves its hunk
  anchors to where the hunks sit in this revision's diff
  (`review.FitPreviousHunkAnchors`). The runner then does what assembly does to
  a bundle it has built: masks it (`bundle.Redact`, which covers the carried
  content), fits it to the evidence byte budget (`bundle.Fit`, whose budget
  counts the previous version, so a larger one trims other evidence and adds
  notes to `truncated`, or fails the revision with the app's `evidence byte
  budget exceeded` as an `input` failure), and stamps the digest. The outcome
  records `bundle_bytes` (the encoded size the model read), `bundle_fitted`
  (fitting cut something) and `bundle_notes`; `chain.json` records them per
  revision with the version number carried. Everything else in the bundle is the
  capture's. With `--reuse`, details are reused as in the app, including when
  the images are unchanged, because the carried version knows which images it
  was offered. For v2, `V2Generator.Next` clears the bundle's previous version
  and hands the previous revision's document over as the generation's previous
  version, as the app does. The first revision has no previous document, though
  its bundle's frozen previous version still feeds risk history and section
  state through completion (`reviewv2.Complete`).
- **Failure.** A revision that fails ends its pull request's chain: the later
  ones are skipped (reason `chain`) and `chain.json` says why. Other pull requests
  go on. The cost guard is checked before each revision. `--resume` keeps the
  leading revisions that succeeded and replays the rest, the first of them reading
  the stored document of the last kept; a rerun clears the chain's links and the
  pair records of the revisions it runs again.
- **Generators.** A generator that can chain implements `Chainer` (the review
  and v2 generators do). The runner and driver do not know the document's format.

## Metrics

`replay metrics <run-dir>` reads a finished run, calls no model, prints the
metrics and writes `metrics.json` into the run directory (`--no-write` prints
only).

```sh
replay metrics ~/.cache/baum/replay-runs/20261114T101500Z
replay metrics <new-run> --compare <baseline-run>
```

Every generator has the runner's metrics, per case and aggregated (N, sum, mean,
median, p90, min, max) over all cases, by size bucket and by the agent of the
case's sessions (`claude_code`, `codex`, `both`, `none`; the manifest records
each case's size and sessions). The statistics are over the cases that succeeded:
a failed case has no document and its time and cost stop where it failed, so the
cases that ran and failed are reported apart, with the count by reason. The
metrics are `wall_seconds`, `calls`, `tokens`, `cost_usd` (a case with no cost is
left out of its statistics, never counted as zero), `repairs`, `failed_parts` (the
outcome's notes, failed sections for a review), `validation_findings` and
`document_bytes`. Every statistic shows its N, and an N below 5 is marked: read it
as an example.

A generator adds its own through the `Meter` interface, registered under the
generator's name with `RegisterMeter` (the manifest names the generator, so
`replay metrics` finds it; a second registration of a name panics, and tests use
`UnregisterMeter`). A `Definer` gives each metric's meaning, which the report
prints. Every count is present for every case, as zero when there is none, so a
mean is over all cases; only a ratio with a zero denominator is absent. For the
review generator, per document:

- `sections`, `sections_without_hunks`; `risks`, `risks_active`,
  `risks_active_<tier>` (`critical`, `high`, `medium`, `low`), `risks_mitigated`;
  `evidence` and `evidence_<status>` for every status;
- `hunks`, `hunks_covered`, `hunks_unassigned`, `hunks_covered_ratio`: coverage is
  the hunks some section lists over the hunks in the document's index; a fitted
  or collapsed index (large pull requests) varies that denominator between cases;
- `risk_lifecycle_violations`: risks whose state contradicts itself (active with a
  mitigated version, mitigated without one, an unknown state, listed in two
  sections in different states) or whose versions are impossible (above the
  document's own `review.version`, or mitigated before it was introduced).

For a chain run, each pair of consecutive revisions that both have a document is
measured (the run also wrote the pair's record next to `chain.json`). The first
pair of each chain is aggregated apart from the later ones: its earlier document
was generated from a previous version other code wrote, while a later pair has
the generator's own output on both sides. Each chain also reports its own totals
and derived figures. The keys:

- **ID survival.** `ids_previous`, `ids_survived`, `ids_unmatched`, `id_survival`:
  the share of the earlier version's section IDs that the later one still has.
  For the review generator the validator already forces earlier IDs to stay, in
  order and never removed, so survival is 1 by construction and is a sanity check
  there; it is meant to discriminate for generators without that constraint.
- **Content continuity of surviving IDs.** `survived_matched_hunk` (the later
  section shares a hunk ID with the earlier one), `survived_matched_path_only`
  (no shared hunk, but a shared file path, as when a hunk's lines changed, which
  changes its ID, or its file was renamed), `survived_no_overlap`, and
  `survived_empty` (the earlier section had no hunks, so it has nothing to
  continue); `continuity` is (hunk + path only) over (survived - empty).
- **Reverse matching.** Each later section is matched to the earlier section it
  resembles most: most shared hunks, then most shared paths, then a similar title
  (at least half of the words in common). `reverse_matched`, `reverse_same_id`,
  and `reverse_id_agreement` (whether the ID follows the content: the share of
  matched later sections carrying the matched earlier section's ID),
  `sections_new` (later sections matching nothing), `sections_dropped` (earlier
  sections matching nothing later), `splits` (earlier sections that two or more
  later sections match best) and `merges` (later sections that two or more earlier
  sections match best).
- **Risk lifecycle.** `risks_carried` (same state in both), `risks_newly_mitigated`
  (active, then mitigated), `risks_reopened` (mitigated, then active),
  `risks_introduced`, `risks_vanished` (active before, absent now),
  `risk_introduced_changed` (a carried risk whose `introduced_version` changed),
  and the later document's `risk_lifecycle_violations` plus the changed ones.

`id_survival_pooled`, `continuity_pooled`, `reverse_id_agreement_pooled` and
`hunks_covered_ratio_pooled` are ratios over the sums of a group (every matched
section of every pair), not the mean of each pair's.

`--compare <baseline-run>` aligns the run with the baseline by case id and
prints, for the cases both completed and for every metric either has, the change
(the run minus the baseline): N (cases that have it in both), how many have it in
one only, how many changed, the sums, and the mean, median, p10 and p90 of the
per-case difference and the median ratio. It lists cases only one run has and
status changes, and warns when the runs differ in dataset, selection, generator,
model or chain mode, since the deltas then mix effects. A case counts as
identical when both runs completed it, its generator metrics decoded in both, and
every metric that is not the runner's (cost, time, calls, tokens, repairs, size)
is present in both with exactly equal values; when that is all of the completed
cases the analysis the metrics see is unchanged. It writes `compare.json` with
`run` and `baseline` fields.

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
offers no image; the app also reuses them when the images are unchanged. For
v2, `--reuse` instead turns on revising and keeping from the previous document
(see "baum.review/2 in levels").

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
the directory, be one of the runner's files, be the same file as another name
ignoring case, or be both a file and a directory that holds another artifact, or
the case fails), and lists them
in `outcome.json` under `artifacts`; a rerun of the case starts its directory over. A
generator that produces more than one file, such as the v2 generator's calls and
levels record, returns the rest this way.

Two optional interfaces add what depends on the format:

- `Chainer` (`Next(c Case, document []byte) (bundle.Bundle, error)`) makes
  `--chain` available: it returns the case's bundle with a generated document
  carried as its previous version; the runner masks and fits it.
- `Meter` (`DocumentMetrics` and `PairMetrics`, each returning numbers by name,
  and optionally `Deriver` for figures over sums) makes `replay metrics`
  report the generator's own measures. Register it with
  `RegisterMeter(<generator name>, meter)` at initialisation; the command and the
  aggregation do not change.

A generator also registers a constructor with `RegisterGenerator(name, build)`,
which receives the command line's `GeneratorConfig` (staged, reuse, prompt
overrides, and `--generator-option` values); `replay --generator <name>` then
selects it with no change to `cmd/replay`. The review generator registers itself
as `review`, the default.

## Tests

The package's tests build a synthetic dataset and run the real pipeline with a
fake backend. `just oss-replay-dataset <dir>` loads every case of a real dataset
through the loader and assembler (no model) and logs counts and case ids only.
