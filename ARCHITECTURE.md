# Architecture

baum turns a pull request into a `baum.review/1` document and renders it. The
same packages run in two hosts: the `baum` binary on a developer's machine,
and the hosted baum service, which imports them from this module. Both hosts
support every field in the schema; they differ only in where evidence comes
from and what they do with the result.

The dependency runs one way. The hosted service imports these packages, but
the `baum` binary never uses hosted baum data and never contacts a baum server:
everything it reads comes from the user's machine, GitHub (with the user's own
token) or the model provider the user configures. The one other contact is the
public, signed price list described under "Running locally".

This file describes the package boundaries and the extension points a host
implements. The packages are being moved here in stages: so far `bundle` holds
the evidence types, the diff index, the `REVIEW.md` loader, reference
extraction, the code checkout archive, description image handling, and the
caps, input redaction, byte trimming and digest applied to a bundle,
`review` holds the document model, validation, history, refresh, output
redaction and the tile catalogue, and `agent` holds the backend interface,
request, result, usage and event types, the budgets and the local environment
allowlist; the Go side of `viewer` only embeds the built app. Until a package
is complete, its section describes the intended shape.

## Pipeline

```
sources ──► bundle ──► agent ──► review ──► document ──► viewer
 (host)    evidence   model     normalise,   JSON on      React
           + checkout  calls    validate,    disk or in   rendering
                      + repair  derive,      a store
                                redact
```

1. A host gathers evidence for one pull request revision through the
   `bundle` source interfaces.
2. `bundle` assembles, redacts and trims it into a `Bundle` plus a read-only
   checkout, and records a digest of the result.
3. `agent` gives the bundle, the checkout and a prompt to a model backend,
   and repairs a rejected document with located findings.
4. `review` maps the model's lenient output onto the strict schema, validates
   it, derives the fields the model never writes, and redacts the output.
5. `viewer` renders the stored document and its earlier versions.

The dependency direction is `bundle` ← `review` ← `agent`. `review` reads
bundle types to resolve anchors and carry history; `bundle` never imports
`review`. The `viewer` has no Go dependency: it reads the document as JSON.

## Packages

| Package | Owns |
| --- | --- |
| `schema` | the canonical `baum.review/1` JSON Schema and its validator |
| `bundle` | evidence types, source interfaces, diff index, checkout export, `REVIEW.md` loading, reference extraction, input redaction, trimming, digest |
| `review` | document types, the strict, lenient and stored schema views, normalisation, validation with located findings, derived fields, version history, model-free refresh, output redaction, the tile catalogue |
| `agent` | the backend interface and its implementations, the prompt interface, the generate-and-repair loop, budgets |
| `viewer` | the React components that render a document and its versions, and the standalone single-page app that `baum view` embeds |
| `sessions`, `linkage` | session discovery and commit-to-session matching, used by `bundle` sources |
| `redact` | the one redaction engine used for input, output and logs |
| `githubapi` | GitHub API plumbing every host shares: the GraphQL transport that turns primary and secondary rate limits into a typed error with the reset time, and GitHub's `BigInt` and `GitObjectID` scalars |

Code that only the CLI needs (flag parsing, configuration, the local version
store) stays under `internal/`.

## `bundle`

### Sources

`bundle` never talks to Git, GitHub or a database itself. A host passes an
implementation of each source:

| Source | Provides | `baum` reads it from |
| --- | --- | --- |
| revision | base, head and merge-base SHAs | the local clone and GitHub |
| comparison | the merge-base-to-head diff and per-commit changes | the local clone |
| export | file trees at head and merge base | `git archive` in a private repository |
| blob | a file at a given commit (`REVIEW.md`) | the local clone |
| pull request | title, body, author, labels, commits | GitHub, with the user's token |
| checks | check runs and commit statuses at head | GitHub |
| conversation | comments, reviews and review threads | GitHub |
| sessions | coding-agent sessions linked to the pull request's commits | `sessions` and `linkage` over the home directory and Entire checkpoints |
| attachments | images referenced in the description | GitHub: the user's token reads the rendered body, whose image URLs GitHub has signed, and the images download from those URLs without credentials |
| previous version | the last stored document for this pull request | the local version store |

A source that has nothing returns an empty result, and the bundle records in
`sources` which inputs existed. Missing evidence is reported as missing,
never as zero.

Every host maps GitHub's pull request, checks, conversation and review
threads onto the bundle through the same helpers in `bundle` (`github.go`),
whether it reads them from stored rows or asks GitHub directly: check and
status identifiers, the pull request state, bot flags, the summary-comment
marker and the placement of review comments in their threads. So a check,
comment or thread has the same identifier in a local and a hosted document.

### GitHub source

`internal/github` is the GraphQL client `baum bundle` reads GitHub with. It reads hosts, repositories, remote URLs, SSH host
aliases and tokens as the GitHub CLI does, through its library go-gh
(`github.com/cli/go-gh/v2`: `auth`, `repository` and `ssh`). The requests
themselves are baum's own, over `shurcooL-graphql`, not go-gh's API client:
that client sends the user's time zone and preview media types, and
rewrites control characters in every response, which would make a local
bundle's text, and so its digest, differ from the hosted service's. baum
also keeps what it fetches, the pagination, head pinning, rate-limit
backoff, `sources` and the mapping onto the bundle.

- **Hosts.** github.com, GitHub Enterprise Cloud with data residency
  (`*.ghe.com`) and GitHub Enterprise Server are all supported. A host is
  read only when gh treats it as GitHub: github.com, a `*.ghe.com` tenant, a
  host in gh's `hosts.yml` (one `gh auth login` was run for), or the one
  `GH_HOST` names. That holds for `--repo host/owner/name` as for a remote:
  any other host is refused before a token is looked up, and is never sent
  one. The endpoint is derived as gh derives it: `api.github.com` for
  github.com, `api.<tenant>.ghe.com` for a tenant, and
  `https://<host>/api/graphql` for an Enterprise Server. An `api_host`
  override in gh's `hosts.yml` is not honoured, since go-gh does not expose
  it outside its own API client.
- **Tokens.** A token is asked for, and sent to, only the host of the
  repository being read. For github.com baum's own sources come first, as
  documented: `GITHUB_TOKEN`, then `GH_TOKEN`, then `github_token` in the
  config file. After them, and for any other host only, the GitHub CLI's:
  go-gh's `auth.TokenFromEnvOrConfig` (`GH_TOKEN` and `GITHUB_TOKEN` for
  github.com and `*.ghe.com` tenants, gh's own rule; `GH_ENTERPRISE_TOKEN`
  and `GITHUB_ENTERPRISE_TOKEN` for an Enterprise Server; then gh's
  `hosts.yml`), then the system keyring through `gh auth token
  --secure-storage --hostname <host>`. baum runs that last step itself,
  under the command's context, so cancelling stops a keyring that waits to
  be unlocked. go-gh's own order puts `GH_TOKEN` before `GITHUB_TOKEN`;
  baum keeps its documented order and uses go-gh only for the GitHub CLI's
  part.
- **Requests.** The token goes only to the host's GraphQL endpoint, the
  client follows no redirect at all (GitHub's GraphQL endpoint never sends
  one), and requests carry `User-Agent: baum` and no other header of the
  client's own. Nothing is cached and nothing is logged; `GH_DEBUG` has no
  effect. Responses reach the mapping exactly as GitHub sent them.
