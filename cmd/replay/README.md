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
| `--dry-run` | List the cases that would run, checking that each loads, and call no model. |

Exit status: zero when every selected case succeeded (and for `--help` and
`--dry-run`); non-zero when any case failed or was not run, after the summary is
written, and when the run is interrupted with Ctrl+C. An interrupted case
leaves no files, so `--resume` runs it again.

`--out` is `baum/replay-runs` under the user cache directory by default (or
`BAUM_REPLAY_OUT`), which is outside any repository. Outputs hold private
content and are never committed.

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
```

`transcript.jsonl` is the agent's raw output: it is not masked, and holds
whatever the agent read and wrote, including repository text. Treat it as
private. `document.json` and `diagnostics.json` are masked by the pipeline.

`outcome.json` is written last, so a case without it did not finish. A case that
fails to load or generate is a row with a reason (`case`, `backend`, `budget`,
`validation`, `interrupted`, ...) and the run goes on. The table printed at the
end has the same columns as `summary.json`. A cost is the price list's cost of
the reported usage; it is empty when the backend reported no usage or the model
is not priced, and totals leave such cases out.

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

## Tests

The package's tests build a synthetic dataset and run the real pipeline with a
fake backend. `just oss-replay-dataset <dir>` loads every case of a real dataset
through the loader and assembler (no model) and logs counts and case ids only.
