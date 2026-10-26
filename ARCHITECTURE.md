# Architecture

baum turns a pull request into a `baum.review/1` document and renders it. The
same packages run in two hosts: the `baum` binary on a developer's machine,
and the hosted baum service, which imports them from this module. Both hosts
support every field in the schema; they differ only in where evidence comes
from and what they do with the result.

The dependency runs one way. The hosted service imports these packages, but
the review commands of the `baum` binary never use hosted baum data and never
contact a baum server: everything they read comes from the user's machine,
GitHub (with the user's own token) or the model provider the user configures.
The one other contact is the public, signed price list described under
"Running locally".

The same binary also carries the hosted service's client commands (`login`,
`logout`, `whoami`, `setup`, `upload` and the `hooks` dispatchers), listed
under "Hosted service" in `--help`. They are opt-in: nothing reaches a baum
server before `baum login`, and the hook commands committed to repositories
run baum only for a logged-in user, in a clone set up for the hosted service,
or, for a hosted install, in a repository onboarded to it; for everyone else
they fall through to Entire. See "Hosted service client" below.

This file describes the package boundaries and the extension points a host
implements. The packages are being moved here in stages: so far `bundle` holds
the evidence types, the diff index, the `REVIEW.md` loader, reference
extraction, the code checkout archive, description image handling, and the
caps, input redaction, byte trimming and digest applied to a bundle,
`review` holds the document model, validation, history, refresh, output
redaction and the tile catalogue, and `agent` holds the backend interface,
request, result, usage and event types, the budgets and the local environment
allowlist, and `decisions` holds the decision schema, the cleaned session
transcript, the reply parser and the citation validator; the Go side of
`viewer` only embeds the built app. Until a package
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
| `viewer` | the React components that render a document and its versions, and the standalone single-page app that `baum internal view` embeds |
| `sessions`, `linkage` | session discovery and commit-to-session matching, used by `bundle` sources |
| `decisions` | the decisions an agent session made: the decision schema, the cleaned transcript a model reads, the strict reply schema and parser, and the citation validator; no prompt |
| `validation/commitlabel` | deterministic validation-run commit labels from supplied session events and verified Git objects; [rule and limits](validation/commitlabel/README.md) |
| `redact` | the one redaction engine used for input, output and logs |
| `githubapi` | GitHub API plumbing every host shares: the GraphQL transport that turns primary and secondary rate limits into a typed error with the reset time, and GitHub's `BigInt` and `GitObjectID` scalars |
| `hosted/...` | the hosted service's client: the `baumcli` commands, the `apiclient` HTTP client and its protocol handshake, the `traceupload` archive builder, and the committed agent-hook vocabulary in `agenthooks`, which the hosted onboarding pull request writes too, and `agentplugins`, which installs user-scoped marketplace plugins through native agent CLIs |

Code that only the CLI needs (flag parsing, configuration, the local version
store, the `doctor` diagnostics) stays under `internal/`.

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

`internal/github` provides the GraphQL client `baum internal bundle` reads
GitHub with and the bounded read-only GraphQL and REST transport local inbox
discovery uses. It parses repositories, remote URLs and SSH host aliases
through the go-gh library
(`github.com/cli/go-gh/v2`: `auth`, `repository` and `ssh`). Bundle requests use
`shurcooL-graphql`; discovery sends bounded requests through `RESTReader`,
including its `GraphQL` method. Neither uses go-gh's API client, which sends
the user's time zone and preview media types and
rewrites control characters in every response, which would make a local
bundle's text, and so its digest, differ from the hosted service's. baum
also keeps what it fetches, the pagination, head pinning, rate-limit
backoff, `sources` and the mapping onto the bundle.

- **Hosts.** github.com, GitHub Enterprise Cloud with data residency
  (`*.ghe.com`) and GitHub Enterprise Server are all supported. A host is
  read only when baum recognizes it: github.com, a `*.ghe.com` tenant, or the
  Enterprise host explicitly named by `GH_HOST`. That holds for `--repo host/owner/name` as for a remote:
  any other host is refused before a token is looked up, and is never sent
  one. The endpoint is derived as gh derives it: `api.github.com` for
  github.com, `api.<tenant>.ghe.com` for a tenant, and
  `https://<host>/api/graphql` for an Enterprise Server. An `api_host`
  override in GitHub CLI configuration is not honoured.
- **Tokens.** Credentials are resolved only for the repository host. GitHub.com
  uses `GITHUB_TOKEN`, `GH_TOKEN`, `github_token` in configuration, then baum's
  own OS credential store. Device authorization and refresh are owned by
  `internal/githubauth`; no GitHub CLI process or credential registry is read.
  Enterprise credentials require an explicit matching `GH_HOST`:
  `GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN` for Enterprise Server, or
  `GH_TOKEN`/`GITHUB_TOKEN` for a `*.ghe.com` tenant. Public GitHub credentials
  never flow to another host by default. Cached GraphQL clients resolve tokens
  per request; discovery readers fail if their credential partition changes.
  The protected local `/api/github-auth` API exposes safe status and public
  device codes while Go owns polling. Account changes cancel discovery and
  evidence work using the saved GitHub.com identity, fence its publication, and
  reset automatic scheduling baselines. Reviews using explicit credentials or
  Enterprise authentication are unaffected by saved-account changes.
  See [`internal/githubauth`](internal/githubauth/README.md).
- **Requests.** The token goes only to the host's derived API endpoint, the
  client follows no redirect at all, and requests identify `User-Agent: baum`.
  REST discovery also sends the JSON accept header, a pinned API version and
  conditional ETags. Bundle requests are not cached and nothing is logged;
  `GH_DEBUG` has no effect. Discovery caches only its minimal inbox response fields, as described
  below. Responses reach the mapping exactly as GitHub sent them.
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
  The hunks of one file are redacted together, as the file's patch, so the
  engine follows a key that runs on from one hunk into the next, past the
  hunk header between them, and masks it in both (goredact v0.4.0 and
  later); each hunk header keeps its `@@ -a,b +c,d @@` line ranges, while
  the section heading after them stays masked when it is a line of the key.
  Patches are also deliberately over-masked for key bodies shown without
  their armor, as an edit inside a committed key shows them: every run of
  three or more lines whose content is pure base64 (`[A-Za-z0-9+/=]`) of at
  least 40 bytes is masked, with a shorter base64 line at either end, and so
  is every hunk heading that is pure base64 of at least 40 bytes. Embedded
  base64 data in a diff is masked the same way.
  The per-hunk line cap cuts after redaction: parsing keeps 16 KiB past
  the cap, and keeps a file's whole patch until redaction when the cap cuts
  a hunk other than its last, so a key the cut runs through is masked
  whole, as it would be in the full patch. A previous section's code excerpt
  lines are redacted as one text too.
  @mentions are not rewritten; a host defangs them where it posts text.
  SHAs, IDs and closed enums are exempt. The checkout is not redacted; it is
  the repository the user already has.
- Trimming cuts in a fixed order and never drops a hunk. Each cut adds a
  `generator.truncated` note. When whole sessions are dropped, one note gives
  the total number omitted; its bytes count against the size limit.
- The checkout has `head/` and `merge-base/` trees, read-only, without Git
  metadata. Files over the size limit, binary files (a NUL byte in the first
  8000 bytes, as git decides; a compared file the diff holds as text is kept,
  since the export applies working-tree-encoding) and submodule contents are
  omitted with a reason; their hunks stay in the index and must still be placed. The
  compared paths are exported first, at the head and then at the merge base,
  and the rest of the head tree fills what the total byte and file budgets
  leave: a file that does not fit is omitted (`omitted_budget`) instead of
  failing the export. An agent unpacking the checkout enforces the same limits
  again and rejects an archive over them.
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
  must satisfy; it also accepts tiles of the kinds the schema retired
  (`schema/legacy-review-tiles.json`);
- the **strict authored** contract: what the model is responsible for;
- the **lenient model-facing** schema, which accepts the shapes models
  commonly produce. Normalisation maps it onto the strict contract without a
  model call.

The stored contract stays strict; leniency exists only in derived views.

### Validation and derivation

- Validation reports every finding in one pass, each located to the item it
  concerns, so a repair can fix them all at once. An anchor that does not
  resolve is shown as unresolved; it never rejects a document.
  File anchors resolve only to regular files inside a checkout tree with
  exactly matching path spelling, even on case-insensitive filesystems;
  evidence citing an unavailable file becomes asserted.
- A hunk may belong to several sections or to none; unplaced hunks are listed
  as `unassigned_hunks`. At most one section is `mechanical`.
- Derived fields are never model-written: statistics, section states, hunk
  tiers, the index, agent reviews, the review exchange, each section's
  `review_score` and the document's `risk_score`. `risk_score` is the highest
  current section score, never an average, with floors set by active high and
  critical risks.
- A count the tool cannot establish is null, never zero.
- `summary.context` (how the change fits into the codebase: a required
  `area` and optional `reached_by`, `contracts` and `related` parts, each
  text with references to files or directories) is optional in the stored
  schema, for documents generated before it existed, and required in the
  strict authored contract, so its absence is a repair finding. Normalisation drops optional parts
  without text and makes reference paths repository-relative; with a
  checkout in the run directory it keeps only paths whose spelling exactly
  matches a file or directory inside either tree, including the case of
  every path component even on case-insensitive filesystems. It never spends
  a repair on a reference.

### History

`review.PreviousVersionOf` converts a stored document into `bundle.PreviousVersion`,
stripping section `derived` fields, applying positional labels to blank titles
and carrying the hunk ranges that let anchors follow a shifted hunk. Both the
hosted builder and local generation call this helper.

Given the previous version, `review` keeps sections stable:

- earlier sections stay first, in their original order, with the same IDs;
- a section whose hunks are all gone becomes `stale`: still readable, but no
  longer part of the current review;
- each section's state (`new`, `changed`, `unchanged`, `stale`) is derived by
  comparing its hunk IDs with the previous version;
- a risk the model omits becomes `mitigated`, and a risk that returns keeps
  its `introduced_version`. A repair cannot drop a risk.

### Stored documents

The schema tightens in place, keeping its `$id`, so a document stored before
a change can fail the schema it is read with now. Every reader of a stored
document (`Refresh`, `ValidateStored`, `PreviousVersionOf`) first applies the
stored-document rules in `stored.go`, which rewrite only what the current
schema rejects, deterministically and idempotently, keeping references
consistent; a document that passes is unchanged. `Refresh` stores the
rewrite. A stored document the rules cannot repair fails `Refresh` with a
`StoredDocumentError`, so a host can tell it from a failure of the refresh
itself. A rule for another in-place change belongs with them.

- A blank section title is labelled by its position (`SectionTitle`).
- Identifiers, identifier references and risk categories have been bounded
  at 128 characters (`MaxIdentifierLength`) since schema revision
  2026-10-07.1. `ShortenIdentifier` rewrites a longer one as its first 115
  characters without trailing underscores, an underscore and the first 12
  hexadecimal digits of the SHA-256 of the whole identifier: it keeps the
  prefix and pattern, two identifiers sharing their first characters stay
  distinct, and a shortened one is unchanged. The rule applies wherever the
  schema closes a value with an identifier pattern, then to every other string
  that equals an identifier it shortened (a summary attention reference, which
  the schema leaves open), never to a longer string, such as prose, that
mentions one. Generation still
  rejects an identifier over the bound. `ShortenStoredIdentifiers` applies
  this rule alone to an encoded document, for a host that serves stored
  documents and keys records of its own (read marks, approvals) by their
  identifiers; such a host compares a stored key through `ShortenIdentifier`.
- Tile kinds and shapes changed in place before they were finalised. A
  stored tile the current `$defs/tile` accepts is kept, and so is one of a
  retired kind the viewer still renders: `schema/legacy-review-tiles.json`
  (`schema.LegacyTiles`) defines those kinds, and the stored schema accepts
  them. A field the schema removed from a current kind, which the viewer
  ignores (an architecture tile's `context` and `deployment`), is removed.
  Any other tile of a section or risk is dropped, and so is one the
  document's own tile rules reject: its internal references must resolve,
  which removing a retired field can break, and a tile kept before it in the
  same section or risk must not have its ID. Nothing outside a tile refers to
  it. The rule runs after the identifier rule, so a tile whose only fault is
  a long identifier is kept. Generation never offers a retired
  kind: the authored and model-facing contracts derive from `review-1.json`
  alone.

### Refresh and redaction

`Refresh` updates checks, threads, comments and evidence status in a stored
document without a model call, after the stored-document rules; both the
platform and `baum internal refresh` use it. It
rebuilds the session index from the bundle and carries over, for each readable session the
bundle still links, the stored index's prompts at turns the bundle lacks
that are timestamped at or before the document's `generated_at`, as stored,
so prompts that decisions cited at generation keep their bounded text while
a prompt only an earlier refresh added is not carried again. Local refresh also
reconstructs the reviewed hunks' IDs, paths, roles and line ranges from the saved
index, so agent findings keep their section and hunk locations and section
finding counts follow resolution and reopening.

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
whole and from every run, past its first 16 runs a 256-byte window at a time,
each distinct window of the value once and at most 256 KiB of such windows
per value and 1 MiB per document, then masked whole, and counts only if a
secret was found in it, so the scan stays linear in the document's size and
a harmless value never fails the run on its own.

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
underscore or its end) is masked whole and counted. A rule may take a key
together with its context: a URL's host or an address's domain
(`KEY@db.example.com`), or a hyphenated tail (`sk-ant-…-prod-east`). So each
part of such a finding before its last `@`, split at `:` and `/`, is a source
too, which matches a password no rule detects alone without its host, and the
finding's parts are scanned again: of the finding and of each key found in a
part, the shortest prefix ending before a run of separators that a rule other
than the e-mail rule still detects whole is a source. A source exists only
once a finding has failed the run, so a harmless identifier is masked this
way only in a run that fails already. Free text is not scanned for secrets by
`Identifiers`, and `Document` masks it after `Prepare`; but `Identifiers`
masks the words of free text whose respelling is a source, such as the
secret repeated upper-cased or respelled in prose, counted in `Masked` only.
The order is `Identifiers`, `Prepare`, `Document` on what `Prepare`
returned, with one redactor, then the `MaskedClosed` check. Text scattered across unrelated
fields, a token wrapped mid-value, or a secret no rule detects on its own,
such as a bare hex string, which reads as a SHA, is not caught. If redaction
fails, the text is dropped rather than kept.

Known limits of the identifier pass:

- A secret found only in free text is masked there, but an identifier that
  holds it in a spelling no rule detects is not matched to it.