- The pull request is `--pr` on the repository `--repo` names
  (`owner/name` on github.com, or `host/owner/name`) or, by default, the one
  the clone's `origin` remote names (https, ssh and scp-like URLs, read with
  `insteadOf` rewrites applied). Without `--pr` it follows the current
  branch's upstream (`branch.<name>.remote` and `branch.<name>.merge`): an
  upstream of `refs/pull/N/head`, as `gh pr checkout` sets for a pull
  request from a fork, is pull request N of that remote's repository, and an
  upstream branch is looked up with GitHub's `associatedPullRequests` on that
  remote's repository. Without a usable upstream, or when it finds nothing,
  it looks up the branch of the same name on `--repo` or `origin`. The
  lookup reads every associated pull request, page by page, and then takes
  the open one, otherwise the most recently updated; several open ones are
  an error. The pull request found may target another repository than the
  one looked up, such as the upstream a fork's branch was opened against.
  With `--repo`, a pull request found through the upstream counts only when
  it is in that repository.
- Each part is its own query, paginated to the end: the header (state,
  draft, title, body and rendered body, author and association, base, head,
  merge commit), the requested users and teams, the commits in order with
  subjects and parents, the issue comments, the submitted reviews, the review
  threads with identity, resolution and every comment, the check suites with
  every run and the commit statuses of the head commit itself, and the base
  branch's protection. Every page of a pull-request-scoped part reads the
  head again; when it moved, the whole fetch starts over, up to three times,
  so every part describes one head.
- The token owner's own draft review comments, which only they can see, are
  left out, as is a thread holding nothing else.
- A check is required when the base branch's classic protection rule or a
  ruleset requires its name as a status check context. Protection that
  cannot be read in full (no access, no such branch, more rules than one
  page) leaves every `required` null rather than false.
- A part GitHub refuses, such as checks for a fine-grained token without
  that permission, or team reviewers for a classic token without
  `read:org`, is left empty and recorded false in `sources`
  (`pull_request`, `requested_reviewers`, `commits`, `comments`, `reviews`,
  `review_threads`, `checks`, `required_checks`); the reason is returned
  beside it. Only the header is required: without it the fetch fails.
- Review comments are read only through the review threads, as the hosted
  service reads them from its thread snapshot. When the threads part fails,
  every inline review comment is missing too; `sources` records only
  `review_threads` as false, and the returned reasons add a
  `review_comments` entry saying so.
- Rate limits and secondary limits are retried after the reset GitHub
  names, or with backoff from one minute when it names none; a 502, 503 or
  504 is retried with backoff from one second. A reset more than two minutes
  away, or a limit that persists through four retries, fails the fetch,
  with the time to try again when GitHub gave one. Cancellation ends a wait
  at once.
- A Bot's comment that opens with the summary-comment marker is a baum
  host's own summary and is left out; a person's comment with the marker
  stays. The marker the hosted App wrote before the product's rename is not
  recognised locally, so such an old summary comment stays in the
  conversation, flagged as a bot's. The REVIEW.md marker registry, once
  built, will flag bot reports through the same hook.
- The description's rendered images are listed for the attachment step,
  with their signed fetch URLs, which are never stored or logged.
- The tests run the GraphQL documents against a fake GitHub, below the
  client's transports; they are not checked against GitHub's published
  schema.

### Contracts

- `REVIEW.md` is read from the base commit, so a pull request cannot change
  the instructions it is reviewed under. When the pull request edits it, the
  bundle says so. Its content is capped and passed through unparsed and
  unredacted.
- Hunk IDs hash the path and the changed lines only, so a hunk keeps its ID
  across pushes and rebases that do not change it.
- Redaction runs after reference extraction and before trimming, on every
  free-text and URL field, with the `redact` engine: secrets and e-mail
  addresses are masked in place over the whole text of each field, so a key
  spanning lines is found and a patch keeps its lines and diff markers.
  A previous section's code excerpt lines are redacted as one text too.
  @mentions are not rewritten; a host defangs them where it posts text.
  SHAs, IDs and closed enums are exempt. The checkout is not redacted; it is
  the repository the user already has.
- Trimming cuts in a fixed order and never drops a hunk. Each cut adds a
  `generator.truncated` note.
- The checkout has `head/` and `merge-base/` trees, read-only, without Git
  metadata. Files over the size limit and submodule contents are omitted
  with a reason; their hunks stay in the index and must still be placed.
  The same limits apply when the checkout is exported and again when an
  agent unpacks it.
- Sessions carry paraphrased command events, never raw command lines, tool
  output, local paths, e-mail addresses or tokens.
- A host may give a session the decisions it extracted from the session's
  transcript. A session without them was not extracted; an empty list means
  it made none. They are redacted like other free text, and trimming cuts
  superseded decisions after every stage of uncited prompt text and current ones only
  with their session. The prompts at the turns current decisions cite are
  kept like a session's first and latest prompts: the older-prompts stage
  spares them, and before any session is dropped their text is shortened,
  longest first, to a bounded head and then to empty text, keeping each
  prompt's entry so its anchor still resolves.
- The same inputs produce the same bundle and digest.

## `review`

### Schema views

`review` derives three views from `schema/review-1.json`:

- the **stored** schema, with every derived field, which a stored document
  must satisfy;
- the **strict authored** contract: what the model is responsible for;
- the **lenient model-facing** schema, which accepts the shapes models
  commonly produce. Normalisation maps it onto the strict contract without a
  model call.

The stored contract stays strict; leniency exists only in derived views.

### Validation and derivation

- Validation reports every finding in one pass, each located to the item it
  concerns, so a repair can fix them all at once. An anchor that does not
  resolve is shown as unresolved; it never rejects a document.
- A hunk may belong to several sections or to none; unplaced hunks are listed
  as `unassigned_hunks`. At most one section is `mechanical`.
- Derived fields are never model-written: statistics, section states, hunk
  tiers, the index, agent reviews, the review exchange, each section's
  `review_score` and the document's `risk_score`. `risk_score` is the highest
  current section score, never an average, with floors set by active high and
  critical risks.
- A count the tool cannot establish is null, never zero.

### History

Given the previous version, `review` keeps sections stable:

- earlier sections stay first, in their original order, with the same IDs;
- a section whose hunks are all gone becomes `stale`: still readable, but no
  longer part of the current review;
- each section's state (`new`, `changed`, `unchanged`, `stale`) is derived by
  comparing its hunk IDs with the previous version;
- a risk the model omits becomes `mitigated`, and a risk that returns keeps
  its `introduced_version`. A repair cannot drop a risk.

### Refresh and redaction

`Refresh` updates checks, threads, comments and evidence status in a stored
document without a model call; the platform's refresh uses it (the
`baum refresh` command is not implemented yet). It rebuilds the session
index from the bundle and carries over, for each readable session the
bundle still links, the stored index's prompts at turns the bundle lacks
that are timestamped at or before the document's `generated_at`, as stored,
so prompts that decisions cited at generation keep their bounded text while
a prompt only an earlier refresh added is not carried again.

