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
  line with `id`, `repository`, `number`, `split` (`calibration` or
  `held_out`), `representative`, `primary`, `size` (`xs`, `s`, `m`, `l`, `xl`)
  and `session_vendors` (`claude_code`, `codex`).

Nothing is written into the dataset, and no dataset content belongs in git.
Dataset paths are never in the repository: pass them with `--dataset` or
`BAUM_REPLAY_DATASET`.

## Running

`replay` runs the developer's installed `claude` or `codex` command with their
own login, as the desktop app does, so run it where those are installed and
signed in. A container has neither. Build the binary in the `oss-test`
container if the host's Go cannot build the module, and run the binary on the
host:

```sh
just oss-go build -o /tmp/replay ./cmd/replay      # inside the container
/tmp/replay --dataset <dir> --out <dir> --split calibration --limit 3 --max-cost-usd 5
```

```sh
replay --dataset <dir> --out <dir> [selection] [settings] [guards]
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
| `--limit N`, `--shuffle-seed N` | Cut the selection, after a seeded shuffle if given. |

Settings (the defaults are the desktop app's: Claude Code, its own model and
effort, a staged review, ten minutes per case):

| Flag | Meaning |
| --- | --- |
| `--backend claude\|codex`, `--model`, `--effort` | The agent. Empty model and effort are the agent's own. |
| `--staged` (default), `--no-staged` | Write the review in stages, or in one pass. |
| `--prompt-override <id>=<file>` | Replace a staged prompt with the file's text; repeatable. The ids are `review-staged-shared`, `review-staged-outline`, `review-staged-details` and `review-staged-context`. The text replaces the built-in text before the `Submitting` heading, which stays baum's, exactly as a custom prompt does in the app. |
| `--time-limit 10m` | Time limit of one case. |
| `--no-transcript` | Do not keep the agent's transcript. |

Guards and control:

| Flag | Meaning |
| --- | --- |
| `--concurrency N` | Cases run at once; default 1. |
| `--max-cost-usd X` | Stop starting cases once the priced cost so far reaches X. Cases in flight finish, so the total can pass X by up to `--concurrency` cases. A run whose backend reports no usage has no cost to count. Default: no guard. |
| `--max-cases N` | Refuse a selection of more than N cases (after `--resume` skips); default 20, 0 is no guard. |
| `--name <run>` | The run's directory name; default is the start time. |
| `--resume` | With `--name`, skip the cases whose outcome there succeeded and run the rest. |
| `--dry-run` | List the cases that would run, checking that each loads, and call no model. |

`--out` defaults to `replay-runs` in the working directory (or
`BAUM_REPLAY_OUT`). That directory is git-ignored; outputs hold private
content and are never committed.

## What is written

```text
<out>/<run>/manifest.json         settings, backend and version, prompt version and digest,
                                  override hashes, caps, the tool's git commit, the case ids
                                  and the SHA-256 of the dataset index
<out>/<run>/summary.json          one row per case, totals, failures by reason
<out>/<run>/cases/<id>/document.json    the generated document (successful cases)
<out>/<run>/cases/<id>/outcome.json     status, reason, wall time, calls, tokens, usage, cost,
                                        repairs, failed sections, per-call timing, validation
<out>/<run>/cases/<id>/diagnostics.json the pipeline's redacted diagnostics (failures too)
<out>/<run>/cases/<id>/progress.log     the pipeline's progress lines
<out>/<run>/cases/<id>/transcript.jsonl the agent's steps
```

`outcome.json` is written last, so a case without it did not finish. A case that
fails to load or generate is a row with a reason (`case`, `backend`, `budget`,
`validation`, ...) and the run goes on. The table printed at the end has the
same columns as `summary.json`.

A cost is the price list's cost of the reported usage. It is empty when the
backend reported no usage or the model is not priced, and totals leave such
cases out.

## Previous versions

A case's bundle may carry the previous review of the pull request (`previous`),
which the model reads as it did in the app. The app reuses a previous
document's section details only when it completes a saved version, which a
replay never does, so every section is generated.

## Adding another kind of output

The runner builds the production `generate.Options` and hands them to a
`Generator` (`ReviewGenerator` runs `generate.Run`). Another kind of generation
implements the same interface and is set in `Settings.Generator`; the
selection, guards, files and summary do not depend on the document's format.

## Tests

The package's tests build a synthetic dataset and run the real pipeline with a
fake backend. `just oss-replay-dataset <dir>` loads every case of a real dataset
through the loader and assembler (no model) and logs counts and case ids only.