- Context is trimmed from a key only where a rule detects the key alone, or
  where it is the host after a credential's `@`; a secret no rule detects
  alone, found with a tail or a prefix (a keyword rule's value), is a source
  only as found.
- In a value over its bound, a secret past its first 16 runs of separators
  that is longer than its 256-byte window, or lies past the value's or the
  document's window budget, is masked with the rest of the value but not
  found, so it is neither counted nor a source.
- Sources, and the findings trimmed into more, are capped at 64 KiB each per
  document; in a run with more, which fails already, other spellings of the
  secrets left out may stay in the rejected document.
- Matching is by whole words, so a key glued without a separator to letters
  or digits in another identifier or in free text (`xakia…`) is not matched
  there.

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
| `anthropic` | the Anthropic Messages API directly, with read-only file tools over the run directory, submitting through a `StructuredOutput` tool (`agent/anthropic`) |
| `claude` | `claude -p` restricted to read-only tools, submitting through structured output (`agent/claude`) |
| `codex` | `codex exec` in its read-only sandbox, with a strict-mode output schema (`agent/codex`) |
| `cursor-agent` | `cursor-agent -p` in Cursor's read-only ask mode with its sandbox enabled, the schema carried in the prompt (`agent/cursor`) |

The CLI backends rely on each tool's own read-only mode; baum does not add a
container. [SECURITY.md](SECURITY.md#agent-isolation) describes what that
leaves open, which for `cursor-agent` is more than for the others.

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

`agent.Fixer` is the one write mode, for a fix a person confirmed
(`internal/fix`, see "Fix jobs" below): `Fix` runs the agent over a checkout
it may edit, with no schema, and returns its final message as the summary.
Only the local profiles implement it. `claude` keeps restricted mode and the
other review flags but has `Read,Glob,Grep,Edit,Write` and `--permission-mode
acceptEdits`: it edits files and runs nothing. `codex` runs `--sandbox
workspace-write` with `sandbox_workspace_write.network_access=false`,
`exclude_slash_tmp`, `exclude_tmpdir_env_var` and `writable_roots` set to
`Request.Scratch`, which is also its commands' `TMPDIR`. `cursor-agent` and
`anthropic` have no write mode. The fix instructions are
`prompt.Fix` (`fix-public-v1.md` with `fix-conflicts.md` and `fix-checks.md`).

`agent/cursor` runs the Cursor Agent CLI under the user's own login; it has
one profile and refuses a request with an `Endpoint`. Before its first run a
`Backend` resolves the executable (`Binary`, else `cursor-agent` on `PATH`,
else `agent` when its help names the Cursor Agent) and checks `--help` for every
option and value a run passes, since Cursor's dated releases give no version
to compare; a missing one asks for `cursor-agent update`. A run is
`cursor-agent --print --output-format stream-json --mode ask --sandbox enabled
--workspace <run> --trust [--model M]` in the run directory, with
`LocalEnvironment` plus `CURSOR_CONFIG_DIR`, `CURSOR_DATA_DIR` and
`AGENT_CLI_CREDENTIAL_STORE` (never `CURSOR_API_KEY`) and `NO_OPEN_BROWSER=1`.
The prompt goes on stdin with the model schema appended under an "Output
schema" heading, because the CLI has no structured output. The document is the
final message (every assistant text after the last tool call, retry or user
turn, joined, since the CLI may split it, and at most the document bound; the
result's text when that holds no JSON value): one
JSON object is kept, a fenced one or one with prose around it is unwrapped, and anything else goes to the validator as it is, so the loop
rejects and repairs it. Any tool call other than reading, listing and
searching files and the to-do list (`readToolCall`, `grepToolCall`,
`globToolCall`, `lsToolCall`, `semSearchToolCall`, `readLintsToolCall`,
`readTodosToolCall`, `updateTodosToolCall`) stops the run with the
`disallowed_tool` status, as does any event off the allowlist of event types,
subtypes and content blocks a reading run writes (`system` init, `user`,
`assistant` text, `tool_call` started and completed, `thinking`, `result`,
`retry`, `connection`, and `interaction_query` only for a question) or one that
does not decode: detection fails closed; the stream reports a call as it
starts, so this detects rather than prevents. These failures, a missing option,
an unavailable sandbox and a missing login are not retried: each unwraps to an
`agent.RejectedError` for the loop, but its own text names cursor-agent
("cursor-agent cannot run: ..." or "cursor-agent run stopped: ...") rather than
the model provider. Model calls are the distinct `model_call_id`s of
the stream plus the final answer, checked against `MaxCalls` as they appear.
Usage is the result event's run total (`inputTokens` already excludes cache
reads and writes), checked against `MaxTokens` when it arrives; a result
without it marks the usage `Unreported`. `Result.Model` is the configured model
(`configured`) or the display name the CLI's init event reports
(`cli_reported`); neither is a provider model ID and no speed or cache TTL is
reported. Cursor bills the run through the user's subscription, so
`pricing.List.BackendEstimate`, `BackendOutcomeCost` and
`BackendAccountingCost` return nil with `billed_by_cursor_subscription` for
this backend whatever its model is called: the recorded cost is unknown, and a
generation-queue job in the daemon carries it as `Prepared.EstimateReason` with a nil estimate
instead of an estimate at API rates. Such a job is not blocked: `Queue.SubmitConfirmed`
queues it only with `Confirmation.Unpriced` (`confirm_unpriced`) on top of the
estimate and any isolation confirmation. A failed process is classified
from its stderr (`unsupported_option`, `sandbox_unavailable`,
`authentication_error`); none is retried, as every attempt would fail the same
way until the user acts.

`agent/anthropic` is not a CLI: baum runs the model call loop itself, over
`net/http` rather than the official SDK (one endpoint, retries that must count
against the run's deadline, and no new dependency in this published module).
Each call sends the system prompt, four tools in a fixed order (`Read`, `Glob`,
`Grep`, and `StructuredOutput`, whose input schema is the request's schema),
the prompt as the first user message with a cache breakpoint after it, and
top-level automatic caching for the growing conversation, both with the
5-minute TTL. Each call is streamed (server-sent events read with the
standard library) and the message is assembled from its events; assistant
turns, thinking blocks and their signatures included, are sent back with the
content the stream delivered. The tools are confined to the run directory through an `os.Root`
(absolute paths, `..` elements and escaping links are refused; listing and
searching never follow a link) and bounded in file size, matches, images and
output bytes; globs are matched by dynamic programming with at most eight
`**` segments, so a pattern cannot make a walk run long. A `StructuredOutput` input that does not match the schema goes back to
the model as a tool error and counts as a structured output rejection; a turn
that ends without one is nudged twice before the run fails. The key and base
URL come from the request's `Endpoint`, else the `Backend`'s own (the local
host sets the key from `ANTHROPIC_API_KEY`). The default model is
`claude-opus-5-5` at `medium` effort; another model is sent without an effort.

Every request sent is one call, charged before the next is made: a call the
request's `MaxCalls` or `MaxTokens` cannot pay for is not sent, and
`max_tokens` is the smaller of 32,000 and the tokens left. Usage is the API's
per-call report in disjoint buckets (message_start's, updated by
message_delta's cumulative counts), with the 1-hour share of cache writes
(zero when unreported, since only the 5-minute TTL is requested), the speed
(standard unless reported) and each call's prompt size, so `Accounting` prices
it per call. A request that was sent without a complete, usable response marks
the usage `Unreported`. Before any response, 429, 529, other 5xx and 408
responses, failed connections and a response that does not start within two
minutes are retried four times, honouring `retry-after` (at most a minute) or
backing off exponentially, and a wait that would outlast the context's
deadline ends the call instead. Once a 200 response has started it is never
retried: a stream that ends early, carries an error event or is silent for
ten minutes (the SDKs' read timeout; a healthy stream can be silent for
minutes while the API buffers a tool input or the model thinks without
displaying it) fails the call. Such a call is billed with unknown usage, so
the run's cost becomes unknown, and the generate-and-repair loop then retries
the whole run within `MaxProviderAttempts`, sending the conversation again.
A tool input or block a `max_tokens` or `refusal` stop leaves incomplete is
not malformed: the stop reason decides, so a truncated call is answered as not
run and a refusal stays a refusal; an incomplete one under any other stop
reason, or a stream line over 8 MiB, is `malformed_response`. Failures are an `*anthropic.Error` with a fixed
`Category` (`authentication_error`, `rate_limit`, `provider_overloaded`,
`provider_error`, `transport_error`, `invalid_request`, `malformed_response`,
`refusal`, `context_window_exceeded`, `no_document`), which is also the status
of the run's error event; a spent budget is the loop's budget reason. The
first, `invalid_request`, `refusal` and `context_window_exceeded` come wrapped
in an `agent.RejectedError`, which the generate-and-repair loop does not retry
and whose text starts with `agent.ProviderRejectedReason(status)`. No error
text, event or log line holds the key.

The `claude` and `codex` backends classify a failed call by the provider's
HTTP status where the harness reports one (`agent.HTTPErrorStatus`): Claude
Code's `api_error_status` on its failed `result`, and the status Codex names in
a failed turn's message (`unexpected status NNN`). A Codex 400 carries only the
response body, whose structured fields classify it (`"type":
"invalid_request_error"`, `"code": "invalid_json_schema"`), as `last status:
429` does an exhausted rate limit; only such an OpenAI-shaped 400 refuses, and
a 400 whose body lacks them is retried as unavailable. Codex's 426 answer to
its WebSocket attempt is a silent fallback to HTTPS and never reaches this
classification. An error event whose class was read this way, rather than
from free text, has `Event.FromProvider` set (`agent.LastProviderErrorStatus`). A 4xx other than 408 and 429
(`authentication_error` for 401 and 403, `not_found` for 404,
`upgrade_required` for 426, `invalid_schema`, `invalid_request`) is a refusal a
retry cannot change and comes back as an `agent.RejectedError`. Every other
failure, 408, 429 and 5xx included, is `the model provider was unavailable`,
and so is any failure classified from free text alone (`agent.ErrorStatus`),
which only labels the diagnostics: a request ID or a quoted run can read like a
status.

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

The module ships and maintains its own public prompt in `agent/prompt`:
`review-public-v3.md`, shared by every backend and ending with a "Submitting"
heading, plus a submit block per backend (`submit-claude.md`, used for claude
and anthropic, `submit-codex.md`, and `submit-cursor.md`, which points at the
schema the cursor-agent backend appends and lists the tools that stop a run),
embedded with `go:embed`.
`Prompts.Instructions` appends the backend's block under that heading, sets
`Version` to `review-public-v3` and `Digest` to the sha256 of the composed body,
and fails for any other backend. A host may supply another implementation; the
hosted service does, with its private prompts. `StaticPrompts` is a fixed
implementation for tests and simple hosts. The prompt is per backend because
each backend submits its result differently. `GroupPrompts` is the local
review group's prompt (`group-review-public-v1`): the same text with
`group-public-v1.md` inserted before the Submitting heading, which says how a
group's bundle, per-member checkout and `members.json` differ and what a
combined review covers (see "Review groups").

A host can also pass `Run.Decorate`, which the loop applies to every document
the model returns before masking and validation, to fill what the host
decides and the model never writes: a review group's `stack` and each
section's `pull_requests`.

The loop owns the rest of what a run sends:

- **Tile guidance.** It appends `tilecatalogue.Instructions(kinds)` to the
  body for the offered tile kinds, so a prompt never carries it. A `Prompts`
  implementation that rewrites its body per backend therefore rewrites the body
  only.
- **Repair text.** `agent.RepairText(backend)` returns the text that opens and
  closes a repair prompt. A backend that submits through a `StructuredOutput`
  tool (`claude`, `anthropic`) is told to call it again; `codex` and
  `cursor-agent`, which answer in their final message, are told to reply with
  the entire corrected document there and to use their file tools when a
  finding needs the file. A prompt's
  `Repair` and `RepairEnding`, when not empty, replace that text, for a host
  whose instructions name the tools differently. They are not part of the
  prompt's JSON encoding.

Local generation (`baum internal generate`, in `internal/generate`) starts every prompt
it sends, repairs included, with `sessions.GenerationRunMarker`; its `Prompts`
wrapper prepends the marker to the body. That is the contract session
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
`1<<min(n,4)` seconds), except a spent budget and an `agent.RejectedError` (a
rejected key, a refused request, a declined prompt, a conversation over the
context window), which end the run with their own reason.

A stall watchdog bounds each attempt when the budgets set `StallTimeout` or
`ToolTimeout` (zero disables each). Backends report to the request's
`Activity`: every harness event is progress, and each tool runs from its start
to its end: Codex's `item.started` and `item.completed` for commands, file
changes, MCP calls and web searches, and Claude Code's `tool_use` and matching
`tool_result`. With an `Endpoint`, the attempt reaches the provider through a
loopback forwarder that passes requests and streamed responses through
unchanged and counts the requests open, so a long model call is never idle.
An attempt is stalled when one tool runs past `ToolTimeout`, or when no model
request is open, no tool runs and nothing has progressed for `StallTimeout`.
Without an `Endpoint` the loop cannot see model calls, so only `ToolTimeout`
applies. An open request is never idle, however long it is silent: a stream
that stops mid-response is left to the harness's own stream timeout and the
run's wall clock. A stalled attempt is cancelled, logged (`review agent
stalled`, with the cause, `tool_timeout` or `idle`, and the tool and its item
ID) and recorded as a `stalled` diagnostic, and retried at once; it counts as
a provider attempt but does not use up `MaxProviderAttempts`. A second stall
anywhere in the run, repairs included, ends it with `ReasonStalled`. An
attempt that returned its result is kept even if the watchdog fired as it
did. The Codex backend logs each
turn and each tool with its command (at most 512 bytes), duration, exit code
and status, never its output.

The document goes through `review`: the validator
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
otherwise returns an error: a spent budget, `ReasonProviderUnavailable`,
`ReasonStalled`, or the cancelled context.

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

### Tasks

```go
generator := agent.Generator{Backend: backend, Logger: logger}
outcome, document, err := generator.RunTask(ctx, agent.Task{
	Directory: dir, Prompt: prompt, Schema: schema, Check: check,
	Budgets: budgets, Endpoint: endpoint})
```

A task is a run that is not a review: a prompt over a run directory (a
checkout and input files the caller put there) whose reply is a JSON document
of the task's schema. Only the Generator's `Backend`, `Logger` and
`RetryDelay` take part. `RunTask` writes `schema.json` to the directory and
sends the prompt as it is, marked as a task (`Request.Task`) so the Claude and
Anthropic backends frame it with a generic system prompt and nudge instead of
the review's, with the same provider retries, stall watchdog and
budgets as `Generate`. Each reply is checked against the schema, then by the
task's `Check`, which returns findings; a rejected reply is written as
`rejected-N.json` and sent back with the findings and the backend's
`RepairText` ending, up to `MaxRepairAttempts` (at least one). The result is
as for a review: the document, an `Outcome` (no schema revision or identifier
pass), `ReasonValidationRejected` with the last rejected document after the
last repair, or an error. `RunTask` logs sizes and counts only, never what the
model wrote. A host masks the outcome with `RedactTaskOutput`, which is
`RedactOutput` without a review contract: every string of the document, keys
included, is free text, and none is a closed value.

## `decisions`

A decision is a choice between plausible alternatives that lands in the
repository, made by the human, by the agent, or proposed by the agent and
approved by the human. `decisions` holds everything about extracting them
that does not depend on where a session is stored or which prompt asks for
them; the hosted service imports it with its own private prompt.

- **Transcript.** `Render` reduces a session tree
  (`sessions/session`) to one turn per root-session user input, numbered by
  the stored input ordinal, never renumbered. A turn keeps the human prompt,
  the agent's visible text, question, plan and denial dialogue, subagent
  final reports and the repo-relative files edited during the turn. Tool
  inputs and outputs, file reads, raw command lines and reasoning are
  dropped. `WithVendor` and `WithSessionFiles` pass what
  `linkage/sessioncorrelate` needs to make paths repo-relative.
- **Masking.** Before text reaches the `Redactor` (secrets and e-mail
  addresses, `bundle.Redactor`), the tree's recorded repository and worktree
  roots and working directories are stripped, any other local path becomes
  `<local path>`, and account names from the tree's home-directory paths are
  masked as `user` where they are joined to path, host or branch punctuation.
  Masking runs before any trimming. It is best effort.
- **Tree.** `NewTree` exposes the rules `Render` applies (dialogue and
  subagent detection, edited paths, path masking and row order) so another
  render of the same tree, such as a validation transcript, shares them
  instead of copying them.
- **Extraction.** `NewExtractor(systemPrompt)` builds one structured-output
  request through a `Model` the host implements: the prompt as the system
  message, `Transcript.Text()` (or an extension's message, below) as the
  user message, and `ExtractSchema`, a
  strict JSON Schema whose properties marshal in the order the model should
  write them. `PromptVersion` is the first 12 hex characters of the SHA-256 of
  the prompt and the marshalled schema, so cached extractions can be keyed on
  it. The package ships no prompt.
- **Reply.** `ParseReply` decodes the reply strictly (unknown, missing or
  trailing data is an error) and normalizes it. `ValidateCitations` keeps the
  model's order and removes only what is wrong: turns that are not transcript
  ordinals, files not edited in a remaining cited turn, an unknown trigger, a
  decided turn that is not cited, and evidence that is not a verbatim quote of
  one passage of a cited turn. A decision with no valid turn or an invalid
  shape is dropped whole. Every removal is reported in `Extraction.Dropped`.
- **Decision IDs.** A reply never carries an ID and none is derived.
  `Identified` gives each decision without one a random UUID
  (`decision_id`); a host calls it when it first stores an extraction and
  keeps the IDs from then on.
- **Extension.** When a session grows, an extraction extends the earlier one
  instead of starting over, so earlier decisions and their IDs stay as they
  were. `ExtractOptions.Existing` holds the earlier decisions and `Cutoff`
  the last turn they cover. The user message then lists the existing
  decisions by handle (`d1`, `d2`, ...; IDs never appear), followed by the
  whole transcript with a marker line before the first turn after the
  cutoff. The reply's `superseded_existing` names the handles a new decision
  reverses; they are marked superseded only when a new decision survives
  validation. A new decision none of whose turns is after the cutoff is
  dropped. The result is the existing decisions, then the new ones without
  IDs. `ContentHash` keys a cached extraction on `Transcript.Text()`;
  `PrefixHash` hashes the transcript without its open last turn, and
  `ExtensionCutoff` finds that prefix in the grown transcript to give the
  cutoff. The host's prompt explains the extension form to the model.

## `viewer`

`viewer` is a pnpm package of React components that render a document: the
summary, sections and their blocks, risks, tiles, hunk evidence, images,
checks, sessions and the version history with comparison. It also builds a
standalone single-page app, which the `baum` binary embeds and `baum internal view`
serves on localhost.

The viewer reads only the document and its versions. A host connects it
through props and context:

| Seam | Required | `baum internal view` | Notes |
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

The host preserves each hunk's rename source as optional
`index.hunks[].old_path`, so Context links to paths renamed away can use the
merge base even without a host file list. Stack review generation also stores
`index.file_comparisons`: each member's comparison base and file changes,
independently of hunk-ID deduplication. The viewer applies all removals before
surviving destinations within each comparison, in member order. Paths renamed
away or removed link into the member tree that contains them; paths still
present link at the stack head. Older stacks retain their fallback.

The sans-serif font comes from the `--viewer-font-sans` CSS variable, which
defaults to baum's bundled type, Instrument Sans for text and Manrope for
headings, both bundled with the package under the SIL Open Font License.
A host overrides the variable to restyle the viewer without forking it; the
hosted service sets its own licensed face this way.

The components and their seams are in [`viewer/`](viewer/README.md). The
package also builds the standalone app (`viewer/app/`) with `pnpm build:app`
into `internal/viewer/dist`, which the `internal/viewer` Go package embeds. The
built output is committed only in a release commit, the commit a `v*` tag
points at: the tag's commit on `main` plus the built viewer and local app, so
`go install` of a tagged version needs nothing but Go. Any other checkout
embeds an empty directory (its `.gitkeep`) until `just viewer-build` runs, and
a binary built from it answers each app page with `viewer.NotBuilt`, a 503
naming the fix.

`baum internal view`'s app fills the `sessions` and `commits` slots from
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

### How `baum internal view` serves it

`internal/viewserver` is the host. `baum internal view` loads one document file (or the
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
commits using only this machine; `baum internal view` serves it, and the session
evidence that `baum internal bundle` collects reuses its loading and matching (see
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
   Entire checkpoint stores, runs in the private repository `gitlocal` makes,
   which has no remote and no promisor configuration, with
   `GIT_NO_LAZY_FETCH=1`, so even a partial clone cannot download an object;
   a checkpoint whose objects are missing is skipped with a warning. Entire
   keeps checkpoints in two stores, read alike (see step 6), and clones usually
   lack the git-refs one, since a clone does not fetch it; fetching it is the
   user's to do (`git fetch <remote>
   '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'`; the `+` lets a
   rewritten ref update), never baum's. Only the
   remotes' URLs are read from configuration, never contacted.
4. Loads the sessions with `sessions.Load`, only those last modified within
   `locallink.SessionWindow` (two days: the matcher's one-day timeframe tolerance plus a day of margin) before the first commit's date, and Entire checkpoints are dated from the branch's own history, or, for a ref, its commit's date, so older ones are skipped unread.
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
6. Reads each commit's `Entire-Checkpoint` trailers from its whole message
   (`git log` `%B`) with `sessions/entirecheckpoint.ParseMessage`, the parser
   the hosted resolver uses. Entire reads the whole message, so a GitHub
   squash merge, which copies each squashed commit's trailers into its body
   paragraphs, names every squashed commit's checkpoint; other trailers are
   read from git's trailer block alone. It takes every id Entire's own pattern
   finds, mid-line included, and every line shaped like the trailer: a
   case-insensitive key at the start of a line, and values that are legacy
   12-hex ids or 26-character Crockford ULIDs in any case, made canonical (hex
   lower-case, ULID upper-case) and kept in message order, each once. A malformed value
   beside a valid one is skipped with a warning line; a commit with no valid
   value is left to the heuristics. Each id is looked up in the clone's two
   Entire stores, reading only its metadata in the private repository, and
   maps to the sessions the checkpoint records; a commit naming several
   checkpoints maps to the union of their sessions. The stores are:
   the git-refs store, one ref `refs/entire/checkpoints/<shard>/<id>` per
   checkpoint, named `refs/entire/checkpoints/` + the last two characters of
   the canonical id + `/` + the canonical id (hex lower-case, ULID
   upper-case), as the platform mirror reads them; any other spelling of the
   id, other shard, extra path segment or invalid id is ignored, so a case
   variant cannot shadow the real ref. Only the shard may differ in case: on a
   case-insensitive filesystem (macOS and Windows by default) git lists a
   loose ref under whichever spelling of its shard directory was made first
   (a ULID's `AB/` merges into a 12-hex id's `ab/`), and where both spellings
   name one id the exact one wins. The ref's commit has the checkpoint as its
   root tree (`metadata.json`, `0/…`); and the legacy branch `entire/checkpoints/v1`
   (the local branch, else a remote-tracking copy), which keeps a 12-hex
   checkpoint under `<id[:2]>/<id[2:]>/`. A ULID can only come from the refs.
   A 12-hex id in both stores (a clone migrated with `entire migrate`) is read
   from its ref, which wins; the session paths in a ref's metadata carry no
   shard prefix, and the parser reads them either way. Such a commit is linked to those sessions
   with `method` `entire_checkpoint` and confidence 1, as the hosted
   `entireEdges` writes them, and then goes through step 7 with those sessions
   already linked and the files they edited already explained. The
   checkpoint is read whatever `Sessions.Sources` asks for. Every linked
   session is listed: when loading did not return it (it was last written
   outside the window, or only Entire holds it), it is mapped from that
   checkpoint. A session that already came from a home-directory store under
   the same id is listed once, from that store, since its transcript is the
   fuller record. Trailers with no valid id, checkpoints the clone does not
   hold, or ones whose sessions cannot be mapped leave the commit to the
   heuristics. A checkpoint in neither store is named in one warning line for the run, with the command
   that fetches the git-refs store (`git fetch <remote>
   '+refs/entire/checkpoints/*:refs/entire/checkpoints/*'`) and, when one of
   the ids is 12-hex, the one that fetches the legacy branch (`git fetch
   <remote> entire/checkpoints/v1`); nothing is fetched. `<remote>` is the
   first remote whose URL is the document's repository, which need not be
   `origin`; where the repository is not known (bundle assembly), it stays a
   `<remote>` placeholder. A checkpoint the clone
   holds but cannot read (malformed or oversized metadata, or objects a partial
   clone never downloaded) gets a "cannot be read" line of its own and no
   fetch hint. An id in both stores is dated for the `SessionWindow` by the
   branch, since a migrated ref's commit dates from the migration.
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

The result is redacted with the engine and the row summary that `baum internal sessions`
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
machine; `baum internal bundle` calls it through `internal/assemble`. It
takes the pull request's commits as the clone holds them (`gitlocal.Commits`
from the merge base to the head), a directory in the clone, the session store
overrides with the `--previous-path` directories the repository used to live
at, and the bundle caps `MaxPromptsPerSession` and
`MaxCommandEventsPerSession`. It loads and matches sessions exactly as steps 4
to 7 of "Local session linkage" do. Loading leaves out baum's own generation
runs, for `baum internal view` too: a session whose top-level session has a prompt
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
   `baum internal sessions --dump` uses (`redact.Engine.RedactJSON`, which `--dump`
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
authentication extends it without changing these
rules; its handlers sit behind the host check and the headers. Cancelling the
context ends the event streams and shuts the server down.

## Hosted service client

`hosted/baumcli` adds the hosted commands to the root command in
`internal/cli`; `cmd/baum` builds the one binary, which the hosted service also
ships to its users until the public release replaces that path. The hosted
service imports `hosted/agenthooks`, `hosted/traceupload` and
`hosted/apiclient` from this module, so the hook strings, the upload archive
format and the protocol constants have one definition on both sides.

- Opt-in. Commands that need the server read a token from `BAUM_TOKEN` or the
  credentials `baum login` stored, and stop with a login hint before any
  request without one. `baum hooks agent --check` and `baum hooks git --check`,
  which gate every committed dispatcher, succeed only for a logged-in user, in
  a clone carrying the marker of `baum setup` or a hook bootstrap, or, for a
  baum built or installed for the hosted service, in a repository whose
  committed agent settings carry baum's dispatcher; or, without any of those,
  for a user for whom a baum daemon ran (its lock file exists), where the
  hook is local: no hosted work, Entire called exactly as the fall-through
  would. The hook run decides hosted or local again, cheapest check first: it
  reads the committed settings (`git cat-file`, up to 5 s) only for a daemon
  user of a hosted install whose working tree settings carry the dispatcher,
  so a hook admitted by the daemon alone hands off to Entire and sends its
  event without waiting for git. They read local files only, and the hooks they admit never contact a
  server; they hand their events to the local daemon, which keeps them on the
  machine (see "Hook events" under "Local daemon"). The rules are in
  [`hosted/baumcli/README.md`](hosted/baumcli/README.md).
- User-scoped marketplace hooks. `baum hooks install --user` registers the
  public `baum-ai/baum` Git marketplace and installs/enables `baum@baum`
  through the Claude Code or Codex CLI (`hosted/agentplugins`), with a final
  native list query verifying installation and enablement. It edits no hook
  files directly and maintains no migration state. Installation fetches the
  public marketplace; hook execution stays local. The dispatcher calls
  `--check --user`, which adds two conditions to the rules above: the
  repository has Entire enabled, and no other hook the agent runs (project,
  user, managed or system settings; Codex project hooks only where Codex
  trusts them) already runs Entire for the event, so an event is not reported
  twice. Where it refuses, the hook does nothing. The run carries `--user`
  too (`baum hooks agent --user AGENT EVENT`): where only a daemon that ran
  admitted it, it records the event and runs no Entire. `baum setup` installs
  them only for a hosted install or a logged-in user.
- Compatibility. A client and a server of different releases agree on an API
  protocol number: requests name the client's, responses the range the server
  serves, and a client outside it stops with an error saying which side to
  update. A server that predates the handshake counts as protocol 1.
- Session discovery for uploads uses `sessions/collector`, with
  `ExcludeNestedClones` so a session in an independent clone nested inside the
  repository is uploaded only for its own repository.

## Running locally

`baum internal generate --pr N`:

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

`baum internal generate` is `internal/generate` (the pipeline) and
`internal/cli/generate.go`. The CLI opens `internal/store`, loads the latest
repository/PR version, and passes `review.PreviousVersionOf` into
`assemble.Request.Previous`. After resolving the pull request and recording its
run, it selects and checks the backend before assembling (`claude.Backend.Check`,
`codex.Backend.Check`, `cursor.Backend.Check`, `anthropic.Backend.Check`, which
only needs the key; `auto` tries `generate.AutoOrder`: claude, then codex, then
anthropic, and never cursor-agent, which runs only when named because it cannot
be held to read-only as reliably), assembles with `Request.Checkout` and `Attachments`
written to temporary files outside the run directory, creates the run
directory with `os.MkdirTemp("", agent.RunDirPrefix+"*")` and unpacks both
archives into it with `bundle.UnpackCheckout` and `UnpackAttachments` under the
bundle's own caps, and runs `agent.Generator` with the `claude.User` or
`codex.Local` profile, never `Isolated` or `Container` (a test guards this),
with `cursor.Backend`, or with `anthropic.Backend` and the key from
`ANTHROPIC_API_KEY`. The
wall-clock budget is a `context.WithTimeout` around the loop, `pass_timeout`
bounds each backend call, and SIGINT and SIGTERM cancel the context so the
backend's process group is killed and the run directory removed.

Completion mirrors the hosted worker's `Generate` and `Complete`: after the loop,
`agent.RedactOutput` masks the document, findings, model and diagnostics (with
the unpacked tree's paths as the known checkout paths) and
`agent.RejectSecrets` rejects a run that masked a secret in a closed value; then
`DocumentValidator.CompleteWithPrompt` derives the host-owned fields and records
the model and prompt version, and `ValidateStored` checks the result strictly
against the bundle. Failures are `*generate.Error` values of a category (input,
backend, budget, validation, interrupted, internal) that `cli.ExitCode` maps to
exit codes 2, 3, 4, 5, 130 and 1. A failure to mask the output is internal, as
the hosted worker reports it as a plain failure rather than a rejected
document. A spent `pass_timeout` is reported as the budget
`generation budget exceeded: pass_timeout` and is not retried; the run's wall
clock as `wall_clock`. The document is returned only for a run that passed every
step, and published by the command afterwards. `internal/store` keeps
versioned documents and immutable generation metadata in SQLite under the
common git directory
(`baum/reviews/versions.sqlite`), so linked worktrees share one history. A new
store falls back to the XDG data directory (or `~/.local/share`) when git is
read-only, keyed by the canonical common-directory digest; `--store` overrides
the root. Existing local or fallback history wins over creating another store,
and a read-only existing store fails writes without hiding its history.
Repository names, including GitHub host, and pull request numbers key rows.

`Latest`, `Versions`, `Get` and `Put` expose the local history.
`RefreshCurrent` updates only the latest document with one SQLite statement
fenced by both its version number and exact previous bytes, preserving
metadata and every replaced version. Concurrent refreshes cannot overwrite a
document prepared against older evidence, and a refresh never replaces a newer
version. A running generation does not block refresh: live facts refresh
independently, as in the hosted service. `Put` publishes metadata and document
in one transaction only if the previous version the caller read is still
latest, so concurrent runs cannot assign a different predecessor to a completed
document. A refresh of that previous version in the meantime does not reject
the generation; the next refresh updates the new version's live facts. A losing run reports a conflict; generation failure or
cancellation before publication writes no version. Export errors after
publication leave the successfully generated version available. Metadata records full revision IDs,
bundle digest, backend and installed version, actual model, schema revision,
prompt version/digest, applied REVIEW.md digest and generation time/usage.
`view --pr` and `validate --pr` read the latest locally without GitHub; both
accept `--version`, `--repo` and `--store`. Reads never create a store. The
viewer can open an earlier version via the CLI selector; browser version
navigation is not yet connected. `--watch` remains for document files only.

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

`baum internal bundle` stops after step 5, and never fetches: a pull request commit
the clone lacks fails the command with the `git fetch` to run, so it reads
git objects only and leaves refs, index, working tree and configuration
alone. It is `internal/assemble`, which follows the hosted assembler's order
over the same `bundle` functions with the sources above behind small
interfaces (`GitHub`, `Git`, `Sessions`, `ImageFetcher`): description images,
the pull request header, checks and conversation, the diff index, commit
changes, sessions, references, attachments, `REVIEW.md` at the base commit,
the checkout export, then caps, redaction, the per-hunk line cap, byte
trimming and the digest. Sessions
follow the comparison because they need its merge base. Command targets are
first checked by `locallink` and then, as the hosted builder does, by
`bundle.KeepKnownCommandTargets` against the diff's paths, so a target a
session names depends on the pull request, not on the working tree. A
session source that fails leaves the bundle without sessions and a warning;
cancellation is an error. Image downloads carry no credentials: the user's
token only reads the rendered body, in which GitHub has signed each image
URL, and only GitHub's upload hosts are contacted (see SECURITY.md). A
previous document can be given in `Request.Previous`, as `generate` will;
`baum internal bundle` leaves it nil. The
bundle is written as `json.Marshal` encodes it plus a newline, so its digest
is the SHA-256 of the output without that newline, and it holds no timestamp
of its own: the same inputs produce the same bytes. `Assemble` can also
write the checkout (a gzip tar read by `bundle.UnpackCheckout`) and the
attachments (a tar read by `bundle.UnpackAttachments`) to the writers in its
`Request`, for `baum internal generate`; `baum internal bundle` discards both. The checkout is
exported either way, since a file the checkout budget omits makes its hunks
textless. `baum internal validate`  checks a stored document with
`review.DocumentValidator.ValidateStored`, after the stored-document rules,
against its bundle when one is given,
and `baum internal refresh` uses `internal/refresh` to fetch current GitHub metadata
with checks pinned to the saved head, re-link local sessions against the saved
merge-base/head range and redact bounded evidence before calling
`DocumentValidator.Refresh`. It exports no checkout, downloads no images and
imports no model backend. Currency requires both saved head and base to match;
command failures leave the existing document unchanged. `baum internal view`
serves the viewer, the selected document, and the sessions and commits linked
to the pull request, read from the local session stores with `sessions` and
`linkage`. When those sessions are not on this machine, the viewer shows the
document's own session metadata.

Estimated cost comes from a price list at
`https://static.usebaum.com/pricing/prices.json`. baum verifies its `.sig`
with a public key embedded in the binary and falls back to a snapshot embedded
at release when the list cannot be fetched or verified. Nothing is cached on
disk: a fetched list replaces the embedded one for the life of the process, and
`pricing.CurrentSource` says which is active (`embedded` or `fetched`), its
version, when it was fetched and why the last refresh failed.

## Diagnostics

`internal/doctor` builds the report behind `baum internal doctor`: `Run(ctx, Options)`
returns an ordered list of checks, each with an `id`, `label`, `status` (`ok`,
`warning`, `problem` or `info`) and `detail`, and, where they apply, `path`,
`version`, `source` and `fix`, plus the worst status overall. It reuses the
code that makes the decisions instead of guessing at them: the real backends'
`Check` (built by `generate.DefaultBackendsFor`, run once and replayed into
`generate.Select` to say what `auto` picks and why), `gitlocal`'s git version
rule, `config.Config.ResolveGitHubTokenSource` for where a token comes from,
and the default-path helpers of `config`, `repos`, `jobs` and `discovery`.
Every outside dependency (environment, `PATH` lookup, command runner, backends,
token lookup, price refresh, registry and daemon) is an `Options` field, so
tests use fake executables and stubs. It creates nothing (the registry is opened
with `repos.OpenReadOnly`), runs `git` and `gh` in `agent.LocalEnvironment`'s
filtered environment and never reruns a failed claude, codex or cursor-agent
check (a missing cursor-agent is information unless it is the configured
backend), and bounds every subprocess with a
timeout and, through `internal/proc`, a process group killed with it (the
package moved from `agent/internal/proc` so both can use it). A configuration
that does not load is a problem, not an abort; its parse errors name a line and
column, and the key for a type or value error, never the text. A host that `github.IsGitHubHost` refuses is a problem
without a token lookup. It does not import `daemon`, which serves the
report: the command line passes `Options.Daemon`, which asks the daemon over
its control socket (never starting it or creating its state directory) for
its version, process id and uptime, and whether this baum would replace it.

`config.Config.BackendPaths` (TOML `[backend_paths]`, keys `claude`, `codex`
and `cursor-agent`, absolute paths only, validated by `config.ReadFile`) is the
explicit executable of a backend. `generate.DefaultBackendsFor(cfg)` passes it
to the backend as its `Binary`, which is looked up by `exec.LookPath` as given,
so an absolute path replaces the `PATH` search; a path that cannot be run fails
the backend's `Check` with `generate.ExplicitPathError` naming
`backend_paths.<name>`, without trying `PATH`. `baum internal generate` and the job
runner both build their backends from the resolved configuration, and
per-repository settings do not override the paths.

## Explicit local repository registry

`internal/repos` owns the private SQLite database beside `config.Path()` and
reuses `gitlocal` and `github.RepositoryFromRemote` for offline clone discovery.
The `clones` table keys canonical working-tree roots separately from the shared
common git directory and normalized repository identity (host-qualified for Enterprise). The
`repositories` table holds settings shared by all clones of that identity.
Transactions serialize registration and settings edits across processes; a list
reprobes paths and reports changed/missing clones unavailable without rewriting
them. No clone-on-demand or generation runner is involved.

A GitHub repository can be picked by `owner/name` without any clone (`picked`
table); clones attach to it later. A repository is picked exactly when it has
a `picked` row, so `Add` picks and `Remove` leaves the repository picked with
zero clones, while `Unpick` deletes its clones too. Settings stay on the
`repositories` row.

A repository's `automatic` setting is `null` (follow the global preference) or
an explicit `true`/`false`. The global preference is a master switch
(`Settings.AutomaticEnabled`): off disables every repository, `true` included;
on, only an explicit `false` disables one. The `preferences` table holds the
global preferences as one JSON row: `automatic` (true until changed) and
`cursor_agent_acknowledged`. `PRAGMA user_version` 1 marks the schema in which
`null` exists: `Open` migrates an older file once, in a `BEGIN IMMEDIATE`
transaction, turning the `automatic: false` every earlier registration stored
into `null`, and keeping a stored `true` (only `baum internal repos add --automatic`
could set it). `OpenReadOnly` reads an unmigrated file the same way without
writing it. An older baum that registers a repository after the migration
still stores `automatic: false`, which now reads as an explicit off: that
repository stays off until changed in the app, which fails safe.
`PUT /api/repos/{id}/settings` replaces the whole object and requires the
`automatic` and `daily_cap` fields, so removing either is an explicit `null`.
`/api/repositories` lists, picks and unpicks repositories by name, and
`PUT /api/repositories/{owner}/{name}/settings` behaves the same way.

`internal/cli` and the reusable `Registry.Handler` use the same service. The
handler exposes registry operations only; `internal/uiserver` owns listening,
instance lifecycle, authentication and host/origin validation and mounts the
handler behind that middleware. The existing document viewer does not mount it.

### Finding clones of picked repositories

`internal/clonefind` finds local clones of GitHub repositories without
prompting, so a picked repository gets a clone attached and the setup screens
can pre-select repositories already on the machine. `Find` collects directories
from agent hook events and the Claude Code, Codex and Cursor session stores
(cheap working-directory probes, no transcript parsing), resolves each to its
working tree with `gitlocal.Probe` (two read-only git commands; `Open` would
create a private mirror per candidate), then walks the usual code folders and
the folders around the clones agents used, to four levels, with the exclusions
`baum setup --all` uses (`internal/scanskip`). Candidates are grouped by the
lowercase `owner/name` of every github.com remote, linked worktrees collapsing
into their main clone. The daemon's `Service` keeps the result in memory,
rescans at start, hourly while a picked repository has no available clone, and
after a registry write (only when a picked repository has no clone row, unless
the last scan was under a minute ago), attaches the best candidate with
`Registry.AttachFound` (a fork clone is filed under the picked upstream when
any remote names it; a path or repository directory registered to another
repository is never moved, and an unpicked repository is never picked again),
and attaches the clone of an agent event's directory at once, after a registry
query that runs no git. On macOS it reads none of the protected folders
(Documents, Desktop, Downloads, iCloud Drive, CloudStorage). `GET /api/clones/found` and
`POST /api/clones/found/refresh` serve it. It makes no network request. See
its [README](internal/clonefind/README.md).

### Listing the account's repositories and cloning one

`internal/githubrepos` serves `GET /api/github/repositories` (ranked, filtered,
cached five minutes; each item says whether it is `picked` and the `clone` path
from the registry or finder) and the clone routes
`GET /api/repositories/{owner}/{name}/clone-target` and
`GET|POST /api/repositories/{owner}/{name}/clone`. `uiserver.LocalAPI` mounts
them as the `GitHubRepositories` provider next to the registry's
`/api/repositories` routes. The listing is `github.Client.ViewerRepositories`
(one paged `viewer.repositories` query, capped at 300, and one open pull request
search for the ranking). A clone is `gitlocal.Clone`: the HTTPS URL is built
from a validated owner/name, the token is injected like a fetch's
(`withCredential`, private environment, scoped header, no helper, no redirects),
Git hooks, templates, fsmonitor and submodules are off; configured checkout
filters such as git-lfs still run. Rewrites that change the clone URL are
refused and certificate verification is forced on with a token. A failed clone
removes only what it created. The daemon runs one clone per repository in the background,
reserves non-overlapping resolved target paths and refuses starts during shutdown,
picks the repository if needed, registers the result with `Registry.AddFor` and
publishes the registry change. See its [README](internal/githubrepos/README.md).

### Plan usage

`internal/planusage` serves `GET /api/plan-usage` (`uiserver.LocalAPI.PlanUsage`):
what remains of the Claude and Codex plans. Codex comes from the newest
`token_count` rate-limit snapshot in `$CODEX_HOME/sessions`; Claude from
Anthropic's undocumented `https://api.anthropic.com/api/oauth/usage`, called with
Claude Code's stored OAuth token, which is never refreshed, logged or stored.
That is the one outbound host the feature adds. Results are cached five minutes,
with at most one read a minute. See its [README](internal/planusage/README.md).

## Authenticated local application host

`internal/uiserver` binds only IPv4 loopback and owns launch-token exchange,
HttpOnly strict session cookies, exact Host and mutation Origin/JSON checks.
Cookies are host-scoped across ports, so the browser application, its API and
its cookie live under a secret `/s/<namespace>/` path returned by the exchange;
the server strips that prefix before routing to providers and to `Options.App`.
Its public bootstrap contains no local data or capability; all API providers
run behind the same middleware. `LocalAPI` composes the registry, read-only
version store and existing `viewserver` linkage/transcript/patch handlers.
Document selectors resolve registered clone IDs, never request-provided paths.
The server makes no baum-server requests. Discovery and index are separate
providers. The server is hosted only by the daemon (see "Local daemon"),
which builds every provider, the generation runner included, once it owns
the per-user lock. SSE has a bounded
in-memory event log; reconnect replays notifications or asks for an index reset.
It does not persist a second review index.

The stream also carries `notice` events for the desktop app's notifications
(`internal/notices`), which the web app ignores: `{"kind","repository","pr",
"clone"?,"title"?,"risk"?,"risk_status"?,"reason"?}` with `kind` one of
`review_finished`, `review_failed`, `review_automatic` (an automatic review
finished) and `review_requested`. They are published when a job finishes or
fails (not when it is cancelled, interrupted or superseded) and when a
discovery snapshot shows a pull request newly review-requested from the user
(a repository's first snapshot after the daemon starts is only a baseline).
The title is GitHub's, trimmed to 200 characters; `risk` (1 to 5) is the
stored review's score, with `risk_status` `lower_bound` when incomplete
coverage may hide a higher one; `reason` is the job's failure category and
nothing free-form. A notice never holds review text, paths or error messages.
A client that wants no history sends a `Last-Event-ID` beyond the latest
event: it gets a `reset` and then only new events.

Version image copies use an additive `attachments` table keyed by repository,
PR, immutable version and attachment ID, committed atomically by `Store.Put`.
`generate` returns copies from the original, hash-checked assembly archive,
not agent-visible files. Reads expose only raster images listed in that
version's document. Existing stores without the table remain readable.
`internal/reviewindex` rebuilds an in-memory projection of discovery and registered
version stores. `store.Snapshot` reads latest metadata, count and small document
summaries in one transaction, plus separate latest-started run records. It does
not move document bodies or maintain a second database. CLI generation publishes
lease-based run status independently of immutable successful version history;
expired leases are read as interrupted without mutating stores during indexing.
Once `Store.Put` commits a version, generation succeeds: failing to record the
run's outcome afterwards is only a warning, and the lease later expires.

### Local pull request discovery

`internal/discovery` reads the viewer and open pull requests through one GraphQL
query per repository per poll, plus cursor pagination with 50 pulls per page.
Each pull includes review, checks and merge states, and up to 100 review threads.
The repository's `viewerPermission` becomes each pull's `viewer_can_push`
(write, maintain or admin; null when GitHub does not say), which decides
whether the app offers a fix.
Missing state is unknown; unresolved counts are nullable and marked as lower
bounds when threads exceed the cap. Comment-only reviews use the latest 100
reviews as a fallback to GitHub's review decision and opinionated reviews.
The user's teams and per-author repository permissions still use conditional
REST requests. Truncated review requests fail the refresh rather than concealing
an automatic-generation trigger. All requests refuse redirects.
Missing team scope and unreadable permissions are explicit unknown evidence;
a fork or unknown author permission must not become an automatic-generation
authorization. This service has no generation or GitHub mutation capability;
the app's GitHub writes (merge, update branch, re-run checks and a confirmed
fix's `git push`) live outside it (see "Fix jobs" and below).

`Service.Cached` resolves the local credential and reads disk without network
access. Disk data is stale until a successful poll in the running service.
`Service.Poll` serializes polls, honors its next deadline, and backs off unchanged
data from 30 seconds to 15 minutes, respecting GitHub's polling interval and rate
reset. GraphQL has no ETag; only REST metadata uses conditional requests.
GraphQL cost, remaining points and reset time map to the public rate projection.
REST requests and GraphQL points have separate persisted budgets, with the
currently limiting budget exposed when exhausted. Both budgets and secondary
retry deadlines are shared by host and credential across repositories and
service restarts. Discovery never sleeps through an exhausted budget.
A failed or rate-limited refresh preserves the last complete inbox as stale;
optional non-rate team or permission failures keep their fields unknown.
Snapshots and minimal typed response fields are atomically replaced in private
cache files, partitioned by host/credential fingerprint/repository. The index
consumes these snapshots; missing PRs in an open-only snapshot are never treated
as closed. Each pull keeps its head branch (`head_ref`). Old cache formats drop
conditional REST responses once so newly retained fields can be populated.
`Service.PollNow` skips the next deadline for local triggers, while honoring both
shared rate budgets. `GET /api/prs` lists every picked repository, and
`POST /api/prs/refresh` takes exactly one of `{"clone": id}` or
`{"repository": "owner/name"}` (a picked repository, with or without a clone;
unknown is `404`).

Merging is a GitHub write the local app makes, and it lives outside
discovery. `GET /api/prs/merge-options?repository=&pr=` returns the allowed
merge methods, GitHub's default for the viewer and the pull request's head and
mergeability (`Client.MergeOptions`, one GraphQL query). `POST /api/prs/merge`
takes `{repository, pr, method, head_sha}` and runs GraphQL `mergePullRequest`
with `expectedHeadOid` set to `head_sha` (`Client.MergePullRequest`), so GitHub
refuses a pull request whose head moved. Both accept only picked repositories
(unpicked is `404`), send each document once with no retry, and map GitHub's
errors to `{code, message}`: `head_moved` and `conflict` and `not_open` are
`409`, `blocked` (required checks, reviews, branch protection) and
`method_not_allowed` are `422`, `forbidden` is `403`, anything else `502`. After
a merge the daemon runs `Service.PollNow` for the repository and publishes an
index change so the inbox drops the pull request.

Two more confirmed writes live in `internal/prupdate`, served by
`uiserver.UpdateHandler` on the same auth, CSRF and picked-repository rules as
merging. Discovery reports `behind` (GraphQL `mergeStateStatus` `BEHIND`, not a
draft, not conflicting) on each pull request; a head change in the review index
clears it with the other merge-state fields. `GET /api/prs/update-branch-options`
reads the pull request over REST (`fix.ReadPullRequest`: branches, `behind`,
`conflict`, `fork`) and `POST /api/prs/update-branch` takes `{repository, pr,
head_sha}`, re-checks that the pull request is open, its head is `head_sha`, it
is not a fork or conflicting and the account can push (`fix.CanPush`), then
sends `PUT .../pulls/{n}/update-branch` with `expected_head_sha` through
`RESTReader.Write`, the one REST method that is not a read. GitHub answers 202
and merges asynchronously. `GET /api/prs/rerun-checks-options` lists the head's
workflow runs (`GET .../actions/runs?head_sha=`) whose conclusion is `failure`,
`cancelled` or `timed_out`, plus the names of failing checks no Actions run is
behind (`fix.FailingChecks` entries without a job ID: other apps' checks and
commit statuses), and `POST /api/prs/rerun-checks` takes `{repository, pr,
head_sha}`, repeats the checks above, re-reads the run list (the request never
names a run) and posts `.../actions/runs/{id}/rerun-failed-jobs` for each. It
answers `{rerun, refused}`: an error only when no run started. Errors are
`{code, message}`: `head_moved`, `conflict`, `not_open` and `up_to_date` are
`409`, `blocked`, `no_runs` and `fork` are `422`, `forbidden` is `403`,
anything else `502`. After either write the daemon runs `Service.PollNow` and
publishes an index change.

The review index exposes discovery states for all open rows, including rows
without a saved review or with an unreadable store. Saved review time comes from
`summary.review_minutes` in the existing SQLite summary projection: only whole
numbers from 1 to 240 are exposed; missing and invalid values remain unknown.
Newer background observations override checks; a changed head clears discovery
review, merge and thread states until the next discovery poll.

A picked repository without a clone is listed from its cached discovery
snapshot: its entries have no `clone_id` and carry `needs_clone: true`, the
same marker as any entry whose repository has no available clone. There is no
store to read for it, so its rows are `no_review`.

### Persistent generation jobs

`internal/jobs` serializes queue mutations in a private atomic JSON journal,
without duplicating review documents. `jobs.Open` takes an exclusive
`internal/filelock` lock beside the journal (`jobs.json.lock`) and keeps it
until its workers stop, so two processes never run one queue; it waits up to
10 s for a predecessor whose shutdown timed out and that is exiting.
`Queue.Drain` refuses new jobs and reports true only when none is queued or
running, under the mutex `Submit` checks, so a restart never cuts a confirmed
job short: one confirmed during it gets `503` with `Retry-After` and is
estimated and confirmed again against the new daemon. Workers use the same `generate.Publish`
entry point as the terminal: previous-version lookup, run lease, generation and
strict validation, then atomic version publication. Running journal entries are
interrupted on restart; queued entries retain their confirmed input fingerprint.
A confirmation covers the estimate, isolation for a fork or an author without
known write access, and, for a subscription-billed backend, that its cost is
unknown (`Confirmation{Estimate, Isolation, Unpriced}`); each is checked on the
server, never trusted from the browser.
Fresh GitHub REST evidence authorizes the head and isolation warning; all
preflight I/O happens outside the queue mutex so cancellation stays responsive.
A job names a clone (`Item.Clone`). An item whose clone is unknown or
unavailable, which includes every repository picked without a clone, is
`jobs.ErrNeedsClone`: estimate and generate answer `409` with
`{"needs_clone":true}` (the isolation answer is `{"isolation_required":true}`),
and a queued job whose clone was removed ends with the reason `needs_clone`
without running (`cancelled` when automatic, so its head is not used up and
it counts no spend; `failed` when manual). Pull requests of such a repository are still discovered.
The runner has no subprocess invocation of baum itself. Only the agent
backends and a fix's git commands run child processes, cancelled through their
context.

`Queue.SubmitAutomatic` is how the automatic scheduler (see "Automatic
generation") queues a job nobody confirmed. It prepares the item like an
estimate, so the job has the same budgets, price list, recheck before running
and isolation as a confirmed one, and refuses for good
(`NotAutomaticError`) what a person would have to confirm: a fork or an author
without known write access, a blocked (unpriced) model, and cursor-agent or
another unestimated backend without the acknowledgement. Under the queue mutex
it refuses with `ErrRestarting` while draining, `ErrBusy` while the PR has a
queued or running job (it never joins or supersedes one), `ErrDuplicate` when
any job, manual or automatic, already exists for that head (except a single
automatic job a daemon stop or restart interrupted, which `Interrupted` lists
and which may run once more; failed and cancelled jobs are final), and
`ErrDailyCap` when the repository's automatic jobs created that day (actual
cost when reported, else the estimate; a job cancelled before calling a model
counts zero) plus this estimate would exceed its `daily_cap`. Jobs record
`automatic` and `trigger` (`new_head`, `review_requested`, `ready_for_review`,
`retry`). Before a queued automatic job starts, the worker checks the cap again
under the same mutex, against the current `daily_cap` the gate returns and the
spending of the jobs that day that already started or finished (reported cost
replacing the estimate) plus this job's estimate; over it, the job is cancelled
with reason `daily_cap` before any model call. The executor's recheck of the
confirmed inputs, before any model call, also refuses an automatic job whose
pull request GitHub now reports as a draft (the queue marks its inputs
`RefuseDraft`; confirmed jobs run drafts); it is cancelled with reason
`draft`. Neither job counts as its head's job: `ErrDuplicate` and
`Interrupted` ignore it, and `Refused` lists it while it is the latest job for
its head.

`internal/pricing` ports the platform's pure pricing implementation and baseline
into the independent OSS module; its signature, tier and normalization tests are
retained. Keep these copied core files synchronized with the platform package.
A bounded HTTPS downloader refuses redirects, verifies the existing release
signing key, rejects flattened lists and installs only newer full-format lists.
An installed download is stored with its signature in the job state directory.
The daemon re-verifies and installs it before opening the queue, so recovered
jobs see the list they were confirmed against; an unverifiable file is ignored.
Estimates and job confirmation never infer the default model or replace it with
a different priced model, and an item whose model has no price is blocked. The
one exception is a backend billed outside the list (cursor-agent): its item has
a nil estimate with an `estimate_reason`, the total is unknown, and
`Queue.SubmitConfirmed` queues it only with the separate `confirm_unpriced`
acknowledgement. Unknown actual usage remains null.

### Review groups

`internal/reviewgroups` keeps review groups, named sets of 2 to 8 open pull
requests of one repository reviewed together, in `groups.sqlite` in the
daemon's state directory (beside `jobs.json`), not in a clone. Their combined
reviews are versions keyed by group ID in the same file, so they never collide
with a member's own versions in its clone's `versions.sqlite`; deleting a
group hides it and keeps every version. `/api/groups` serves the definitions
(create, rename, set members, delete), each with its latest version's
metadata, risk, the members that version covered and whether it is outdated
(other members, or a member head in the review index that differs from the one
reviewed), and a group's versions, documents and patches.

A job item names `{clone, group}` with `pr` 0, so automatic scheduling,
duplicate-head checks and notices, all keyed by pull request, never match a
group job. Preparation reads every member from GitHub like a pull request
job (`Prepared.Members` holds each member's head, which the confirmation
covers) and needs a saved review of each member in the clone's store, or
answers `409 {"members_need_reviews": [...]}`. Execution passes
`generate.Options.Group` and publishes through `reviewgroups.Publisher`.
`generate.Run` then assembles each member with the ordinary assembler (no
description images, an equal share of the checkout budget), merges the
bundles with `assemble.MergeGroup` (the first member's header titled with the
group's name and a body listing the members, every hunk once in member order,
the union of the other evidence, one `FileComparison` per member, refitted to
the caps), unpacks each checkout at `members/<number>/` with no top-level
`head/`, writes `members.json` (each member's saved review in brief) and runs
the group prompt. Its decorator sets `stack` (the group's name and each
member's number, head and saved review version) and each section's
`pull_requests` from the members whose hunks it lists, so the stored document
is stack-shaped like a hosted stack review, with `review.pull_request` the
lowest member. The group page reads patches from the member whose comparison
changes the file and whose patch holds the cited hunk's range; it lists no
linked sessions or commits.

### Fix jobs

A job has a kind: a review (`Item.Kind` empty) or a fix (`"fix"`), which
shares the queue, journal, concurrency, progress events and cancellation with
reviews and shows in the same Jobs list. A fix is estimated alone
(`POST /api/jobs/estimate` with `{"items":[{"clone","pr","kind":"fix"}],"mode":"single"}`)
and queued only from that confirmed estimate (`POST /api/jobs` with
`estimate_id`); a direct request is `400` (`ErrFixNeedsConfirmation`), and
automatic generation never queues one. A review and a fix of one pull request
neither supersede nor block each other, and a fix of a head does not count as
that head's review for the automatic scheduler.

`jobs.Local` prepares a fix from fresh REST evidence (`internal/fix`):
`/pulls/{n}` for the head, its branch and repository, the base branch and
`mergeable` (read again once after 1.5 s while GitHub has not computed it),
`/repos/{o}/{r}` for `permissions.push`, and the head's failing check runs and
commit statuses. `Prepared.Fix` records what it fixes (`conflicts`, `checks`)
and the branches; a fork, an account that cannot push, a backend that cannot
fix, or nothing broken is the item's `blocked_reason` (`fork`,
`no_push_access`, `unsupported_backend`, `nothing_to_fix`), which the dialog
explains. The harness, model and budgets resolve as a review's, and the
estimate is the same budget-sized one. Before running, the executor prepares
again and refuses a changed head (`head_changed`) or changed fix inputs, then
reads the failing checks' output and the last 200 lines of up to four GitHub
Actions job logs (`RESTReader.Tail`, which follows the signed log redirect
without the token).

`fix.Run` then works in a `gitlocal.Fix`: a linked worktree of the
registered clone under the user cache directory's `baum/fixes/<id>`, detached
at the head fetched into `refs/baum/fix/<id>/` from the remote whose URL names
the repository (`ErrHeadMoved` when the branch moved on). For a conflict it
merges the base commit itself; a clean merge needs no agent. The agent
(`agent.Fixer`) runs with the prompt, the untrusted data between nonce-tagged
markers, and the run budgets. baum then checks that no conflicted file keeps a
marker, commits what was left (completing the merge), requires a new commit
that descends from the head, and pushes it with a plain `git push` to the head
branch. Phases are `reading checks`, `fetching`, `merging`, `fixing`,
`committing` and `pushing`. A finished fix records `commit`, `commit_url` and
the agent's masked `summary`; a failed one a reason (`head_changed`,
`no_remote`, `git`, `backend`, `budget`, `no_changes`, `conflicts_remain`,
`history_rewritten`, `push_rejected`, `push_failed`) and a bounded detail.
The worktree, its registration and the refs are removed whatever happens.
After a push the daemon polls the repository's discovery at once. Git commands
in the checkout set `GIT_DIR` and `GIT_WORK_TREE` and an empty
`core.hooksPath`; see [SECURITY.md](SECURITY.md#fixing-a-pull-request).

### Local application presentation

`app/` builds into the `internal/localapp/dist` embed, which, like the
viewer's, only a release commit carries.
`localapp.Handler` is the daemon's `App.Assets`, mounted as `uiserver`'s
`Options.App`, so the application's files
are served only under the authenticated `/s/<namespace>/` path; the public
bootstrap stays the server's own `/` and `/launch.js`. The app resolves its
API, SSE and asset URLs relative to that path and routes with query strings on
it. All data, auth exchange and SSE retain launch-cookie/Origin protection. Its PR host
supplies the same `ReviewViewer` slots and URL/attachment seams as the hosted
PR page, sourcing documents, immutable versions, patches, sessions and commits
from registered local clones. The renderer stays in `viewer/`; the app owns
only local inbox, jobs, confirmation and settings presentation. The common
viewer build/freshness scripts include both embedded applications.

### Free background synchronization

`internal/background` runs one cancellable worker in the daemon, once it owns
its lock. It has registry/discovery/metadata capabilities only; the generation
queue and model backends are not dependencies, and it never enqueues
generation. `Service.Discovered` receives each snapshot a poll returned without
error; the daemon passes them to the automatic scheduler, which alone decides
about jobs. It polls immediately without
blocking HTTP startup, then schedules discovery and latest-review evidence with
conditional-cache deadlines, bounded requests, error backoff and host rate resets.
Every picked repository is polled, with or without a clone: discovery needs
only `owner/name`. A repository without an available clone gets no per-PR
refresh (that reads a store and the clone's saved reviews), and polling it
still feeds `Service.Discovered`. Per-repository target cursors and repository rotation prevent a small renewed
GitHub budget from perpetually serving only the first PRs. Linked worktrees are
deduplicated by common git directory. No-review PRs fetch only a header and checks;
saved reviews use `refresh.Refresher` and `Store.RefreshCurrent`'s exact-byte CAS.
`Service.RefreshNow` refreshes named PRs of one clone the same way outside
that schedule (see "Refresh on hook events"); it shares the per-host rate
pause with `Sync` in both directions.

Discovery's live freshness map uses its own short mutex; cached readers never
wait behind a network poll. Disk-only startup remains stale. Minimized current
head/state/title/check observations share the private credential-partitioned
cache and augment the index, including historical PRs. A newer discovery result
wins over older observations. Authenticated `/api/background` and index events
report refresh health without filesystem paths, credentials or remote bodies.

Local generation stores additive `agent.Accounting` in immutable version
metadata and the durable queue. Accounting retains disjoint provider token
buckets and available per-call model identity, speed, TTL and prompt range.
Retries and repairs append reports; partial or absent reports stay unknown.
Queue progress is saved before SSE publication and snapshots detach mutable
accounting. Existing version records are never backfilled. Pricing sums
individually attributable model reports; missing pricing dimensions leave a
nullable cost and a fixed reason. The local SPA renders both job accounting
and selected-version provenance without changing the review document schema.

## Local daemon

`internal/daemon` runs one long-lived local server per user, shared by the
desktop app and coding-agent hooks; the `baum` command line is how both reach
it. It owns the authenticated app server above, and runs the registry,
discovery, review index, background refresh, diagnostics, the generation queue
and the automatic generation scheduler. There is one server implementation and
no command opens it in a browser: the desktop app reads the launch URL from the
daemon's `credential.json`. It also keeps the record of the events agent
and Git hooks send it.

The top-level `baum --help` lists only what users run: `hooks`, `daemon` and
the hosted service commands. The engine commands (`generate`, `bundle`, `view`,
`validate`, `refresh`, `repos`, `sessions` and `doctor`) live under the hidden
`baum internal` parent, which `baum internal --help` lists. They are debugging
and diagnostic tools that run the same engine by hand, and there are no
top-level aliases for them.

### Lifecycle

`daemon.Run` takes an SQLite write lock (`daemon.lock.sqlite`) in the private
state directory and holds it for its whole life. The operating system releases
it however the process exits, so a lock is never stale; the new owner deletes
the socket, credential and status files a crashed daemon left. A second
`Run` returns `ErrRunning` at once when the holder answers and is not
stopping; otherwise (it is starting, or stopping and giving its jobs up to
the 20 s shutdown timeout) it waits up to 35 s for the lock. Once it owns the lock it runs `Options.Prepare` (the command line
resolves the login shell environment there), builds the application with
`Options.Start`, listens on loopback, writes the credential and the status
file, and listens on the control socket last, so a daemon that answers on
the socket serves the app.

Locks are `internal/filelock`: an existing lock file is opened only through
SQLite, never with a plain descriptor, because on Unix closing any descriptor
of a file drops every POSIX lock the process holds on it. Code inside the
daemon reports on it through `daemon.Self` rather than probing its own lock or
socket.

`Client.Ensure` (`baum daemon ensure`) says hello on the socket. A daemon of
this version or newer is kept. One speaking an older protocol is stopped at
once (its running jobs are interrupted) and replaced. One with the same
protocol but an older version (an older release by semver; a development
build is older than any release, and two development builds are left alone)
is kept and sent `stop` with `{"when_idle":true,"executable":<the client's
baum>}`: an idle daemon stops at once and the client starts its own version;
a busy one answers with `restart_pending` and, once `Drain` succeeds, starts
that executable detached on the same port and stops, so no confirmed run is
interrupted. Of racing clients the newest version is the one it restarts
as, and a client tells its user about the deferred restart only when it
first schedules it. Because a client may be a sandboxed agent's hook and the
executable runs unsandboxed with the daemon's environment, the daemon
records it only from a client newer than itself and only if
`executablePolicy` (`trust.go`) accepts it, and checks it again just before
`Respawn` (which gets the resolved path): after `filepath.EvalSymlinks` it
must be the daemon's own `os.Executable()`, the `baum` that `exec.LookPath`
finds on the login shell's `PATH` (the desktop app links its bundled baum
there; see `desktop/README.md` for which install locations pass the checks
below), or one of `daemon.TrustedExecutables` (empty), a regular executable file
owned by the user or root and not group- or world-writable, under
directories up to `/` that pass the same owner and mode test (sticky
world-writable ones such as `/tmp` fail; on macOS a directory group-writable
only by the admin group, gid 80, such as `/Applications`, passes, since its
writers can become root, but never the staff group, gid 20, or a file). On
Windows only the allowlist
applies. A refusal is logged; the daemon keeps running without
`restart_pending`, and is replaced when a newer client next finds it idle.
A daemon also restarts when its own executable is replaced on disk, by an
update or a development rebuild, even at the same version: every 5 seconds it
compares the file at its `os.Executable()` path with the one it started from,
and once a different file there has stayed unchanged for one check and passes
the same policy, it schedules `restart_pending` into that path as if a newer
client had asked. Until then, helpers it starts from its own path (the GitHub
credential helper) run the new file and refuse the old parent. A failed
restart is not retried until the file changes again.
A daemon speaking a newer protocol is an
`IncompatibleError` naming both versions.
When nothing answers, a second lock (`start.lock.sqlite`) serializes starts:
the holder starts `baum daemon run --detached` and waits until it answers,
and every other caller finds it running once it gets the lock, so concurrent
calls start exactly one daemon. The daemon that answers may not be the one
the holder started (a direct `baum daemon run` of another version can win the
daemon lock), so it gets the same version and protocol decision as one found
running. A daemon that holds the lock but never
answers is reported with its process id from `daemon.json`, not killed.

A detached daemon runs in its own session (Unix `setsid`; Windows
`DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`, breaking away from the
caller's job object when the job allows it), in the home directory, with no
standard input and its standard output and error in `daemon.out` (the
previous start's in `daemon.out.1`). It outlives every client.

`baum daemon stop` (and the version restart) sends `stop`; while a daemon
holds the lock but does not answer yet (it is starting), the client keeps
asking until it does. The daemon marks itself stopping before it answers
(under the same lock as the idle check of a `when_idle` stop or the idle
restart, so any later hello reports it and `ensure` waits for it to exit
rather than using it), gives the generation queue `ShutdownTimeout` (20 s) to
cancel running jobs, which are recorded as interrupted (a backend that does
not return in time leaves its job running in the journal, and the next start
records it as interrupted), stops background work and the app server, removes
its files and releases the lock. The client waits until the lock is free.
SIGINT and SIGTERM stop it the same way.

### Environment

Whatever started it (an agent hook with the agent's environment, the desktop
app with a bare launcher `PATH`, a terminal), the daemon replaces its
environment before building anything. `internal/shellenv` runs `$SHELL` (else
the user database's shell) as `-i -l -c` (csh: `-i -c`) in a new session with
no terminal and a 10 s timeout after which its process group is killed. The
shell gets only the session variables (`HOME`, `USER`, `LOGNAME`, `TMPDIR`,
`LANG`, `LC_*`, `TZ`, the XDG directories, `SSH_AUTH_SOCK`,
`DBUS_SESSION_BUS_ADDRESS`, `DISPLAY`, `WAYLAND_DISPLAY`), a default `PATH`
and `BAUM_RESOLVING_ENVIRONMENT=1`, and runs `baum daemon print-env <marker>`,
which prints the environment as JSON between two copies of a random marker,
so whatever startup files print is ignored; once it has, whatever is left of
the shell's process group is killed. `shellenv.Merge` then keeps only:
`PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`, `LANG`, `LC_*`, `TZ`,
the XDG directories, `SSH_AUTH_SOCK`, `GIT_SSH_COMMAND`, `GIT_SSH`,
`DBUS_SESSION_BUS_ADDRESS`, `DISPLAY`,
`WAYLAND_DISPLAY`, proxy and CA variables, `GITHUB_TOKEN`, `GH_TOKEN`,
`GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN`, `GH_HOST`, `GH_CONFIG_DIR`,
`GH_PATH`, `ANTHROPIC_API_KEY`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `BAUM_*`
and, on Windows, the system variables programs need. `SSH_AUTH_SOCK` comes
from the starting process when it has one (launchd's agent for an app started
from Finder) and from the login shell otherwise, so the daemon can fetch a
pull request commit missing from a clone with an SSH remote. A socket goes
stale when its session ends: before each fetch `shellenv.Agent` checks that it
still exists, and only when a socket it had has gone stale asks the login
shell again (one shared run, remembered when the shell has none, retried a
minute after a failure) or drops it; it is asked only for SSH remotes. The
daemon installs a `gitlocal.Background`, so its fetches never wait for an
answer: `GIT_TERMINAL_PROMPT=0`, `SSH_ASKPASS_REQUIRE=never`, the fetch
timeout, and `ssh -o BatchMode=yes` when no ssh command is configured; a
configured `GIT_SSH_COMMAND`, `core.sshCommand` or `GIT_SSH` (plink, a
wrapper) runs as written. A missing key or unknown host is a git error. The backends never receive the agent or any `GIT_` variable. The
locations, `HOME`, the XDG
directories, `TMPDIR`, `USERPROFILE`, `APPDATA` and `LOCALAPPDATA`, only ever
come from the starting process and stay unset when it lacks them: the daemon
resolved its lock and socket from them before reading the shell, and a state
directory only the shell named would let a second daemon share its job
journal. Every other kept variable comes from a resolved shell environment,
so a token only the agent's environment had is dropped. On Unix, PATH is the
exception: login-shell directories come first, followed by inherited directories
not already present; empty entries are omitted when merging. This preserves tools
from the launching terminal without changing login-shell tool precedence. When the shell is
slow, fails or prints nothing,
the kept variables come from the inherited environment. Windows has no login
shell: there `shellenv.Resolve` returns the inherited environment with the
`PATH` a new logon of the user gets, the system `Path` and then the user's
from the registry (`CreateEnvironmentBlock`), so `gh` and agent CLIs
installed after the starting process are found. The log records the shell, the
outcome and the kept variable names, never values. `backend_paths` remains the
way to name a backend executable explicitly.

### Automatic generation

`internal/automatic.Scheduler`, built by `localApplication` and run beside
background refresh, turns pull request changes into jobs on the same queue as
confirmed runs. It has no backend or GitHub client of its own: discovery feeds
it and `Queue.SubmitAutomatic` decides each job.

- **Enabled** per repository: the global preference (on by default) is a
  master switch, which the desktop tray's pause turns off; while it is on, a
  repository is enabled unless its `automatic` is `false`. Disabling a
  repository (or the switch), or unregistering it,
  forgets its state.
- **Turning off.** `Scheduler.Gate`, installed with
  `Queue.SetAutomaticGate`, reads the current preferences and repository
  settings from the registry. The queue asks it under its mutex when an
  automatic job is submitted and again before the worker starts one, so a pass
  that read the settings before an off cannot queue after it, and a queued job
  never starts once its scope is off. Until the gate is installed (right after
  `jobs.Open`) workers leave queued automatic jobs alone, so a journaled job
  cannot start on restart before the settings are checked.
  `Queue.CancelDisallowedAutomatic` cancels
  queued automatic jobs the gate refuses, with its reason (`automatic_off`,
  `repository_automatic_off`, `repository_removed`,
  `cursor_agent_acknowledgement_required`, `settings_unreadable`); every pass
  runs it, and `PUT /api/automatic` runs it before answering. Running jobs keep
  running.
- **Daily cap at start.** The gate also returns the repository's current
  `daily_cap` and the scheduler clock's day; the queue checks it itself (the
  gate never calls the queue or takes the scheduler's lock), so a cap lowered
  while a job waited, or cost an earlier job reported, stops a queued job
  before it starts (`daily_cap`). Each pass asks `Queue.Refused` for such jobs
  (and those stopped as drafts) made after the baseline and puts each one's
  head back as pending once (`refused` records the job) if the PR is still
  open, eligible and at that head; the same pass submits it, `ErrDailyCap`
  pauses the repository as below, and the head runs once the pause ends.
- **Baseline.** Enabling is dated when a pass or snapshot first sees it. The
  first snapshot `Observe` gets that was fetched at or after that time records
  each open PR's head, whether the user's review is requested, whether team
  membership was readable and whether it is a draft, and queues nothing: PRs
  open at first launch, registration or re-enabling are not caught up. An older
  (cached) snapshot is not a baseline; the next poll's is.
- **Triggers.** Afterwards, a PR whose head differs from the recorded one (a
  new PR included), whose review request turned on (not merely because team
  membership became readable), or that was marked ready for review, gets a
  pending head if it is eligible: the user's review is requested or the user
  authored it, it is not a draft, it is not from a fork, and its author has
  known write access. Drafts get no automatic review, so pushes to a draft
  wait for "ready for review"; manual generation of a draft still works. A
  queued automatic job whose PR turned draft before it started is cancelled
  (`draft`) by the recheck before any model call, and its head is pending
  again (trigger `ready_for_review`) once a snapshot shows the PR eligible at
  that head. A PR
  that closes, moves to another head while ineligible, or stops being eligible
  loses its pending head.
- **Interrupted runs.** Each pass asks `Queue.Interrupted` for automatic jobs
  a daemon stop or restart cut short that are the only job for their head; if
  the PR is still open at that head and eligible, the head is pending again
  with trigger `retry`, once (`retried` records it). The retry goes through
  the same pass, so Drain, the cap and the switches apply. Failed and
  cancelled runs are never retried; the next push triggers as usual. A run
  interrupted before a repository's baseline (turned off, then on again) is not
  retried: the baseline marks its heads as already retried.
- **State.** `automatic.json` in the job state directory (0700) holds each
  enabled repository's PRs (recorded head, request and draft flags,
  eligibility, pending head and trigger, last head handled, retried head,
  last cap- or draft-stopped job put back),
  its pause and its notice. It is rewritten
  atomically on every change, so a restart neither forgets pending heads nor
  treats recorded ones as new; an unreadable file starts over with new
  baselines.
- **No clone.** A picked repository without a clone is observed and its
  state kept, but a pass never submits for it: it needs a clone to generate
  in. Its pending heads wait, and the first pass after a clone appears
  submits them as usual. A job whose clone disappeared after the pass chose
  it is not submitted: `Queue.SubmitAutomatic` returns `jobs.ErrNeedsClone`,
  which keeps the head pending without using an attempt, a retry time or a
  new notice. One whose clone disappeared while it was queued is cancelled
  with the reason `needs_clone` before any model call, like a daily-cap
  refusal: `Queue.Refused` lists it, no spend is counted, and the head is
  pending again, so the first pass with an available clone submits it once.
- **Passes** run at start, every 30 s, when a snapshot leaves pending work,
  when settings change and when a job finishes. A pass submits due pending
  heads in order unless the daemon is stopping or has a restart pending
  (`daemon.Self`), so the queue can drain. A head whose latest saved review
  already has it is skipped. Outcomes: queued, or `ErrDuplicate`, marks the
  head handled; `ErrBusy` waits for the PR's job; `ErrRestarting` ends the
  pass and is retried; `ErrHeadChanged` drops it (discovery brings the new
  head); `ErrDailyCap` pauses the repository until local midnight with the
  `daily_cap` notice, or until its cap is raised above or cleared from the
  amount that caused the pause; a `NotAllowedError` (turned off meanwhile)
  leaves the head for the next pass, which forgets it if the repository stays
  off; a `NotAutomaticError` (a draft, a fork, an unpriced model, cursor-agent
  not acknowledged) drops it with its reason as the notice; other errors retry
  with backoff, five times.
- **Spending.** No default cap. Each job keeps the configured run budgets, and
  `daily_cap` is enforced per repository and calendar day, when a job is
  queued and again before it starts. cursor-agent runs
  automatically only after `cursor_agent_acknowledged`, checked by
  `SubmitAutomatic` from the stored preference, never from a request.
- **Interfaces.** `GET /api/automatic` returns the preferences and each
  repository's effective state, cap, today's automatic spend, pause, notice
  and pending count; `PUT /api/automatic` changes the preferences. A changed
  pause or notice, and every repository's status after a `PUT`, is published
  as an `automatic` SSE event for other app windows and the desktop app's
  notifications. `Trigger(ctx, repository)` polls discovery for one
  repository (`PollNow`: before discovery's poll deadline, within its rate
  budget) and passes the snapshot to `Observe`. Hook events reach the
  scheduler only this way (see "Refresh on hook events").

### Hook events

`internal/hookevents` carries what `baum hooks agent` and `baum hooks git`
report to the daemon; the hook side is in `hosted/baumcli` (rules in its
[README](hosted/baumcli/README.md), "Hook events"). Nothing of it goes to a
baum server: forwarding to the hosted service waits for a hosted event
endpoint.

- **Event.** One JSON object per hook run, sent as the params of the `event`
  control request: `v` (the event format, `hookevents.Version`, 1), a random
  `id`, `time`, `kind` (`agent` or `git`), `agent`, `name` (the agent event or
  Git hook), `dir`, `exit` (the hook's status), `client` (baum's version), and
  `session` (an agent payload's top-level `id`, `transcript_path`, `cwd`,
  `hook_event`, `source`, `reason`, read by a streaming scanner from the whole
  payload, the last of a repeated member winning; nothing else of it) or `git`
  (`args` without a pre-push remote URL, and `lines`, the refs or commits Git
  wrote to the hook). `Event.Bound` cuts fields at character boundaries, then
  trims Git lines and halves the longest fields until the escaped encoding fits
  one control line. The daemon ignores fields it does not know and records
  kinds it does not know, so adding a field keeps `v`; it refuses a higher `v`
  with `event_version`.
- **Sending.** `Sender.Send` never fails a hook and returns within `Budget`
  (300 ms) whatever happens: the request gets 150 ms, and the spool write, the
  hook log and a daemon start run in a goroutine on the budget's context. When
  the budget ends Send returns and cancels it: a spool write under way finishes
  while the process lives (atomic, so an exit leaves the event whole or
  absent), and no daemon start begins, since its lock checks fail. When nothing
  listens it spools the event and starts a daemon with `Client.StartDetached`,
  unless the user stopped the daemon (`daemon.MarkStopped`, written by `baum
  daemon stop` and cleared by `Ensure` and a foreground `run`),
  `BAUM_NO_DAEMON=1` or `CI` is set. `StartDetached` spawns `baum daemon run
  --detached` under the start lock unless a daemon holds the daemon lock or one
  was started less than `StartBackoff` (3 min) ago: it records each start in
  `started.json`, which the daemon removes once it serves, so a daemon that
  cannot start is not started again by every hook (a start time in the future
  does not count). It does not wait for the daemon and passes the hook's
  environment without `TMPDIR`; the socket path never depends on `TMPDIR`, so
  the daemon resolves the socket the hook looked for. `Spawn` marks the
  caller's inherited descriptors close-on-exec first. A daemon that does not
  answer in time, answers an error, or does not know `event` gets the event
  through the spool; one older than the hook's baum is sent `stop` with
  `when_idle`, as `Ensure` sends it: an idle one stops and the next hook starts
  the newer version, a busy one restarts as it when its jobs finish. A daemon
  holding the socket but not answering is never replaced by a hook.
- **Spool.** `<state>/spool/`, 0700, one 0600 file per event named by the
  nanosecond it was spooled, written to a temporary file and renamed.
  `MaxSpoolEvents` (1000) and `MaxSpoolBytes` (8 MiB): a hook drops the
  oldest first and logs each drop; hooks spooling at once can each pass the
  bound by one, and the next `Put` restores it. The daemon drains it when it
  starts and every 30 s, oldest first, removing a file once recorded; a file
  that is not a small private file or not an event is removed and logged,
  and an event of a newer `v` is left for a newer daemon. Hooks log what they
  spool, drop and start to `<state>/hooks.log` (0600, 256 KiB, one older
  file), never an event's contents.
- **Record.** `hookevents.Store`, opened by the daemon that holds the lock:
  JSON lines `{"received","via","event"}` in `<state>/events/events.jsonl`
  (0600 in a 0700 directory), rotated at 1 MiB with two older files kept, a few
  thousand events. Lines that are too long or not records are skipped. An
  event id is recorded once however often it arrives (a late answer and the
  spool can both deliver it), within the last 4096 ids, across restarts;
  readers dedupe by id beyond that. A record that cannot be opened (a shared
  directory, an unreadable file) is logged and the daemon runs without the
  `event` method, so hooks spool rather than start a daemon on every call.
  `Store.Recent(n)` reads the latest records, oldest first; spooled events
  arrive late, so readers order by `event.time`. `Store.Subscribe(buffer,
  match)` delivers new records matching a filter; a subscriber that falls
  behind misses records (logged) and can reread `Recent`; `SubscribeCounted`
  also reports how many it missed, so it rereads only after a miss. This is the
  interface the live features (in-progress sessions, refresh on session end
  or push, decision capture) build on. In-progress sessions (see "Live
  sessions") and refresh on session end or push (see "Refresh on hook
  events") do; decision capture is not built yet.

### Refresh on hook events

`internal/hookrefresh` refreshes pull requests as soon as a hook says they may
have changed, rather than at background synchronization's next pass. The
daemon subscribes it to the record (`Store.Subscribe` with `Buffer`, 64, and
`Match`) while it builds the application, before the spool drains or the
socket serves, and runs it beside background refresh, one worker in all.

- **Events.** An agent's `session-end` (Claude Code, Codex and Cursor send
  it) and a `pre-push` Git went on with (exit 0). Not `stop`, which ends a
  turn, nor `post-rewrite`: a rewrite changes nothing on GitHub until it is
  pushed, and the push's pre-push reports it. An event older than `MaxAge`
  (10 min), which the spool delivered late, is left to the periodic refresh.
- **Never blocking.** `Store.Add` hands a record to the subscription without
  waiting; while the worker is busy, what does not fit the buffer is dropped,
  and the record logs it. The worker collects events for `Debounce` (3 s, at
  most 256; more are dropped and logged), then maps them together.
- **Mapping.** An event's directory (the agent's `cwd`, else the hook's
  `dir`) must lie in an available registered clone, the longest registered
  path containing it; any other directory is ignored without running git, so
  with nothing registered (a hosted-only user whose hooks started the daemon)
  an event costs one registry read per `Window` and nothing else. The registry
  is listed at most once per `Window` (30 s). git then reports the working
  tree's common directory, which must be the clone's (a linked worktree inside
  the clone counts, a repository nested in it does not). For a session end it
  reports the work checked out (`prlink.Local`: branch, the branch its
  upstream names in the same repository, commit), which maps to one pull
  request of the discovery cache (`Cached`, no network) exactly as live
  sessions link it (`prlink.Find`, see "Live sessions"). For a push it reports
  the repository the named remote fetches from, which must be the clone's: a
  push to a fork is ignored, and so is a push to a URL that names no remote
  (Git passes the URL as the name; the hook drops it, and an argument that
  reads as a URL, scp-style address or path runs no git). A push names each
  branch it updates or creates, never a deletion or a tag, and maps to the
  non-fork PRs whose `head_ref` is that branch and to the PR whose head is the
  head the push replaces. A linked worktree outside every registered path is
  ignored. A PR the cache does not have yet waits for discovery.
- **Timing.** A session end's PRs are refreshed after the debounce, a push's
  `PushDelay` (10 s) after its hook ran, since pre-push runs before the push
  reaches GitHub. When that refresh does not show the pushed head, the PR is
  refreshed once more `Recheck` (30 s) later (`MaxChecks`, 2), then left to the
  periodic refresh. A PR is refreshed at most once per `Window` (30 s); later
  events postpone a scheduled refresh by at most 60 s in all. A burst of any
  size costs, per window, one refresh of each PR it names (two when the pushed
  head is late), one git inspection per directory and remote, and at most one
  discovery poll per repository.
- **Refresh.** `background.Service.RefreshNow` refreshes the due PRs of one
  clone's store (a repository's clones with different common directories keep
  separate saved reviews, each refreshed) as `Sync` does: metadata for a PR without a saved review, and
  metadata and evidence (`refresh.Refresher`, CAS publication) for one with.
  It shares the per-host rate pause with `Sync`, and an event whose
  repository's cached rate budget is exhausted is skipped. A failed refresh is
  not retried here.
- **Automatic generation.** When a refresh shows a head other than the one
  the cache had, the cached PR could get an automatic job (the user authored
  it or is asked to review it, it is not from a fork, its author has write
  access, and the refresh shows it open and not a draft), and automatic
  generation is on globally and for the repository, the worker calls
  `Scheduler.Trigger` for the repository, at most once per `Window`. Trigger
  polls discovery and passes the snapshot to `Observe`, the only path to a
  job, so the scheduler's baseline, eligibility, dedupe, hold, gate, daily cap
  and no-catch-up rules apply unchanged, and a head it already saw queues
  nothing. Since background refresh and Trigger observe from two goroutines,
  `Observe` ignores a snapshot fetched before the newest one it took for the
  repository. The worker never queues or runs generation.
- **Stopping.** The worker stops with the daemon's run context, cancelling a
  refresh or poll under way. Its schedule is in memory only: a restart drops
  pending refreshes, which the periodic refresh covers.

### Live sessions

`internal/livesessions` tracks the agent sessions running on this machine
from the agent hook events in the record, and serves them to the app. It adds
to reading session files from disk (see "Session transcripts"), which stays
the full history and the answer for a session that has ended; it does not
replace it.

- **Events.** `Tracker.Run` subscribes to `Store.Subscribe` (agent events whose
  name it knows), rebuilds from `Store.Recent` (every record, filtered to the
  last two hours (and all of a recently ended session) and applied in `event.time` order, since a spooled event is
  recorded late), and follows new events. An event counts when it has an
  agent, a session id, a time and a directory (`session.cwd`, else `dir`). The
  agents share baum's event names, which map to a normalised kind:
  `session-start` to `start`; `user-prompt-submit` and `before-submit-prompt`
  to `prompt`; `pre-task`, `post-task`, `post-todo`, `post-tool-use`,
  `pre-compact`, `subagent-start` and `subagent-stop` to `tool` (the hooks each
  agent has, so Claude Code reports subagent and todo activity, Codex tool
  runs, and Cursor prompts and compaction); `stop` to `stop`; `session-end` to
  `end`. Other names are ignored. A session is keyed by agent and id; an id
  over 128 bytes or an agent over 32 is not a session baum's hooks describe and
  is ignored. An event id is applied once (the last 4096 are remembered).
- **Missed events.** `Store.Add` drops an event for a subscriber whose buffer
  is full. The event loop therefore never waits for git: it applies the event
  at once, and a new session is hidden until a background worker looks up its
  link (`ResolvePending`; a directory in no registered clone is dropped then).
  Each sweep (30 s) checks the subscription's drop count
  (`Store.SubscribeCounted`) and, only when it missed events since the last
  reconcile, runs `Reconcile`, which rereads `Store.Recent` and applies the
  events not yet applied, so a dropped `session-end` is found within 30
  seconds rather than leaving a session waiting until it times out. Without a
  drop the record is not read, since reading it takes the store's lock, which
  every hook's event needs too.
- **States.** `running` after a start, prompt or tool event. `idle` after a
  `stop`, which agents send at the end of every turn (waiting for the user),
  and for a running session silent longer than `IdleAfter` (10 minutes): an
  agent that crashed sends nothing, and one thinking for long reports nothing
  either. `ended` after `end`. Any later work event makes an idle session
  running again; an event after the end, newer than it, starts a new run (a
  resumed session: `started_at` restarts), and an event older than the end is
  ignored. A late-delivered older event never moves `last_activity_at` back,
  but an older start can move `started_at` earlier. A session silent for
  `DropAfter` (2 hours) is dropped, an ended one `EndedFor` (24 hours) after
  its end, so the Sessions page can list what ended recently. At most
  `MaxSessions` (200) are held: a new one evicts an ended session (the one
  that ended longest ago), else the least recently active, so a running session
  goes only when every session held is running. The clock is injected
  (`Tracker.Now`); `Sweep` applies the timeouts every 30 seconds. The record
  rotates by size and can hold less than a day, so the tracker also writes its
  ended sessions (the API's fields, no path) to `live-sessions.json` in the
  record's directory when they change, and a rebuild loads them first and then
  applies the record's events over them; a rebuild also takes all events of a
  session that ended within `EndedFor`, not only those of the last two hours.
- **Linkage.** Not a second linkage: `Resolver` uses what session linkage and
  discovery already have. The directory's repository is found with
  `gitlocal.Open` (so a subdirectory or a linked worktree is its clone), whose
  git directory is matched against the registered clones' `common_dir`;
  a directory in no registered clone, or one whose clone is unavailable, is
  not tracked at all. The branch is `Repo.CurrentBranch` (empty when
  detached). The pull request (`prlink.Find`, shared with refresh on hook
  events) is the open one in the discovery cache
  (`Discovery.Cached`, never GitHub) whose head branch (`head_ref`, new in the
  cache, which refetches responses cached without it) is the branch and that
  is not a fork's (a fork's branch of the same name is another branch); the
  newest when several. When no head has the branch's name, the head branch
  named by the branch's upstream (`branch.<name>.merge`, when its remote is the
  same repository) is tried, then the pull request whose head commit is the
  commit checked out, which covers a detached HEAD and a push to a branch of
  another name. Without one the session is still tracked against its
  repository and branch, and only a PR page it does not match omits it. The
  clone list is read at most every 30 seconds and a directory that fails to
  link is not looked up again for a minute, since listing and opening a clone
  run git. A session is looked up when it is new and at most every 30 seconds
  after, since the branch changes while it runs and a pull request appears
  once pushed; a lookup that fails keeps the last link. Failing to list the
  registered clones is not retried for 30 seconds either.
- **API.** `GET /api/live-sessions` (a `LocalAPI.Live` provider, behind the
  same auth as every application route; a daemon with no record answers
  `{"available":false,"code":"live_sessions_unavailable"}`) answers
  `{"sessions":[…]}`, the most recently active first, each
  `{"agent","session_id","state","started_at","last_activity_at","ended_at"?,
  "last_event","repository","branch"?,"pr"?}`: `state` is `running`, `idle` or `ended`,
  `last_event` is `start`, `prompt`, `tool`, `stop` or `end`, `repository` is
  `owner/name` in lower case, `branch` is absent when detached and `pr` when
  the branch has no pull request, and `ended_at` unless the state is `ended`. The working directory and transcript path
  stay inside the tracker.
- **Stream.** The tracker publishes a `live` event with no data (like `index`,
  a notification the app answers by reading the endpoint) when what the app
  sees changes, at most once a second, and on a `reset` the app rereads like
  everything else. A change that only moves `last_activity_at` by less than 30
  seconds is not one (the endpoint still serves the fresh value), so a session
  working steadily costs the app a request every 30 seconds or at a state
  change, not one a second. The app keeps the list it has when a read fails.
- **App.** The app rereads the list on `live` (and `open` and `reset`). An
  inbox row shows "Agent running" or "Agent idle" (a count when several) for
  sessions of its pull request, matched by repository and number across
  clones; the PR page lists them in a "Sessions in progress" panel, with their
  agent, state, branch, start and last activity, above the review. The
  Sessions page (`?page=sessions`, with a sidebar entry counting the running
  ones) lists the running and idle sessions, and those that ended within the
  retention, newest end first. The inbox and the PR page never show an ended
  session: when one leaves their list the page reads the session
  list from disk again, where the finished session now is (the existing
  sessions UI), so nothing is lost if the files were not complete earlier.

### Transports and control protocol

The app transport is `internal/uiserver` on `127.0.0.1` (a free port unless
`--port`), unchanged: launch-token exchange, the cookie scoped to the secret
`/s/<namespace>/` path, exact Host and Origin checks. Its token is written to
the credential file instead of being printed by a foreground process.

The control transport is a Unix domain socket, mode 0600 in a 0700 directory:
`$XDG_RUNTIME_DIR/baum/daemon.sock`, else `daemon.sock` in the state
directory, else (a path too long for `sun_path`) `/tmp/baum-<uid>/daemon.sock`.
That fallback is fixed rather than under `TMPDIR`: an agent sandbox points
`TMPDIR` at a directory that lasts one session, and the app, a terminal and a
sandboxed hook must all compute the same socket, one that outlives the session.
The daemon creates the directory 0700 and refuses one that is not private or
not the user's, and clients refuse to dial a socket in such a directory, so
another user who creates `/tmp/baum-<uid>` first can only keep the daemon from
starting. That is where a daemon listens; clients dial the `socket` the
running daemon recorded in `daemon.json` in the state directory, so one with a
different `XDG_RUNTIME_DIR` (ssh, su, a sandbox) still reaches it. The daemon
checks every 5 s that its socket file exists; when something removed it (a
`/tmp` cleaner, a user tidying up) it creates the directory again and listens
anew on the same path, since clients would otherwise find no socket and no
replacement could take the lock it holds. Windows 10 1803 and later have
`AF_UNIX` sockets, so Windows uses the same socket in the state directory,
protected by that directory's current-user DACL, instead of a named pipe
(which would need a new dependency for overlapped I/O). Each request is one
JSON line, `{"protocol":1,"method":"hello","client":"<version>"}`, answered by
one JSON line, `{"ok":true,"status":{…}}` or `{"ok":false,"error":{"code":…,
"message":…}}`; a connection may carry several, and lines are limited to
64 KiB. `hello` and `stop` work in every protocol version, so any client can
identify and replace any daemon. `ping` and every method registered in
`Options.Methods` require the client's protocol to equal the daemon's, and
answer `protocol_mismatch` with the daemon's status otherwise. Increment
`daemon.Protocol` for any incompatible change. The application registers its
methods through `App.Methods`; a method answers with its own error code by
returning a `daemon.Error`. `event` is the one it has (see "Hook events");
hook events that may move a head reach the automatic scheduler through its
`Trigger` (see "Refresh on hook events"). Remote attach would be
another transport beside these two; none exists.

### Desktop app interface

The desktop app talks to the daemon only through the bundled `baum` binary
and two files:

1. `baum daemon ensure --json` starts the daemon if needed (replacing an older
   one), waits until it serves, and prints one JSON line on stdout:
   `{"protocol":1,"version":"v0.3.0","pid":4242,"started_at":"2026-10-19T16:00:00Z","app":"http://127.0.0.1:53124","socket":"/run/user/1000/baum/daemon.sock","state":"/home/user/.local/state/baum/daemon"}`.
   `stopping` appears only while it shuts down, and `restart_pending` when an
   older daemon was kept because it is running jobs and will restart as this
   version once they finish (see Lifecycle). On failure it exits non-zero
   with the reason on stderr. `--port` and `--concurrency` apply only when it
   starts the daemon.
2. `<state>/credential.json`, mode 0600 (Windows: current-user DACL), holds
   `{"url":"http://127.0.0.1:PORT/#token=<43 characters>"}`, the launch URL.
   Loading it runs the bootstrap page, which exchanges the fragment for the
   session cookie. A `page=diagnostics` member beside the token
   (`#token=…&page=diagnostics`, which the app adds after first-run setup)
   opens `?page=diagnostics` under the namespace; no other value is carried
   over. The token stays valid for any number of exchanges for the
   daemon's whole life, so the app can be relaunched against one daemon; a new
   daemon writes a new token atomically, and the old one then gets 401 (or no
   answer, if the port changed). The token also works as
   `Authorization: Bearer` on `/api/` routes.
3. `<state>/daemon.log` is the daemon's log (it rotates at 1 MiB, keeping
   `daemon.log.1` and `.2`); `<state>/daemon.out` holds what a detached daemon
   wrote before its log opened, or a crash.
4. The setup screen reads two reports, neither of which changes anything:
   `baum daemon login-path --json` (hidden) resolves the login shell's
   environment as a starting daemon does (`shellenv.Resolve`, then
   `shellenv.Merge` over the inherited one) and prints
   `{"shell","login_shell","error","path":[…],"baum":{"path","resolved","version","self"}|null}`,
   `baum` being the first executable `baum` on that `PATH` (relative entries
   skipped), with its links resolved and its `--version` (5 s timeout; its
   own version when it is this executable). `baum hooks status --user --json`
   prints `{"agents":[{"agent","name","detected","plugin","installed","enabled","version","error"}]}`
   from native plugin registries, with executable detection and installation/
   enablement separate from the agent's hook trust decision. It installs and
   removes `baum@baum` through native CLIs with `baum hooks install --user`
   and `uninstall --user`. Native status queries have a 10-second bound and
   actions a 90-second total budget; the desktop resolves the login shell PATH for native plugin commands and allows
   240 seconds for PATH resolution, both agents and process overhead.
5. `baum daemon stop` stops the daemon gracefully and waits for it to exit
   ("Quit and stop background reviews"), and marks it stopped by the user, so
   hooks do not start it again until `baum daemon ensure`. `baum daemon
   status [--json]` prints the same status, or exits non-zero when none runs.

`<state>` is `$XDG_STATE_HOME/baum/daemon`, by default
`~/.local/state/baum/daemon` on every platform, and is reported in `state`.

### Diagnostics

`baum internal doctor` reports the daemon (`daemon` check): its state directory, and
when it runs its version, process id, uptime, protocol and socket; a warning
when it is older than the doctor's baum, speaks a newer protocol, or holds the
lock without answering. The `baum-command` check looks up `baum` on `PATH`
(in the daemon, the login shell's), which is what the agent hooks run: OK when
it resolves to the reporting executable (`Options.Executable`), a warning
naming its path and `--version` when it is a different baum, and a warning
when there is none; both fixes point at the desktop app's "Command Line and
Hooks…".

## Desktop app

`desktop/` is a Tauri v2 shell, a Rust crate outside the Go module's
packages. It bundles the release `baum` binary and depends on it only through
`baum daemon ensure --json` and the daemon's private credential file.
Everything that knows where the daemon lives stays in Go. The shell owns the
window, its navigation policy and the reconnect screen, the tray (job counts,
pause of automatic generation, an icon that shows idle, running or attention),
the window's close and quit behaviour and start at login; the daemon serves
`app/` to it like any loopback client. Besides `ensure` and `stop`, the tray
reads `GET /api/jobs`, `/api/automatic` and `/api/diagnostics` and sets
`PUT /api/automatic` with the launch token as a bearer, over the app API the
window uses. It also owns first-run setup
(`src/onboarding.rs`, `src/install.rs`, `../app/src/desktop/setup.tsx`): a bundled screen,
with its own narrow capability, from which the user installs the bundled
`baum` as the `baum` command on the login shell's `PATH` (a symbolic link to a
stable path of the bundled binary; an AppImage and Windows run and link a
copy in the app's data directory, which Windows adds to the user `PATH`;
the password prompt only for root-owned system directories) and the
native `baum@baum` marketplace plugins; after that the window opens the
daemon's diagnostics page. `setup.json` records onboarding and the command
links/copies it installed; agent registries own plugin installation and
enablement. Plugin status/install/removal use the resolved login shell PATH,
with an inherited-PATH fallback warning if resolution fails. Command removal
uses its saved command records; plugin removal targets only `baum@baum`. See [`desktop/README.md`](desktop/README.md).