Everything a model returns is untrusted. `review.OutputRedactor` masks a
document, or any other text a host passes it (the findings, the diagnostics);
a host applies it to everything its generation returns. Every string of the
document is scanned as one text, so a secret spread over code lines, table
rows or steps is caught; a string keeps its exact text only if it is a reference that resolves to the
redacted bundle or the checkout, or its value meets the schema's constant,
enum, closed pattern or date-time format. A value kept only by a closed
pattern or date-time is still free lowercase text, so each is also scanned on
its own: whole, and from after every run of characters in it that are not
letters or digits, a 4 KiB window at a time, so a long value costs its length
plus a window per restart. Each finding is masked in place; `MaskedClosed`
counts the values a finding masked, and a host should reject the run when it
is not zero; a finding whose text already reads as its mask, such as an
address the redactor wrote, is no finding. A value longer than its schema's
`maxLength` (128 characters for
identifiers and categories) cannot be valid; a value whose schema sets no
`maxLength`, such as a SHA, hunk ID or date-time, may be valid at any length,
and one with more than 16 runs fits none of those patterns. Either is scanned
whole and from its first 16 runs, then masked whole, and counts only if a
secret was found in it, so the scan stays linear in the document's size and a
harmless value never fails the run on its own.

Normalisation respells every identifier, category and tile-local name the
model writes in lowercase with one underscore for each run of other
characters, and a secret respelled that way (`sk-ant-api03-AbC…` as
`sk_ant_api03_abc…`) is one no rule detects. So a host runs the redactor's
`Identifiers` on the document as the model wrote it, before `Prepare`: the
values normalisation respells, taken from its own walk, are scanned the same
way, bounded by the length of their respelling, and masked with the
format-preserving mask, which normalisation then respells to `x`, `9` and
underscores; they count in `MaskedClosed` too. The same secret written in
another spelling, upper case or already respelled, is not detected again. So
the respelling of each secret found and counted in an identifier, taken per
finding and again widened to the words of that identifier it lies in, is a
source when at most 128 bytes long, and every identifier whose respelling
holds a source as whole words (from an underscore or its start to an
underscore or its end) is masked whole and counted. A source exists only
once a finding has failed the run, so a harmless identifier is masked this
way only in a run that fails already. Free text is neither
scanned nor masked by `Identifiers`; `Document` masks it after `Prepare`. The
order is `Identifiers`, `Prepare`, `Document` on what `Prepare` returned, with
one redactor, then the `MaskedClosed` check. Text scattered across unrelated
fields, a token wrapped mid-value, or a secret no rule detects on its own,
such as a bare hex string, which reads as a SHA, is not caught. If redaction
fails, the text is dropped rather than kept.

Known limits of the identifier pass:

- A secret found only in free text is masked there, but an identifier that
  holds it in a spelling no rule detects is not matched to it.
- A key the redactor finds as part of a larger span, a URL with credentials,
  an e-mail address or a greedy hyphenated tail, is a source only as that
  whole span, so another spelling of the key alone is not matched.
- In an identifier over its bound, a key past the first 16 runs of
  separators, behind an underscore, is masked with the rest of the value but
  not found, so it is neither counted nor a source.
- Free text that repeats an identifier's secret in another spelling is left
  to `Document`, which finds it only if a rule detects that spelling.
- Matching is by whole words, so a key glued without a separator to letters
  or digits in another identifier (`xakia…`) is not matched there.

### Tiles

The tile catalogue (`catalogue.json`) and the tile guidance are public and
live here. Tiles are off by default; a host offers an allow-list of kinds,
and the model schema and prompt include only those kinds.

## `agent`

### Backends

```go
type Backend interface {
	Name() string
	Run(ctx context.Context, req Request) (Result, error)
}
```

A request carries the prompt, the model-facing schema, the checkout directory
and the call and token budgets, and optionally an `Endpoint` (a complete
provider base URL and key) that replaces the backend's default. A result
carries the document, usage and an event log. Usage can be explicitly
unreported (`Unreported`) when a backend cannot say what a run consumed, which
differs from zero. `LocalEnvironment` gives a CLI backend's child process only
an allowlist of the caller's environment variables (paths, locale, proxy and
certificate settings) plus the names the backend adds. Provider credentials,
cloud and GitHub tokens, Git settings and the SSH agent never pass, even when a
backend names them. Only variables are filtered: `HOME` and the XDG directories
stay, so a child can still read credentials kept in files under them. Backends:

| Backend | Runs |
| --- | --- |
| `anthropic` | the Anthropic API directly, with read-only file tools over the checkout |
| `claude` | `claude -p` restricted to read-only tools, submitting through structured output (`agent/claude`) |
| `codex` | `codex exec` in its read-only sandbox, with a strict-mode output schema (`agent/codex`) |
| `cursor-agent` | `cursor-agent` confined to the checkout |

The CLI backends rely on each tool's own read-only mode; baum does not add a
container. [SECURITY.md](SECURITY.md#agent-isolation) describes what that
leaves open.

`agent/claude` has two profiles over the same read-only flags, which are not
configurable. `User`, the zero value, is for local runs: the child gets
`LocalEnvironment` plus `CLAUDE_CONFIG_DIR`, the user's own `HOME` and login,
the binary found on `PATH`, and `ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`
only when an `Endpoint` is given. Before its first run a `Backend` runs
`claude --version` once and refuses a release older than the pinned minimum; a
run that dies on an unknown option reports the same upgrade request.
`Isolated` is for a host that supplies the whole environment: `HOME` and
`CLAUDE_CONFIG_DIR` under `<run>/home`, a fixed `PATH`, and the key and base URL
of the `Endpoint`, which it requires. Cancelling a run kills the child's
process group on Unix; on Windows only the child itself, not its descendants.
Because the child has its own process group, Ctrl-C does not reach it: a local
caller must cancel the context it passes to `Run` on SIGINT and SIGTERM (for
example with `signal.NotifyContext`), or the CLI keeps running.

`agent/codex` has the same shape. `Local`, the zero value, runs `codex exec
--sandbox read-only` with the user's own `HOME` and `CODEX_HOME` (so `codex
login` works), `LocalEnvironment` plus `CODEX_HOME`, and `CODEX_API_KEY` and the
base URL only when an `Endpoint` is given. The output schema goes to a private
temporary file removed after the run, never into `CODEX_HOME`. Once per
`Backend` it checks `codex --version` against the pinned minimum (the release
the hosted agent runs) and runs `codex sandbox -P :read-only -- cat <file>` to
prove the read-only sandbox starts on this machine, refusing the run otherwise
(and always on Windows, where the sandbox was not examined). A run that dies on
an unknown option reports the upgrade request. `Container` is for a host that already runs
inside its own boundary: it passes `--sandbox danger-full-access`, puts `HOME`
and `CODEX_HOME` under `<run>/home` with a fixed `PATH`, and requires an
`Endpoint`. No code under `internal/` or `cmd/` may name it; a test enforces
this. `codex.StrictSchema` turns the lenient review schema into the form
OpenAI's strict structured outputs accept.

`agent.RunDirPrefix` is the convention for a local run's directory name
(`os.MkdirTemp("", agent.RunDirPrefix+"*")`); nothing in `agent` creates one.
Session discovery skips sessions recorded under a path element with that
prefix as a backstop. The primary protection is that the backend persists no
session and loads no hooks.

### Prompts

The review instructions reach the loop through an interface:

```go
type Prompts interface {
	// Instructions returns the review instructions for a backend. The loop
	// ignores Version and Digest; the host records the version in the
	// document's generator.prompt_version and keeps the digest.
	Instructions(ctx context.Context, backend string) (Prompt, error)
}

type Prompt struct {
	Version, Body, Digest string
	// Repair and RepairEnding replace the loop's repair wording when set.
	Repair, RepairEnding string
}
```

The module ships and maintains its own public prompt in `agent/prompt`. It is
not written yet: `prompt.Prompts` returns `prompt.ErrNotAuthored` for every
backend, so a caller fails with that error instead of reviewing with empty
instructions. The layout it will take is a shared `review-public-v1.md` plus a
submit text per backend (`submit-claude.md`, `submit-codex.md`), embedded with
`go:embed`. A host may supply another implementation; the hosted service does,
with its private prompts. `StaticPrompts` is a fixed implementation for tests
and simple hosts. The prompt is per backend because each backend submits its
result differently.

The loop owns the rest of what a run sends:

- **Tile guidance.** It appends `tilecatalogue.Instructions(kinds)` to the
  body for the offered tile kinds, so a prompt never carries it. A `Prompts`
  implementation that rewrites its body per backend therefore rewrites the body
  only.
- **Repair text.** `agent.RepairText(backend)` returns the text that opens and
  closes a repair prompt. A backend that submits through a `StructuredOutput`
  tool (`claude`, `anthropic`) is told to call it again; `codex`, which answers
  in its final message, is told to reply with the entire corrected document
  there and to use its file tools when a finding needs the file. A prompt's
  `Repair` and `RepairEnding`, when not empty, replace that text, for a host
  whose instructions name the tools differently. They are not part of the
  prompt's JSON encoding.

Local generation (`baum generate`, not built yet) must start every prompt it
sends with `sessions.GenerationRunMarker`. That is the contract session
evidence relies on: a CLI backend records its run in the same session stores
as the developer's own sessions, in a worktree of the same clone, and local
linkage leaves out every session whose top-level prompt starts with the
marker (see "Local session evidence"). A run without it would be read as
evidence about the pull request it reviews.

### Generate and repair

```go
generator := agent.Generator{Backend: backend, Prompts: prompts, Validator: validator,
	Redactor: redactor, Logger: logger}
outcome, document, err := generator.Generate(ctx, agent.Run{
	Bundle: b, EncodedBundle: encoded, Directory: dir, TileKinds: kinds,
	Budgets: budgets, Endpoint: endpoint})
```

The caller unpacks the checkout (and attachments) into `Directory`, which the
backend then runs in. `Generate` masks the secrets the model writes into
identifiers, writes `bundle.json` and `schema.json` there, builds the prompt
(instructions, tile guidance, the bundle and numbered source excerpts of the
changed files) and asks the backend for a document. A provider failure is
retried within `MaxProviderAttempts`, waiting `RetryDelay` (by default
`1<<min(n,4)` seconds). The document goes through `review`: the validator
completes it and returns located findings. On findings the loop sends back the
bundle, the rejected document and the findings, without source excerpts, as a
repair, up to `MaxRepairAttempts` (at least one), and a repair does not
reactivate a risk the first pass omitted
(`review.PreserveRiskOmissionsForRepair`). Every call and token, repairs
included, counts toward the call and token budgets, and a repair the budget
cannot pay for fails naming it. A secret masked in an identifier ends the
repairs: the run is rejected whatever a repair returns. After the last repair,
or at once on a secret, a run still rejected ends with
`ReasonValidationRejected` and the last rejected document in the outcome (at
most `MaxRetainedDocumentBytes`, and only when it is JSON). A run that fails
otherwise returns an error: a spent budget, `ReasonProviderUnavailable`, or the
cancelled context.

`Outcome` is what the loop reports besides the document: the model, the schema
revision, the failure reason, the findings, the rejected document, the
diagnostic events (at most 512 over the run), the calls, tokens, provider
attempts, errored attempts, validation repairs and usage, the structured output
rejections, and what the identifier pass masked. A host embeds it in its own
outcome type, and its JSON field names are the hosted agent's wire format.

The loop returns text the model wrote, so the host applies `RedactOutput` to
every outcome before it stores or shows one: it masks the document, the kept
rejected document, the findings, the model and each diagnostic string, counts
the spans it masked, and reports the closed values (identifiers, categories,
references) in which it found a secret. `RejectSecrets` then rejects such a run
exactly as the hosted worker does: `ReasonValidationRejected`, the document
withheld and only its masked copy kept. They are for hosts to call, including
the local CLI (not yet wired); today only the hosted worker calls them.

## `viewer`

`viewer` is a pnpm package of React components that render a document: the
summary, sections and their blocks, risks, tiles, hunk evidence, images,
checks, sessions and the version history with comparison. It also builds a
standalone single-page app, which the `baum` binary embeds and `baum view`
serves on localhost.

The viewer reads only the document and its versions. A host connects it
through props and context:

| Seam | Required | `baum view` | Notes |
| --- | --- | --- | --- |
| document source | yes | the file given, and versions from the local store | the current document, a version list and a loader for one version |
| attachment URL | no | served from the local cache | resolves an image ID to a URL; without it, image blocks draw nothing |
| navigation | yes | in-app history | link component and the selection and version state in the URL |
| hunk patch source | no | `GET /api/patch` (below) | the full patch for a hunk when the document does not carry it |
| file opener | no | not used | opens a cited file in the host's own diff view instead of following its link |
| row actions | no | not used | a slot beside each section and risk, for example a read mark or approval |
| side pane | no | not used | a slot for discussion or other host features |
| header slots | no | not used | statistics, generation controls, approval state, a back link |
| rail groups | no | Sessions and Commits, from `/api/sessions` (below) | changed files, linked sessions and commits, each listed in the rail with the host's pane |
| version shown | no | not used | a callback with the versions on screen, for example to record that one was read |

Optional seams default to absent, and the viewer renders the document alone.
The viewer never decides who can see a document; that is the host's job.

The sans-serif font comes from the `--viewer-font-sans` CSS variable, which
defaults to Inter, bundled with the package under the SIL Open Font License.
A host overrides the variable to restyle the viewer without forking it; the
hosted service sets its own licensed face this way.

The components and their seams are in [`viewer/`](viewer/README.md). The
package also builds the standalone app (`viewer/app/`) with `pnpm build:app`
into `internal/viewer/dist`, which the `internal/viewer` Go package embeds. The
built output is committed.

`baum view`'s app fills the `sessions` and `commits` slots from
`GET /api/sessions`, with `SessionList` and `CommitList`, which take plain
view models and never fetch. The app asks once when the page opens, and again
on each `document` event (the server recomputes when the head changes), and
keeps the last answer on screen while it does.

- While the request is out, both groups are in the loading state, so a
  `?pane=sessions` or `?pane=commits` link survives; the rest of the review
  does not wait for it. The first request can take seconds.
- Available: the Sessions group lists the linked sessions, each with the
  commits it is linked to, and the Commits group lists every commit with the
  sessions matched to it, the method and the confidence. Each opens the other.
- Unavailable, or a first request that fails (network error, a status other
  than 200, a body that is not an answer, a field of the wrong type): the
  Sessions group lists the sessions the document itself recorded, under a note
  giving the server's `reason` as plain text (a failed request says it could
  not be read), and the Commits group is left out. Only a failed request with
  no session in the document shows the group as failed. A later request that
  fails keeps the last answer; a server answer of unavailable replaces it.
- A linked session opened alone (`?pane=session&session=<id>`) asks
  `GET /api/sessions/{id}` for its transcript, only then, and draws it under
  the session with `SessionCard` and its conversation (`SessionTranscript`),
  the components the hosted app uses, through `SessionList`'s
  `renderSession`. The card's agent activity is read from the same answer. It shows a loading state, a failure with
  Retry (a 404 says the session is no longer linked), and a note when the
  server cut the transcript short. `?turn=<n>` opens it at that prompt; the
  parameter is cleared with the selection. The document's own sessions have
  no transcript.

### How `baum view` serves it

`internal/viewserver` is the host. `baum view` loads one document file (or the
embedded `schema.Example` for `--sample`), binds `127.0.0.1` on the given port
or a free one, and prints the URL. It never contacts a server or fetches
anything. The server has six API routes and an asset fallback:

| Route | Serves |
| --- | --- |
| `GET /api/document` | the document's bytes exactly as read, `no-store` |
| `GET /api/events` | a Server-Sent Events stream, always valid, so the app stays connected; pings keep it open and, with `--watch`, a `document` event follows each change |
| `GET /api/health` | `{"status":"ok"}` |
| `GET /api/sessions` | the sessions on this machine linked to the document's pull request, as below; `no-store` |
| `GET /api/sessions/{id}` | the redacted transcript of one of those sessions, as below; `no-store` |
| `GET /api/patch?path=` | the patch of one file the document cites, as below; `no-store` |
| any other `GET` | the embedded file of that name, or `index.html` for a path that names none, so the app's own URLs work as deep links |

Only the embedded files and the one document are ever served; a request path
is looked up in the embedded file system and never reaches the disk. A request
whose `Host` is not the bound `127.0.0.1:port` or `localhost:port` is refused
with 403, which stops DNS rebinding. Every response carries a `'self'`-only
Content-Security-Policy, `nosniff` and `no-referrer`; there are no CORS
headers, and any method but `GET` and `HEAD` gets 405. An unknown `/api/` path
is 404.

A document with findings is served anyway, with the findings on stderr. It must
be valid JSON of at most 32 MiB, or `view` exits at start-up. With `--watch` the
file is polled by path every 500ms and read once it has stopped changing, so an
editor's save by rename is followed. A change that is not valid JSON or is too
large is warned about and ignored, and the previous document keeps being
served. Findings print again on each accepted change.

### Local patches

Most hunks a document cites carry no inline `diff`, which the generation budget
leaves out. The app fills them in from `GET /api/patch?path=<path>`, answered by
`locallink.Patch` over the same clone and remote matching as the sessions
(`gitlocal.Open`, any remote naming the document's repository, no fetch).

- Only a `path` that is the `path` of a hunk in `index.hunks` of the document
  being served is answered; anything else, including a missing, empty or
  over-long (4096 bytes) `path`, is 404 and runs nothing. A repeated `path`
  parameter is not an error: the first is the one asked for. The document
  records one path per hunk, the file's name at the head.
- The range's changed files are listed once per merge base and head
  (`gitlocal.Repo.ChangedFiles`: `git diff --raw -z -M -l1000`, bounded like the
  other reads) and kept while the commits stay the same. That list gives the old
  name of a renamed file and marks a submodule (a gitlink on either side).
- The patch is `git diff` from the merge base (else the base) to the head as the
  hosted service reads it (`gitlocal.Repo.FilePatch`): `-M -l1000`, then `--`
  and a `:(literal)` pathspec for the path and, for a renamed file, its old path
  too, so a rename shows only its edit. The fixed flags are `--no-ext-diff
  --no-textconv --no-color --unified=3 --diff-algorithm=myers`, with full commit
  ids, so a path cannot be an option or a glob. The merge base must be in the
  clone: the base would give a different diff.
- A path must be one the range changes (a key of that list; a deleted file is
  listed by the path it had). Any other, a directory included, whether one file
  or several changed under it, is unchanged and runs no diff. A submodule has no
  patch, as in the hosted service. The diff must also be one file's: one `diff
  --git` section, or for a type change (a file to a symlink, say) the two
  sections of the same path, as the hosted service shows it, with each section's
  header lines dropped. Any other diff has no text patch.
- It runs in `gitlocal`'s private repository (empty configuration, no
  attributes, no replace refs, no remote, `GIT_NO_LAZY_FETCH=1`), so neither a
  `diff.external`, a textconv or filter driver, a pager nor any other setting of
  the clone, and no environment variable, changes or runs anything.
- The response is `200 {"patch":"@@ -1,3 +1,4 @@..."}`: the file's diff from its
  first `@@` line. git's header lines before it (`diff --git`, `index`, the
  rename lines, `---` and `+++`) are dropped; the viewer reads only the hunks.
  Every outcome that is not a patch is `200 {"patch":null}` with nothing about
  why: a binary file or a mode-only change, a submodule, a path the range does
  not change (a directory is one), commits or blobs the clone lacks (including a partial
  clone), a directory that is not the repository's clone, a patch over 1 MiB
  (`locallink.MaxPatchBytes`), the sample document. A failure that may pass (git
  failing, the one-minute limit) is 500, and a server that is stopping is 503;
  the app turns both into a rejected request and tries again when the excerpt is
  next opened.
- Answers that hold until the document changes (a patch, no text patch, unchanged,
  too large) are kept per path; those a person can fix (no clone, no remote,
  missing commits) and failures are computed again on the next request. A
  document reload (`--watch`) drops every answer and the set of cited paths. At
  most four git reads run at once, each bounded by the server's lifetime.

### Local session linkage

`internal/locallink` links the sessions on this machine to a pull request's
commits using only this machine; `baum view` serves it, and the session
evidence that `baum bundle` collects reuses its loading and matching (see
"Local session evidence"). `Resolve`
takes the pull request's `owner/name`, head and base commit ids, a directory
inside the clone and the session store overrides, and does this:

1. Opens the clone containing the directory. A directory outside a clone is
   `not_a_clone`.
2. Compares the URL of each of the clone's remotes with the repository. `owner/name` is taken
   from an HTTPS, HTTP, SSH, `git://` or scp-like URL, ignoring case, a `.git`
   suffix, credentials, host and port; a local path or `file://` URL names no
   repository. Any matching remote links, so a fork clone (origin the fork,
   the pull request's repository as `upstream`) works. If none matches the
   answer is `remote_mismatch`, and its reason lists up to five remotes as
   `name → owner/name` (never a URL or its credentials); a clone with no
   remotes says so.
3. Expands the document's head, and its merge base (else its base), which the
   schema allows to be abbreviated to 7 hex digits, with
   `gitlocal.ResolveCommit` in the clone; an abbreviation matching several
   commits is `ambiguous_commit` and anything that is not hex is `no_head`. The
   merge base is preferred because it gives the same range and is more likely
   to be local. It then lists the commits in `merge_base..head` with
   `gitlocal.Commits`: oid, author, committer date, parents, subject, trailers
   and the files each changed (without rename detection, which would need
   blobs, so a rename lists both paths). A head or start the clone lacks is
   `commits_missing`; a partial clone that has the commits but not the trees
   is `partial_clone`. It never fetches: every read of objects, including the
   Entire checkpoint branch, runs in the private repository `gitlocal` makes,
   which has no remote and no promisor configuration, with
   `GIT_NO_LAZY_FETCH=1`, so even a partial clone cannot download an object;
   a checkpoint whose objects are missing is skipped with a warning. Only the
   remotes' URLs are read from configuration, never contacted.
4. Loads the sessions with `sessions.Load`, only those last modified within
   `locallink.SessionWindow` (two days: the matcher's one-day timeframe tolerance plus a day of margin) before the first commit's date, and Entire checkpoints are dated from the branch's own history so older ones are skipped unread.
   `sessions.Load` finds a session recorded in any working tree of the clone:
   it lists them with `gitlocal.Worktrees` (`git worktree list --porcelain
   -z`, run against the clone's repository directory with the read
   environment; deleted worktrees included), and a session whose recorded
   working directory is one of them, the checkout containing the directory, a
   previous path, or a directory inside one, belongs to the repository. Path
   matching stops at a separator, so `/repo-other` is not inside `/repo`, and
   ignores case on Windows. The answer is the same from the main checkout and
   from any worktree. Each Entire session is mapped once, from the newest
   checkpoint holding it: one `git cat-file --batch` reads the metadata of every
   checkpoint in the window and another the metadata of their sessions, and
   only the checkpoints holding the newest snapshot of a session no
   home-directory store has are read whole. A newest snapshot that cannot be
   mapped, or that carries no transcript (Entire sometimes writes a checkpoint
   with only metadata and the prompt), gives way to the next newest; a
   snapshot is not necessarily fuller than the one before it. Sessions and those checkpoints are mapped up to eight at
   a time. It then
   projects each into a
   `linkage.RunCandidate` as the hosted resolver does: `RunID` the session id,
   `Agent` the vendor that ran, `Source` the store it came from, branches, files
   touched and edited, and role-tagged commit refs from `sessioncorrelate`,
   and the transcript's modified time as `IngestedAt`, which bounds a session
   that recorded no end. `UserEmail` is empty, so it never rules a commit out.
5. Projects each commit into a `linkage.EnrichedCommit`: the first parent as
   `AncestorOID`, more than one parent as `IsMerge`, the changed files, and the
   agents named by `Co-Authored-By` trailer addresses. `BranchNames` is left
   empty, which the branch step reads as unknown.
6. Reads each commit's `Entire-Checkpoint` trailer as the hosted resolver
   does: one 12-hex id, case-insensitive key, repeated ids allowed; a malformed
   value or two distinct ids is unusable. A usable id is looked up on the
   clone's checkpoint branch (the local branch, else a remote-tracking copy),
   reading only its metadata in the private repository, and maps to the
   sessions the checkpoint records. Such a commit is linked to those sessions
   with `method` `entire_checkpoint` and confidence 1, as the hosted
   `entireEdges` writes them, and then goes through step 7 with those sessions
   already linked and the files they edited already explained. The
   checkpoint is read whatever `Sessions.Sources` asks for. Every linked
   session is listed: when loading did not return it (it was last written
   outside the window, or only Entire holds it), it is mapped from that
   checkpoint. A session that already came from a home-directory store under
   the same id is listed once, from that store, since its transcript is the
   fuller record. An unusable trailer, a checkpoint the clone does not hold,
   or one whose sessions cannot be mapped leaves the commit to the heuristics.
7. Matches each commit the way the hosted resolver does, with the default
   config and `steps.Default`. As its `matchRemaining` does,
   `linkage.MatchCommit` runs over the sessions not yet linked, and runs
   again, without the ones it linked, while the files the linked sessions
   edited do not cover every file the commit changed, until nothing more
   matches or the files-overlap link cap (three) is reached, a tie that would
   pass the cap being cut to it. Then, as its `dropSupersededGuesses` does,
   once the commit has a link at or above `linkage.ReviewConfidence` (0.7) the
   links below it are dropped. So two sessions that each edited part of a
   commit are both linked, and a weak guess beside a confident link is not.
   Only matched edges are kept: the hosted resolver's unmatched edge, its
   reasons and a trailer-only attribution name no session. A session that arrives more
   than once under one id is matched once, as its best reading: one with a
   transcript, then the newest.

The result is redacted with the engine and the row summary that `baum sessions`
uses (`locallink.RowOf`), and carries no transcript, prompt, file path, e-mail
address or session store path; a linked session's transcript is its own route
(see "Session transcripts" below).

`GET /api/sessions` answers one of two shapes. Available:

```json
{
  "available": true,
  "sessions": [
    {
      "id": "aaaaaaaa-0000-4000-8000-000000000001",
      "source": "claude_code",
      "vendor": "claude_code",
      "title": "Ask xxx.xxxxxxxx@xxxxxxx.xxx about the feature (XXXXX9XX9XXX9XXX9XXX)",
      "branch": "feature",
      "started_at": "2026-07-01T10:00:00Z",
      "ended_at": "2026-07-01T10:30:00Z",
      "outcome": "committed",
      "edited_files": 0
    }
  ],
  "commits": [
    {
      "oid": "3f2c0a9e5b1d4c6a8e7f90123456789abcdef012",
      "subject": "Add the feature",
      "author": "Grace Hopper",
      "committed_at": "2026-07-01T10:30:00Z",
      "links": [{ "session": "aaaaaaaa-0000-4000-8000-000000000001", "method": "commit_ref", "confidence": 0.95 }]
    }
  ]
}
```

`sessions` holds only sessions with at least one matched commit, ordered by when
their transcript or checkpoint was last modified, oldest first; `commits` holds every commit of the pull request, oldest first, with `links`
empty for a commit no session matched. `links` are ordered by confidence;
`method` is `entire_checkpoint`, `commit_ref`, `start_commit` or `files_overlap`, and `source` is
the store (`claude_code`, `codex`, `cursor` or `entire`), which differs from
`vendor` for a session an Entire checkpoint recorded. A pull request none of
whose commits match still answers `available` with `"sessions": []`. A subject
or author is at most 200 characters.

Unavailable:

```json
{ "available": false, "code": "commits_missing", "reason": "this clone does not have the pull request's commits; ..." }
```

`code` is one of `sample` (the built-in example, which belongs to no clone),
`no_head` (the document lacks a repository, head or base, or they are not hex
commit ids), `ambiguous_commit`, `not_a_clone`, `remote_mismatch`,
`commits_missing`, `partial_clone`, `too_many_commits` (over 2000) and `error`
(anything else, including a computation that took more than two minutes; the
cause is printed to stderr, not sent).
The viewer then falls back to the document's own sessions, under a note with
the reason, and omits the Commits group.

The route is lazy and cached. Nothing is computed at start-up; the first
request starts the computation, and requests that arrive meanwhile wait for it
instead of starting another. An available answer is kept until the document's
repository, head or base changes, which with `--watch` happens when the file
is rewritten with a new head. An unavailable answer is not kept, so fetching
the commits takes effect on the next request, except an `error`, which is kept
for 30 seconds so that reloading does not start a slow computation again each
time. When the server stops, it cancels a running computation and waits up to
ten seconds for it to remove its private repository. The route sits behind the same
host check and security headers as the others, and only `GET` and `HEAD` are
allowed.

### Local session evidence

`locallink.BuildSessionEvidence` is the bundle's sessions source on this
machine; `baum bundle` calls it through `internal/assemble`. It
takes the pull request's commits as the clone holds them (`gitlocal.Commits`
from the merge base to the head), a directory in the clone, the session store
overrides with the `--previous-path` directories the repository used to live
at, and the bundle caps `MaxPromptsPerSession` and
`MaxCommandEventsPerSession`. It loads and matches sessions exactly as steps 4
to 7 of "Local session linkage" do. Loading leaves out baum's own generation
runs, for `baum view` too: a session whose top-level session has a prompt
starting with `sessions.GenerationRunMarker` is neither linked nor allowed to
take a link from another session, and is dropped from an Entire checkpoint's
ground truth. Each session linked to at least one commit becomes one bundle
session:

- `id` is the vendor's session id, the one `/api/sessions` lists, and
  `vendor` the agent that ran. The session tree is one entry: a subagent is
  never a session of its own.
- `models` are the distinct models of every invocation in the tree, sorted;
  `title`, `branch`, start, end and `outcome` are the top-level session's, the
  branch only as that session recorded it (a branch seen in a subagent can be
  another repository's).
- `prompts` are the top-level session's human prompts and slash commands with
  text, by turn index: the first and the latest `MaxPromptsPerSession - 1`
  (none at 0). Each carries the latest non-blank reply the model gave it, and
  prompt and reply are cut to `bundle.PromptScanChars` characters, as the
  hosted service reads them. As `touched_paths` each has the distinct paths
  read or edited under it, including by the subagents it spawned at any
  depth. `sessioncorrelate.PathResolver` places them with the hosted rules
  (recorded repository and worktree roots, then working directories, then
  anchors, with a leading in-repo agent worktree such as
  `{.claude,.agents}/worktrees/<name>/` removed), and with the clone's checkouts given
  (`WithCloneRoots`: every worktree git lists, the checkout itself and the
  previous paths) an absolute path resolves only inside one of them, and a
  relative one only when joining it with each working directory its session
  recorded (else the tree's) lands at the same place inside them. A previous
  path that is the file system root, the home directory or above it is not
  used and is reported as a warning, as is a clone whose worktrees git cannot
  list. A file
  outside the clone, beside it or in the home directory, is left out, even
  when the session ran in a directory above the clone.
- `command_events` are the tree's execute tool calls that carry a command
  line, the latest `MaxCommandEventsPerSession` (all at 0) in time order.
  Each is `bundle.ParseCommandParaphrase` of the command: per simple command
  of a pipeline or `&&`/`;` chain, the program's base name across `/` and
  `\` (a command whose program is dynamic, a directory or not a plain name
  is left out), its closed category, and at most one target: the first
  argument that is a known repository path. An argument is a candidate only
  when it is path-shaped (local, with a `/` or an extension, free of
  whitespace, quotes, `=`, `@`, `:`, `\` and shell metacharacters) and is
  not the value of a flag that carries code, a message or a credential
  (`-c`, `-e`, `-m`, `-p`, `-r`, `--command`, `--eval`, `--exec`,
  `--execute`, `--message`, `--print`, `--run`, a two- or three-letter
  cluster of short flags ending in `c`, `e` or `m` such as `-am`, or a flag
  whose name contains `pass`, `token`, `secret`, `key`, `auth` or `cred`).
  It is normalised (cleaned, `./` dropped, a trailing Go `/...` pattern
  kept; the repository root and anything escaping it are never targets) and
  kept only when the `knownPath` predicate accepts it: here a path the pull
  request's commits change or a directory above one, a path the session
  touched or a directory above one, or a file or directory that exists in
  the checkout at exactly that path without leaving it through a symbolic
  link. Each argument is checked in turn, and the first known one is the
  target. A target is dropped rather than guessed. Its `outcome` is the tool
  result's status as `bundle.CommandOutcome` maps it, `unknown`
  without a result. Command lines and tool output never leave the session
  graph.
- The linkage confidence is the highest of the session's matched edges, over
  every commit it is linked to. Sessions are returned by confidence, then
  start (unknown last), then id, which is the order `bundle.ApplyCaps` cuts
  them in.

The sessions are returned unredacted: `bundle.Redact`, the bundle's input
redaction, masks their titles, branches, prompts, replies, paths and command
fields with the rest of the bundle, before trimming. `bundle.CommandText`,
`bundle.ParseCommandParaphrase`, `bundle.CommandOutcome` and
`bundle.PromptScanChars` are the ones the hosted service uses for its stored
sessions.

Known differences from the hosted service's database queries, accepted as
they are: blank prompt and reply text is told by Go's `strings.TrimSpace`
rather than PostgreSQL's `btrim` (which trims spaces only); an empty model
name is not listed; a tool call is counted toward the command-event cap only
when `bundle.CommandText` finds a command in it, where the query also counts
an array of empty strings; and when a tool use has more than one result the
first one recorded decides its outcome.

### Session transcripts

`GET /api/sessions/{id}` serves the transcript of a session the current
`/api/sessions` answer lists, and only those: it reads that answer from the
same cache, computing it through the same single-flight path when it is not
there yet, and any other id, a session read but not linked, or an unavailable
answer is 404. The answer keeps the linked sessions' graphs for this
(`Result.Graphs`, never part of its JSON).

The transcript is shaped and bounded first, from the graph as read, and
redacted after, so nothing past the limits below is ever redacted or held:

1. `internal/transcript` builds it (below). Each text it keeps (a prompt, a
   reply, an agent message, a tool call's input or output) is read up to 8 KiB past its
   limit and passed through the redaction engine before it is cut, so a
   secret that the cut runs through is still recognised and masked.
2. The encoded transcript is redacted as a whole with the engine
   `baum sessions --dump` uses (`redact.Engine.RedactJSON`, which `--dump`
   reaches through `locallink.RedactGraph`): secrets and e-mail addresses are
   found over the encoded text, so a key and its value in separate strings are
   caught together, and in every string value and object key as decoded, so a
   rule anchored at a line's start fires after an escaped newline (an env
   file in a tool's output). The union is masked, only inside strings and
   never in an escape, so the result stays valid JSON.
3. The user's home directory is written as `~` where it starts a path: after
   the start of a string, a quote, white space (an escaped newline or tab
   included), a separator or one of `= : ( [ { , ; | < >`, and not where the
   name runs on into a longer one (another user's home), so
   `/data/home/user/src` keeps its name. It is matched as given (trailing
   separators dropped), as its symbolic links resolve, JSON-escaped, and for
   a Windows path also with forward slashes and in any case. The home comes
   from `$HOME` (or the platform's equivalent), else the account. Paths
   outside it (a clone elsewhere, a Windows home under `/mnt/c` in WSL) are shown as
   they are.

Nothing else is removed: prompts, agent messages, tool inputs and tool output
are shown as recorded, masked. The response does not carry the graph's store
paths, vendor ids or repository and worktree roots, and never a cost (this
machine has no prices) or an e-mail address of the session's user. Each
session's body is built once per answer, on its own, so a large one does not
hold up another, and a build that fails is tried again on the next request.

`internal/transcript` shapes the graph into the view model of the viewer's
session card and conversation (`oss/viewer/src/transcript.ts`, in the hosted
API's field names), with the hosted service's rules: a prompt is a top-level
input of kind prompt or slash command from a human with text left once
wrapper constructs are stripped; a nested session's activity counts toward
the prompt whose tool call started it; activity under any other input is the
session's and no prompt's (`unattributed`). `session` is the hosted session
detail's shape: each prompt carries its last text replies (`replies`, a
preview of each, with `replies_omitted` counting the earlier ones) and
`replies_included` is always true. `activity` holds, by turn id, what the
hosted service serves one prompt at a time from its activity endpoint, since
there is no such endpoint here. `user_email` is always empty, and
`cost_usd` always null.

```json
{
  "session": {
    "run_id": "aaaaaaaa-0000-4000-8000-000000000001",
    "source": "claude_code",
    "title": "Fix the parser",
    "git_branch": "feature",
    "working_dir": "~/src/widgets",
    "models": ["claude-opus"],
    "started_at": "2026-07-01T10:00:00Z",
    "ended_at": "2026-07-01T10:30:00Z",
    "outcome": "committed",
    "user_email": "",
    "turn_count": 2,
    "commit_count": 1,
    "confidence": 0.95,
    "prompts": [
      {
        "turn_id": "…", "ordinal": 1, "started_at": "…", "text": "…", "text_length": 34,
        "truncated": false, "effects": { "files_read": 2, "files_edited": 1, "reads": 3, "files_changed": 1,
        "diff_hunks": 1, "added_lines": 4, "removed_lines": 1, "tool_calls": 2, "execute_calls": 1,
        "failed_calls": 0 }, "cost_usd": null, "cost_is_floor": false, "tokens": 900, "estimated_tokens": 0,
        "replies": [{ "text": "…", "text_length": 25, "truncated": false, "timestamp": null, "model": "claude-opus" }],
        "replies_omitted": 0
      }
    ],
    "prompts_total": 2,
    "prompts_truncated": false,
    "replies_included": true,
    "totals": { "…": "as effects" },
    "unattributed": { "…": "as effects" },
    "tokens": 1800,
    "estimated_tokens": 0,
    "cost_per_prompt": true,
    "cost_usd": null,
    "cost_is_floor": false,
    "has_inner_trace": true
  },
  "activity": {
    "<turn_id>": {
      "messages": [{ "text": "…", "text_length": 25, "truncated": false, "timestamp": null, "model": "claude-opus" }],
      "messages_truncated": false,
      "invocations": [{ "model": "claude-opus", "input_tokens": 600, "output_tokens": 300, "cache_read_tokens": 0, "cache_write_tokens": 0, "started_at": null }],
      "invocations_total": 1,
      "token_usage": { "tokens": 900, "estimated_tokens": 0, "uncached_input_tokens": 600, "output_tokens": 300, "cache_read_tokens": 0, "cache_write_tokens": 0, "unclassified_tokens": 0, "api_calls": 1, "models": ["claude-opus"] },
      "tool_calls": [{ "id": "…", "name": "Bash", "verb": "execute", "input": "{\"command\":\"go test ./...\"}", "input_truncated": false, "output": "ok", "output_truncated": false, "status": "ok", "timestamp": null }],
      "tool_calls_truncated": false
    }
  },
  "truncated": false,
  "limit_bytes": 4194304
}
```

It is bounded so a session of many megabytes does not stall the page
(`transcript.DefaultLimits`): a prompt's text is cut at 32 KiB, an agent
message at 16 KiB and a tool call's input and output at 4 KiB each (at a
character, with the full length or a `*_truncated` flag beside it); the
session's own title, branch, working directory, agent and model names are cut
at 1 KiB each, and at most 50 model names are listed; a prompt carries at
most its last 10 replies, each previewed at 4 KiB (a preview is not a cut:
the whole message, to its own limit, is in the activity); a prompt's activity
lists at most 100 messages and 200 tool calls, and only its first model call
(`invocations`, which the card reads only to tell whether there were any;
`invocations_total` and `token_usage.api_calls` count them). The body as
encoded (JSON without HTML escaping) is held to 4 MiB, counted item by item:
once an item does not fit, nothing after it is listed, and the prompts left
out are reported (`prompts_truncated`, with `prompts_total`). The redaction
and home replacement after it keep each string's length or shorten it, except
that a mask splitting a multi-byte character leaves invalid bytes, each
written as U+FFFD in three bytes (two more per byte, up to four more for a
split three-byte character); 1 KiB of the 4 MiB is held back for that and for
the truncation flags. Any
cut sets `truncated`. The body is built once per session and answer and kept
with it.

The server exposes `Mux` and `Use` so a host with its own routes or
authentication, such as a later `baum ui`, extends it without changing these
rules; its handlers sit behind the host check and the headers. Cancelling the
context ends the event streams and shuts the server down.

## Running locally

`baum generate --pr N`:

1. resolves the pull request with the user's GitHub token;
2. exports the head and merge-base trees with `git archive` from a private
   repository that borrows the clone's objects, and writes the checkout under the
   same limits as above;
3. collects sessions from the home directory and Entire checkpoints and
   links them to the pull request's commits;
4. reads the pull request's rendered body with the user's token, which carries
   image URLs GitHub has already signed, and downloads the description images
   from them without credentials; `bundle` caches nothing, and writes the
   images only to the attachments archive `generate` reads;
5. loads the previous local version, if any, and builds the bundle;
6. runs the configured backend with the public prompt; tiles are off unless
   enabled;
7. validates, derives and redacts the document, and stores it as the next
   version.

Git reads (the comparison, patches, blobs and the exports) run in a private
temporary bare repository that borrows the clone's objects and has default
configuration, like the hosted git mirror. The clone's configuration,
attributes and replace refs cannot change the output, and its refs,
configuration and worktree registrations are left as they were. An export is
the hosted service's own command, `git archive --format=tar
--worktree-attributes`, so it is the same archive: the clone's attributes and
filters are not applied.

The clone is written to only when it lacks a pull request commit: baum
fetches it under `refs/baum/`, with the user's git configuration and no
hooks. A partial clone missing the pull request's objects is reported, not
fetched from on demand.

`baum bundle` stops after step 5, and never fetches: a pull request commit
the clone lacks fails the command with the `git fetch` to run, so it reads
git objects only and leaves refs, index, working tree and configuration
alone. It is `internal/assemble`, which follows the hosted assembler's order
over the same `bundle` functions with the sources above behind small
interfaces (`GitHub`, `Git`, `Sessions`, `ImageFetcher`): description images,
the pull request header, checks and conversation, the diff index, commit
changes, sessions, references, attachments, `REVIEW.md` at the base commit,
the checkout export, then caps, redaction, byte trimming and the digest. Sessions
follow the comparison because they need its merge base. Command targets are
first checked by `locallink` and then, as the hosted builder does, by
`bundle.KeepKnownCommandTargets` against the diff's paths, so a target a
session names depends on the pull request, not on the working tree. A
session source that fails leaves the bundle without sessions and a warning;
cancellation is an error. Image downloads carry no credentials: the user's
token only reads the rendered body, in which GitHub has signed each image
URL, and only GitHub's upload hosts are contacted (see SECURITY.md). A
previous document can be given in `Request.Previous`, as `generate` will;
`baum bundle` leaves it nil. The
bundle is written as `json.Marshal` encodes it plus a newline, so its digest
is the SHA-256 of the output without that newline, and it holds no timestamp
of its own: the same inputs produce the same bytes. `Assemble` can also
write the checkout (a gzip tar read by `bundle.UnpackCheckout`) and the
attachments (a tar read by `bundle.UnpackAttachments`) to the writers in its
`Request`, for `baum generate`; `baum bundle` discards both. The checkout is
exported either way, since a file the checkout budget omits makes its hunks
textless. `baum validate`  checks a stored document with
`review.DocumentValidator.ValidateStored`, against its bundle when one is given,
and `baum refresh` runs the `review` steps on an existing document. `baum view`
serves the viewer, the document, its versions, the cached images, and the sessions and commits linked
to the pull request, read from the local session stores with `sessions` and
`linkage`. When those sessions are not on this machine, the viewer shows the
document's own session metadata.

Estimated cost comes from a price list at
`https://static.usebaum.com/pricing/prices.json`. baum verifies its `.sig`
with a public key embedded in the binary, caches it under the XDG cache
directory, and falls back to a snapshot embedded at release when the list
cannot be fetched or verified.
