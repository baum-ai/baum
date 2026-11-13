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

The binary provides only the desktop daemon, setup helpers, local agent
plugins and support commands. It has no hosted authentication, upload or
repository setup commands, and never manages or executes Entire. Teams may
run Entire independently; baum continues to parse their checkpoint objects.

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
| `review` | document types, the strict, lenient and stored schema views, normalisation, validation with located findings, derived fields, version history, model-free refresh, a pull request's model-free facts, output redaction, the tile catalogue |
| `agent` | the backend interface and its implementations, the prompt interface, the generate-and-repair loop, budgets |
| `viewer` | the React components that render a document and its versions, and the standalone single-page app that the reference viewer host embeds |
| `sessions`, `linkage` | session discovery and commit-to-session matching, used by `bundle` sources |
| `decisions` | the decisions an agent session made: the decision schema, the cleaned transcript a model reads, the strict reply schema and parser, the citation validator, and the published extraction prompt |
| `features` | grouping work into features: the assigner, summarizer and consolidation model calls with their published prompt versions, decision attribution, status and the large-update rule; [rules](features/README.md) |
| `validation/commitlabel` | deterministic validation-run commit labels from supplied session events and verified Git objects; [rule and limits](validation/commitlabel/README.md) |
| `redact` | the one redaction engine used for input, output and logs; `redact.Shared` compiles it once per process (again only if `redact.Config` changes), and `redact.Memo` remembers masked text by a salted hash of its input, for text masked again unchanged, such as the live sessions' lines (`transcript.Renderer.Mask`) every second |
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

`internal/github` provides the GraphQL client `internal/assemble` reads
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
backoff, `sources` and the mapping onto the bundle. After a pull request's
header, `Fetch` reads its parts three at a time; a caller may bound each
request (`WithRequestTimeout`) rather than the whole read, and count the
requests a read made (`CountRequests`).

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
- Callers select the pull request and repository through `github.ResolveOptions`.
  Desktop jobs carry an explicit repository and pull request. Package callers
  without a number can resolve the current branch through its upstream or the
  clone's origin; associated pull requests are paginated, preferring an open
  one and otherwise the most recently updated. Several open matches are an
  error. Fork branches may resolve to a pull request in the upstream repository.
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
- Content trimming cuts in a fixed order and never drops a hunk. Each cut adds a
  `generator.truncated` note. When whole sessions are dropped, one note gives
  the total number omitted; its bytes count against the size limit.
- The diff index (every hunk's entry without its patch) has its own byte
  limit (`max_diff_index_bytes`). A pull request over it is still reviewed:
  `Fit` first collapses each generated or vendored file (lockfiles among
  them) to one entry, then other files one at a time, the largest saving
  first, and only then leaves whole files out of the index: generated and
  vendored first, then docs, then tests and config, code last, each kind
  from the end of the diff. Facts, which no model reads, have no diff index
  limit. A collapsed entry has an ID hashed from the path and the IDs of
  the hunks it stands for, the file's path, role, summed line counts and the
  new-side span of its hunks, and no patch (`textless_reason` `collapsed`, or
  the checkout's `omitted_` reason when it had one). Each stage adds one
  `generator.truncated` note with its count. Past `max_diff_files` changed
  files, assembly does not read a file's patch: generated and vendored files
  are the first left unread, each is one textless entry (`file_limit`) with
  the comparison's line counts and an ID hashed from the path and the
  file's blob IDs and modes (the compared commits when the comparison has
  no blob IDs), and one note gives their count. Like a hunk ID, either ID
  changes whenever the file's change does, so a later version reuses a
  section's details only for unchanged code. The
  checkout does not export such a file as a compared path; only the head
  tree's pass takes it, while the budget lasts. Only the
  patch-byte limit (`max_diff_patch_bytes`) still fails assembly.
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

A section's `details` state exists only in the stored schema. A host sets it
on a section that lacks its details: `pending` while they are still being
written (a live draft shown while a job runs) and `failed` when they could
not be written after repairs (a published version keeps the outline). Such a
section keeps its outline (`id`, `title`, `kind`, `hunks`, `assessment`,
`derived`); its `summary` may be absent or null, its `attention` null and its
`evidence`, `blocks` and tiles empty, and its risks are those the previous
version recorded for it. Without the state a section still needs its summary.
The model never writes the state: the strict authored contract and every
schema derived from it for a model (`ModelSchema`, Codex's strict schema)
leave it out and require an object summary, exactly as before the state
existed, so an authored section that carries it is a validation finding.
`pending` belongs to a live draft only: a host never publishes or saves a
version with it, and turns any section still pending into `failed` first
(`review.FailPendingDetails`). A section the next version drops keeps
`failed` and turns `pending` into `failed`, since nothing will write its
details; the viewer shows an obsolete section without details as
"Details unavailable", with no retry. After a rollback to a release without
the state, a desktop daemon fails stored validation and refresh of a local
document that carries `details` or the `incomplete_sections` limitation;
the hosted service never stores either, so a hosted rollback is unaffected.

A staged review (`agent.Generator.StagedReview`) is written in parts, and
`review` holds everything it validates them with, so no rule has a second
copy:

- **Stage schemas.** `OutlineSchema`, `DetailsSchema(kinds)` and
  `ContextSchema` project the model schema (`ModelSchema`): the outline keeps
  the document's `schema`, `summary` (every model-written field but
  `context` and `review_minutes`), `relations` and, per section, `id`,
  `title`, `kind`, `hunks` and `assessment`; the details are
  `{"sections": [...]}` with each section's `id`, `summary`, `attention`,
  `risks`, `evidence`, `blocks` and `tiles`; the context is
  `{"context": ...}`. Each property is the model schema's own, rules naming
  a dropped property go, and so do unreachable definitions.
- **The staged contract**: the strict authored contract, except that a
  section may carry the `details` state (with a null summary, a null
  attention and no evidence, blocks or tiles) and the summary may lack its
  context. `PrepareStaged` and `CompleteStaged` are `Prepare` and
  `CompleteWithPrompt` under it; the one-pass contract is unchanged.
- **Parts.** `PrepareOutline` keeps the outline fields, gives every section
  pending details, applies the whole-document rules (schema, hunk and
  section IDs, at most one mechanical section) plus one of its own (every
  logical section with hunks has an assessment), and puts the previous
  version's sections first in their order, as completion would.
  `PrepareSectionDetails` merges one section's details into that outline and
  validates the whole document, every other section still pending, so each
  finding is the section's own. `PrepareContext` does the same for the
  context. `AssembleStaged` builds a draft (sections not written are
  `pending`, or `failed` once they failed) or the final document (no
  `pending`, and the reading time set).
- **Reuse.** `ReusableSectionDetails` returns, for each outline section
  whose hunk IDs are those of the previous version's section with the same
  ID, that section's previous details, when they were complete (no
  `details` state): its summary, attention, active risks without their
  lifecycle fields, evidence, blocks and tiles. A mitigated risk is left
  out, so completion keeps it mitigated. The generator validates what it
  reuses like model output and sends a section it rejects to the model.
  Nothing is reused when the bundle offers an image the previous version
  was not offered (`NewImages`, against `PreviousVersion.Attachments` from
  its `index.attachments`): only the details stage opens and places images,
  and images often arrive after the code, as a session is linked later.
- **Reading time.** `ReviewMinutes` replaces the model's estimate in a
  staged review: the words of the overview (text, attention, review
  exchange, release note, breaking change), the context, and each section's
  title, summary, attention, behaviour steps (title, text, before, after,
  question), risks, evidence (claim, detail, expects) and decisions (title,
  text, rationale, trigger, rejected alternative, evidence) at 400 words a
  minute, rounded up and kept within 1 to 240. Code excerpts add no time.
  A one-pass review keeps the model's estimate.

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
  critical risks. A current section without its details (`pending` or
  `failed`) is scored by its assessment and the risks it holds, and makes
  the document's score a lower bound with an `incomplete_sections`
  limitation, since the risks its details would add are unknown.
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
  its `introduced_version`. A repair cannot drop a risk;
- a section without its details (`pending` or `failed`) says nothing about
  whether its risks were addressed, so, as in a stale section, the previous
  version's risks are carried as they were recorded: an active one stays
  active under its `introduced_version` and a mitigated one keeps its
  `mitigated_version`.

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
platform and `internal/refresh` use it. It
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

Every run has no MCP server: Claude Code gets `--strict-mcp-config` with an empty
`--mcp-config`, Codex `--ignore-user-config`. A local run may set
`Request.MCP` to give the agent exactly one, baum's own (the daemon's
`/api/mcp`, over HTTP); see
"MCP server for agents" for the flags and the token's path.

A backend may also implement `agent.Completer`: one structured completion
with a system prompt, a user message and a schema, no working files, used by
decision capture. It is normally one model call. The anthropic backend makes one Messages call offering only
the StructuredOutput tool (`tool_choice` auto, one call, since current models
refuse a forced choice while thinking); claude runs `claude -p` with no tools
but StructuredOutput, no settings, MCP servers or session persistence, and at
most two turns so a reply the harness rejects is retried once; codex
runs `codex exec --ephemeral --ignore-user-config` read-only with its shell
tools disabled, the chosen reasoning effort (Codex's own when none) and the
system prompt as its model
instructions, in one turn, which a call to one of its remaining tools would
extend by a request. Both CLIs run in an empty temporary directory with the prompt on
stdin after `sessions.GenerationRunMarker`.

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
`prompt.Fix` (`fix-public-v1.md` with `fix-conflicts.md`, `fix-checks.md` and
`fix-threads.md`).

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
`pricing.List.BackendOutcomeCost` and `BackendAccountingCost` return nil with
`billed_by_cursor_subscription` for this backend whatever its model is called:
the recorded cost is unknown. Such a job is not blocked and needs no
acknowledgement: it starts, manually or automatically, like any other. A failed process is classified
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
A chosen effort (`Backend.Effort`) replaces both.

The local app's reasoning effort is one of the preferences, per use case
(`effort` for reviews, which chat follows, `fix_effort` with a fix agent of
its own, and `decisions.effort` for data extraction): Default (empty) or
`low`, `medium` or `high` (`config.Efforts`). It reaches the backends through
`config.Config.Effort` and `generate.DefaultBackendsFor`: Claude Code as
`--effort`, Codex as `-c model_reasoning_effort` and the Anthropic API as
`output_config.effort`; cursor-agent has none. Default leaves it to the
harness: local Claude Code runs (the User profile) think as Claude Code
decides, while the Isolated profile of hosted and evaluation runs always sets
`MAX_THINKING_TOKENS=0` and `CLAUDE_CODE_DISABLE_THINKING=1`.

Every request sent is one call, charged before the next is made: a call the
request's `MaxCalls` or `MaxTokens` cannot pay for is not sent (zero is no
limit), and `max_tokens` is the smaller of 32,000 and the tokens left. Usage is the API's
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
the run's cost becomes a lower bound, and the generate-and-repair loop then retries
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
a failed turn's message (`unexpected status NNN`). Claude Code that is not
signed in sends no request: its synthetic assistant message carries `"error":
"authentication_failed"` and its result has `is_error` and no status, so that
class alone makes the failure an `authentication_error` refusal. A Codex 400 carries only the
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

A prompt the user replaced (globally or for a repository) adds `+custom` to
the version, and the stored metadata's `prompt_source` says where the custom
prompts used came from: `repository` when any did, `global` when only global
ones did, absent when every prompt was built in. The daemon's jobs and
`baum decisions` use the prompts resolved for the repository (repository, then
global, then built in); `baum internal generate` opens no registry and uses the
built-in prompts. The replaceable review prompts are the four of a staged
review and a review group's (`review-group`), which is the whole text before
the Submitting heading, the review instructions included; the one-pass review
prompt (`prompt.Prompts`) is not replaceable, as no job writes one pull
request's review in one pass. A retry of a staged review's details refuses to run when the
prompts' digest differs from the saved version's `prompt_digest`. The local app
edits the overrides through `api/settings/prompts` and
`api/repositories/{owner}/{name}/prompts` (see the app README, Settings ›
Prompts) and shows `prompt_source` beside a review version.


The module ships and maintains its own public prompt in `agent/prompt`:
`review-public-v6.md`, shared by every backend and ending with a "Submitting"
heading, plus a submit block per backend (`submit-claude.md`, used for claude
and anthropic, `submit-codex.md`, and `submit-cursor.md`, which points at the
schema the cursor-agent backend appends and lists the tools that stop a run),
embedded with `go:embed`.
`Prompts.Instructions` appends the backend's block under that heading, sets
`Version` to `review-public-v6` and `Digest` to the sha256 of the composed body,
and fails for any other backend. A host may supply another implementation; the
hosted service does, with its private prompts. `StaticPrompts` is a fixed
implementation for tests and simple hosts. The prompt is per backend because
each backend submits its result differently. `GroupPrompts` is the local
review group's prompt (`group-review-public-v4`): the same text with
`group-public-v1.md` inserted before the Submitting heading, which says how a
group's bundle, per-member checkout and `members.json` differ and what a
combined review covers (see "Review groups").

A staged review takes its instructions through `StagedPrompts`, whose
`StagedInstructions(ctx, backend)` returns a `StagedPrompt`: `Shared` and
one text per stage (`Outline`, `Details`, `Context`), with a `Version` and a
`Digest` covering all four. The module's `prompt.StagedPrompts`
(`review-staged-public-v1`) embeds `review-shared-public-v1.md` (the stages
and what each writes, so none repeats another, plus the ground rules,
inputs, checks, anchors, writing and calibration of `review-public-v6.md`)
and `review-outline-public-v1.md`, `review-details-public-v1.md` and
`review-context-public-v1.md`, which carry the v6 rules for the parts each
stage writes, name the stage's own schema file and end with a "Submitting"
heading under which the backend's submit block goes, as for `Prompts`.

A host can also pass `Run.Decorate`, which the loop applies to every document
the model returns before masking and validation, to fill what the host
decides and the model never writes: a review group's `stack` and each
section's `pull_requests`. `Run.ModelSchema` adjusts the model schema a run
offers: a review group's asks for `stack` with only its `title`, the group's
name.

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

Local generation (`internal/generate`) starts every prompt
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

A local Claude Code or Codex call can keep its session: `Request.PersistSession`
omits `--no-session-persistence` (Claude Code) or `--ephemeral` (Codex);
`ResumeSession` continues an earlier session (`--resume <id>`, `codex exec
resume <id>`) and implies persisting; `ForkSession`, with `ResumeSession`,
continues in a new session (`--fork-session`, `codex exec fork <id>`) and
leaves the original untouched. `Result.SessionID` is the id the CLI reported
(Claude Code's `init` event, else its `result` event; Codex's `thread.started`
`thread_id`), the new session's for a fork. Only the `User` and `Local`
profiles accept these options, and only for reviews and tasks, not fixes; the
hosted profiles, Cursor and the Anthropic backend refuse them, and a fork
without a session to resume is an error. Without them every argv is unchanged.
Codex's `exec resume` and `exec fork` take no `--sandbox`, so the same
read-only sandbox is passed as `-c sandbox_mode="read-only"`. `ResumeSession`
must be a canonical UUID (both CLIs report one), which also keeps an id from
being read as a flag. A resume or fork must run with the same `Directory` as
the session it continues: Claude Code stores sessions per working directory
(`~/.claude/projects/<encoded cwd>`) and Codex filters by it too. A resumed
Claude Code session keeps the system prompt it recorded, so per-turn framing
belongs in the user prompt.

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

### Staged review

```go
outcome, document, err := generator.StagedReview(ctx, agent.StagedRun{
	Run: agent.Run{Bundle: b, EncodedBundle: encoded, Directory: dir,
		TileKinds: kinds, Budgets: budgets},
	Prompts: prompt.StagedPrompts{}, Progress: onProgress})
completed, findings := validator.CompleteStaged(document, b, dir, outcome.Model, version, now)
```

`StagedReview` writes a review in three stages so a reader sees part of it
sooner and a failure loses less. `Generate` is unchanged and still writes a
review, and every review group, in one pass; `StagedReview` refuses
`Run.Decorate`. Each stage is a `RunTask` task in the same run directory,
labelled with its name (`Task.Label`), so its schema and rejected replies
are `schema-outline.json`, `rejected-details-1.json` and so on, and the
stage prompts name their own file. Every stage's prompt starts with the
same prefix (the shared instructions, the bundle and the source excerpts),
followed by the stage's instructions, the accepted outline (for the later
stages) and, on a repair, the rejected part and its findings; a repair
resends the prefix, which its stage's earlier call has cached, and nothing
the stage did not write. Across stages the prefix probably does not hit the
provider's cache: each stage's schema (the `StructuredOutput` tool, Claude
Code's `--json-schema`, Codex's output schema) comes before the prompt in
what the provider caches, and it differs per stage. The evaluation after
the daemon wiring measures it; if it matters, the follow-up is one schema
shared by the stages and a cache breakpoint after the prefix.

The details and the context run at the same time only when the backend
reports that its calls may share the run directory
(`agent.ConcurrentCaller`): Claude Code's `User` profile (the schema goes on
the command line), Codex's `Local` profile (a temporary schema directory per
call) and the Anthropic API backend (HTTP and read-only tools). Claude
Code's `Isolated` profile shares one `HOME` and config directory under the
run directory, Codex's `Container` profile writes the output schema into the
shared `CODEX_HOME`, and Cursor's per-workspace state is not known to be
safe, so they run the details first and then the context, which is skipped
when no section has its details. A host that wraps a backend must forward
`ConcurrentCalls` for it to apply.

1. **Outline**, first: the overview and each section's `id`, `title`,
   `kind`, `hunks` and `assessment`, with little or no reading of files.
   `review.PrepareOutline` validates it, and a repair resends only the
   outline and its findings. A failed outline fails the run as `Generate`
   fails (`ReasonValidationRejected` with the rejected outline, or the
   error).
2. **Section details**, once the outline is accepted. A section whose hunks
   are unchanged since a previous version with complete details keeps those
   (`review.ReusableSectionDetails`) and is not sent, unless the bundle
   offers an image that version was not offered. The model writes the
   rest in one call; each section is validated on its own
   (`review.PrepareSectionDetails`), and a repair sends only the sections
   still failing, each with its rejected details and its findings, and
   cannot reactivate a risk the section's first details left out
   (`review.PreserveSectionRiskOmissions`). A section still invalid after
   `MaxRepairAttempts`, or never reached because the stage ended early (a
   spent budget, a provider failure), is published with `details: "failed"`
   and its outline. If no section ends with its details (none written and
   none reused), the run fails as a one-pass run would; a regeneration that
   reuses some sections succeeds even if every section sent fails.
3. **Context**, with or after the details: `summary.context`, with
   the reading the context rules ask for (`review.PrepareContext`). If it
   fails, after its repairs or with any error, the document has no context
   and the run still succeeds.

The result is `review.AssembleStaged`'s final document (complete sections,
failed ones without details, the context when written, and
`summary.review_minutes` from `review.ReviewMinutes`, which no model
writes), checked with `review.PrepareStaged`; it never holds `pending`. A
host completes it with `CompleteStaged` where it completes `Generate`'s
document with `CompleteWithPrompt`, after `RedactStagedOutput` (where it
calls `RedactOutput`) and `RejectSecrets`: each stage's outcome carries its
own findings, rejected part, model and diagnostics, and
`RedactStagedOutput` masks them, and each stage's reason, as `RedactOutput`
masks the run's. One identifier pass masks every reply of every stage
before anything is built from it, so its
counts cover the run; a secret masked in a closed value during the outline
skips the later stages, since the host rejects the run anyway.

`StagedRun.Progress` is told of each stage result as it lands (the outline
accepted, each section's details written, reused or failed, the context
written or failed) with `AssembleStaged`'s draft so far, in which sections
not yet written are `pending`; calls come one at a time from the stages'
goroutines. A host completes a draft with `CompleteStaged` to show it, and
never stores one with `pending`.

**Budgets.** The outline has a quarter of the run's calls and tokens (at
least one of each). What it leaves goes two thirds to the details (at least
one call while any remains) and the rest to the context, or all to the
context when every section was reused, so the stages never spend more than
the run's budgets together. `MaxProviderAttempts`, `MaxRepairAttempts`,
`StallTimeout` and `ToolTimeout` apply per stage and per call as in
`Generate`, and the host's wall clock covers the whole run. A stage whose
share is spent fails with `GenerationBudgetExceededReason`, with the
consequences above.

**Outcome.** `StagedOutcome` embeds the run's `Outcome`, with the calls,
tokens, attempts, repairs, usage, accounting, diagnostics (at most 512) and
sessions of every stage summed, and has each stage's own `StageOutcome`:
its outcome, with its reason when it failed even if the run went on, and
for the details stage the sections written, reused and failed. Each stage
records the backend session of its call; the details stage's is its first
pass, the one that wrote most of the document, and it is the run's
`SessionID` (the outline's when nothing was sent to the details stage).

**Continuing a version.** A run given `StagedRun.Outline` (a saved version's
outline, `review.StoredOutline`: its summary and the sections it wrote, not the
stale ones it carried) writes no outline and no context: the outline is
prepared as an outline reply, `StagedRun.Context` is kept when it still
validates, and the details of every section `StagedRun.Reuse` (that version,
refitted to the diff) holds complete are reused, so only the sections it
published without their details are written, with every call and token of the
run's budgets. The bundle's previous version stays the one before, which
completion reconciles against. An outline that no longer validates fails the
run before any call. The daemon's details retry is its only host (see
"Persistent generation jobs").

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
included, is free text, and none is a closed value. A task with a `Label`
(lowercase letters, digits and dashes) writes `schema-<label>.json` and
`rejected-<label>-N.json` instead (`TaskSchemaFile`), so tasks running in
one directory at once, such as a staged review's stages, keep their own
files; without one the names are unchanged.

## `decisions`

A decision is a choice between plausible alternatives that lands in the
repository, made by the human, by the agent, or proposed by the agent and
approved by the human. `decisions` holds everything about extracting them
that does not depend on where a session is stored or which prompt asks for
them, and the published extraction prompt; the hosted service imports it
with its own prompt.

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
  it. `NewPublicExtractor` sends the published prompt,
  `decisions-public-v1`; its `PromptVersion` is that name followed by the
  hash, so its extractions never share a cache with a host's prompt of the
  same text.
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
standalone single-page app, which the `baum` binary embeds and the reference viewer host
serves in package integrations and tests.

The viewer reads only the document and its versions. A host connects it
through props and context:

| Seam | Required | the reference viewer host | Notes |
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
| details retry | no | not used | writes again the details of a current section whose `details` is `failed`; the section offers Retry only with it |
| context pending | no | not used | says the current draft's context is still being written, so the Summary shows "Writing context…" while it has none |

Optional seams default to absent, and the viewer renders the document alone.
A section without its details (`details` `pending` or `failed`) shows its
outline: its title, assessment and hunks under a live status, "Writing
details…" or "Details unavailable", and read marks, approvals and discussion
treat it as any section.
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

the reference viewer host's app fills the `sessions` and `commits` slots from
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
- A commit opened alone (`?pane=commit&commit=<sha>`) asks `GET /api/commit`
  for its message body and changed files, only then, and shows them under the
  commit: the totals ("+N −M across K files"), the body as plain text, and the
  files folded, each file's diff read from `GET /api/commit-patch` when it is
  opened and drawn with `CodeDiff`, as the Files pane draws a file (see "Local
  commits"). Loading, a failure with Retry and "No diff to show" are its
  states; the list shows a commit's totals once it has been read.

### Reference viewer host

`internal/viewserver` is the reference host used by package integrations and
viewer tests. The desktop uses its handlers behind the authenticated local
application boundary. The standalone CLI launcher has been removed.
The server has ten API routes and an asset fallback:

| Route | Serves |
| --- | --- |
| `GET /api/document` | the document's bytes exactly as read, except that a hunk without a `github_url` gets the derived one (see "Hunk evidence in saved documents"), with a strong `ETag` of those bytes and `no-cache`: a request whose `If-None-Match` names the current one is a 304 without a body |
| `GET /api/events` | a Server-Sent Events stream, always valid, so the app stays connected; pings keep it open and, with `--watch`, a `document` event follows each change |
| `GET /api/health` | `{"status":"ok"}` |
| `GET /api/sessions` | the sessions on this machine linked to the document's pull request, as below; `no-store` |
| `GET /api/sessions/{id}` | the redacted transcript of one of those sessions, as below; `no-store` |
| `GET /api/patch?path=&version=` | the patch of one file the document, or another saved version of its pull request, cites, as below; `no-store` |
| `GET /api/commits` | the commits of the `/api/sessions` answer, each with its linked sessions; `no-store` |
| `GET /api/commit?oid=` | one of those commits' message body and changed files, as below; `no-store` |
| `GET /api/commit-patch?oid=&path=` | the patch of one file one of those commits changed, as below; `no-store` |
| `GET /api/file?path=` | one file the document cites, as it is at the document's head, as below; `no-store` |
| any other `GET` | the embedded file of that name, or `index.html` for a path that names none, so the app's own URLs work as deep links |

Only the embedded files and the one document are ever served; a request path
is looked up in the embedded file system and never reaches the disk. A request
whose `Host` is not the bound `127.0.0.1:port` or `localhost:port` is refused
with 403, which stops DNS rebinding. Every response carries a `'self'`-only
Content-Security-Policy, `nosniff` and `no-referrer`; there are no CORS
headers, and any method but `GET` and `HEAD` gets 405. An unknown `/api/` path
is 404.

### Hunk evidence in saved documents

A document saved to the local store carries no hunk evidence: `store.Put`
removes every `index.hunks[].diff` and `index.hunks[].github_url`
(`review.StripHunkEvidence`), which together were about 70% of a saved
document's bytes. Everything else is kept byte for byte, in its order: each
hunk's `id`, `path`, `old_path`, `change`, `role`, `new_range`, `additions`
and `deletions`, the sections' hunk assignments and tiles (a tile's code is
the model's, not a hunk's `diff`), `unassigned_hunks` and the stats. Both
fields are optional in `review-1.json`, so the schema is unchanged.

- Every writer of the store goes through it: `generate.Publish` (generation
  jobs), `RefreshCurrent` (background refresh, which keeps the stored
  `index.hunks` as they are) and
  `CompleteCurrent` (a details retry, which splices sections into the stored
  bytes). A replacement of a document stored without evidence is stored
  without it whatever it carries. A version stored with evidence, before
  this, is never rewritten: it is read, served and refreshed with its
  `diff` and `github_url` as stored, and the app shows its inline excerpts.
- What `Publish` returns keeps the evidence as generated, before the store
  removes excerpts from its persisted copy.
- `github_url` is derived where a document is served: `viewserver`
  (the reference viewer host, and each version the daemon's app serves through
  it) and the app's `GET /api/document` give each hunk without one
  `bundle.HunkDiffURL(path, new_range[0])` (`review.DeriveHunkURLs`), the
  value generation records: GitHub's fragment shape
  `diff-<sha256(path)>R<first new line>`. A hunk that has one keeps it, so an
  older document whose hunks all carry theirs is served byte for byte; an
  older one with hunks that lack it (a hosted-style document opened with
  the reference viewer host, say) gets it derived for those hunks, which changes
  nothing the reader sees. The viewer never used the value as a
  link (it is not an absolute URL), and builds the same fragment on the pull
  request's files page itself. One difference: generation hashes the path as
  git gave it, before redaction, so for a path the redactor masked the
  derived fragment hashes the masked path, as the viewer's own link does.
- A document read without the clone gets its patches back. The one such
  reader is a seeded chat session (see "Chat with the review agent"), whose
  `review.json` is `locallink.EmbedPatches` of the stored version: each hunk
  without a `diff` gets the hunk of its file's patch with the same new range
  and the same counts of added and removed lines, read from the clone by the
  document's own repository, head, base and merge base and masked as
  "Local patches" describes (`review.EmbedHunkPatches`), until the patch text
  read reaches the generation bundle's byte cap (`max_bytes`); a file the
  clone cannot provide (missing commits, no remote, binary, over 1 MiB) or a
  failed read leaves its hunks without a `diff`, and only the context ending
  fails the turn. Every hunk also gets its derived `github_url`. A hunk with
  an explicit `"diff": null` counts as one without, and a duplicate key is
  read as `encoding/json` reads it, the last one. The patches are read ahead
  over one `Patcher`, four at a time (read or held unused), in the order the
  hunks use them (`review.HunkPatchPaths`), and used one by one in that
  order, so the hunks that get text within the cap never depend on which read
  finishes first; the clone is opened and the range resolved once, then each
  file costs one `git diff`.
- Nothing else hands a stored document on. The other readers use only
  fields the store keeps: refresh, `PreviousVersionOf` and `StoredOutline`
  (ranges, sections), group reviews (`reviewgroups.MembersFile`: summaries,
  sections and risks), the decision log and feature log (summary text), the
  index projection (summary fields) and validation. No command exports a
  stored document, and nothing uploads one: the hosted service generates and
  stores its own documents (its generation reads its own hunks' `diff`), and
  no hosted path accepts a locally generated document. Group documents live
  in the daemon's group store, not this one, and keep their evidence; their
  patches are not retained (see "Retained review commits"). Staged drafts are
  held in memory while a job runs and never stored.

### Local patches

Most hunks a document cites carry no inline `diff`: the generation budget
leaves most out, and the local store keeps none (see "Hunk evidence in saved
documents"). The app fills them in from `GET /api/patch?path=<path>`, answered by
`locallink.Patch` over the same clone and remote matching as the sessions
(`gitlocal.Open`, any remote naming the document's repository, no fetch).

- Without `version`, or with the served document's own `review.version`, the
  served document is answered. Another positive `version` names a saved
  version of the same pull request, which the local version store reads
  from the store (`store.Get`, `viewserver.Options.Versions`); its document
  must name the same repository, pull request and version, and is kept until
  the served document changes. That version's head, base and merge base and
  its own `index.hunks` are used, never the working tree or a branch. A
  version that is not a plain positive number, does not exist, belongs to
  another pull request, or that a server without saved versions (a document
  file, the sample) is asked for, is 404; one the store fails to read is 500.
  The daemon's app resolves `version` to the stored document itself and
  hands the request on without it.
- The daemon's app keeps one viewer per clone and stored document
  (`internal/uiserver/documents.go`) for `/api/document` and the routes
  above. A request finds it by the document's store revision
  (`store.Revision`, see "Running locally") without reading the document; only
  a revision it has not seen, or a store that cannot vouch for one, reads and
  hashes the document. A viewer is built once, outside the cache's lock:
  concurrent requests for it wait for that build, and other documents do not.
  A viewer unused for 10 minutes is closed, as are the least recently used
  ones beyond 16 viewers or 256 MiB of served documents and kept answers
  (`viewserver.Server.CachedBytes`), checked when a viewer is added and
  whenever a request releases one, since its answers may have grown; the one
  asked for last stays whatever its size. A viewer evicted while requests hold it answers them and closes
  when the last one ends.
- Only a `path` that is the `path` of a hunk in `index.hunks` of that version
  is answered; anything else, including a missing, empty or
  over-long (4096 bytes) `path`, is 404 and runs nothing. A repeated `path`
  parameter is not an error: the first is the one asked for. The document
  records one path per hunk, the file's name at the head. A generated or
  vendored file is answered like any other, masked: the bundle cuts its text
  only when the bundle is over its byte budget, as a size measure, not to hide
  it.
- A local pull request document carries no `index.file_comparisons`; only
  group reviews do, and their patches come from `GET /api/groups/{id}/patch`,
  which picks the member comparison (`internal/reviewgroups`). Every hunk of a
  pull request document is that version's merge base to head, so that range
  is the one read.
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
- The patch is masked by the code that masks a stored hunk's text
  (`bundle.RedactFilePatch`): the same diff parsing (`parseFileHunks`, without
  the line cap, so each line loses a CR before its newline) and the same
  `redactPatches` over the file's hunks as one text, with the one redaction
  configuration (`redact.Config`, which has no per-repository input). The
  guarantee is that every byte masked in a stored hunk is masked in the
  patch; the bundle and `locallink` tests check the two against each other.
  The texts are not always byte-for-byte equal: a stored hunk the line cap
  cut is a prefix of the patch's, redacted with less trailing context (the
  redaction margin), and a type change's stored hunks keep the second
  section's header lines as hunk lines, which the patch drops. A patch diff
  parsing reads as textless has no text patch. The patch is masked with the
  rules of the running binary, not those the document was saved with (see
  the goredact upgrade note at `TestRedactionMarginCoversTheEngine`).
- Size: a patch over 1 MiB (`locallink.MaxPatchBytes`) has no text. The
  stored path's own size reasons (`omitted_size`, `omitted_budget`,
  `trimmed_oversized`) are not recorded in documents, so a smaller patch of a
  file the stored hunk dropped or shortened for size is served, masked.
- Every outcome that is not a patch is `200 {"patch":null}` with nothing about
  why: a binary file or a mode-only change, a
  submodule, a path the range does
  not change (a directory is one), commits or blobs the clone lacks (including a partial
  clone), a directory that is not the repository's clone, a patch over 1 MiB
  (`locallink.MaxPatchBytes`), the sample document. A failure that may pass (git
  failing, the one-minute limit) is 500, and a server that is stopping is 503;
  the app turns both into a rejected request and tries again when the excerpt is
  next opened.
- Answers that hold until the document changes (a patch, no text patch, unchanged,
  too large) are kept by the commits and path they were read for (the merge
  base, else the base; the head; the path), so versions that compare the same
  commits share them; those a person can fix (no clone, no remote,
  missing commits) and failures are computed again on the next request. A
  document reload (`--watch`) drops every answer, the set of cited paths and
  the saved versions read. At most four git reads run at once, each bounded by
  the server's lifetime and one minute. `locallink.Patcher` also shares a read
  that is running among requests for the same clone, repository, range and
  path, so the daemon's app, which serves each saved version from its own
  server over one `Patcher`, reads a file once for versions with the same
  commits; it keeps the changed-file lists of the last eight ranges.
- A `Patcher` also keeps, for the last eight clones and ranges it read
  (directory, repository, merge base or base, head), the opened clone (its
  private repository), the remote match and the resolved commits, until 30
  seconds after the range's last read (`rangeIdle`), 5 minutes after it was
  opened however busy (`rangeMaxAge`; the next read opens it again) or
  `Close`, so a further
  file of a known range costs one `git diff` instead of about seven git
  processes (opening, remotes, two commit lookups, the patch). Only a
  resolution that succeeded is kept: no clone, no matching remote or missing
  commits are resolved again on the next request. A read that finds objects
  missing, a partial clone or git failing drops the kept range, so the next
  request opens it again; a read cancelled by its own request, or an answer
  about its path alone, does not. A remote edited meanwhile is seen once the
  range has idled out or reached its maximum age. An evicted, aged, idled or
  closed range is closed when its last read ends. `locallink.Patch`,
  `EmbedPatches` and the reference viewer host close theirs, and the daemon closes
  the app's document cache and the group API (`reviewgroups.API.Close`), and
  so their `Patcher`s, when it stops; only a daemon killed within a range's
  idle time leaves its private repository behind.

### Retained review commits

A saved version's lazy patches are read from its head, base and merge base by
id, so those commits must stay in the clone after a force push, a deleted
branch or the latest head moving `refs/baum/pull/<N>/` on, and a `git gc`
(the user's or automatic) would otherwise prune them. `internal/reviewrefs`
keeps them reachable with refs that live exactly as long as their versions:

- Layout, in the clone's common git directory (shared by its linked
  worktrees): `refs/baum/reviews/<repository>/<pr>/<version>/<role>`.
  `<repository>` is the store key's lowercased repository (`owner/name`, or
  `host/owner/name` for Enterprise) as one component, every byte but `a-z`,
  `0-9`, `-` and `_` written `%XX` (`acme%2Fwidgets`), so one clone holding
  the reviews of several repositories never mixes them; `<role>` is `head`,
  `base` or `merge-base`, one ref per commit the metadata records
  (`gitlocal.RetainPrefix`, `gitlocal.ParseRetainRef`). Nothing outside
  `refs/baum/reviews/` is created, moved or deleted, and only names that
  layout writes exactly are ever deleted; `refs/baum/pull/`, `refs/baum/fix/`
  and `refs/baum/default` keep their meaning.
- Git runs as for the private repository's reads (no caller `GIT_`
  variable, no system or global configuration, an empty home, no replace
  refs, `GIT_NO_LAZY_FETCH=1`) with `GIT_DIR` the clone's common directory,
  whose own configuration git still reads, and with `core.hooksPath` the
  private repository's empty hooks directory (so no `reference-transaction`
  hook runs), `core.logAllRefUpdates=false` (no reflog), `core.fsmonitor`,
  `gc.auto` and `maintenance.auto` off. Writes are one `git update-ref
  --no-deref --stdin` transaction with old-value checks: `create` for a new
  ref, `update <new> <old>` to move one, `delete <old>`. `--no-deref` acts on
  the ref itself, never on a ref a symbolic ref names; and since baum never
  writes symbolic refs, one found at a retained name (`for-each-ref`'s
  `%(symref)`) is foreign: it is never moved or deleted, nor taken for a
  version's ref, and `Keep` reports it and the daemon logs it. Deleting a
  packed ref rewrites `packed-refs` under its lock, so a ref command the user
  runs during a cleanup (bounded, at most daily) may briefly wait on git's
  lock timeout. Commits are looked up with
  `cat-file --batch-check` in the private repository; a commit the clone
  lacks (pruned already, a partial clone) is skipped, never fetched, and the
  lazy path keeps falling back to the GitHub link for it.
- Written on publish: once `generate.Publish` has stored a version
  (`Options.Stored`), `reviewrefs.Keep` writes that version's refs, from
  the daemon's generation jobs (a single
  pull request; a group's combined review lives in the daemon's state, and
  a details retry rewrites a version in place with the same commits). It
  runs outside the store's transaction: a failure is a warning on the
  command's progress or a daemon log line, and never fails or undoes the
  version. A write another writer beat (the daemon's reconciliation) is no
  failure when the refs it left are the version's. A version saved to an
  explicit store-root option outside the clone's own store roots (`store.Roots`)
  gets no refs, since nothing would delete them with it.
- Reconciled by the daemon: the background worker (see "Free background
  synchronization") calls `reviewrefs.Reconcile` for each available clone of
  a picked repository, once per common git directory, on its first pass,
  then once a day, an hour after a failure, and on the next pass while a
  bound left work; a 30-second pass otherwise runs no git for it. A pass
  lists the retained refs, then reads every version's commits from each of
  the clone's own store roots (`store.Store.Commits`, metadata only), creates
  the refs versions saved before this existed (or whose write failed) lack,
  for commits the clone holds, and deletes the refs of versions no store
  holds (a store reset or removed, the clone pointed at another store root).
  Refs to write go newest version first, so commits old versions lost do not
  hold up a new one's; commits are looked up 3000 at a time, at most 12000
  per pass, and a pass writes and deletes at most 600 refs each
  (`reviewrefs.DefaultLimits`). A bound that left refs asks for the next
  pass, except a lookup bound reached with nothing written, which the next
  pass would only reach again. Listing the refs before reading the stores
  means a version published meanwhile, whose refs are written only after it
  is stored, never loses them. When a transaction fails (a write another
  writer beat, a name clashing with another ref), the pass is listed, read
  and planned once more, then applied in halves down to single refs, so one
  ref that cannot change holds up no other; those are logged.
- The roots read are those of the daemon's environment (`XDG_DATA_HOME`, else
  the home directory, for the fallback root). A separate engine consumer running
  with another `XDG_DATA_HOME`, whose clone is read-only so its store went to
  the fallback root, writes refs the daemon then finds in no store it reads
  and deletes, and the daemon's own fallback store is not that run's; the
  clone-local root is the same for both.
- Failures delete nothing: a store that exists but cannot be read fails the
  pass before any ref changes, and when no store root holds a database at all
  nothing is deleted, since a missing store cannot be told from one that
  moved. A clone that is unavailable or cannot be opened is skipped with a log
  line; nothing reaches the app.
- Group reviews are not covered (a known gap): `GET /api/groups/{id}/patch`
  reads each member's head and its comparison's `base_revision` from the
  group document, and nothing retains those commits, so a gc after a force
  push can leave a group's patches unavailable (`{"patch": null}`).

### Local commits

A commit opened alone in the Commits pane shows its message body, its changed
files and, file by file, their diffs. `GET /api/commit?oid=<full id>` answers
`locallink.ReadCommit` and `GET /api/commit-patch?oid=<full id>&path=<path>`
answers `locallink.CommitPatch`, over the same clone and remote matching as
the patches (`viewserver.Options.Commit` and `CommitPatch`).

- Only an `oid` that is the `oid` of a commit in the current `/api/sessions`
  answer (what `/api/commits` lists) is answered, compared exactly; anything
  else, including an abbreviated id, a ref name, a revision expression or an
  unavailable sessions answer, is 404 and runs nothing. A request never names
  a revision of its own.
- `/api/commit` is `200 {"oid","body","files":[{"path","old_path"?,"status",
  "additions"?,"deletions"?}]}`. `body` is the message after its subject line
  and the blank lines after it. The files are the commit's change against its
  first parent (a merge commit too), or the empty tree for a root commit
  (`gitlocal.Repo.CommitChanges`: `git log -1 --format=%P%x1f%B` bounded to
  1 MiB, then `git diff-tree -r -z -M -l1000 --raw` and `--numstat`, bounded
  like the comparison). `status` is `added`, `modified`, `deleted` or
  `renamed` (a type change is `modified`); `old_path` is a renamed file's old
  name; `additions` and `deletions` are left out for a binary file. A commit
  this machine cannot read (no clone, no matching remote, the commit missing,
  a partial clone, too large) is `200` with `"files": null` and a `code`.
- `/api/commit-patch` answers only for a `path` that is one of that commit's
  files, by its path or a renamed file's old path, else 404. The patch is
  `git diff-tree -p` of the commit against its first parent (`--root` for a
  root commit) with the flags and `:(literal)` pathspecs of `/api/patch`, the
  old path included for a rename (`gitlocal.Repo.CommitFilePatch`), cut to the
  file's diff from its first `@@` line and masked as the stored hunks are.
  The answer is `/api/patch`'s:
  `{"patch":"..."}`, or `{"patch":null}` for a binary file, a submodule, a
  rename without an edit or a patch over 1 MiB.
- Both share `/api/patch`'s bounds: at most four git reads at once (one pool),
  the one-minute limit, 500 for a failure and 503 while stopping. A commit's
  answer and each file's patch are kept per document while they hold (the
  commit read, or too large); the rest are read again. A document reload
  drops them.
- The app's `/api/commit`, `/api/commit-patch`, `/api/facts/commit` and
  `/api/facts/commit-patch` route to the same handlers through the per-document
  viewer cache (see "Pull request facts").

### Local files

A Summary context chip for a file the pull request does not change opens the
file in the app (see the app README). `GET /api/file?path=<path>` answers
`locallink.ReadFile` over the same clone and remote matching as the patches
(`viewserver.Options.File`).

- Only a `path` the current document cites is answered (`citedFiles`): a
  context reference (`summary.context.<part>.refs[].path`), a hunk's `path` or
  `old_path`, or the `path` of an anchor of type `file` anywhere in the
  document. Anything else, including a missing, empty or over-long path, is
  404 and runs nothing.
- The file is read at the document's head (`review.head`), never the working
  tree: `git rev-parse` resolves the head, then `gitlocal.Repo.ReadBlob`
  (`git ls-tree -z -l <head> -- :(literal)<path>` and `git cat-file blob`, the
  path checked by `validGitPath`, the blob capped at 10 MiB).
- The answer is `200 {"path","content","truncated"}`. `content` is at most
  512 KiB of the file's text with invalid UTF-8 dropped, and `truncated` says
  the file goes on. It is `null` for a directory, a submodule, a path the head
  lacks, a binary file (a NUL byte), a blob over the cap, a clone that cannot
  answer, and the built-in sample.
- It shares `/api/patch`'s bounds: the four-read pool, the one-minute limit,
  500 for a failure and 503 while stopping. A stable answer (the text, or no
  text at the head) is kept per document; a document reload drops it.
- The app routes `/api/file` to the same handler through the per-document
  viewer cache (`internal/uiserver/api.go`).
- A group review's files come from `GET /api/groups/{id}/file?version=&path=`
  (`internal/reviewgroups/http.go`), with the same answer and the same guard
  (`locallink.CitedFiles` over the group's document), read at the review's
  head, or its top member's for a stack whose document names none, from a
  registered clone; it shares the group's patch reads' pool and a 20-second
  limit.

### Local session linkage

`internal/locallink` links the sessions on this machine to a pull request's
commits using only this machine; the reference viewer host serves it, and the session
evidence that `internal/assemble` collects reuses its loading and matching (see
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
   Sessions the agent's own store says began more than `SessionWindow` after
   the last commit's date (`sessions.Options.StartedBefore`) are not loaded
   either. Such a session cannot have created a commit, and it ends too late
   to fall within the matcher's tolerance of one, so an old pull request does
   not read every session written since. Claude Code's start is the earliest
   timestamp among its first records, and Codex's is its `session_meta`
   timestamp. Cursor sessions and Entire checkpoints are not bounded this way.
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
   checkpoint in the window not read before (metadata is remembered by the
   checkpoint's tree) and another the metadata of their sessions, and
   only the checkpoints holding the newest snapshot of a session no
   home-directory store has are read whole. A newest snapshot that cannot be
   mapped, or that carries no transcript (Entire sometimes writes a checkpoint
   with only metadata and the prompt), gives way to the next newest; a
   snapshot is not necessarily fuller than the one before it. Sessions and those checkpoints are mapped up to eight at
   a time (two in a background pass, `sessions.WithBackground`), and the
   transcript bytes decoded at once are bounded by a 256 MiB budget
   (`sessions.DefaultDecodeBudgetBytes`; a larger session runs alone). It then
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
   shard prefix, and the parser reads them either way. A commit's
   `checkpoint_sessions` records the union of candidate session IDs. Checkpoint
   membership is not contribution evidence and never creates a confidence-1
   link. The checkpoint is read whatever `Sessions.Sources` asks for. Missing
   local transcripts are mapped from it when possible; membership survives an
   unreadable transcript. A native session under the same ID is preferred.
   Trailers with no valid ID or missing checkpoints leave candidate discovery
   to the other stores and matching to the independent
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
7. Matches sessions using the shared evidence steps, with local policy:
   checkpoint association never bypasses validation. Every session recording
   successful creation of the commit is linked by `commit_ref`, regardless of
   checkpoint membership. Remaining sessions must explain changed files and
   pass the normal timing and agent filters. Sharing the starting parent SHA
   is insufficient and the local resolver omits `start_commit`. Exact file-score
   ties remain unresolved instead of crediting every concurrent session or
   choosing one by ID. Matching continues until paths are explained, nothing
   matches, or the file-link cap (three) is reached. Once a link is at or above
   `linkage.ReviewConfidence` (0.7), weaker links are dropped. A session arriving
   more than once is matched once from its fullest, then newest, reading.

The result is redacted with the engine and the row summary that the local session reader
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
`method` is `commit_ref` (creation evidence) or `files_overlap` (inferred contribution), and `source` is
the store (`claude_code`, `codex`, `cursor` or `entire`), which differs from
`vendor` for a session an Entire checkpoint recorded. A pull request none of
whose commits match still answers `available` with `"sessions": []`. A subject
or author is at most 200 characters. A commit may also carry
`checkpoint_sessions`, the IDs associated with its checkpoints. Those IDs
alone never add a linked session or make its transcript available.

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
machine; `internal/assemble` calls it. It
takes the pull request's commits as the clone holds them (`gitlocal.Commits`
from the merge base to the head), a directory in the clone, the session store
overrides with `PreviousPaths` for directories the repository used to live
at, and the bundle caps `MaxPromptsPerSession` and
`MaxCommandEventsPerSession`. It loads and matches sessions exactly as steps 4
to 7 of "Local session linkage" do. Loading leaves out baum's own generation
runs, for the reference viewer host too: a session whose top-level session has a prompt
starting with `sessions.GenerationRunMarker` is neither linked nor allowed to
take a link from another session, and is dropped from an Entire checkpoint's
candidate associations. Each session linked to at least one commit becomes one bundle
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

The daemon refreshes every saved review's evidence on a timer. Rebuilding
evidence is the expensive part: it decodes every transcript in the window. So
a process reuses what it built:

* **Unchanged inputs.** `BuildSessionEvidence` first runs `sessions.Discover`,
  which reads directories, file metadata and the Entire refs but no
  transcript. It reuses its last answer while all of these are unchanged, for
  at most `locallink.EvidenceMaxAge` (10 minutes):
  * the request;
  * `Discovery.Fingerprint`: each session file's path, size and modification
    time, the worktrees, the Codex thread index and the Entire refs;
  * what each checkout has checked out: `HEAD` and the branch's commit. Index
    rewrites from `git status` are ignored.
* **Unchanged sessions.** When any of that changed, which in a busy clone is
  nearly always, because some agent is writing a session in the window, only
  the sessions that changed are mapped. The process keeps a digest of each
  session it mapped (`locallink.sessionDigests`, up to
  `locallink.DefaultDigestBytes`, 64 MiB), keyed by `sessions.Session.Key`:
  each file's path, size and modification time, or for an Entire session the
  clone and the tree of its checkpoint, which names its content. A session
  with a file modified in the last 2 seconds, or in the future, gets no key:
  a same-size rewrite within one modification-time tick would look
  unchanged. Such a session is mapped and not reused until it is older.
  `Discovery.LoadReusing` skips a session whose digest is held. The digest holds what linking reads
  from the graph: whether it is a generation run, whether it has a
  transcript, the top-level session's start, end and branch, the
  `sessioncorrelate` evidence, and the session's Git-writing shell calls
  (when each ran, the directories of its commit-creating git commands and its
  branch), which the reflog matcher places in the clone's working trees, so a
  reused session still competes for the commits its calls may have made. For
  each linked session it also holds the
  evidence that was built for it, with what that evidence depended on
  besides the transcript:
  * the caps;
  * the session's id, path and vendor, and for Codex the thread names it
    looked up in the thread index;
  * where each absolute path it touched lands among the clone's checkouts.
    These are replayed against the current checkouts, so an unrelated new
    worktree changes nothing;
  * the answers about which paths its commands name exist in the checkout or
    the pull request.

  Each of these is checked again, so the evidence is the same as a full
  rebuild. A linked session whose evidence no longer fits is mapped by
  loading again, refusing its digest. The other sessions are still reused.
  If an unmapped session's digest is gone by the time linking reads it, the
  build loads once more and maps everything. Choosing each Entire session's
  snapshot uses small per-checkpoint facts that `sessions` remembers by
  clone and tree. A checkpoint is read whole again, through the checkpoint
  cache below, only when a session chosen from it has to be mapped.
* **New evidence contexts.** A digest may retain a context-independent slim
  evidence basis, capped at 4 MiB per session within the existing 64 MiB
  digest budget. It keeps only the evidence projector's inputs, with bounded
  prompt/reply text and without tool output, assistant content or patch
  hunks. New caps, known paths, checkout placements and thread names run the
  same projector against this basis. Optional bases are shed before compact
  facts; admission may replace colder optional bases but cannot evict facts.
  An absent or oversized basis preserves the full-read fallback and can be
  refilled by that mapped read. Every retained byte/string is detached from
  the graph; byte accounting includes the whole basis.
* **Linked-session views.** `locallink.Resolve` uses compact facts to select
  the linked sessions, then maps only those whose graphs it must return. The
  local app uses `ResolveSummary` for lists and decision attribution: its
  compact facts include titles and outcomes, so even a linked graph evicted
  from the mapping cache is not decoded merely to list its metadata. The
  summary returns the same JSON and a lazy graph reader bound to the pinned
  request. Opening a transcript rechecks that it is still linked and reads its
  graph then, under the daemon's local-read semaphore and the viewer's shared
  transcript job, lifetime and timeout. Default `Resolve` still returns full
  linked graphs. Missing facts and changed selection retain the full-read
  fallback.
* **Feature linkage.** `locallink.LinkCommits` shares those compact facts and
  skips unchanged session graphs too. The feature worker caches successful
  recorded commit ranges by clone, base, head and shallow boundary, bounded
  at 128 ranges and 16 MiB of estimated metadata. Full daily relinks bypass
  range reuse, but keep their input fingerprint so a following unchanged
  window can skip linking immediately.
* **Closed and merged pull requests.** A refresh of a pull request that is
  closed or merged sets `EvidenceRequest.Frozen`. Its sessions are read once in
  the process, then kept for the same commits without looking at the stores
  again.
* **One background pass.** A pass that refreshes several pull requests holds
  `sessions.HoldMappings`, so the mapping cache keeps up to
  `sessions.HeldMapCacheBytes` (256 MiB) of mapped graphs while the pass runs
  and each session is decoded once in it. When the pass ends, the cache
  shrinks back to its 64 MiB limit. Both limits count the graphs' estimated
  memory, about a sixth of their transcripts' size.
* **Growing transcripts.** A session an agent is still running changes on
  every append, so none of the above applies to it. For each Claude Code
  transcript and Codex rollout, regardless of age, `sessions.Map` may keep
  the decoded lines and the byte offset they end at, and decodes only what was
  appended since; the mapper still runs over every line. A file that shrank,
  was replaced or had its earlier bytes rewritten is decoded whole. The lines
  kept leave out the raw record copies the mapper does not read, and are
  bounded by `sessions.DefaultTailCacheBytes` (64 MiB of estimated memory). The
  [sessions README](sessions/README.md#incremental-decoding) has the details.
* **Entire checkpoints.** A refresh reads the clone through one `gitlocal`
  repository: the caller's (`EvidenceRequest.Repo`, which the daemon's
  refresh and review jobs set), or one it opens, shared by session loading,
  the checkpoint trailers' lookups and the observed-commit lookup. The Entire
  refs are resolved once, by `sessions.Discover`. Checkpoint listings, dates
  and metadata are remembered per clone by the commits and trees that name
  them (`entire.Listing`), so a refresh whose checkpoint branch and refs did
  not move runs no git for them, and a branch that moved forward is dated
  from its new commits alone. Checkpoints read whole are kept, as the
  sessions they map to, in a cache keyed by their tree and bounded at 64 MiB
  of checkpoint bytes (`sessions.DefaultCheckpointCacheBytes`). See the
  [sessions README](sessions/README.md#entire-checkpoint-reuse).

Callers get deep copies of reused evidence, because they cap and redact it in
place. A reused answer prints no warnings.

### Session transcripts

`GET /api/sessions/{id}` serves the transcript of a session the current
`/api/sessions` answer lists, and only those: it reads that answer from the
same cache, computing it through the same single-flight path when it is not
there yet, and any other id, a session read but not linked, or an unavailable
answer is 404. A full linkage answer may keep its linked graphs
(`Result.Graphs`, never part of its JSON). The daemon's metadata-only answer
instead supplies a lazy reader: an explicit transcript request revalidates
linkage to the pinned document commits, under the same local-read semaphore,
and shares one graph read and render for that session. A no-longer-linked or
unreadable graph is not served. The lazy read has the viewer's lifetime and a
two-minute bound; failed builds are retried on the next request.

The transcript is shaped and bounded first, from the graph as read, and
redacted after, so nothing past the limits below is ever redacted or held
(`transcript.Renderer`, which the live sessions' transcripts use too):

1. `internal/transcript` builds it (below). Each text it keeps (a prompt, a
   reply, an agent message, a tool call's input or output) is read up to 8 KiB past its
   limit and passed through the redaction engine before it is cut, so a
   secret that the cut runs through is still recognised and masked.
2. The encoded transcript is redacted as a whole with the engine
   `locallink.RedactGraph` uses (`redact.Engine.RedactJSON`): secrets and e-mail addresses are
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
wrapper constructs (and the lead of a message the desktop app sent,
`prompttext.AppMessageLead`) are stripped, and not one the harness wrote under
the human's name: Claude Code's `isMeta` lines without an origin (such as the
line saying where a pasted image was saved) and another agent's
`<agent-message>`, such as a subagent's report, whose replies and activity
count toward the prompt before it. A system input, such as a subagent's report
Claude Code marks `promptSource: "system"` or a task notification, likewise
hands its replies and activity to the prompt before it, since the agent's
answer to it is that prompt's turn going on; a nested session's activity counts
toward the prompt whose tool call started it; activity under any other input is the
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

## Desktop CLI and local hooks

`cmd/baum` builds the desktop runtime. `internal/cli` registers daemon control,
`desktop-setup` and local hooks, plus hidden clone registration, review
preferences, GitHub credential and diagnostics helpers. It exposes no hosted
client commands or standalone review-generation commands.

`internal/hookcli` implements native plugin management and local callbacks.
`internal/agentplugins` owns bounded native CLI operations and trust/status
queries; `internal/agenthookdefs` defines baum's local plugin vocabulary and
stable dispatcher strings. A plugin check requires a Git worktree and a daemon
that has run. It reads no credentials or third-party hook configuration.
Independent Entire hooks do not suppress baum events. Callbacks never install
repository hooks or invoke Entire. The appliance still imports the separate
legacy `hosted/agenthooks`, `hosted/traceupload` and `hosted/apiclient` packages.

See [local desktop hooks](internal/hookcli/README.md) and
[native plugins](internal/agentplugins/README.md).

## Running locally

A desktop generation job uses `internal/generate` to:

1. resolves the pull request with the user's GitHub token;
2. exports the head and merge-base trees with `git archive` from a private
   repository that borrows the clone's objects, and writes the checkout under the
   same limits as above;
3. collects sessions from the home directory and Entire checkpoints and
   links them to the pull request's commits;
4. reads the pull request's rendered body with the user's token, which carries
   image URLs GitHub has already signed, and downloads the description images
   from them without credentials; `bundle` caches nothing, and writes the
   images only to the attachments archive `generate` reads. After session
   linkage and count caps, native Claude Code and Codex transcripts also supply
   retained inline pasted and tool-result images through `sessions/sessionimages`.
   Only readable linked roots and reachable subagents contribute; Entire
   checkpoints and baum generation sessions supply no images. Native image
   discovery follows no image paths or URLs from transcript text;
5. loads the previous local version, if any, and builds the bundle, with the
   previous version's hunk anchors refitted to the diff
   (`review.FitPreviousHunkAnchors`, as the hosted builder does);
6. runs the configured backend with the public staged prompt, in stages (see
   "Staged review"): the outline, then the sections' details and the
   context; tiles are off unless enabled;
7. validates, derives and redacts the document, and stores it as the next
   version, with any section whose details failed `failed` and no context
   when the context stage failed.

The shared engine retains a one-pass mode for package consumers and tests;
the desktop uses staged generation. Desktop jobs open `internal/store`, load
the previous local version and pass `review.PreviousVersionOf` into
`assemble.Request.Previous`. They select and check the backend before
assembling with `generate.SelectDesktop`: only `auto`, `claude` and `codex`
run. Automatic selection tries Claude Code then Codex and never an API-key
backend. A configured `cursor-agent` or `anthropic` job fails with
`generate.ErrBackendNotOffered`; it never silently switches backend.

Assembly writes checkout and attachments to temporary archives, then unpacks
them into a private run directory under the bundle's caps. Local execution
uses `claude.User` or `codex.Local`, never `Isolated` or `Container`. The
review timeout bounds the run and the pass timeout bounds each backend call;
cancellation stops the backend process group and cleans up temporary files.

A review the daemon runs with the local `claude` or `codex` backend keeps its
agent session and run directory (`generate.Options.KeepSession`, set by
`jobs.Local` for single pull requests only: not for review groups, Cursor or the
Anthropic API backend, and not for hosted runs), so a later chat can fork the
session in the same directory. The run directory is then made under
`<user cache>/baum/review-runs/` (`internal/reviewruns`, the same cache base as
fixes), mode 0700, still named `agent.RunDirPrefix+"*"`, with symlinks resolved
because the CLIs record the resolved cwd. The loop asks every call to persist
its session. Each call, a repair included, is a fresh session that carries the
whole context pack (a repair resends the bundle and the rejected document), so
`Outcome.SessionID`, the session of the last successful call, is the one that
produced the accepted document and is self-contained. In a staged review every
stage's calls are sessions of their own, all in `Outcome.Sessions`; the one
kept is the details stage's first pass, which wrote most of the document and
read the code for it, or the outline's when no section was sent to the details
stage (`StagedOutcome.SessionID`). A run that succeeds with a
session id records `store.Metadata.Session` (`backend`, `id`, `directory`,
absent in older versions) and keeps the directory, after deleting the
`rejected-*.json` files of the repairs, every stage's included (raw model
output the chat does not need); a failed, rejected or cancelled run, a run without a session id, and a
version the store refused remove it as before. A cache directory that cannot be
created is a warning on the progress output, and the review then runs in a
temporary directory without persisting anything.

Persisting makes the CLI write transcripts that were never written before: every
call (the first attempt, provider retries, each repair) leaves a Claude Code
session in `$CLAUDE_CONFIG_DIR` (else `~/.claude`) `/projects/<encoded run
dir>/<id>.jsonl`, or a Codex rollout in `$CODEX_HOME` (else `~/.codex`)
`/sessions/YYYY/MM/DD/rollout-*-<id>.jsonl` whose `session_meta` records the run
directory as cwd. `Outcome.Sessions` lists every id the calls reported.
`reviewruns.RemoveSessions` deletes them: after a successful run, those of the
calls that did not produce the accepted document (in a staged review, every
session but the kept one: the outline's, the context's and the repairs');
on any failure, rejection,
missing id, refused store or interruption, all of them (Claude's whole project
directory, which is unique to the run, and the Codex rollouts whose first line
records that cwd); and when a kept directory is pruned, every session of it,
chat forks made later included. It acts only on a directory directly under the
review-runs root with the run prefix, deletes only a real Claude project
directory of that encoded name and `rollout-*.jsonl` regular files whose cwd is
that directory, never follows a symlink and ignores files that are gone.
Nothing scans the Codex store for a run that recorded its sessions. Each run
directory has a record next to it in the root, `.sessions-<run dir name>`
(`reviewruns.Create` writes it with the start time; `generate` adds the ids of
`Outcome.Sessions` and `chat` adds the ids of its forks, `reviewruns.Record`).
Removing the directory deletes the recorded Codex rollouts by file name in the
dated folders from two days before the start, newest first, checking each
one's cwd before deleting it, and then deletes the record. The record is
marked complete only when `generate` saw no errored attempt (an errored call
may leave a session without reporting its id). A chat call marks it pending
until its session ids are recorded (every id its attempts reported, retried
ones included), lost if it ends without a session id, and incomplete after any
errored attempt. A directory
with no record (made before records existed) is scanned from any date; one
whose record is incomplete, lost or pending is scanned from its start date.
The scan also reads the first line of Codex rollouts, newest first, at most
50000 (past the cap only files named by a recorded id are still matched), and
deletes those whose cwd is the run directory. A directory that is already gone
with its record is skipped by `PruneVersions`, which lists a directory shared
by several versions once; `Expire` also deletes the sessions of a record whose
directory is gone, and stale `.sessions-*tmp-*` files.
The directory itself is pruned the same way and only real directories directly
under the root whose names carry the prefix: after a successful publish the directories of a pull request's versions
beyond the latest two (`reviewruns.PruneVersions`), and, when the daemon starts
and then daily, any directory last modified more than 14 days ago
(`reviewruns.Expire`). The directory of a review still running is neither
referenced nor old, so neither pass touches it. The session collector never
ingests these sessions: Claude Code, Codex and Cursor discovery decide by the
cwd the transcript records, which is a run directory (prefix, outside every
repository root), not by the transcript's path; review runs load no hooks
(`--setting-sources ""`, `--disable hooks`), so nothing in `livesessions` or
`hookevents` sees them.

#### Chat with the review agent

`internal/chat` answers a reviewer's questions about a pull request with the
agent that wrote a saved review version, desktop and daemon only. A
conversation belongs to one repository, pull request and review version and
keeps one agent session; it uses the review's harness and model
(`Metadata.Backend` and `Metadata.Model`), so only a review by the local
`claude` (User profile) or `codex` (Local profile) backend can be asked about.
Cursor, the Anthropic API backend and hosted reviews are refused
(`unsupported_backend`) before anything is queued.

Sessions. The first turn forks the review's kept session (`ResumeSession` the
review's id, `ForkSession`, `PersistSession`) in the review's run directory,
so the agent starts with everything it read and the review's session stays
untouched; the fork's new id becomes the conversation's session. For a review
written in stages that is the details stage's session, the one that read the
code for the sections, or the outline's when the details stage sent nothing;
the other stages' sessions were deleted when the version was stored, and a
details retry keeps none, so the conversation forks the session of the run
that wrote the version. Every later
turn resumes the conversation's own session in the same directory, without
forking. When the review kept no session, it is another harness's, or its run
directory is gone, the turn seeds a new run directory under the review-runs
root (`reviewruns.Create`): the checkout of the review's head commit exported
from the registered clone (`head/` only, within the review's checkout limits;
a clone that lacks the commit fails the turn with `needs_clone`), the stored,
already-masked review as `review.json`, with its hunks' masked patch text read
back from the clone (`locallink.EmbedPatches`, see "Hunk evidence in saved
documents"), and the earlier answered turns as
`conversation.json` (question, masked here, and `answer_markdown`, oldest
first, the newest that fit in 64 KiB), and starts a new persisted session there. The
conversation is then marked `seeded` and the turn's `note` says it started
without the review's own context. A resume or fork whose session the CLI
cannot find falls back the same way, once per turn: `agent.Generator.Continue`
(the loop's provider retries, stall watchdog and budgets for one call of a
continuing session) returns `agent.ErrSessionNotFound` at once, without
retries. The backends report it themselves for a resume or fork: Claude Code
ends with a failed `result` event (no turns) whose `errors` say "No
conversation found with session ID" (on a fork its `session_id` is a fresh,
empty one, which is dropped), and Codex exits with a line of stderr, any line
after warnings, naming the thread or rollout it could not find ("no rollout
found for thread id"). The loop also recognises such text in an error of a
resumed call that made no model call. A seeded directory for which no session
was recorded is removed. Session ids are kept only when they are UUIDs: a
stored one that is not valid counts as gone and seeds a new session. When the
agent reports no session for an answer, the answer stands, the turn's `note`
says the next question starts without this conversation's context, and the
next turn seeds a session with the earlier turns rather than forking the
review again. Before a directory is
used `reviewruns.Use` checks it is a real run directory directly under the
root and sets its modification time to now, so the 14-day expiry never removes
a directory a chat used recently. `PruneVersions` still removes the
directories of versions beyond a pull request's latest two, which a forked
conversation shares; a chat on such an older version falls back to a seeded
session on its next turn. A seeded directory belongs to no version, so only the
expiry removes it, with its CLI sessions.

Turns. Every turn sends the public chat instructions
(`agent/prompt/chat-public-v1.md`, `prompt.Chat`) as the user prompt, because
a resumed or forked session keeps the system prompt it was recorded with: the
review is finished, answer the question concisely, read code as needed, cite
files and lines of the head, set `suggests_fix` only for a concrete change.
Requests are marked as tasks (`Request.Task`), which only changes the system
prompt of a new Claude Code session (a seeded one). The question and the focus
(the section, stop or overview it is about, with its title and anchors) follow
as untrusted data between `BEGIN/END UNTRUSTED DATA` markers that carry a
random nonce, masked for secrets, and the prompt starts with
`sessions.GenerationRunMarker` like a review's. The reply must match the
answer schema (`chat.Schema`: `answer_markdown` up to 20000 bytes, at most 20
`citations` of `{path, start_line, end_line}`, `suggests_fix` an object with a
`summary` or null; strict-mode friendly for Codex). Each citation is checked
against the run directory's `head/`: a clean, relative path that stays inside
the checkout after symlinks are resolved, an existing regular file, and
`1 <= start_line <= end_line <=` its line count. A reply the schema rejects, or
with citations that fail, gets one repair that resumes the same session (never
a fresh call, which would lose what the agent read); citations still wrong
afterwards are dropped, so a stored answer never carries a broken one; when
only citations were wrong and the repair breaks the schema, the first answer
stands without them. A path that names the file only on a case-insensitive
file system is stored as the checkout spells it. The
answer is masked with `agent.RedactTaskOutput` before it is checked further,
stored or returned, and the log carries sizes and counts only. The question
itself is the reviewer's own text: it is stored and served as typed, and
masked only where it goes to an agent (the turn's prompt and a seeded
`conversation.json`). A turn's time limit is `run_timeout`; its call and
token budgets are half of a review's when the config file sets them, none
otherwise. Each backend call can contain the agent's full tool loop, so its
pass deadline is at least the turn's `run_timeout`; the run context enforces
the time limit chosen in Settings.

Storage and jobs. Conversations and turns live in the clone's review store
(`versions.sqlite`, tables `conversations` and `turns`, keyed by the lowercased
repository and pull request like versions); the session id and directory never
leave the daemon. A question is a `chat` job (`jobs.KindChat`), queued at once
without an estimate (`Queue.Ask`), whose turn is stored in the same step: a
conversation with a turn still queued or running refuses another with `busy`,
checked and inserted in one SQLite transaction. Chat jobs run in their own lane
of two workers beside the review workers, so a question never waits behind a
review, and at most one per conversation. The job keeps the agent's transcript
like a fix (`has_transcript`), reports phase, calls, tokens and cost
(`pricing.BackendAccountingCost`), and fails with the generation reasons
(`budget`, `backend`, `validation`, `interrupted`, `internal`) or
`needs_clone`. A failed, cancelled or interrupted job settles its turn the same
way, with the job's cost even when a next question settled the turn from
its ended job first; a turn whose job is gone (a daemon that stopped while it
ran) reads as interrupted and does not keep the conversation busy. A `chat` server-sent
event (`{conversation, repository, pr, turn, state}`, never the answer) goes
out when a question is queued and when it settles.

Completion mirrors the hosted worker's `Generate` and `Complete`: after the loop,
`agent.RedactOutput` masks the document, findings, model and diagnostics (with
the unpacked tree's paths as the known checkout paths) and
`agent.RejectSecrets` rejects a run that masked a secret in a closed value; then
`DocumentValidator.CompleteWithPrompt` derives the host-owned fields and records
the model and prompt version, and `ValidateStored` checks the result strictly
against the bundle. A staged run masks with `agent.RedactStagedOutput` instead,
which masks every stage's outcome too, then rejects secrets the same way, marks
any section still pending as failed (`review.FailPendingDetails`) and completes
with `CompleteStaged`; the version records the staged prompt's version and the
digest of its texts as sent, whose shared part, which starts every stage's
prompt, starts with `sessions.GenerationRunMarker`. The progress output names
each stage's passes ("details, pass 2: the validator found problems") and, at
the end, how many sections lack their details and whether the context is
missing. Failures are `*generate.Error` values of a category (input,
backend, budget, validation, interrupted, internal) that `cli.ExitCode` maps to
exit codes 2, 3, 4, 5, 130 and 1. A failure to mask the output is internal, as
the hosted worker reports it as a plain failure rather than a rejected
document. A spent `pass_timeout` is reported as the budget
`generation budget exceeded: pass_timeout` and is not retried; the run's wall
clock as `wall_clock`. The document is returned only for a run that passed every
step, and published by the desktop job afterwards. `internal/store` keeps
versioned documents and immutable generation metadata in SQLite under the
common git directory
(`baum/reviews/versions.sqlite`), so linked worktrees share one history. A new
store falls back to the XDG data directory (or `~/.local/share`) when git is
read-only, keyed by the canonical common-directory digest; a store option can
override the root. Existing local or fallback history wins over creating another store,
and a read-only existing store fails writes without hiding its history.
Repository names, including GitHub host, and pull request numbers key rows.
Documents are stored without their hunks' `diff` and `github_url` (see "Hunk
evidence in saved documents").

`Latest`, `Versions`, `Get` and `Put` expose the local history.
`RefreshCurrent` updates only the latest document with one SQLite statement
fenced by both its version number and exact previous bytes, preserving
metadata and every replaced version. Concurrent refreshes cannot overwrite a
document prepared against older evidence, and a refresh never replaces a newer
version. A refresh whose document differs from the stored one only in
`review.evidence_refreshed_at` (`review.RefreshChanged`, which compares the
whole documents as canonical JSON with that field blanked) writes nothing: it
checks the same fence with a read and leaves the stored bytes, the summaries
row and the store file as they were. The stored `evidence_refreshed_at` is
therefore the time of the last refresh that changed the document, not of the
last read, which the viewer shows as when the evidence was refreshed; the
scheduler keeps its own read times (see "Free background synchronization").
A running generation does not block refresh: live facts refresh
independently, as in the hosted service. `Put` publishes metadata and document
in one transaction only if the previous version the caller read is still
latest, so concurrent runs cannot assign a different predecessor to a completed
document. A refresh of that previous version in the meantime does not reject
the generation; the next refresh updates the new version's live facts. A losing run reports a conflict; generation failure or
cancellation before publication writes no version. Export errors after
publication leave the successfully generated version available. Metadata records full revision IDs,
bundle digest, backend and installed version, actual model, schema revision,
prompt version/digest, applied REVIEW.md digest and generation time/usage.
The desktop reads saved versions from the local store. Reads never create a
store, and historical versions keep their own revision and evidence selectors.

Git reads (the comparison, patches, blobs and the exports) run in a private
temporary bare repository that borrows the clone's objects and has default
configuration, like the hosted git mirror. The clone's configuration,
attributes and replace refs cannot change the output, and its refs,
configuration and worktree registrations are left as they were. An export is
the hosted service's own command, `git archive --format=tar
--worktree-attributes`, so it is the same archive: the clone's attributes and
filters are not applied.

The clone is written to only when it lacks a pull request commit: generation
jobs fetch the head from
`refs/pull/<N>/head` and the base from its branch, falling back to the commit
id, into `refs/baum/pull/<N>/`, from the remote whose URL names the pull
request's repository (`upstream` in a fork clone), with the user's git
configuration and no hooks. A fetch that fails, or a clone with no such
remote, fails the run with the `git fetch` to run. A partial clone missing the
pull request's objects is reported, not fetched from on demand.

`internal/assemble` follows the shared assembler's order behind small source
interfaces (`GitHub`, `Git`, `Sessions`, `ImageFetcher`): description images,
the pull request header, checks and conversation, diff index, commit changes,
sessions, caps, native session images, references, attachments, `REVIEW.md` at
the base commit, checkout export, then redaction, per-hunk line limits, byte
trimming and digest.
Sessions follow comparison because they need its merge base. The linker and
`bundle.KeepKnownCommandTargets` check command targets against the pull
request's diff paths, independent of the working tree. A failed session source
leaves a warning and no sessions; cancellation is an error.

Image downloads use GitHub-signed URLs without credentials and contact only
GitHub upload hosts (see SECURITY.md). `Request.Previous` carries the previous
review; optional writers receive bounded checkout and attachment archives. The
checkout is exported even without a writer because omitted files make their
hunks textless. Bundle JSON has no timestamp of its own: the same inputs
produce the same bytes and SHA-256 digest.

Native images retain human-input or tool-result origins, their root prompt,
optional child input and tool ID. The host deduplicates content across both
image sources, retaining all origins and additional description URLs. The
20-image/10-MiB-per-image defaults are shared; description images come first.
Session images are content-sniffed and limited to 40 million pixels. A
session image longer than 1568 px on either side is resized to fit
(`assemble.MaxSessionImageEdge`; a JPEG stays a JPEG, PNG and WebP become PNG)
when that makes it smaller; its ID, hash and size describe the resized bytes.
GIF and APNG animations, JPEGs with EXIF data (whose orientation would be
lost) and images above 24 million pixels are kept as captured, and an image
past the count limit is dropped before it is decoded.
Content is matched against description images before resizing, so a session
copy of a description image still merges with it. Origins
of sessions removed during fitting are pruned; an image with no remaining
source is omitted before the archive is written. Session-only images use
attachment-ID source anchors, never a fabricated PR-body source. Saved versions
keep their provenance and their images through the existing store and
authenticated attachment route.

`internal/refresh` fetches current GitHub metadata with checks pinned to the
saved head, re-links local sessions against the saved merge-base/head range
and redacts bounded evidence before `DocumentValidator.Refresh`. It exports
no checkout, downloads no images and imports no model backend. Currency
requires both saved head and base to match; failures leave the stored review
unchanged. The desktop serves saved versions with linked local sessions and
commits; when those sessions are absent, document session metadata remains.

What usage would cost at API prices comes from a price list at
`https://static.usebaum.com/pricing/v3/prices.json`. baum verifies its `.sig`
with the price-list public key embedded in the binary (a key that signs only
price lists) and falls back to a snapshot embedded
at release when no newer verified list is available. The daemon can persist a
verified signed list beside its job journal and reload it on restart.
`pricing.CurrentSource` says which is active (`embedded`, `fetched` or `cached`),
its version, when it was fetched and why the last refresh failed. Pricing is
best-effort and only reports, in Analytics and a run's recorded usage: no
budget, cap, limit, confirmation, ordering or dedupe reads a price or the
list's version.

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
filtered environment and never reruns a failed backend check. The desktop
command and daemon set `Options.Desktop`, which checks only Claude Code and
Codex and never offers an API-key fallback, and bounds every subprocess with a
timeout and, through `internal/proc`, a process group killed with it (the
package moved from `agent/internal/proc` so both can use it). A configuration
that does not load is a problem, not an abort; its parse errors name a line and
column, and the key for a type or value error, never the text. A host that `github.IsGitHubHost` refuses is a problem
without a token lookup. It does not import `daemon`, which serves the
report: the command line passes `Options.Daemon`, which asks the daemon over
its control socket (never starting it or creating its state directory) for
its version, process id and uptime, and whether this baum would replace it.

### Profiling the daemon

The daemon should use next to no CPU while nothing changes. To measure what
it does, `baum daemon profile` watches the running daemon without restarting
it:

```sh
baum daemon profile                          # 60s CPU profile, heap, goroutines and work counters
baum daemon profile --duration 5m --trace    # longer window, plus an execution trace
baum daemon profile --watch --interval 10s   # one line per interval: CPU share, heap, work that ran
go tool pprof -top baum-profile-*/cpu.pprof  # where the CPU time went
```

The profile command saves `cpu.pprof`, `heap.pprof`, `goroutine.txt`,
`stats-start.json`, `stats-end.json` and `summary.txt`. The summary gives the
CPU time the daemon used as a share of one core, from `getrusage`, and lists
the counters that moved in that window, largest first.

`internal/workstats` keeps those counters. Each is an atomic add, so they are
always on. They count:

* calls to `sessions.Map`, its decodes by source, the bytes and lines
  actually decoded (only the appended part of a transcript that grew), hits on
  its cache, transcript reads continued from an offset or from the start, and
  why a kept transcript was read from its start again;
* git processes in total and by subcommand;
* background passes and pull request refreshes;
* session loads by entry point;
* analytics refreshes, review index builds and live transcript builds.

Gauges report state such as the size of the session caches: the estimated
memory of the mapped graphs kept (`sessions.map_cache.bytes`) and of the
decoded transcript lines kept (`sessions.tail_cache.bytes`), each beside the
transcript bytes behind it (`.transcript_bytes`). Compare them with the heap
profile, where kept lines are allocated in the
transcript decoders and graphs in the session mappers.

The daemon serves the counters at `GET /api/debug/stats` and Go's runtime
profiles at `GET /api/debug/pprof/` (`profile?seconds=N`, `trace?seconds=N`,
`heap`, `goroutine`, and so on; never `cmdline`). Like every `/api/` route they
need the launch credential as a Bearer token. The CLI reads that credential
from the daemon's state directory. A profile costs nothing until it is
requested.

`config.Config.BackendPaths` (TOML `[backend_paths]`, keys `claude`, `codex`
and `cursor-agent`, absolute paths only, validated by `config.ReadFile`) is the
explicit executable of a backend. `generate.DefaultBackendsFor(cfg)` passes it
to the backend as its `Binary`, which is looked up by `exec.LookPath` as given,
so an absolute path replaces the `PATH` search; a path that cannot be run fails
the backend's `Check` with `generate.ExplicitPathError` naming
`backend_paths.<name>`, without trying `PATH`. The job runner builds its backends from the resolved configuration, and
per-repository settings do not override the paths.

`config.Config.AgentEnv` (TOML `[agent_env]`, validated names, at most 32) is
the user's environment for agents; the app's `repos.Preferences.AgentEnv`,
edited with `PUT /api/settings/agent-env`, replaces it when it sets anything.
`AgentEnviron` expands a leading `~` and `$NAME` from the daemon's
environment. `generate.DefaultBackendsFor` gives it to the `claude` (User
profile), `codex` (Local profile) and `cursor-agent` backends, which set it
over `LocalEnvironment` with `agent.WithUserEnv` before their own run
variables, so it may name a `GIT_` variable the allowlist drops. A fix passes
it to `gitlocal.FixOptions.Env` for every git command in the checkout and the
push, without the variables that would redirect git.

`config.Config.ClaudeConfigDir` (never in the file) is the Claude Code
configuration directory the app chose for a run: `repos.Preferences`'
`ClaudeConfigDir` for reviews and extraction (`Resolve`), and
`FixClaudeConfigDir` for a fix with its own agent (`ResolveFix`; a fix that
follows reviews takes theirs), and `Decisions.ClaudeConfigDir` for extraction
with its own agent (`ResolveExtraction`, set with `PUT
/api/settings/decisions`). All three are checked by `agentdirs.Claude.Choose`. `DefaultBackendsFor` gives it to the `claude`
backend alone as `CLAUDE_CONFIG_DIR`, set over `agent_env`'s. `PUT
/api/automatic` takes `claude_config_dir` and `fix_claude_config_dir` (`~`
expanded, stored absolute, an existing directory, `""` for the
environment's), refuses one `claude auth status --json` reports signed out
with that directory (`Scheduler.ClaudeSignedIn`, `claude.SignedInWith`; an
answer it cannot read does not refuse), and records the directory with
`agentdirs.Claude.Remember`. `planusage.Service.Others` reads the plan of each
directory chosen for a run that uses Claude Code
(`Preferences.ClaudeConfigDirs`) and serves it with `config_dir`.

`config.Config.CodexHome` is the same for Codex: `Preferences.CodexHome`,
`FixCodexHome` and `Decisions.CodexHome` under the same rules,
`DefaultBackendsFor` gives it to the `codex` backend alone as `CODEX_HOME`,
`PUT /api/automatic` takes `codex_home` and `fix_codex_home` and `PUT
/api/settings/decisions` `codex_home`, checked by `agentdirs.Codex.Choose`
with `codex login status` (`codex.SignedInWithHome`). The plans of
`Preferences.CodexHomes` (runs whose backend is `codex`) are read with
`planusage.Codex{Home: dir}`.

`internal/agentdirs` is the one place that says which agent directories baum
reads. `agentdirs.Claude` and `agentdirs.Codex` each have `All(home)`: the
default (`~/.claude`, `~/.codex`), the process's variable (`CLAUDE_CONFIG_DIR`,
`CODEX_HOME`) and a record beside `config.toml` (`claude-dirs.json`,
`codex-dirs.json`; `Remember`, at most 16). Session discovery
(`collector.FindSessions`, `FindStores`, with `ClaudeProjectsDir` and
`CodexSessionsDir` still overrides), clone finding, the live transcript store
checks (`livesessions.claudeTranscript`, `codexRollout`), the running-session
registry (`readClaudeRegistry`) and review-run cleanup
(`reviewruns.RemoveSessions`) all read each directory. `baum hooks install
--user` records the directory it installed into, and an agent hook the one its
session runs with (`hookcli.rememberSessionDir`, on the events that locate the
terminal).

## Local application persistence

[`internal/appdb`](internal/appdb/README.md) owns the shared per-user
`$XDG_STATE_HOME/baum/app.sqlite` (default `~/.local/state/baum/app.sqlite`).
Repository settings, groups, decisions/features, jobs, scheduler state, inboxes,
analytics/history and persistent discovery/pricing caches share it. Small JSON
state documents use bucket/key rows; read-modify-write operations use SQLite
transactions. Version 1 is the first release baseline; newer and unrecognized
schemas are refused and pre-release files are not imported. The package README
lists the stores and release-readiness checks. Clone review databases, OS
credentials, optional config, runtime credentials/locks/spool/logs and actual
agent working directories/transcripts remain independent.

## Explicit local repository registry

`internal/repos` owns the repository tables in the shared private
`app.sqlite` application database (`internal/appdb`) and
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
global preferences as one JSON row: `automatic` (true until changed), the
review harness and model, the fix agent (`fix_backend`, `fix_model`), data
extraction's (`decisions`: `enabled`, `backend`, `model`), `pull_requests`
and `agent_env`; a field an older baum saved and this one dropped
(`cursor_agent_acknowledged`) is ignored on read. The harnesses and models are
global: a repository's `backend` and `model` an older baum stored are ignored
on read and dropped on the next write.
The shared application database owns schema versioning; pre-release registry
files are not imported. `null` follows the global preference.
`PUT /api/repos/{id}/settings` replaces the whole object and requires the
`automatic` field, so removing it is an explicit `null`. A `daily_cap` an
older baum stored is ignored on read, and one an older app sends is dropped
before the strict decode (`withoutDailyCap`), not refused.
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
the folders around the clones agents used, to four levels, using exclusions
defined by `internal/scanskip`. Candidates are grouped by the
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
`planusage.History` samples the plans every 15 minutes while the daemon runs and
keeps 90 days of changed figures in the `state/plan-usage-history` document in `app.sqlite`, for analytics.

### Local analytics

`internal/analytics` serves `GET /api/analytics?days=7|30|90&repo=&agent=`
(`uiserver.LocalAPI.Analytics`) for the app's Analytics page. Its parts, all
started in `localApplication`:

- `Index` keeps per-session aggregates, one `analytics_sessions` row per session file, in `app.sqlite` in the state
  directory, for sessions whose working directory is inside a registered clone
  (`sessions/collector.FindSessions` per clone with its worktrees, then
  `sessions.Map` and a reduction that keeps no text). Entries are keyed by the
  files' sizes and modification times, so a refresh reads only what changed; it
  runs at start, every five minutes and on a `session-end` hook event, and
  answers requests with what it has and its progress while it works.
- The report prices usage at read time from `pricing.Active()` (grouped by
  model, speed and long-prompt band), counts the hosted way (root sessions,
  human prompts, each commit once), and computes the agent-written share from
  the clones' commits by their `user.email` (`gitlocal.AuthoredCommits`) against
  the sessions' created commits, Entire checkpoints and co-author trailers.
- baum's own work comes from the jobs queue and `Actions`, a ledger in
  the `state/analytics-actions` document in `app.sqlite` that the merge and update handlers' writers record
  into when GitHub accepts a write (`Actions.RecordMerges`, `RecordUpdates`).
- `Delivery` caches one GitHub search per picked repository
  (`github.AuthoredPullRequests`) for an hour; a request starts the search and
  answers `loading` rather than waiting.

See its [README](internal/analytics/README.md).

## Durable desktop reading state

`internal/uistate` stores the Inbox's seen/cleared record, snoozes and archives, and the
walkthrough's seen stops and draft comments, in `app.sqlite`'s `ui` document
bucket. The records survive changing loopback origins and fresh webviews;
localStorage is used only for presentation choices. Walkthrough keys include
repository, pull request and review version; one transaction retains the 40
most recently saved records, using write order even if the wall clock moves
backwards.

`GET`/`PUT /api/ui-state/inbox` and
`GET`/`PUT /api/ui-state/walkthrough?key=…` run inside the existing local
Host/authentication/Origin/JSON boundary. Each value is a bounded typed record.
Writes compare that record's revision inside an immediate transaction and
return the current value on conflict. Client sequence high-water marks
acknowledge retried writes without replaying an already committed append; the retry ledger is not served.
The frontend shares a hydrated record, queues semantic changes and rebases
conflicts without replacing unrelated fields. It never saves initial defaults
before a successful read. Failed reads/writes keep unsaved text and offer Retry.
Drafts remain local until the person explicitly submits a GitHub review.

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
It does not persist a second review index. An event's data is at most 16 KiB.
`POST /api/mcp` is the exception to this middleware: it takes only an MCP
token, never the launch token or the cookie (see "MCP server for agents").

The generation queue's changes go out as two kinds. A `progress` event
`{"job": <job>}` announces anything but a running job's usage: queued, started,
a phase, a draft, a transcript, an end. A report that changes only usage (calls,
tokens, repairs, accounting, cost; `jobs.Queue.SetUsageChanged`), which a
running job sends every two seconds, is a `job` event
`{"job": {id, state, phase, calls, tokens, repairs, actual_cost_usd,
cost_reason, accounting, updated_at}}`, so listeners that read everything again
on `progress` (the app, the desktop tray) are not woken by it. Data over the
limit, a long run's accounting or a large job, goes out as `progress` with `{}`
instead, so a change is never dropped.

The stream also carries `notice` events for the desktop app's notifications
(`internal/notices`), which the web app ignores: `{"kind","repository","pr",
"clone"?,"title"?,"risk"?,"risk_status"?,"reason"?,"commit"?,"agent"?,"session"?,
"branch"?,"message"?}` with `kind` one of
`review_finished`, `review_failed`, `review_automatic` (an automatic review
finished), `review_requested`, `checks_failed`, `conflict`,
`changes_requested`, `ready_to_merge`, `fix_pushed`, `fix_failed`,
`details_failed`, `session_permission` and `session_input`. The last two are an agent session
that began waiting for the user's permission or answer (see "Live
sessions"): `agent`, `session` (its id), `branch` and `message` (the
tracker's cleaned, masked `waiting_message`) name it, `pr` is 0 when its
branch has none, and the tracker hands each wait over once. The others are
published when a job finishes or fails (not when it is cancelled, interrupted
or superseded) and when a discovery snapshot shows a pull request newly
review-requested from the user (a repository's first snapshot after the
daemon starts is only a baseline). A fix job gives `fix_pushed` with the
pushed commit's first seven characters in `commit`, or `fix_failed` with its
`reason`, instead of a review notice, and nothing when it completes without a
commit (its agent changed nothing); a details retry gives nothing when it
completes and `details_failed` with its `reason` when it fails. The four pull request kinds are
transitions of the user's own pull requests between two successive snapshots
of one repository: checks became failing, the pull request became conflicting,
the review decision became changes requested, or the merge state became
ready (the state the inbox calls Ready). A pull request first seen, or gone
from the snapshot (closed or merged), gives none; a state GitHub did not
report keeps the previous one; one that changed several ways gives one notice
(changes requested, conflict, checks, ready, in that order of precedence).
The title is GitHub's, trimmed to 200 characters; `risk` (1 to 5) is the
stored review's score, with `risk_status` `lower_bound` when incomplete
coverage may hide a higher one; `reason` is the job's failure category and
nothing free-form. A notice never holds review text, paths, transcripts or
error messages.
A client that wants no history sends a `Last-Event-ID` beyond the latest
event: it gets a `reset` and then only new events.

`GET /api/index?view=waiting` answers `{"waiting":N}`, the number of pull
requests in the app's Inbox, which is what the desktop app polls for its tray
icon, tray text and dock badge. `internal/inboxqueue` counts it on each request
from the cached index, the job queue, the stack groups and the saved `inbox`
ui-state, with the app's queue rules: each open pull request asking for the
user's review, and each of their own with something to fix, merge or update
and no fix running, once across clones, less stacked pull requests that must
wait to merge and snoozed or archived ones. Saved inboxes and their badge switches play
no part, as in the app's Inbox total.

`GET /api/inbox` answers the same queue with which saved inboxes hold each
open pull request (`{count, items: [{key, kind, fixing, snoozed, archived, inboxes}],
inboxes: [{id, query, count}]}`), matched with `internal/prfilter`, the Go
twin of the app's filter language; `?query=Q` answers which pull requests a
query not saved yet holds (`{query, problem, matches}`). The app reads it with
the index and matches nothing itself while the daemon has answered.

Version images are content-addressed. `attachment_blobs` holds one copy of
each distinct image, keyed by the SHA-256 of its bytes, and
`version_attachments` maps each repository, PR, immutable version and
attachment ID to a copy; `Store.Put` commits both with the version, and
regenerating a review with the same images stores no new bytes. Reads check
the copy against its hash. `generate` returns copies from the original,
hash-checked assembly archive, not agent-visible files. Reads expose only
raster images listed in that version's document. Existing stores without the
tables remain readable. A writable open moves an earlier store's per-version
`attachments` rows into the shared copies in `BEGIN IMMEDIATE` batches of at
most 32 MiB, deleting each batch's rows as it commits and dropping the table
once it is empty. Concurrent openers wait for each batch rather than fail, a
move that cannot finish resumes on the next writable open without failing
this one, and reads fall back to rows not moved yet, including any an older
binary writes afterwards.
`internal/reviewindex` rebuilds an in-memory projection of discovery and registered
version stores. `store.Snapshot` reads latest metadata, count and small document
summaries in one transaction, plus separate latest-started run records. It does
not move document bodies or maintain a second database. The document summaries
(title, PR state, risk score, review minutes) are a projection of each pull
request's latest document, kept in the `summaries` table of the same
`versions.sqlite`, keyed by repository and PR with the version and byte length
of the document it was computed from. SQLite triggers on `versions` (insert,
update of `document`, delete) upsert it from the same SQL extraction as the
fallback, in the transaction of whichever writer changed the latest document:
`Put`, `RefreshCurrent`, `CompleteCurrent` and an older baum binary alike. A
failing or conflicting write rolls the projection back with the document. A
document SQLite cannot parse (nested deeper than its limit) drops the row
instead of failing the write, and `Snapshot` then extracts as it always did.
Trigger names carry a hash of their SQL: a write-open that does not find
exactly the current ones replaces them, empties the table and re-projects it in
one transaction, so a changed extraction never leaves rows computed by an older
one. Every write-open projects each latest document without a current row; the check is a read-only query that
parses no document and writes only when something is stale, so existing stores
are backfilled on their first write-open and later opens are cheap. `Snapshot`
uses a row only while its version is the latest and its length equals
`length(document)` (read from the record header, not the body). Any other row,
or a missing table (an old store, or a read-only open before any write-open
by this version), falls back per row to extracting from the document. A
read-only open never writes. When every row is current, Snapshot parses no
document body.
Document revisions name each version's stored bytes without reading them, so
a reader can tell a document it has already parsed from one rewritten since:
`RefreshCurrent` and `CompleteCurrent` rewrite the latest version in place,
often to the same length within one file-time tick. The `document_revisions`
table holds a random 64-bit revision per version, which triggers on
`versions` (any insert or update, and delete) renew in the writer's
transaction, an older binary's included. Trigger names carry a hash of their
SQL; a write-open that does not find exactly the current ones replaces them
and renews every revision in one transaction. `Latest` and `Get` return the
revision read with the document (`Version.Revision`), and `Revision` the
version and revision alone. Without the table or exactly the current
triggers (a store no write-open of this version has seen, read without
writing) the revision is 0, which names nothing. Generation publishes
lease-based run status independently of immutable successful version history;
expired leases are read as interrupted without mutating stores during indexing.
Once `Store.Put` commits a version, generation succeeds: failing to record the
run's outcome afterwards is only a warning, and the lease later expires.

`GET /api/index` and `GET /api/prs` revalidate: the JSON of a cached snapshot is
encoded once per cache version and variant (`/api/prs`: once per
discovery `Revision` of the picked repositories, until a snapshot reaches its
next poll), answered with a strong `ETag` (digest of the bytes), `Cache-Control:
no-cache` and 304 for a matching `If-None-Match`. The app fetches them with
`cache: "no-cache"` so the webview sends the validator; every other endpoint
stays `no-store`.

The app asks for the trimmed forms: `GET /api/index?history=1&detail=summary`
leaves each entry's `latest` with `version`, `head`, `backend`, `model`,
`generated_at`, `calls`, `tokens` and `recorded_cost_usd` (no `accounting`
attempts, digests, policy or kept session, several KB per entry; a version's own
read serves them), and `GET /api/prs?view=health` answers one
`{repository, stale, fetched_at, next_poll, rate, error?, settling?}` per picked
repository, without the pull requests and observations (the index lists those).
Both are encoded and tagged once per version like the full forms, which stay the
default for other clients; an unknown `/api/prs` view is `400`.

`Service.Build` is uncached: each call probes every clone with git, reads the
discovery cache and opens every store. The daemon reads it through one
`reviewindex.Cache`, shared by `GET /api/index` (all views; the `history`
filter works on a per-request copy), the Inbox count, notices and review
groups. Concurrent readers share one build, which
runs under the daemon's context, so a reader that gives up does not fail the
others; a failed build is not kept. A generation counter invalidates: every
in-process change that publishes an `index` event (the daemon's `changed`,
which settings, registry, clone, job, discovery, merge, review, update, inbox,
group and GitHub-account changes all go through) calls `Invalidate` first, so
a client reading after the event gets a new build, and a build that started
before an invalidation still answers its waiting readers but is not kept.
Discovery, which only the daemon runs, invalidates through
`discovery.Service.Updated` after each write of a repository's cache and each
change to its live freshness. It wakes the watcher below, which publishes
only if the index moved. Background pass callbacks use the same path instead
of publishing directly at both the poll and read boundaries. The watcher also
compares the background repository errors the app displays, so a refresh
failure or recovery is announced even when the index rows stayed the same.
Routine background success times and retry counts do not trigger an event.
The watcher's comparison also omits `metadata_fetched_at`: an unchanged
successful discovery poll advances that bookkeeping timestamp, but changes no
visible row. Applied observations still carry `observed_at`, which the app ages,
and all actual PR fields participate. The API snapshot retains its timestamps;
the watcher copies entries before dropping the field from its comparison. A write of the credential's budget record alone
(a probe saves only that), which `Cached` does not read, does not invalidate. A
panicking build is an error for its readers and is not kept. A
kept snapshot expires at the earliest instant at which building again could
differ with nothing written: a `generating` run's lease end (it then reads as
interrupted); and at the latest after `reviewindex.MaxAge` (2 minutes), for what nothing models (below). An
applied observation's age does not count: the entry carries `observed_at` and
the app shows it as stale two minutes later, so the index does not change with
time alone. The index reads discovery through `CachedStable`, which keeps
failed or not-yet-validated data stale but does not age it at `NextPoll`. Other
discovery readers retain the deadline-based `Cached` answer.
Nothing is persisted: a restarted daemon builds on first read.

A build after an invalidation reads again only the repositories whose inputs
moved. The cache keeps each repository's rows, problems and next change (a
part), keyed on the repository's `discovery.Revision`, its clones as the
registry lists them (ID, common directory, availability, which clone reads a
shared store). For each store it reads, the key also holds the size,
modification time, mode and file identity of `versions.sqlite` and its
write-ahead log under both roots. Unlike the fingerprint, it does not look
through lease renewals: renewing a lease that had already lapsed makes an
interrupted run read as generating again. The registry is listed on every
build. The key is taken before the repository is read, so a write landing
during the read moves the key and the next build reads again. A part is read
again when its key moved or its own next change passed. It is also read again
when it came from a failed read, or when its key cannot vouch for it: a
discovery without `Revision`, a store file modified within the last three
seconds, a failed stat, or a custom `OpenStore` without a `StoreStamp`. Every
repository is read again once `MaxAge` has passed since the build that last
read them all, and no part is used once `MaxAge` has passed since it was read,
so `MaxAge` still bounds what no key covers. Merged in repository order, the
parts give the same snapshot a full build does. A write for one repository
therefore costs a read of that repository's discovery cache and stores, one
`Revision` (an in-memory stamp that resolves the credential) and the store
stats of each other repository, and one registry listing. It also re-encodes
the merged snapshot once for `/api/index` and once for the watcher's hash.
`Service.Build` keeps nothing and reads everything.

`uiserver.Server.WatchIndex` catches changes made outside the daemon
(another process editing the registry) and those that
come with time. Every 10 seconds (`uiserver.IndexWatchEvery`) it computes `reviewindex.Fingerprint`, which
runs no git and opens no store: the registry's picked repositories and clone
rows as stored (`Registry.RepositoryRows`, one query on the registry's
connection), the GitHub account revision (`githubauth.Manager.Revision`), and
for each clone the size, modification time and mode of `versions.sqlite` under
both roots `store.Open` chooses between and of its git `config`, plus whether
its working tree and `.git` exist; a missing file is part of the digest. Only
when the fingerprint moved (it then invalidates), the cached snapshot expired,
or another reader replaced it does the watcher read the index, and it publishes
`index` only when the snapshot's JSON hash differs from the last one it
published. The first read publishes.

With no event stream connected the watcher builds nothing, since nobody would
be told: a moved fingerprint still invalidates the cache, so a reader of
`/api/index` never gets a stale snapshot, and the read it owes happens when a
stream connects (which wakes the watcher). The desktop shell's tray keeps its
own `/api/events` stream open (with reconnect), so while the desktop app runs
there is always a subscriber and this idle skip rarely applies; it covers a
daemon running without the shell. What helps while the app runs is the next
point. A run's 30-second lease
renewal (`store.HeartbeatRun`) rewrites `versions.sqlite` but changes nothing
the index shows, so the fingerprint looks through the latest renewal made by
this process (`store.SeenThroughHeartbeats`: the file stamp the renewal left
maps back to the one before it, and any other write still moves it). A
renewal by another process, such as the CLI, still moves the fingerprint.

The fingerprint reads registry rows rather than `app.sqlite`'s file times or
`PRAGMA data_version`: every daemon store (jobs, chat, UI state, analytics,
automatic generation, decisions) writes `app.sqlite`, so both signals move on
every job progress save, which is no input to the index. A store's SQLite
header change counter is not read either: closing any descriptor of a SQLite
file drops the POSIX locks this process's own connections hold on it, and the
daemon reads and writes stores itself. The databases use the write-ahead log
(see `internal/appdb`), so a commit changes the modification time of
`versions.sqlite-wal`, and a checkpoint that of `versions.sqlite`; the
fingerprint stats both (`store.StampOf`). Two commits within one timestamp
step of a check, at the same size, look alike until `MaxAge`. What nothing
models is also bounded by `MaxAge`: a clone made unavailable by a change the
stats miss, and a GitHub credential replaced outside baum. Connecting and
disconnecting go through the daemon, which invalidates; another process can
only rotate the same account's token, which keeps its discovery partition.
Detecting an outside keyring edit at once would take a credential read per
tick, which is a process (`baum internal github-credential`) running the
native keyring, the cost the cache removes: before it, every build ran one per
picked repository.

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
`review_required` preserves whether GitHub's `reviewDecision` is
`REVIEW_REQUIRED`, independently of recorded reviews. The Inbox uses that flag
to name a required review; an otherwise unexplained blocked merge says
"Blocked on GitHub". A missing approval alone is not a review requirement.
The user's teams and per-author repository permissions still use conditional
REST requests. Truncated review requests fail the refresh rather than concealing
an automatic-generation trigger. All requests refuse redirects.
Missing team scope and unreadable permissions are explicit unknown evidence;
a fork or unknown author permission must not become an automatic-generation
authorization. This service has no generation or GitHub mutation capability;
the app's GitHub writes (merge, update branch, re-run checks, a submitted
review, a confirmed fix's `git push` and that fix's replies on the review
threads it addressed)
live outside it (see "Fix jobs" and below).

`Service.Cached` resolves the local credential and reads disk without network
access. Disk data is stale until a successful poll in the running service.
`Service.Poll` serializes polls, honors its next deadline, and backs off unchanged
data from 30 seconds to 15 minutes, respecting GitHub's polling interval and rate
reset. GraphQL has no ETag; only REST metadata uses conditional requests.
`Service.PollNow` skips the deadline but not the budgets; the app's refresh
(`r`) uses it. `Service.Probe` asks, in one GraphQL query for up to 50
repositories (about one point), for each one's newest open pull request, open
count and latest update, and reports those that differ from the fingerprint
its last complete poll recorded. The background worker probes once a minute
and polls a changed repository at once, so a pull request opened or pushed
from anywhere shows within about a minute while unchanged repositories keep
the backoff. Resolving a review thread does not move a pull request's update
time, so the probe misses it; the next scheduled poll, or the app reading the
threads (below), shows it. Each polled pull request keeps GitHub's
`updated_at`, which the background refresh compares with its last read; it is
left out of the unchanged comparison, so it does not reset the backoff. GitHub
works out a pull request's mergeability in the background after a push, so a
poll right after one can read the old conflict or none. A poll that finds a
new head, or a new pull request, sets the next poll `settleInterval` (15 s)
away (or GitHub's polling interval, if longer), as do up to `maxSettles` (3)
more while a non-draft pull request's `mergeable` or `mergeStateStatus` is
still `UNKNOWN`; the snapshot says `settling`, and the background worker polls
the repository at that deadline instead of its interval. A fix that pushed
polls its repository at once and again 5, 15, 30 and 60 s later
(`discovery.AfterPush`) until the pull request shows the pushed head with
checks running or finished: GitHub queues the checks seconds after the push,
the first poll finds none and a clean merge state, and neither the probe
(check results do not move `updated_at`) nor the settle poll (the background
worker does not see a poll it did not make) would read them before the usual
backoff. A repository never polled or not returned waits for its scheduled
poll; the probe assumes one credential, leaving out any repository whose
credential differs from the first's.
GraphQL cost, remaining points and reset time map to the public rate projection.
REST requests and GraphQL points have separate persisted budgets, with the
currently limiting budget exposed when exhausted. Both budgets and secondary
retry deadlines are shared by host and credential across repositories and
service restarts. Discovery never sleeps through an exhausted budget.
A failed or rate-limited refresh preserves the last complete inbox as stale;
optional non-rate team or permission failures keep their fields unknown.
Snapshots and minimal typed response fields are atomically replaced in
`cache/discovery/<hash>` application database documents, partitioned by host/credential fingerprint/repository. The index
consumes these snapshots; missing PRs in an open-only snapshot are never treated
as closed. Each pull keeps its head and base branches (`head_ref`,
`base_ref`). Old cache formats drop
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
`method_not_allowed` are `422`, `forbidden` is `403`, anything else `502`.
Permission refusals retain GitHub's explanation and direct the user to the
connection's permissions and scopes. A missing workflow scope explains that
workflow changes need workflow access, which is opt-in, and offers merging on
GitHub or Settings' Reconnect with workflow access.
After a merge GitHub accepted, the daemon records it (`discovery.Service.Merged`:
the pull request leaves the cached open ones and is observed `MERGED` at the
merged head, with no read) and publishes an index change, so the inbox shows
it merged at once rather than when the background refresh next reads it. It
then answers, and runs `Service.PollNow` for the repository in the background,
publishing another index change when that read succeeds: on a busy repository
the read takes seconds, and it may wait behind a background poll.

Submitting a review is the app's other write outside discovery.
`POST /api/prs/review` takes `{repository, pr, head_sha, event, body,
comments: [{path, line, side, start_line?, body}]}` for a picked repository
(unpicked is `404`) and creates one pull request review through REST
(`Client.SubmitReview`) with `commit_id` set to `head_sha`, the commit the
review was generated for, sent once with no retry. `event` is `APPROVE`,
`COMMENT` or `REQUEST_CHANGES`; the last two need a body or a comment; at most
100 comments, `side` `RIGHT` by default, and a range sends `start_side` equal to
`side`. GitHub's refusals map to `{code, message}`: `forbidden` `403`,
`not_found` `404`, `own_pull_request` and `invalid_line` (a line outside the
diff) `422`, anything else `failed` `502`. The answer is `{url}`, the review on
GitHub.

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
`409`, `blocked`, `no_runs`, `fork` and `no_clone` are `422`, `forbidden` is `403`,
anything else `502`. After either write the daemon publishes an index change
and answers, then runs `Service.PollNow` in the background and publishes another
when that read succeeds, as after a merge.

The same handler lists and resolves review threads for the Inbox's panel. `GET
/api/prs/review-threads?repository=&pr=&unresolved=` answers `{threads,
capped}`. `prupdate.Cache` keeps each pull request's list in memory for
a minute and serves it while it holds as many threads as `unresolved`,
discovery's count (absent: not compared; a capped list is never compared), so
a thread opened or resolved on GitHub is read again once discovery sees it, or
once the entry expires, as is an edit within a thread. A list read from GitHub
whose uncapped count differs from `unresolved` replaces discovery's count for
that pull request (`discovery.Service.Unresolved`, through `Service.Counted`)
and publishes an index change, so the Inbox row agrees with the panel before
the next poll. A change of GitHub account, or
disconnecting, empties it with the daemon's other account-dependent caches
(`ResetIdentity`), and a read under way when that happens is not stored. Otherwise the list is read
with `github.Client.UnresolvedThreads` (the fix's `FixReviewThreads` document): each
thread's node `id`, `path`, `line`, `outdated`, the first comment's `author`,
its headline cut to 200 characters (`excerpt`: `fix.Headline`, the first
wholly bold line among the first four, else the first line of text, without
HTML comments, tags, images, code blocks or Markdown markup, which the fix
dialog's excerpts use too), the
comment count, the first comment's `url`, `can_resolve`
(`viewerCanResolve`) and `messages`: every comment read (the first 20; the
fix addresses the first 10), each `{author, body, url}`, the body as text cut to
4000 characters, untrusted. `POST /api/prs/resolve-thread` takes `{repository, pr,
thread}`, reads the thread by its node id (`ThreadState`) and refuses one that
is not on that pull request (`not_found`, `404`) or that the account cannot
resolve (`forbidden`); a thread already resolved answers success without a
write. Otherwise it sends `resolveReviewThread` once and, like the other writes,
polls discovery and publishes an index change. No reply is posted. A thread
resolved from the app, or found resolved, leaves the cached list at once.

`GET /api/prs/failing-checks?repository=&pr=&head_sha=` lists the failing
checks of `head_sha` (the head the app shows; absent, the pull request's head
is read first) with `fix.FailingChecks`, the fix's evidence read: check runs
of their latest attempt whose conclusion is `failure`, `timed_out`,
`startup_failure` or `action_required`, and commit statuses in `error` or
`failure`, at most 20. It answers `{head_sha, checks}`, each check's `name`,
`conclusion`, `title` (its output title, else the headline of its summary or a
status's description, cut to 160 characters), `url` (`details_url`, else
`html_url`, or a status's `target_url`; the fix prompt leaves it out) and
`job_id` (the GitHub Actions job behind a check run). `prupdate.Cache` keeps a
head's list for 2 minutes, forgets it when either re-run starts a run of that
head, and is emptied with the threads on an account change.

`GET /api/prs/checks?repository=&pr=&head_sha=` lists every check of
`head_sha` (`fix.AllChecks`: check runs of their latest attempt and commit
statuses). It reads up to 5 pages of check runs and 3 of statuses (and 3 of
workflow runs to name Actions jobs), sorts, then keeps at most 100, so
failing checks and commit statuses are never what is cut. It answers
`{head_sha, checks, total, capped}` (`total` is how many GitHub reports,
`capped` is true when `checks` lists fewer), each check's `name`,
`status` (`queued`, `in_progress` or `completed`; a pending commit status is
`in_progress`), `conclusion` (the run's conclusion or the status's state, empty
until completed), `title`, `url` and `job_id` (only for a failing GitHub
Actions run, so `rerun-job` accepts it). Failing checks come first, then
those not completed, each by name. The cache keeps a head's list for 20
seconds, and a re-run forgets it.

`GET /api/prs/conflicts?repository=&pr=` answers `{head_sha, base_ref,
base_sha, files}`: the files in which the pull request's head conflicts with
its base branch's current tip. The daemon reads the pull request from GitHub,
then in the repository's first available clone fetches the head (when
missing) and the base tip into `refs/baum/pull/<N>/` and runs `git merge-tree
--write-tree --name-only -z` in the private mirror (`gitlocal.Repo.Conflicts`),
which touches neither the working tree nor any branch; `files` is empty when
the merge is clean. It needs git 2.38 or newer. Without an available clone with
a remote naming the repository it answers `no_clone` (`422`).

`POST /api/prs/rerun-job` takes `{repository, pr, head_sha, job_id}`,
re-checks that the pull request is open, its head is `head_sha` and the
account can push, re-reads the head's failing checks and refuses a `job_id`
that is not one of their jobs (`not_found`, `404`), then posts
`.../actions/jobs/{id}/rerun` once (GitHub answers `201`; refusals are worded
as a workflow run's). It is recorded as a check re-run in analytics.

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

### Pull request facts

A pull request with no saved review still shows what is known about it
without a model. `GET /api/facts?clone=&pr=&head=` answers its facts
(`review.Facts`, schema `baum.facts/1`): the stats, index (hunks, checks,
comments, reports, threads, sessions), agent reviews and unassigned hunks a
completed document derives, filled by the same code (`review.BuildFacts`
over `bundleIndex`, `agentReviews`, `setValidation`, `countFindings` and
`setReviewExchange`), with no version, sections or model-written text. Its
bundle is assembled as generation's is, through the same GitHub client,
clone (fetching a missing pull request commit) and local session stores,
in facts mode (`assemble.Request.Facts`): no head/ or merge-base/ tree is
exported and no description image is downloaded.

`internal/facts` builds them only when a page asks, never in the
background, and keeps the latest per clone, repository and pull request in
memory; they are never written to a version store, and `/api/versions` and
`/api/document` never see them. A request about the head the page shows is
answered from memory until the background refresh's interval
(`Service.RefreshInterval`, one minute) has passed since the last build, so
a page that keeps asking reads GitHub at most once per interval; a new head
builds at once, a head GitHub does not report yet stands for the interval,
and concurrent requests for the pull request, whatever head they name, share
one build that a request giving up does not cancel. A failed build stands for
10 seconds, so a retry soon reads again, while facts built before for the
same head still answer; a rate-limited one stands for the interval. While the host's rate limit is paused (`Service.Paused`
of the background refresh) only facts already held for that head answer,
else `503` with `{"code":"rate_limited","resume_at"}`; a build that is
rate-limited extends the shared pause (`Service.Limited`). A pull request
without a registered clone gets none (the selector is a clone ID).
`/api/facts/sessions`, `/api/facts/sessions/{id}`, `/api/facts/patch`,
`/api/facts/commits`, `/api/facts/commit` and `/api/facts/commit-patch` serve
what the version routes serve, through the same
per-document viewer cache, for the facts last built, and never read GitHub.
They take the `head` of the facts the page shows and answer `404` when the
facts last built describe another, so a page is never answered for facts it
was not shown.

### Persistent generation jobs

`internal/jobs` serializes queue mutations in the `jobs` table of the shared
private `app.sqlite` application database, without duplicating review
documents. Each job is one row: its id, a creation sequence (`seq`, which
keeps the history oldest first), created and updated times, state, an
`archived` flag and the job as JSON. A save writes only the jobs changed
since the last successful write, in one transaction; a job dropped by a
rolled-back submission has its row deleted. The history has no size limit.
A fix's summary is cut to 16 KiB and a job's failure detail to 4 KiB, each
marked as truncated. A job whose JSON is still over 1 MiB (`maxJobJSON`) is
journaled without its summary, detail, thread outcomes, failed sections,
fix threads and usage attempts, and the error is logged. One still over the
limit is not journaled at all; the other jobs of the write are. A database
from an older baum keeps the history in the `state/jobs` document: the first
`jobs.Open` moves it into rows, in order, and deletes the document in the
same transaction, so a failed move leaves it in place and `Open` fails. An
older baum run after that sees no `state/jobs` and starts a new document;
the next upgrade appends its jobs after the existing rows.

A job that ended more than 120 days ago (`jobs.ArchiveAfter`, by its
`updated_at` and the queue's clock) is archived: it leaves memory, `List` and
the API, and its transcript file is deleted. Its row stays, with `archived`
set, and `Open` no longer loads it. Nothing deletes a row. Archiving runs at
`Open` and then daily, until the queue stops. Queued and running jobs are
never archived. Nor is an unsettled job that is its head's latest, which
`Refused` lists (see below), or a job whose archiving would leave an
unsettled job its head's latest. One transaction sets the flags, writes the
latest content of an archived job changed since its last write, and adds the
archived jobs to `job_heads`, which counts, per head (`headKey`: repository
case-insensitively, then pull request and head, or group and member heads),
the archived runs `ErrDuplicate` compares (`runs`) and the archived chat and
details jobs only `Interrupted` also counts (`others`). The queue loads the
counts at `Open` and keeps them in memory. A failed archive keeps the jobs
until the next pass. Seen, cancel, transcript and draft answer `404` for an
archived job, and a question's turn reads it as interrupted. Analytics'
longest window counts jobs created in the last 90 days, so archiving
changes none of its figures.
`jobs.Open` takes an exclusive
`internal/filelock` lock beside the database (`<database path>.jobs.lock.sqlite`) and keeps it
until its workers stop, so two processes never run one queue; it waits up to
10 s for a predecessor whose shutdown timed out and that is exiting.
Reviews, fixes and feature builds share one lane, which runs up to
`repos.MaxJobConcurrency` (4) jobs at once; questions have their
own lane (see the chat section). A free slot takes the oldest job a person
started (everything but an automatic job) before any automatic one, so an
automatic backlog never holds up a person's review, fix or feature build;
it never stops a running automatic job for one. Automatic jobs follow, oldest
first (a waiting one that took a newer head keeps its place, see
`Queue.SubmitAutomatic`). A person's start of the very run a queued automatic
job holds (`Queue.SubmitConfirmed` with the same inputs) makes that job
theirs: it keeps its id, loses `automatic` and `trigger`, and goes ahead with
the person's jobs; a person's group review does the same for the waiting
automatic reviews of its members at their current heads, and cancels a
member's waiting automatic review of another head as `superseded` in the
same write, before queuing its own review of that member, so the stale one
cannot start meanwhile. A running automatic member review is not stopped;
the group waits for it. A promoted job settles as a person's: an interrupted
run is not retried automatically, and a missing clone fails it. The
`job_concurrency` preference lowers the
limit (`PUT api/settings/job-concurrency`, 1–4), and `Queue.SetConcurrency`
applies it to the running queue: lowering it stops no running job, it only
holds new ones until fewer run. A job waits while another job of one of its
pull requests (a group's members count) runs, and later jobs may start before
it. The daemon's log (`Queue.SetLogger`) has a line when each job starts and
one when it ends, with its state, reason, detail, calls, tokens and duration
and, for a failure, the executor's error, which the job itself may keep no
text of (a provider's error, an internal one).
A failed journal write does not stop a worker: the queue retries it with
backoff (1 s up to 30 s) until it succeeds, signals the waiting workers after
it does (a job whose start could not be journaled waits until then), and tries
once more at shutdown after the workers stop.

A run's time limit is its only bound by default: `budgets.review_timeout` (10
minutes) for a review of a pull request or a group and `budgets.run_timeout`
(30 minutes) for every other agent run, a fix, a question to a review's agent
or a feature build. The preferences' `review_time_limit_minutes` and
`run_time_limit_minutes` (`PUT api/settings/time-limits`, 1–240) replace the
config file's (`Preferences.Resolve`). `max_calls` and `max_tokens` are zero,
no limit, unless the config file sets them; `agent.Exceeds` and the backends
treat a zero call or token budget as none, and Claude Code gets `--max-turns`
only with a call budget.
`Queue.Drain` refuses new jobs and reports true only when none is queued or
running, under the mutex `Submit` checks. A restart into a newer version uses
`Queue.DrainRunning` instead, which waits only for running jobs: it holds the
queue, so no queued job starts in either lane, and once none runs it refuses
new jobs in the same step, so a restart never cuts a started job short. A job
started while the restart waits is accepted and queued behind the hold; one
started after it drained gets `503` with `Retry-After` and is started again
against the new daemon. It does not drain while a person's group generation
queues its members' reviews and then its own job, so a group is accepted or
refused whole. Queued jobs stay journaled, and the new daemon runs
them in the usual order (`Queue.next`); `Resume` releases the hold when the
restart fails. Workers use the same `generate.Publish`
entry point as the terminal: previous-version lookup, run lease, generation and
strict validation, then atomic version publication. Running journal entries are
interrupted on restart; queued entries retain their confirmed input fingerprint.
The daemon opens the queue held (`jobs.OpenHeld`) and resumes it once the
application is wired, so a journaled feature build or chat job never starts
before the feature log or the chat service exists.
`GET /api/jobs` reads the queue in three forms, so no client reads the whole
history on every refresh; each answers `{"available":true,"jobs":[…]}`:

- With no parameters, the current jobs, oldest first (history order): every
  queued or running job, every failed job not yet seen (`seen_at` unset), and
  the latest job of each subject for each kind (`Queue.Current`). A subject is
  a review group, or a pull request (repository compared without regard to
  case), or, for a repository's feature build, the repository with pull request
  0; a kind is the review's empty kind, `details`, `fix`, `chat` or
  `feature_build`. That is what the app's global list and the desktop tray read.
- `?repository=owner/name&pr=N`: every loaded job of that pull request, oldest
  first, without the jobs of a group (`Queue.OfPullRequest`). The PR page reads
  its fixes and questions this way.
- `?limit=N` and/or `?before=<job id>`: a page of the whole history, newest
  first (`Queue.Page`), from the newest job or just before the named one, at
  most `limit` jobs (1 to 200, 50 when only `before` is given), with
  `"next":"<job id>"` for the following older page while older jobs exist
  (omitted when none). The Jobs page loads finished jobs this way.

The forms are exclusive: `repository` with `pr` together, and neither with
`limit` or `before`. An unknown parameter, a missing or non-positive `pr`, a
`limit` outside 1 to 200 and a `before` naming no loaded job answer `400`.
`GET /api/jobs/{id}` is one loaded job wherever it is in the history (`404`
otherwise), for the transcript page of a job the current jobs no longer hold;
`GET /api/jobs/{id}/transcript` likewise finds any loaded job.

`POST /api/jobs` with `items` and `mode` starts reviews, a group's review or a
fix at once (`Queue.Generate`): it prepares the items and queues them in one
request, with no estimate to confirm and nothing about the price that stops a
run. Jobs carry no cost estimate (an `estimate_usd` an older baum journaled is
ignored), and a resubmission joins a queued job with the same inputs whatever
price list either was prepared under. The one confirmation is isolation for a fork or an author without known
write access (`confirm_isolation`; without it the answer is
`{"isolation_required":true}`), checked on the server, never trusted from the
browser. A single item the daemon will not run (a fix's `blocked_reason`) is
`409` with `{"blocked_reason":"…"}`, which the app says in words. Internally
`Generate` is `Estimate` (a preparation of the items, the name historical)
then `SubmitConfirmed`, which prepares again and refuses changed inputs;
there is no estimate endpoint.
Before any model call the executor rechecks a queued job (`Local.confirmed`).
A changed head, group members or isolation warning stops it, and so does a
changed GitHub account. A changed agent, model or limit does not: the run uses
the settings current when it starts, as one started then would. The account
is compared through `Prepared.account`, a hash of the credential partition
kept in memory only; a job read back from the journal compares the whole
`ConfigDigest`, which also covers the account. An automatic pull request
review whose head moved is cancelled as `superseded` (the new head has its
own run), and a group job whose group was deleted or whose stack dissolved is
cancelled as `group_removed`.
Fresh GitHub REST evidence authorizes the head and isolation warning; all
preflight I/O happens outside the queue mutex so cancellation stays responsive.
A job names a clone (`Item.Clone`). An item whose clone is unknown or
unavailable, which includes every repository picked without a clone, is
`jobs.ErrNeedsClone`: `POST /api/jobs` answers `409` with
`{"needs_clone":true}` (the isolation answer is `{"isolation_required":true}`),
and a queued job whose clone was removed ends with the reason `needs_clone`
without running (`cancelled` when automatic, so its head is not used up;
`failed` when manual). Pull requests of such a repository are still discovered.
The runner has no subprocess invocation of baum itself. Only the agent
backends and a fix's git commands run child processes, cancelled through their
context.

A single pull request's job, manual or automatic, writes its review in stages
(`generate.Options.Staged`, see "Staged review"); a review group's stays one
pass. While it runs, `generate.Options.OnDraft` hands the job each draft, the
review so far masked (`RedactOutput`) and completed (`CompleteStaged`) as a
version would be, with the sections not yet written `pending` and no context
until the context stage lands. The queue keeps only the latest draft of each
running job, in memory, counts them in the job's `draft` field (each draft is a
`progress` event) and serves it at `GET /api/jobs/{id}/draft` as
`{"draft": <count>, "document": …}`, under the same authentication as the rest
of `/api/jobs`; a job without one, or ended, is `404`. A draft is never a
version: it is not stored, listed or read through the document routes, and it
goes with the job when the job ends, the `draft` count back to zero. A daemon
restart interrupts the job, which runs again from its outline when it is
started again, as any interrupted job does. When the stages settle the job
publishes one version through `generate.Publish`, with any section still
without its details `failed` and without a context when the context stage
failed; the job fails only when the outline fails (or the run fails as a
one-pass run would). The job then records what the version lacks:
`failed_sections` (their IDs) and `context_failed`. The stages report their
calls through the job's tracked backend from their own goroutines, so its
counts are locked, and it forwards `agent.ConcurrentCaller`, as the pass
timeout's and `backend_paths`' wrappers do, so the details and the context run
at once where the backend allows it.

A section's Retry on the latest saved version queues a `details` job
(`jobs.KindDetails`, `{"clone", "pr", "kind": "details", "version"}`, single
mode). It is prepared like the pull request's review, with the backend and
model that wrote the version, and is refused (`409`, `blocked_reason`) with
`head_changed` when the pull request's head is no longer the version's (the
app offers to regenerate instead), `base_changed` when its base branch has
moved from the version's base commit (likewise), `version_changed` when the version is not
the latest, and `nothing_to_retry` when no section of it failed. It runs only
the details stage, for every section of the version published without its
details (`generate.Options.Continue`): the outline and context come from the
stored version (`review.StoredOutline`), its complete sections are reused as
they are, and the bundle is assembled again against the same head, base and
merge base (anything else fails the job before a model call), with the
version before it as the previous version, so completion reconciles risks and
sections as it did. The document keeps the version's number, model, prompt
version and generation time, and its metadata is unchanged. The write
rebases on the version as it is then: the sections it wrote are spliced into
the latest bytes by ID (`review.DocumentValidator.SpliceSections`), with the
risk score, the reading time and the risk line counts taken from the
completion, which derived them from the same sections, and the open agent
findings per section counted again, so a refresh in the meantime keeps its
newer checks, threads and sessions. The result is checked with
`ValidateStored` and replaces the version in place with
`store.Store.CompleteCurrent`, fenced like `RefreshCurrent` by the generation
identity, the exact previous bytes and the version being the latest; a
conflict reads and splices once more. A second conflict, a changed
generation, or a newer version stands the job down, `cancelled` with reason
`superseded`, and so does a moved head, base or replaced version when the job
starts (`head_changed`, `base_changed`, `version_changed`). A review queued
for the pull request at another head, manual or automatic, supersedes a
queued or running retry at once (`cancelled`, `superseded`); a retry never
makes the pull request busy or a head duplicate for the automatic scheduler.
It never writes the outline or the context again, keeps no agent session, and
a section that fails again stays `failed`. A failed retry's notice is
`details_failed`; a completed one has none.

`Queue.SubmitAutomatic` is how the automatic scheduler (see "Automatic
generation") queues a job nobody confirmed. It prepares the item like a
person's start, so the job has the same budgets, recheck before running and
isolation as a confirmed one, and refuses for good (`NotAutomaticError`) what
a person would have to confirm: a fork or an author without known write
access, and a draft. A model without a price, cursor-agent included, runs
automatically like any other. Under the queue mutex
it refuses with `ErrRestarting` while draining, `ErrBusy` while the PR (or the
group) has a running job or a queued one, except a pull request's (never a
group's) queued automatic review of another head (see below; it never joins
one),
`ErrDuplicate` when any job, manual or automatic, already exists for that head
(a group's: its member heads) (except a single
automatic job a daemon stop or restart interrupted, which `Interrupted` lists
and which may run once more; failed and cancelled jobs are final). An
archived job still counts for its head (`job_heads`), and is never retried:
any archived run of a head makes it `ErrDuplicate`. Jobs
record `automatic` and `trigger` (`new_head`, `review_requested`,
`ready_for_review`, `retry`, `stack_reviewed`, `setup`). A job an older baum cancelled with reason
`daily_cap`, for the spending cap baum no longer has, loads as an ordinary
cancelled job. The executor's recheck of the confirmed inputs, before any model call, also refuses an automatic job whose
pull request GitHub now reports as a draft (the queue marks its inputs
`RefuseDraft`; confirmed jobs run drafts); it is cancelled with reason
`draft`. Such a job does not count as its head's job: `ErrDuplicate` and
`Interrupted` ignore it, and `Refused` lists it while it is the latest job for
its head.
A new head of a pull request whose automatic review is still queued (not
started) replaces that review in place: the waiting job is prepared for the
new head and keeps its id, `created_at` and place in line, with the new
trigger, so the pull request is reviewed at its newest head however often
it is pushed, and no job of the older head is left behind. A group's
queued job is never taken over (it waits for its members, and its recheck
follows their moves). A running review of the old head is not stopped; the
new head waits for it (`ErrBusy`). `Queue.QueuedAutomatic` lists the
automatic jobs waiting to start, for the scheduler's bound.

`pricing` owns the shared pricing implementation and compiled baseline used by
the local product and hosted platform. Signature, tier and normalization tests
live with that implementation; `internal/pricing` is a stateless import facade
for local callers.
A bounded HTTPS downloader refuses redirects, verifies the existing release
signing key, rejects flattened lists and installs only newer full-format lists.
An installed download is stored with its signature in the `cache/pricing`
application database document.
The daemon re-verifies and installs it before opening the queue, so jobs are
priced with the newest verified list; an unverifiable file is ignored. A job
records the version it was prepared with as provenance only: a different
active list is not a changed input, neither `SubmitConfirmed`, the executor's
recheck nor the queue's dedupe compares it, and the run is priced, and its
saved review records, the list active when it runs. Preparing a job never
infers the default model or replaces it with a different priced model, and a
model without a price, or a backend billed outside the list (cursor-agent),
runs like any other. Unknown actual usage remains null.

### Inboxes

`internal/inboxes` keeps the inboxes people define, saved filters over the
desktop app's Up next queue listed under Inbox in the sidebar, in
the `state/inboxes` document in the shared `app.sqlite` application database. `/api/inboxes` serves the list
(`GET`) and replaces it (`PUT`, guarded by a revision so two windows cannot
overwrite each other). The daemon only stores them; the app matches the rules,
including `no_bots` against the pull request's `author_bot` from discovery.

### Review groups

`internal/reviewgroups` keeps review groups, named sets of 2 to 8 open pull
requests of one repository reviewed together, in the shared
`app.sqlite` application database in the state directory, not in a clone. Their combined
reviews are versions keyed by group ID in the same file, so they never collide
with a member's own versions in its clone's `versions.sqlite`; deleting a
group hides it and keeps every version. `/api/groups` serves the definitions
(create, rename, set members, delete), each with its latest version's
metadata, risk, the members that version covered and whether it is outdated
(other members, or a member head in the review index that differs from the one
reviewed), and a group's versions, documents and patches. A group's combined
review is written in one pass, never in stages, so its job has no draft.

Stacked pull requests are grouped by default. After every complete discovery
snapshot `localApplication` passes the repository's open pull requests to
`Store.SyncStacks`. It plans stacks with the hosted platform's rules
(`stacks.Plan`: pull requests by one author, each based on another's head
branch, as chains or trees; see `stacks/README.md`) over the live stack groups
and creates, updates or dissolves stack groups (`stack` true) of 2 to 8
members. A new stack group is named after its bottom pull request's title
until its first review names it (below). Pull requests in a person's group,
or in a stack group the person deleted, take no part, and a person's rename
or member change makes a stack group theirs.

A job item names `{clone, group}` with `pr` 0, so pull request scheduling and
notices, keyed by pull request, never match a group job; the queue's busy and
duplicate checks key a group job by group ID and member heads. Preparation reads every member from GitHub like a pull request
job (`Prepared.Members` holds each member's head, which the confirmation
covers) and needs a saved review of each member in the clone's store, or
answers `409 {"members_need_reviews": [...]}`. `Queue.Generate`, which starts
what a person asked for, instead sets `Item.AwaitMembers`: preparation accepts
members without a saved review of their current head (`Prepared.Awaiting`), and
their own reviews are queued with the group's job, except members whose review
is already queued or running in the group's clone. A group job does not start
while any member has a review or a fix queued or running (`membersBusy`). When
it starts and finds a member's head moved, often after a fix pushed, it does
not fail: `requeueGroup` prepares it again at the new heads, queues reviews of
the moved members that lack one of their new head (automatic ones for an
automatic group), and puts the job back to waiting, at most
`maxGroupRequeues` times (`Job.Requeued`). Only the heads may differ: the same
members, the same GitHub account and no isolation confirmation the job lacks;
otherwise, or past the limit, it fails with reason `members_changed`, and a
person's cancel during the requeue stays final. GitHub can lag a push by
seconds, so a preparation that does not show the move yet is read again a few
times, while the job keeps its worker slot. A member whose new review fails
leaves the group to read that member's previous saved review. If
a member still has no saved review when it runs, it fails with reason
`members_need_reviews`. Execution passes
`generate.Options.Group` and publishes through `reviewgroups.Publisher`.
`generate.Run` then assembles each member with the ordinary assembler (no
description images, an equal share of the checkout budget), merges the
bundles with `assemble.MergeGroup` (the first member's header titled with the
group's name and a body listing the members, every hunk once in member order,
the union of the other evidence, one `FileComparison` per member, refitted to
the caps), unpacks each checkout at `members/<number>/` with no top-level
`head/`, writes `members.json` (each member's saved review in brief) and runs
the group prompt. The model names the group in `stack.title`. Its decorator
sets `stack` (a title and each member's number, head and saved review
version) and each section's `pull_requests` from the members whose hunks it
lists, so the stored document is stack-shaped like a hosted stack review, with
`review.pull_request` the lowest member. The title is the model's name, on
one line and cut to 80 characters, for a stack group
(`generate.Group.GeneratedName`), and the group's name otherwise or when the
model wrote none. Storing the version renames a stack group to its title, in
the same transaction and only while the group is still a stack group, so a
person's rename is never written over. The group page reads patches from the member whose comparison
changes the file and whose patch holds the cited hunk's range; it lists no
linked sessions or commits.

### Fix jobs

A job has a kind: a review (`Item.Kind` empty) or a fix (`"fix"`), which
shares the queue, journal, concurrency, progress events and cancellation with
reviews and shows in the same Jobs list. A fix starts alone, at once
(`POST /api/jobs` with `{"items":[{"clone","pr","kind":"fix"}],"mode":"single"}`;
`"only":"checks"` on the item, the Inbox's Fix CI, limits it to the failing
checks: preparation leaves `conflicts` false and reads no review threads, and
any other `only` is refused);
one in a batch is refused, and automatic generation never queues one. A review and a fix of one pull request
are neither refused nor superseded by each other, though they never run at the
same time (the queue starts no job while another of its pull request runs), and a fix of a head does not count as
that head's review for the automatic scheduler.

`jobs.Local` prepares a fix from fresh REST evidence (`internal/fix`):
`/pulls/{n}` for the head, its branch and repository, the base branch and
`mergeable` (read again once after 1.5 s while GitHub has not computed it),
`/repos/{o}/{r}` for `permissions.push`, and the head's failing check runs and
commit statuses. On a pull request the signed-in account opened
(`viewerDidAuthor`), it also reads the unresolved review threads with one
GraphQL document (`FixReviewThreads`, up to four pages of 50 threads with the
first 10 comments of each; `github.Client.UnresolvedThreads`) and keeps,
through `fix.SelectThreads`, those the account can reply to and resolve whose
first comment comes from an `OWNER`, `MEMBER` or `COLLABORATOR` or a bot, at
most 20, dropping other authors' comments and bounding each body to 2 KiB.
`Prepared.Fix` records what it fixes (`conflicts`, `checks`, and `threads`, a
digest of the kept threads and their comments) and the branches;
`fix_threads` keeps the threads' places and excerpts for the job, with
`fix_threads_capped` and `fix_threads_untrusted`. A fork, an account that
cannot push, a backend that cannot
fix, or nothing broken is the item's `blocked_reason` (`fork`,
`no_push_access`, `unsupported_backend`, `nothing_to_fix`): the start answers
`409` with it and nothing is queued, and the app says why where the person
clicked. The budgets resolve as a review's, with `run_timeout` as the time
limit. The harness and model are the
preferences' fix agent (`fix_backend` and `fix_model`, Claude Code or Codex,
set with `PUT /api/automatic` or `baum internal reviews set --fix-backend`) when one
is chosen (`Preferences.ResolveFix`), and otherwise the review's. Before running, the executor prepares
again and refuses a changed head (`head_changed`) or changed fix inputs, then
reads the failing checks' output and up to four GitHub Actions job logs
(`RESTReader.Download`, which follows the signed log redirect without the
token). Of each log it keeps the last 200 lines up to the end of the last step
that logged an `##[error]` line, preferring the job's steps over its post-job
cleanup, without timestamps or terminal escapes (`fix.jobLog`); a log without
an error line gives its last 200 lines. It then reads the review threads
again, refusing threads whose
digest changed (`threads_changed`).

`fix.Run` then works in a `gitlocal.Fix`: a linked worktree of the
registered clone under the user cache directory's `baum/fixes/<id>`, detached
at the head fetched into `refs/baum/fix/<id>/` from the remote whose URL names
the repository (`ErrHeadMoved` when the branch moved on). For a conflict it
merges the base commit itself; a clean merge needs no agent. The agent
(`agent.Fixer`) runs with the prompt, the untrusted data between nonce-tagged
markers, and the run budgets. baum then checks that no conflicted file keeps a
marker, commits what was left (completing the merge), requires a new commit
that descends from the head, and pushes it with a plain `git push` to the head
branch. Phases are `reading checks`, `reading review comments`, `fetching`,
`merging`, `fixing`, `committing`, `pushing` and `replying`. A finished fix
records `commit`, `commit_url` and the agent's masked `summary`. A fix whose
agent changed nothing completes without a commit, with reason `no_changes`
and its `summary`, which says why: it is the agent's judgement, not a
failure, so it sends no notice and the app shows that summary in neutral
words (jobs saved as failed by an older daemon are shown the same way). A
failed fix records a reason (`head_changed`, `threads_changed`, `no_remote`,
`git`, `backend`, `budget`, `conflicts_remain`, `history_rewritten`,
`workflow_changed`, `push_rejected`, `push_failed`, `signed_out`) and a
bounded detail. `signed_out` is an agent failure whose provider refused its
sign-in (`agent.SignInRejected`); review and chat jobs report the same reason
in place of `backend`. A
`budget` detail names the limit the agent reached (its time limit, in
minutes, or its call or token budget); a `backend` one points to the daemon
log, where `fix.Options.Logger` (the job's logger) has the agent's own error,
which the job never keeps. A
fix of review comments alone fails with `workflow_changed` when its commit
changes a path under `.github/workflows/` or `.github/actions/`.

A fix can also be made from a chat conversation. The estimate item carries
`fix_chat: {conversation_id, turn?}` (`jobs.FixChatRef`; `fix_chat` rather than
`chat` because a job's own `chat` is the turn a chat job answers). The app sends
only ids: `fixChat` (`internal/jobs/fixchat.go`) reads the conversation from the
clone's review store, for the item's pull request only, so another pull
request's conversation is `chat_unavailable`, as is one without an answered turn
or a `turn` that was not answered. It takes the answered turns up to and
including `turn` (the newest answered one when zero) and builds `fix.ChatContext`
from them: question as stored (unmasked), the stored masked answer markdown, the
citations as `path:start-end` and `suggests_fix.summary`. `fix.CapChat` caps it
at 32 KiB, dropping the oldest turns first and cutting a turn only when the newest
alone exceeds the cap, with a marker whenever its question, suggestion or
citations were cut too; turns dropped are counted (`ChatContext.Omitted`) and
the prompt says so. A fix from a conversation is eligible under the conditions
of any fix (open, not a fork, `permissions.push`, a supported backend, a
registered clone) and is chat only: `prepareFix` reads no checks, mergeability
or review threads for it, so it never merges the base, fixes checks or replies
on threads, and `nothing_to_fix` does not apply. The conversation must be
about the newest stored review version of the pull request (`chat_outdated`). `Prepared.Fix` records
`chat`, `chat_turn` and `chat_digest` (`fix.ChatDigest` of the capped context),
and the prepared item records the turn it resolved. The turn is pinned: the
confirmation re-prepares the same item, so execution reads the conversation up
to the estimated turn even when later turns arrived, and a stored conversation
that differs from the digest (`chat_changed`) stops the fix before the agent runs.
`fix.Options.Chat` adds a section to the prompt (`agent/prompt/fix-chat.md`: make
the change the conversation asks for and only that, verify it, the conversation
is data) and one `BEGIN UNTRUSTED DATA` block per turn, oldest first, each
through the redactor (questions included) and with the nonce stripped, like a
thread. A fix from a conversation never pushes a workflow change
(`workflow_changed`).
The commit message says "Make the change from a conversation". The phase is
`reading the conversation`.

A fix also keeps a live transcript of its agent's run for the app: the
messages, reasoning, tool calls and tool output, taken from the harness's own
event stream as it is already parsed (`agent.Request.Transcript`, entries of
`agent.TranscriptEntry`; deliberately apart from `agent.Event`, which never
holds text). The queue gives the executor `transcripts/<job id>.jsonl` beside
`app.sqlite`, created private (0700/0600), one `{"seq":N,...}` line per entry,
appended and flushed as each arrives. Title and text are masked with the bundle
redactor and cut (1 KiB, 16 KiB); the file stops at 8 MiB with a final error
entry, and only the 50 most recently written transcripts are kept. A tool
appears as a `running` entry and again, with the same `id`, as `completed` or
`failed` with its output; its title names a path inside the checkout relative
to it (`agent.TitlePath`). `Job.has_transcript` says one exists, and
`GET /api/jobs/{id}/transcript?after=N` returns `{"entries","next","more","done"}`
(at most 500 entries after `seq` N; `404` without a transcript). Transcripts
never leave the machine.

`POST /api/jobs/{id}/seen` (body `{}`) records that the user looked at a failed
job: it sets `Job.seen_at` once, in the job's row of the queue's journal, and
leaves jobs in any other state unchanged (`404` for an unknown job). A failed
job never runs again, so the desktop tray never flags one with `seen_at`.

Review threads reach the prompt as untrusted blocks headed with local ids
(`T1`…`T20`) that baum maps to the GraphQL node ids, which the prompt never
contains. The agent ends its final message with a JSON array of
`{"id","addressed","summary"}` in a code fence whose info string is
`baum-threads <nonce>`, the nonce of the data markers. `parseAnswer` takes the
last such fence, which must close the message, decodes it strictly (unknown
fields, wrong types or trailing data answer nothing), ignores unknown and
repeated ids, masks secrets in each summary and reduces it to one plain line
of at most 200 characters without links, URLs, HTML, `@` or control and
format characters; the job's `summary` is the message without the block.
After a successful push, and only then, `fix.Run` replies on each addressed
thread through `github.Client`: it reads the thread again by node id
(`ReviewThreadState`) and skips one now resolved (`already_resolved`) or not
on this pull request or not open to the account's reply (`failed`), posts
`Addressed in <short sha>: <summary>` with `addPullRequestReviewThreadReply`,
then resolves it with `resolveReviewThread`. Each document is sent once,
never retried. The job's `thread_outcomes` hold each thread's `resolved`,
`replied` (the reply posted, the resolution not), `skipped`,
`already_resolved` or `failed` with the agent's cleaned summary; none of these
fails the fix.
The worktree, its registration and the refs are removed whatever happens.
After a push the daemon polls the repository's discovery at once. Git commands
in the checkout set `GIT_DIR` and `GIT_WORK_TREE` and an empty
`core.hooksPath`; see [SECURITY.md](SECURITY.md#fixing-a-pull-request).
Before the merge and the agent, `Fix.CheckSigning` makes an unreferenced
`commit-tree -S` commit when `commit.gpgSign` is set (commit-tree ignores the
setting), and a failure is `fix.ReasonSigning` with git's error.

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
from registered local clones. With no saved version, it passes the pull
request's facts (see "Pull request facts") to the viewer's `facts` prop, whose
Details and Activity tabs draw them with the document's parts
(`FactsDetails`, `FactsActivity`), and fills the Sessions, Commits and Files
changed slots from the facts' routes; the version menu, history and chat
never see them. While a review of the pull request runs in stages, the page
reads the job's draft (`GET /api/jobs/{id}/draft`, again on each change of
its `draft` count): without a saved version it shows the draft as soon as the
outline lands, sections still being written as "Writing details…" and the
Summary's context as being written (`contextPending`), and the facts before
that; with one it keeps the saved version, with the regeneration's status and
a "View draft" toggle. A draft has no version menu, chat, walkthrough,
approvals or read ticks. A saved version published without some sections'
details or its context says so in a short notice, and the latest version's
failed sections offer Retry (`onRetryDetails`), which queues a details job
(see "Persistent generation jobs") whose sections read as being written while
it runs; a pull request whose head moved is told to regenerate instead. The
renderer stays in `viewer/`; the app owns
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
The first probe that succeeds after one failed, as when the machine comes back
online, polls every repository whose last poll failed (other than on a rate
limit) at once, so the failure clears without waiting out a backoff that grew
while offline.
Every picked repository is polled, with or without a clone: discovery needs
only `owner/name`. A repository without an available clone gets no per-PR
refresh (that reads a store and the clone's saved reviews), and polling it
still feeds `Service.Discovered`. A pass polls every due repository first and
publishes an index change, so no repository's pull requests wait behind
another's reads. It then reads, within `refreshBudget` (30 s), the saved
reviews that are due; open pull requests without one are left to discovery.
An open one is due when discovery shows a newer `updated_at` (compared with
GitHub's own time when the last read was due), another head, another check
rollup or another unresolved thread count than at the last read, and otherwise
once unread for `openReadAge` (2 h). A saved review whose pull request left the
open ones gets one final read while its document still says open or draft;
once the published document records `merged` or `closed` it is retired and
never read in the background again, until discovery lists it open again. A
closed draft's document keeps `draft`, so it retires once a final read in this
process succeeded, and each new process reads it once more. Changes go first; then the longest
unread. A failed read is retried after the interval, doubling, without backing
off the repository's polls; after `maxReadFailures` (3) in a row it waits for
the pull request's `updated_at` or head to move (an open one also after a day),
for a refresh the person asks for (`Service.Asked`, which also reads the
repository's open saved reviews once), for GitHub answering again if it failed
while the probes did, or for the next process. A rate-limited one stays due and
goes first after the reset, so a small renewed budget still reaches every pull
request. Reads are kept in memory; in a new process (or identity) those due
by age or check rollup, and final reads, are spread over `restartSpread`
(30 min) by a hash of the pull request. The reads make at most
`refreshRequestsPerHour` (1,000) GraphQL requests an hour
(`github.CountRequests`), and wait while discovery's ledger shows fewer than
`refreshReserve` (1,000) GraphQL points left or a secondary limit
(`discovery.Service.GraphQLBudget`). Each GitHub request of a read may take
30 s (`github.WithRequestTimeout`); only a read that never ends stops at 10
minutes, and `Fetch` reads a pull request's parts three at a time. `Run`
passes every 30 seconds, or sooner when a repository is due sooner, as a
settling one is. Linked worktrees are
deduplicated by common git directory. Saved reviews use `refresh.Refresher`
and `Store.RefreshCurrent`'s exact-byte CAS, which writes nothing when only the
refresh time changed. A read counts as the last read whether or not it wrote,
so an unchanged pull request is not due again until its evidence is old.
After a pass, `Service.Retain` reconciles the retained review refs of each
due common git directory (see "Retained review commits").
`Service.RefreshNow` refreshes named PRs of one clone the same way outside
that schedule (see "Refresh on hook events"), retired, stopped or past the
hour's budget, and its reads count as the last read; it shares the per-host rate pause with `Sync` in both directions,
as the PR page's facts do (`Service.Paused` and `Service.Limited`).

Discovery's live freshness map uses its own short mutex; cached readers never
wait behind a network poll. Disk-only startup remains stale. Minimized current
head/state/title/check observations share the private credential-partitioned
cache and augment the index, including historical PRs. A newer discovery result
wins over older observations. Authenticated `/api/background` and index events
report refresh health without filesystem paths, credentials or remote bodies.

Local generation stores additive `agent.Accounting` in immutable version
metadata and the durable queue. Accounting retains disjoint provider token
buckets and available per-call model identity, speed, TTL and prompt range.
Retries and repairs append reports; partial or absent reports are marked
`Unreported`. While a run is in progress, the backend reports its calls' usage
so far (`agent.Request.Usage`: Claude Code per stream event, Codex per turn,
the Anthropic API per response), and the queue adds it to the accounting it
publishes, at most every two seconds, as `job` events (see "Authenticated local application host").
Queue progress is saved before SSE publication and snapshots detach mutable
accounting. A progress report is written without holding the queue's lock,
so job lists and the other workers do not wait for the disk; reports that
arrive while one is written are written together next, in order. A job list
may show a report while it is being written, which a crash at that moment
loses; a published one it never loses. State changes are still written under
the lock. Existing version records are never backfilled. Pricing sums
individually attributable model reports and estimates the rest at the lowest
rates they could have been billed at, with a fixed reason marking the cost a
lower bound; the cost is null only when nothing can be priced. The local SPA renders both job accounting
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

The top-level help lists `hooks`, `daemon` and `desktop-setup`. The hidden
`internal` group contains only `repos add`, `reviews show/set`,
`github-credential` and `doctor`, which support desktop onboarding, credential
storage and diagnostics. Engine packages remain callable by their hosts and
package tests, with no standalone CLI wrappers.

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
a busy one (a job queued or running) answers with `restart_pending`, starts
no further queued job, and once `DrainRunning` finds none running starts
that executable detached on the same port and stops, so no confirmed run is
interrupted and a queued backlog does not hold the update back: the new
daemon runs it. Of racing clients the newest version is the one it restarts
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
client had asked. Until then, on Linux, the GitHub credential helper still
works: the daemon starts it from `/proc/self/exe`, the binary it is running,
and the helper compares that with its parent's, so neither side looks at the
replaced path. A failed
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
its files and releases the lock. Background work and control requests get 10 s
more between them; one that ignores its cancellation (a stalled network read)
is left behind and logged, so a stop ends well inside the client's 35 s wait.
The client waits until the lock is free.
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
the shell's process group is killed. When the interactive shell prints no
environment before the timeout (a `.bashrc` that runs `exec tmux` exits with
"not a terminal"), the same command runs once more as `-l -c`, a login shell
that is not interactive, within the same 10 s; the result then carries why
(`Result.Fallback`), which the daemon logs and `baum daemon login-path`
reports as `warning`. `shellenv.Merge` then keeps only:
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
outcome, how long the shell took and the kept variable names, never values;
the `daemon ready` line records how long the daemon took from holding its
lock to serving, shell included, since it listens only afterwards. `backend_paths` remains the
way to name a backend executable explicitly.

Some shells set an agent up only for interactive use (fnm or nvm in
`~/.bashrc`), so the login shell's `PATH` holds an older copy or none. `baum
desktop-setup`, which the user runs in their own terminal, finds `claude` and
`codex` on that terminal's `PATH` (`internal/agentcli`), prints each one's path,
version and whether it is at least the backend's `MinimumVersion`, and writes
`terminal.json` beside `config.toml`: per agent, in the terminal's `PATH`
order (one agent's directory may hold an older copy of the other), the
executable and its directory with symbolic links resolved (fnm's per-shell `fnm_multishells`
directory vanishes with the shell). `agentcli.WithRecorded` puts the recorded
directories that still exist first on a `PATH`; the daemon applies it after
`shellenv.Merge` and, through `agentcli.Keep`, again within 15 s of the record
changing, and `baum daemon login-path` applies it too, so every lookup of
`claude`, `codex` and the `node` an npm-installed agent starts finds what the
terminal runs. The live-session Codex queue looks Codex up once at start, so it
follows a new record after the daemon restarts.

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
  `repository_automatic_off`, `repository_removed`, `settings_unreadable`);
  every pass
  runs it, and `PUT /api/automatic` runs it before answering. Running jobs keep
  running.
- **Stopped before start.** Each pass asks `Queue.Refused` for automatic
  jobs stopped before any model call without using up their head (stopped as
  drafts, or because their clone went away) made after the baseline and puts
  each one's head back as pending once (`refused` records the job) if the PR
  is still open, eligible and at that head.
- **Baseline.** Enabling is dated when a pass or snapshot first sees it. The
  first snapshot `Observe` gets that was fetched at or after that time records
  each open PR's head, whether the user's review is requested, whether team
  membership was readable and whether it is a draft, and queues nothing: PRs
  open at first launch, registration or re-enabling are not caught up
  (except by the setup catch-up below). An older
  (cached) snapshot is not a baseline; the next poll's is.
- **Triggers.** Afterwards, a PR whose head differs from the recorded one (a
  new PR included), whose review request turned on (not merely because team
  membership became readable), or that was marked ready for review, gets a
  pending head if it is eligible: the user's review is requested or the user
  authored it (narrowed to one of these, or widened to every pull request, by
  the `pull_requests` preference), it is not a draft, it is not from a fork,
  and its author has known write access. Hook refresh applies the same rule
  before it triggers a poll. Drafts get no automatic review, so pushes to a
  draft wait for "ready for review"; manual generation of a draft still works.
  A queued automatic job whose PR turned draft before it started is cancelled
  (`draft`) by the recheck before any model call, and its head is pending
  again (trigger `ready_for_review`) once a snapshot shows the PR eligible at
  that head. A PR
  that closes, moves to another head while ineligible, or stops being eligible
  loses its pending head.
- **Setup catch-up.** `POST /api/automatic/catch-up`
  (`Scheduler.CatchUp`), which the desktop app sends once when first-run
  setup finishes, answers 202 and then, apart from the request, records the
  enabling of each repository automatic generation covers, polls it at once
  (that poll is its baseline when it had none, and goes through background
  discovery's handling, so the stack groups are current) and makes the three most
  recently updated eligible open pull requests across them pending with
  trigger `setup`, unless their head is already pending or handled. A pull
  request in a stack group counts with its group as one, and the group's
  eligible members are made pending together, so the group's review follows
  theirs. The pass submits them like any other pending head: a head already
  reviewed is skipped, the switches and a restart's drain apply, and an interrupted run
  is retried once. A repository whose poll fails is left out.
- **Interrupted runs.** Each pass asks `Queue.Interrupted` for automatic jobs
  a daemon stop or restart cut short that are the only job for their head; if
  the PR is still open at that head and eligible, the head is pending again
  with trigger `retry`, once (`retried` records it). The retry goes through
  the same pass, so a restart's drain and the switches apply. Failed and
  cancelled runs are never retried; the next push triggers as usual. A run
  interrupted before a repository's baseline (turned off, then on again) is not
  retried: the baseline marks its heads as already retried.
- **Stack groups.** After the pull requests, each pass looks at the
  repository's stack groups (`Scheduler.Groups`). Once every member is
  recorded, eligible and has no pending head, and every member's saved
  review is of its current head, it queues the group's review (trigger
  `stack_reviewed`) with those member heads, unless the group's latest
  version already read every current member at its current head (a merged
  member alone calls for none). Automatic generation must have handled at
  least one member's current head (its `done`): a stack reviewed before the
  baseline, or only by hand, is not caught up. Each set of member heads is
  submitted once (`done`), with the pull requests' retry rules and triggers:
  failures to prepare back off and give up after five attempts, an
  interrupted run is retried once (`retry`), and a job stopped before it
  started is submitted again (`ready_for_review` after a draft). A
  member GitHub shows moved on or turned draft before discovery does is
  retried a minute later. Groups a person made are never generated
  automatically.
- **State.** The `state/automatic` document in the application database holds each
  enabled repository's PRs (recorded head, request and draft flags,
  eligibility, pending head and trigger, when the PR started waiting for it
  (`waiting`, kept when a newer head replaces a pending one), last head handled,
  retried head, last stopped job put back), its stack groups (member heads handled,
  attempts, retried heads, last stopped job put back, trigger) and its notice. A `paused_until` and
  `paused_cap` an older baum saved for its daily cap are ignored on load, and
  its `daily_cap` notice is dropped. It is rewritten
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
  with the reason `needs_clone` before any model call, like a draft:
  `Queue.Refused` lists it, and the head is pending again, so the first pass with an available clone submits it once.
- **Passes** run at start, every 30 s, when a snapshot leaves pending work,
  when settings change and when a job finishes. A pass submits due pending
  heads, the PRs that have waited longest first across repositories (then by
  repository and number), unless the daemon is stopping or has a restart
  pending (`daemon.Self`); the new daemon's passes submit those heads. It leaves at most two
  automatic jobs waiting in the queue for each job the queue runs at once
  (`job_concurrency`, so 8 by default); further heads stay pending for a
  later pass, where a newer push simply replaces the pending head. A head
  that its PR's waiting automatic review takes over is submitted whatever
  the bound, since it adds no job. Stack group reviews and the member reviews a
  group queues are not bounded. A head whose latest saved review
  already has it is skipped. Outcomes: queued, or `ErrDuplicate`, marks the
  head handled; `ErrBusy` waits for the PR's job; `ErrRestarting` ends the
  pass and is retried; `ErrHeadChanged` keeps it pending, in its place, and
  tries it again a minute later, by when discovery has usually brought the
  new head, which replaces it; a `NotAllowedError` (turned off meanwhile)
  leaves the head for the next pass, which forgets it if the repository stays
  off; a `NotAutomaticError` (a draft, a fork or an author without write
  access) drops it with its reason as the notice; other errors retry
  with backoff, five times.
- **Spending.** There is no spending cap: baum cannot price what a
  subscription bills, so no price limits automatic generation. Each job keeps
  the configured token and call budgets. A model without a price,
  cursor-agent included, needs no acknowledgement.
- **Interfaces.** `GET /api/automatic` returns the preferences and each
  repository's effective state, notice and pending count; `PUT /api/automatic`
  changes the preferences. A changed notice, and every repository's status after a `PUT`, is published
  as an `automatic` SSE event for other app windows and the desktop app's
  notifications. `Trigger(ctx, repository)` polls discovery for one
  repository (`PollNow`: before discovery's poll deadline, within its rate
  budget) and passes the snapshot to `Observe`. Hook events reach the
  scheduler only this way (see "Refresh on hook events").

### Hook events

`internal/hookevents` carries what `baum hooks agent` and `baum hooks git`
report to the daemon; the hook side is in `internal/hookcli` (see its
[README](internal/hookcli/README.md)). Events stay on this machine.

- **Event.** One JSON object per hook run, sent as the params of the `event`
  control request: `v` (the event format, `hookevents.Version`, 1), a random
  `id`, `time` (when the hook started), `kind` (`agent` or `git`), `agent`, `name` (the agent event or
  Git hook), `dir`, `exit` (the hook's status), `client` (baum's version), and
  `session` (an agent payload's top-level `id`, `transcript_path`, `cwd`,
  `hook_event`, `source`, `reason`, and for Claude Code's `notification` its
  `notification_type` and `message`, cut to 512 bytes, read by a streaming
  scanner from the whole payload, the last of a repeated member winning, and
  for Claude Code's and Codex's `post-tool-use` and `post-tool-use-failure`
  the `tool_digest` of the call, and a Claude Code subagent's `agent_id`
  (see "Agent requests and messages"); nothing else of it) or `git`
  (`args` without a pre-push remote URL, and `lines`, the refs or commits Git
  wrote to the hook). An agent's `session-start`, `user-prompt-submit` and
  `before-submit-prompt` also carry `terminal` (`focus.Capture`): `env`, the
  variables of `hookevents.TerminalEnv` the hook had (tmux, kitty, iTerm2 and
  Terminal identifiers, `CLAUDE_CODE_ENTRYPOINT` and the display variables),
  `tty`, the controlling terminal of the nearest ancestor that has one (agents
  start hooks without one), and `processes`, the hook's ancestors, parent
  first, as `pid`, `start` (`/proc` starttime on Linux, `kern.proc.pid` start
  time on macOS) and `name`, at most 24 (see "Focus" under "Live sessions").
  Git's `post-commit`, `post-rewrite` and `pre-push` run from dispatchers the
  daemon installs in registered clones (`internal/githooks`: they chain a
  hook the clone had, saved as `<hook>.pre-baum`, are skipped in a clone
  with `core.hooksPath` and are taken out again for a clone that sets `git
  config baum.hooks false`, which also makes `--check-dispatcher` decline, or
  leaves the registry; where the repository uses Entire (`.entire/settings.json` in
  the default branch's tree, or checked out in any worktree, or git cannot
  say), legacy dispatchers that run Entire's hooks are left, elsewhere the legacy `commit-msg` and
  `prepare-commit-msg` ones are retired). They carry the session
  their agent names in its environment (`CLAUDE_CODE_SESSION_ID` becomes
  `agent` `claude-code` and `session.id`, with `session.agent_pid` from
  `CLAUDE_PID`, only when that process is among the ancestors and is named like
  the agent; no other
  variable is kept) and `terminal` with `processes`
  alone (`focus.Ancestors`), so live sessions can tell which agent committed or
  pushed when no session is named. A `post-commit` carries `git.head` and
  `git.branch` (empty when detached), the commit and the branch it was made
  on, read with one `git rev-parse` as the hook ran, and a `post-rewrite` its
  `amend` or `rebase` argument and the `old new` pairs in `git.lines` (see
  "Commits and pushes" under "Live sessions").
  `Event.Bound` cuts fields at character boundaries and keeps only
  `TerminalEnv`, then trims Git lines, drops `terminal` and halves the longest
  fields until the escaped encoding fits one control line. The daemon ignores fields it does not know and records
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
  the newer version, a busy one restarts as it when its running jobs finish. A daemon
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
  sessions"), refresh on session end or push (see "Refresh on hook
  events") and decision capture (see "Decision capture") do.

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
  job, so the scheduler's baseline, eligibility, dedupe, hold, gate and
  no-catch-up rules apply unchanged, and a head it already saw queues
  nothing. Since background refresh and Trigger observe from two goroutines,
  `Observe` ignores a snapshot fetched before the newest one it took for the
  repository. The worker never queues or runs generation.
- **Stopping.** The worker stops with the daemon's run context, cancelling a
  refresh or poll under way. Its schedule is in memory only: a restart drops
  pending refreshes, which the periodic refresh covers.

### Live sessions

Codex's `PermissionRequest` runs before automatic review. The hook reads the
latest rollout `turn_context` matching the payload's `turn_id`; when its
`approvals_reviewer` is `auto_review`, the local event carries `auto_review`
and the hook does not ask the app for a decision. The tracker ignores that
permission event as a wait, so it sends no attention notification. Unknown
context retains manual approval behavior. The read searches backwards through
at most 64 MiB with bounded memory and skips oversized transcript lines.

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
  `post-tool-use-failure`, `pre-compact`, `subagent-start` and
  `subagent-stop` to `tool` (the hooks each
  agent has, so Claude Code reports subagent and todo activity, Codex tool
  runs, and Cursor prompts and compaction; the user-wide Claude Code plugin
  also reports every tool call as `post-tool-use`); `notification` (Claude
  Code) and `permission-request` (Claude Code and Codex) to `wait`; `stop` to `stop`;
  `session-end` to `end`. Other names are ignored, so an older daemon ignores
  the wait events a newer plugin sends, and nothing changes for a newer
  daemon whose plugin sends none. A session is keyed by agent and id; an id
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
- **States.** `running` after a start, prompt or tool event. `idle` while it
  waits on its user, and for a running session silent longer than
  `IdleAfter` (10 minutes): an agent that crashed sends nothing, and one
  thinking for long reports nothing either.
- **Waiting.** A session waits on its user for a `waiting_reason`, from
  `waiting_since`: `permission` after a Claude Code `permission_prompt`
  notification or a Codex permission request; `input` after a notification
  asking a question (`elicitation_dialog`, `elicitation_url_dialog`,
  `agent_needs_input`, `quota_auto_resume_stale`) or of a type it does not
  know or with none; `turn_finished` after a `stop`, which agents send at the
  end of every turn, and after Claude Code's `idle_prompt`, which repeats a
  stop a minute later and never replaces a question asked since.
  Notifications that ask nothing (`auth_success`, `elicitation_complete`,
  `elicitation_response`, `agent_completed`, `quota_auto_resume_fired`,
  `quota_auto_resume_disabled`, `plugin_notification`) are ignored. The same
  reason again keeps the wait (and its start); a start, prompt or tool event,
  or the end, clears it, except a `subagent-stop`, which is ignored while the
  session waits: Claude Code runs a subagent after every stop (its
  SubagentStop arrives a second or two after the Stop), and a background
  subagent can finish while its session waits; and a `session-start` whose
  `source` is `compact`, also ignored while it waits: Claude Code compacts a
  session that has sat waiting a while (seen about an hour after its stop) and
  no stop follows. The list (`GET api/live-sessions`) settles a session silent
  past `IdleAfter` with no reason (`settle`): it stays `running` while a tool
  call it made has no result in its transcript (a Claude Code `tool_use` with no
  `tool_result` naming its id, a Codex call with no `*_output` for its
  `call_id`; a subagent or a long command reports nothing until it returns),
  and otherwise waits as `quiet` from its last activity: it stopped without a
  stop (Claude Code sends none for an interrupted turn) or its agent died. A
  prompt typed after an unanswered call ends it; results are matched by the id
  that opens their line, so one longer than a line read whole still counts. The
  tracker's own state stays `idle`, so the desktop's notifications, which ask
  only about `permission` and `input`, never announce it. A Claude Code or Codex
  session with no title and no activity never had a conversation (opened and
  left, or used only to resume another; Claude Code writes no transcript until
  then) and is left out of the list (`empty`). Since agents report few
  events during a turn, `Transcripts.Watch` builds the list every second
  while a session is running and every 15 seconds otherwise. Applied hook
  events and app-driven resume or end changes wake the watcher to shorten
  its wait when a session starts running; repeated events never postpone a
  scheduled read. It reads a working session's transcript (one neither ended nor waiting)
  when it grew and the others' no more often than every 5 seconds, only what they appended, and
  publishes `live` when it differs from the last one: a running session's
  steps, files and message, and a session settling, reach the app without a
  hook event, what an agent says within a couple of seconds.
  **Replying and ending.** The list marks a session `messageable` when it
  waits at its prompt (a finished turn, or for Claude Code gone quiet) and
  `endable` when it waits or went quiet, each only where baum can reach its
  agent (`Transcripts.Senders`, `Transcripts.Enders`), and `endable` only
  where its agent's end check (`Transcripts.EndChecks`, begun once per list,
  `ClaudeProcesses.EndCheck`) says ending would go through, so End is never
  offered to fail. `POST
  api/live-sessions/{agent}/{id}/message` with `{"text"}` (at most 10 000
  characters) puts it into the session: Claude Code's through its cross-session inbox
  (`ClaudeInbox`, one JSON line on the socket its
  `~/.claude/sessions/<pid>.json` names; macOS and Linux), which starts the
  next turn in the session's own terminal, opened with a line saying the user
  sent it from baum (Claude Code tells its model an inbox message came from
  another session); Codex's with `codex queue --thread
  <id> --message` (`CodexQueue`, when `codex` is on PATH), the text alone,
  which Codex takes as the user's own turn, offered after a
  finished turn only, since a quiet Codex session may have no terminal left
  and the queue would run its turn unseen. `POST .../end` ends a Claude Code
  session as `/exit` does (`ClaudeExit`): SIGTERM to its process when that
  reports itself idle; it exits in under a second, fires SessionEnd and can
  be resumed. A process that reports `busy` is mid-turn, and one that
  reports `shell` has finished its turn but still runs a background command
  it started, which ending would kill: neither is offered End, and a request
  for one is refused with 409 (`session_running_command` for the second,
  saying so). The list marks the second `end_blocked: "running_command"`, and
  the app's menu shows its End disabled, noting the command still runs. Codex has no clean way to close a session from outside, so
  none is offered. Both find the process by its registry entry, confirmed as
  the one that wrote it (`procStart` against `/proc/<pid>/stat` field 22 and
  `pidDomain` against this pid namespace on Linux, `ps -o lstart=` on macOS),
  since an entry a killed session left can name a reused pid; a session with
  no such process is recorded ended (`Tracker.Gone`), one baum cannot confirm
  is left alone. The list's end check and the tracker's `Alive`
  (`ClaudeProcesses`, shared by both) read the registry once per look for
  every session they ask about, and trust a process they confirmed while
  `kill(pid, 0)` finds it and its entry still names the same pid, start and
  namespace, for at most `ReconfirmEvery` (30 seconds), so a listing forks no
  `ps` per session each second; a process is gone the moment its pid is, and
  only a killed session whose pid another process took between two looks can
  be trusted that long. Sending and ending confirm the process themselves
  (`FindClaude`), with no cache, just before they reach it. On Linux the
  daemon's pid namespace and machine id are read once.
  The session's `last_prompt` is the latest thing the user
  wrote to it: a prompt, a message the app sent (which Claude Code records as
  a peer's meta line), an answer to its question (`toolUseResult.answers`) or
  a message sent while it worked (a `queued_command` attachment, read from the
  start of a line an image makes too long), but not what the harness queues
  the same way: a background task's `<task-notification>` or another agent's
  `<agent-message>`, such as a subagent's report. Neither route runs on
  Windows.
  A notification's `message` becomes
  `waiting_message`: format characters dropped, control characters and runs of
  spaces made one space, masked as transcripts are (`transcript.Renderer.Mask`,
  before it is cut), and cut to 160 characters. Claude Code says nothing when
  a permission is given, so the user-wide plugin's every-tool-call hook (run
  in the background, so the agent never waits for it) is what clears a
  permission wait: at the end of the tool the user allowed. A Codex wait is
  cleared by its own tool event. `Tracker.Waiting` is told of each
  `permission` and `input` wait of a shown session once, as it begins (a new
  session's once it is linked), never of a finished turn, of the waits a
  rebuild replays or of one that began more than `AnnounceWithin` (5
  minutes) ago; the daemon hands it to `notices`. `ended` after `end`. Any later work event makes an idle session
  running again; an event after the end, newer than it, starts a new run (a
  resumed session: `started_at` restarts), and an event older than the end is
  ignored. A late-delivered older event never moves `last_activity_at` back,
  but an older start can move `started_at` earlier. A session silent for
  `DropAfter` (2 hours) is dropped, unless `Tracker.Alive` confirms its agent
  still runs (a Claude Code session's process, as `ClaudeProcesses` finds it;
  no other agent registers one), which `Sweep` asks again each time, so one
  waiting hours for its user stays and one whose agent exited without its end
  event goes within a minute. An ended one goes `EndedFor` (24 hours) after
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
  The file's `open` holds the sessions that had not ended, likewise without a
  path: a rebuild restores one only when it was silent for `DropAfter`, which
  the record's events then no longer reach, and `Alive` confirms its agent
  still runs.
  An ended session waits for nothing, so the file holds no waiting fields,
  and files written before them load unchanged; a running session's wait is
  rebuilt from the record's events, its message included. `last_prompt_at`
  is the latest prompt event, which the Sessions page orders running sessions
  by.
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
  none when several match. When no head has the branch's name, the head branch
  named by the branch's upstream (`branch.<name>.merge`, when its remote is the
  same repository) is tried, then the pull request whose head commit is the
  commit checked out, which covers a detached HEAD and a push to a branch of
  another name. If no open PR matches, a unique cached merged or closed PR
  with the exact same head commit in the same repository is still linked.
  A closed PR is never inferred from a branch name alone, and ambiguous
  matches remain unlinked. The live-session response includes `pr_state`
  (`OPEN`, `MERGED` or `CLOSED`) from that match, and a state change notifies
  the UI even when the PR number stays the same.
  Without one the session is still tracked against its
  repository and branch, and only a PR page it does not match omits it. The
  clone list is read at most every 30 seconds and a directory that fails to
  link is not looked up again for a minute, since listing and opening a clone
  run git. A session is looked up when it is new and at most every 30 seconds
  after, since the branch changes while it runs and a pull request appears
  once pushed; a lookup that fails keeps the last link. Failing to list the
  registered clones is not retried for 30 seconds either.

  A lookup runs git only when the directory's `gitlocal.CheckoutStamp`
  changed since git last read it. The stamp is read from files, not git: what
  `HEAD` names, its branch's commit, and the common `config`. Otherwise it
  reuses the branch, head, upstream and remotes git last gave, for at most
  10 minutes, and matches them against the current discovery cache, so a
  newly opened pull request still links.

  The cache is decoded again only when discovery changed it. `Resolver`
  keeps each repository's last decoded snapshot with the
  `discovery.Revision` read before it. The revision is read from memory:
  the credential's partition, the identity epoch, whether the last poll
  validated the cache, and how many times the daemon's discovery service
  wrote it. Only that service writes the cache. Every session, commit and
  push of a repository then links against one decoded snapshot. A snapshot
  whose next poll is due counts as stale, as `Cached` would read it. An
  ended session, or a restored one with no directory, is relinked from its
  recorded evidence (`Relink`) each sweep. Once a relink leaves it and its
  commits and pushes as they were, the tracker keeps the generation of each
  repository involved: its revision, and whether it was stale. Later sweeps
  skip the session while those generations hold. A write of the cache, a new
  validation or a due poll changes the generation, so the next sweep relinks
  it, within the same 30 seconds as before.
- **Commits and pushes.** An agent often works away from its directory: it
  commits in another worktree of the clone, or creates a branch, pushes it and
  checks its directory back out on `main`. The tracker also follows Git's
  `post-commit`, `post-rewrite` and `pre-push` (one that let the push go)
  events. A hook whose environment names a session of a known agent
  (`agent`, `session.id` and an `agent_pid` among its ancestors) made the
  work of that session, unless a tracked session's agent is nearer the hook
  than `agent_pid` (an agent the named one started), which made it; if the tracker holds no entry for it (it ended and was
  dropped, or was not seen), the work is still looked up (`LinkCommit`,
  `LinkPush`) and recorded as an observation, without a session to show.
  Otherwise `terminal.processes` are the hook's ancestors: the session whose
  agent is the nearest of them (the parent its own start and prompt events
  recorded, matched by `pid` and `start`) made the commit or push; a process
  two open sessions name is neither's, and Git run by no session's agent
  belongs to none. A `post-rewrite` records each new commit of its `old new`
  lines (a malformed line, and one whose old and new commits are equal, a
  commit a rebase replayed unchanged, is skipped), unless the session already recorded
  that commit from the `post-commit` of the same amend or pick. A commit (`LinkCommit`) is the commit and branch its hook recorded,
  with the clone's remotes and the branch's upstream (`prlink.At`), never what
  the worktree has checked out by the time the daemon looks, so a branch
  switched or a worktree reused right after does not move it; a commit without
  them (a hook that could not read them) is ignored. A push (`LinkPush`) is
  the pushed branch and commit in the repository the remote's push URL names,
  as refresh on hook events reads a push. A session's own work comes before
  its directory: when its latest commit or push that has a pull request
  exists, the session takes that repository, branch and pull request, with
  `link_method` `commit` or `push`, whatever its directory has checked out
  (another session, or a subagent, may have switched it to another branch);
  only a session with no such work takes its directory's pull request. Each
  later lookup checks the cache again,
  so a pull request opened after the push is found within 30 seconds and one
  closed falls back to an older one. At most 16 durably saved commits and pushes are kept
  per session; the record's within a day are replayed on a rebuild, and the
  state file keeps those looked up (repository, branch, commit, pull request
  and event id, no directory), so a restart after the record rotated keeps
  them. Independently, `commitattribution` retains all resolved session-owned
  post-commit, post-rewrite and pre-push observations in the application database, with the
  repository, exact SHA, agent, session, kind, event id and observation time.
  They have no live-session TTL. Pending writes are retried and retained in
  the recovery file, including after the live session expires; unsaved work
  is not truncated to the live history limit. Startup backfills existing saved
  hook work before filtering expired sessions. The shared local resolver reads
  post-commit observations as commit-creation evidence (`commit_ref`) for
  reviews, bundles and feature attribution. Push and checkout associations
  alone never establish contribution; missing transcripts remain missing.
- **API.** `GET /api/live-sessions` (a `LocalAPI.Live` provider, behind the
  same auth as every application route; a daemon with no record answers
  `{"available":false,"code":"live_sessions_unavailable"}`) answers
  `{"sessions":[…]}`, the most recently active first, each
  `{"agent","session_id","state","started_at","last_activity_at","ended_at"?,
  "last_event","waiting_reason"?,"waiting_since"?,"waiting_message"?,
  "repository","branch"?,"pr"?,"title"?,"summary"?,"activity"?,"focus"?}`: `state` is `running`, `idle` or `ended`,
  `last_event` is `start`, `prompt`, `tool`, `wait`, `stop` or `end`, the
  waiting fields are present while the session waits (see "Waiting"; the
  message only when the agent sent one), `repository` is
  `owner/name` in lower case, `branch` is absent when detached and `pr` when
  the branch has no pull request, and `ended_at` unless the state is `ended`. The working directory and transcript path
  stay inside the tracker.
- **Titles and summaries.** The list is served by `Transcripts`, which adds
  each session's `title` and `summary` (`Listed`, `title.go`). The title is
  Claude Code's last `custom-title`, else its last `ai-title`; Codex's last
  `thread_name` for the id in `~/.codex/session_index.jsonl`; Cursor's
  composer name. Without one it is the first prompt the user typed (a Claude
  Code user line that is not meta, a compaction summary, a tool result or a
  tag-wrapped command; a Codex `user_message` event, or from Codex 0.165 an
  `item_completed` event's `UserMessage` item). The summary is the
  latest compaction's: a Claude Code `isCompactSummary` user line, without
  the text wrapped around it, or a Codex `compacted` line's
  `payload.message` (decoded from the start of the line, which the history
  after it can make longer than a line is read). The list carries its first
  numbered section, without the heading, as one line of at most 240
  characters; the transcript route adds the whole of it (`summary`, masked,
  at most 32 KiB) whenever it sends the transcript. The files are the
  transcript's, under the same store checks (`inStore`); a session the hooks
  named no path for is found by its id (`~/.claude/projects/*/<id>.jsonl`, or
  the Codex rollout named for it). Transcripts are read from where the last read
  stopped, 16 MiB at a time (the last 256 KiB name a Claude Code session
  until a long one is caught up), at most every 5 seconds (30 for Cursor),
  again only when the files' sizes or times changed, and right away after a
  failed read. Everything is one line or a block, masked and cut, and
  dropped with the session.
- **Activity.** The same pass reads what the agent is doing into `activity`
  (`activity.go`): `steps`, its last 12 tool calls oldest first (`tool`, a
  one-line masked `target` of at most 160 characters, `at`), `files_touched`
  (distinct files written by Edit, Write, MultiEdit, NotebookEdit or a Codex
  `apply_patch`, over the whole transcript read, at most 10 000 remembered)
  and `last_message` (the latest assistant text, at most 1 200 characters,
  its line breaks kept so the app renders it as Markdown).
  Targets are paths relative to the session's working directory when inside
  it. Claude Code's `assistant` lines and Codex's `response_item` payloads
  (`function_call`, `custom_tool_call`, `local_shell_call`, assistant
  `message`) are read; Codex `shell`, `exec_command` and `local_shell` show
  as `Bash` and `apply_patch` as `Edit`, an MCP tool as `server: tool`; a code-mode `exec` call (JavaScript around `tools.<name>(...)`) is shown as the first tool it calls. Cursor
  sessions have none; it is absent until something is found. `focus` is
  `session` or `window`, how precisely Focus could bring a running session
  forward, absent once it ended or when its hooks recorded no `terminal`.
- **Focus.** `POST /api/live-sessions/{agent}/{id}/focus` (`internal/focus`,
  served when `Transcripts.Focus` is set, as the daemon sets it) brings a
  running session forward from the latest `terminal` its hooks recorded, which
  stays in the tracker's entry, is never served and is not in
  `live-sessions.json`. It answers `{"focused":"session"|"window","message"?}`,
  the message saying what could not be done, or 409
  `{"code":"focus_failed","message"}` when nothing was brought forward, and
  404 for a session the tracker does not list. Each command it runs has 5 s.
  On Linux and macOS only, and in order:
  - The recorded ancestors still running, from the first that runs (the shells
    that started the hook exit with it) up to the next that exited or whose id
    another process took (a different `start`), are the session's chain; none
    left is "The session's process has exited."
  - A Claude Code session whose chain holds Claude desktop's process
    (`claude-desktop`, `Claude`), found by its id among the
    `cliSessionId` and `previousCliSessionIds` of the unarchived records in
    `claude-code-sessions/*/*/local_*.json` under Claude desktop's
    configuration directory (`~/.config/Claude`, `~/Library/Application
    Support/Claude`), opens on its conversation with
    `claude://code/continue?session=local_…` (`xdg-open`, `open`), then its
    window is raised as below. These are Claude desktop's internal files and
    link: a record it no longer writes reads as no session.
  - In tmux (`TMUX`, `TMUX_PANE` and a terminal device recorded), on the server
    of the recorded socket: the client attached to the pane's session used
    last, else the client used last at all (`list-clients`), is switched to the session, and
    the pane's window and the pane are selected. The chain from then on is the
    client's, read now, and the terminal device is the client's: the recorded
    variables describe the terminal the tmux server started in. No client
    attached says so. A pane whose `pane_tty` is not the recorded terminal
    device is not the session's (a terminal started from a tmux pane passes
    `TMUX` on to its own shells), and the session is treated as outside tmux.
    A lookup with an empty session or pane terminal is treated as a missing pane
    before any client is switched. Without a recorded terminal, inherited tmux
    variables are ignored.
  - The terminal's tab, from the first match in the chain: kitty focuses the
    window whose program is the process below kitty (`kitten @ --to
    $KITTY_LISTEN_ON focus-window --match pid:…`), only when the recorded
    `KITTY_PID` is that kitty, so its remote control socket is known (`kitten`
    from the `PATH`, else beside kitty's executable, as in kitty.app); without
    it, a session outside tmux is told to turn on `allow_remote_control` and
    `listen_on`. On macOS, iTerm2 (`iTerm2`, `iTermServer-*`) and Terminal run
    an AppleScript (`osascript`) that selects the session or tab whose `tty`
    is the terminal device and activates the application; a refused Automation
    permission (-1743) says where to allow it.
  - The window, unless the tab step activated it: on macOS the application
    bundle holding the executable of the nearest process in one (`open -a`),
    or iTerm2 by its bundle identifier for its `iTermServer`, which runs from
    iTerm2's support folder (`open -b com.googlecode.iterm2`);
    on Hyprland (`HYPRLAND_INSTANCE_SIGNATURE`) the window of the nearest
    process in the chain that has one, the one used last (`hyprctl -j
    clients`, then `dispatch focuswindow address:…`, which must answer `ok`),
    after switching to its workspace (`dispatch workspace`) when no monitor
    shows it (`hyprctl -j monitors`) and it is not a special workspace; a
    Hyprland configured in Lua (`hyprland.lua`) evaluates `dispatch`'s
    argument as Lua and says so, and is then given `hl.dsp.focus({ workspace
    = … })` and `hl.dsp.focus({ window = "address:…" })`;
    on X11 (`DISPLAY` without `WAYLAND_DISPLAY` or a `wayland` session type)
    `xdotool search --onlyvisible --pid`, then without `--onlyvisible` for a
    minimized window, and `windowactivate`, else `wmctrl
    -lp` and `-i -a`. Other Wayland compositors give one program no way to
    raise another's window. The display variables come from the recorded
    `terminal` over the daemon's own, since the daemon may have started in
    another session.
  - `session` when the tmux pane, the tab or the conversation was selected,
    `window` when only the window was raised. `Predict`, which the list's
    `focus` is, judges the same from the record without running anything: on
    Linux nothing outside Hyprland and X11; `session` for tmux with a recorded
    terminal, a Claude desktop process in the chain (`claude-desktop`, `Claude`),
    a kitty with its socket, and on macOS iTerm2 or Terminal with a terminal device;
    `window` otherwise. Codex hooks whose chain contains only `codex`, `systemd`
    or `launchd` and no terminal can belong to a shared app server with separate
    TUI clients. They offer session focus. Focus matches the thread ID in terminal
    titles (`tui.terminal_title` including `thread-id`): first Codex tmux panes
    on the recorded server (`list-panes`), then kitty's tabs through its recorded
    remote-control socket, iTerm2/Terminal tabs on macOS, or Hyprland/X11 window
    titles. A failed tmux lookup falls through to native terminals, since its
    variables may be inherited. A shortened ID needs at least 24 characters and
    an ellipsis. Missing or ambiguous matches select nothing and explain why.
    Connecting or updating Codex through baum also puts `thread-id` last in
    its user terminal title via `internal/codexconfig`, preserving other
    title items. When no terminal matches, Focus repairs an older user's
    title setting and asks them to restart the TUI and resume the conversation.
    Configuration errors leave the file unchanged; ambiguous matches and
    terminal API failures do not change settings.
    This selects an existing TUI; it never resumes a second conversation.
- **Transcripts.** `GET /api/live-sessions/{agent}/{id}/transcript`
  (`Transcripts`, mounted beside the list by the same `LocalAPI.Live`
  provider) serves the transcript of a session the tracker lists, and only
  those: any other agent and id is 404, whatever the disk holds. The file is
  never named by the request. It is the `transcript_path` the session's
  hooks last reported (kept in the tracker's entry, never served, and not in
  `live-sessions.json`), read only when, its symbolic links resolved
  (`filepath.EvalSymlinks`, the store's root resolved too), it is a regular
  file strictly inside the agent's own store, the one discovery reads
  (`~/.claude/projects` under the home directory or the `projects` folder of
  another directory of `agentdirs.Claude.All` for Claude Code, a `.jsonl`;
  `~/.codex/sessions` or the `sessions` folder of another home of
  `agentdirs.Codex.All` for Codex, a `rollout-*.jsonl`). A Claude Code
  subagent transcript beside it that resolves outside the store is left out.
  Without a path a Codex session is looked up by id in each home (`findRollout`, once per
  session): the `rollout-*-<id>.jsonl` under `~/.codex/sessions` whose first
  line names that thread as a root. Only names are walked, so the rollouts
  Codex compresses as they age (`.jsonl.zst`, which a store scan rejects) do
  not stop it; a Claude Code one is found by its id in the project folders
  of each store (`<folder>/<id>.jsonl`), as a session read back
  from `live-sessions.json` has no path. A Cursor session is read from Cursor's own database
  (`cursortranscript.DefaultStorePath`, a regular file) by its conversation
  id, since Cursor keeps no transcript file baum reads. The files are mapped
  with `sessions.Map`, as discovery maps them (a Codex root rollout without
  its collaboration children, which only a scan of the whole store finds), and
  rendered by `transcript.Renderer`, the same code as `GET /api/sessions/{id}`
  (shaped, bounded and redacted as "Session transcripts" describes, home as
  `~`). Each session's answer is cached: its files are stat'ed at most once a
  second (Cursor's database, which Cursor writes all the time and whose
  lookup reads its whole index, once every 5 seconds), and rebuilt only when
  a file's size or modification time changed; requests arriving together wait
  for one build. At most 8 transcripts are held, the least recently asked for
  dropped first, and a session the tracker no longer lists is dropped at the
  next request. A rebuild that fails (a line caught mid-write) keeps the last
  good transcript. The answer is `{"available":true,"version","live",
  "transcript","images"?}`, `version` a hash of the files' paths, sizes and
  times, `transcript` the body `GET /api/sessions/{id}` answers and `images`
  the media types of the images pasted with each prompt, by its turn, both
  omitted when the request's `?version=` is the current one; or `{"available":false,"live",
  "code":"transcript_unavailable","message"}` with why, in a sentence the app
  shows. The Timeline's `GET /api/timeline/sessions/{agent}:{id}/transcript`
  answers the same way for a captured session of a repository it serves,
  whether the tracker lists it or not (`Transcripts.Read`): from the path
  capture stored, the tracker's while it lists the session, held to the same
  store rules; `live` is false once the tracker does not list it. Its cache
  entries are kept until the least recently asked for makes room, not dropped
  with the tracker's list.
- **Pasted images.** `GET /api/live-sessions/{agent}/{id}/transcript/images/
  {turn}/{n}` serves the n-th image (from 0) pasted with a prompt of a session
  the tracker lists, as it was pasted: images are not masked. Only PNG, JPEG,
  GIF and WebP are served, with `nosniff` and a `default-src 'none'` policy;
  anything else is 404. Each rebuild finds them in the session's own
  transcript file (`findImages`): a Claude Code prompt's record holds them as
  `image` blocks, found by the record's uuid the graph's vendor ids give the
  prompt; a Codex prompt's record names only where the image was saved, so its
  images are read from the user `response_item` of the turn the prompt opened
  (`internal_chat_message_metadata_passthrough.turn_id`), as data URLs. A
  request reads the file again for the one image it asks for.
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
  agent, state, branch, start and last activity, above the review, and in its
  Sessions list with the sessions linked through its commits, once each. The
  Sessions page (`?page=sessions`, with a sidebar entry counting the sessions
  waiting on the user, in the caution tone, or else the running ones) lists
  first, under "Needs you", the sessions with a `waiting_reason` the app
  knows, longest wait first, then the other running and idle sessions, and
  those that ended within the retention, newest end first. The inbox and the PR page never show an ended
  session: when one leaves their list the page reads the session
  list from disk again, where the finished session now is (the existing
  sessions UI), so nothing is lost if the files were not complete earlier.
  Each Sessions row's link opens the session's transcript
  (`?page=sessions&session=<agent>:<id>`), drawn with the viewer's
  `SessionCard`. The view reads it 500 ms after the last of a burst of `live`
  events and every 3 seconds while the session works (15 while it waits; a
  change that only moves `last_activity_at` sends no event, and replies are
  written between hooks), naming the version it shows, and draws again only
  for a new one, keeping the reader's open turns and, at the end of the page,
  their place at the end.

### Agent requests and messages

`internal/agentrequests` lets the app answer what Claude Code and Codex ask
their user, and send a running session a message, through their hooks (the
hook side is in `internal/hookcli`, rules in its
[README](internal/hookcli/README.md)). Everything is held in
the daemon's memory; only `Settings` (`codex_approvals`, default false) is
saved, in the `state/agent-requests` document in `app.sqlite`.

- **Unattended sessions.** A Claude Code session nobody answers in a
  terminal is never held: the hook gives no decision at once when
  `CLAUDE_CODE_ENTRYPOINT` is set to anything but `cli` (`claude -p` sets
  `sdk-cli`) or `CLAUDE_CODE_SESSION_ATTENDED` is `0`; an interactive session
  sets `cli` and `1`, and without either variable the hook holds.
- **Requests.** A `permission-request` hook whose event the daemon took sends
  an `Ask` over `agent.request` (a waiting control method): agent, session,
  the subagent's `agent_id` when one asks, kind (`permission`, or `question`
  for Claude Code's AskUserQuestion), tool, the hook's start time, `Digest`
  of the tool call (SHA-256 of the tool name and the input's canonical JSON;
  an AskUserQuestion by its questions alone), and what the app shows:
  label/value rows (Bash: Command, Description, and Sandbox when
  `dangerouslyDisableSandbox` and Background when `run_in_background` is on;
  Edit: File, Replace, With, and Replace all when `replace_all` is on;
  MultiEdit: File, the number of edits, the old and new text of the first 8
  and the rest as JSON; Write: File, Content; NotebookEdit: Notebook, Edit
  mode, Cell, Source; WebFetch: URL, Prompt; Agent: Description, Subagent,
  Model, Prompt; Codex `apply_patch`: Patch), then every other member of the
  input that is not null, "", [] or {} as one compact JSON row ("Other
  input", or "Input" for a tool with no rows), Claude Code's
  `permission_suggestions` as labels ("Always allow Bash(make:*) in this
  project"), and questions with their options. The hook keeps the payload and
  builds the agent's answer from it; the daemon answers a `Decision`
  (behaviour, suggestion index, option indexes or the user's own text per
  question), or nothing. `Store.Ask` cleans, masks (the tracker's `Mask`) and
  cuts every string (values keep newlines, 4000 characters; others one line),
  holds at most 64 requests, and waits for an answer, a withdrawal, the
  client's disconnect or, for Codex, `CodexWait` (25 s); a Codex request
  answers nothing at once while `codex_approvals` is off (checked again under
  the lock it is inserted under), and turning it off withdraws the waiting
  ones. A request is refused, answering nothing at once, when the mask turned
  non-empty text into "" (the redaction failed: nothing unmasked is shown),
  or when its answers could not be told apart once cleaned and cut: two
  equal questions, two equal options of one question, or a label containing
  ", " in a multi-select question.
- **Withdrawal.** `Store.Run` follows the record's agent events
  (`Store.Subscribe` with `Match`): a session's `user-prompt-submit` or
  `stop` withdraws its main agent's requests, a `subagent-stop` the requests
  of that `agent_id`, `session-end` all of them, and a `post-tool-use` or
  `post-tool-use-failure` (Claude Code's PostToolUseFailure, plugin 1.2.0)
  whose `tool_digest` matches withdraws the oldest request of that tool call
  (the user answered in Claude Code's terminal, which asks alongside);
  parallel calls of other tools stay. Only requests whose hook started at or
  before the event are withdrawn; hooks stamp their events with when they
  started, so an asynchronous PostToolUse never post-dates the next request
  for an identical call. The last 512 such marks (10 minutes) are kept so a
  request that arrives after its tool call finished, or its turn ended, is
  answered nothing at once.
- **Turned down in the terminal.** Claude Code kills a waiting hook when the
  user turns its request down in the terminal (No, or Esc), and interrupts
  the turn without a Stop (checked with Claude Code 2.1.311): the
  disconnect withdraws the request. When a main agent's request ends so,
  neither answered nor withdrawn and more than a minute before `ClaudeHold`
  (the hook's one-hour timeout, after which the terminal's dialog still
  asks), the store calls `Interrupted`, and the tracker's
  `Tracker.Interrupted` makes the session's `permission` or `input` wait a
  `turn_finished` one from then (unless an event since is newer): it waits
  for its user's next prompt. Likewise a request the app answered calls
  `Answered`, and `Tracker.Answered` ends the session's wait as a tool event
  would: the agent goes on (after a denial too) but reports nothing until
  the tool call ends, which for a long command would leave it shown waiting.
  A daemon restart rebuilds the session from the record, which has no event
  for either (nor for a delivered message, below), so it shows its earlier
  wait until the next event.
- **Messages.** At most one per session, replaced by a new one, accepted only
  for a listed session that has not ended and is not waiting for its next
  prompt (`turn_finished`): a Stop hook will come. `agent.take-message`
  removes and returns it as a `daemon.Delivery`, whose `Written` puts it
  back when the answer could not be written, unless a newer one replaced it
  or its turn ended while it was out (a `user-prompt-submit` or
  `session-end` since it was queued, among the recent marks); the Stop hook
  makes it the agent's next instruction. A written answer calls `Resumed`,
  and `Tracker.Resumed` makes the session run again from then: the stop
  that delivered it is older, so the tracker ignores it even when recorded
  later, and a follow-up that only writes text is not shown as a finished
  turn. A `user-prompt-submit` or `session-end` drops a message queued
  before it (it belonged to the turn that is over; a `stop` does not, since
  its hook takes the message after recording the stop), and so does the list
  once the session is no longer listed.
- **Known limits.** An answer racing the hook's disconnect is best-effort:
  the app is told `ok` and the hook may be gone. A written `take-message`
  answer can still be lost by a hook that goes away before reading it. The
  mask treats e-mail-like spans in URLs as addresses and masks them, so such
  a URL is shown masked.
- **API.** `LocalAPI.AgentRequests` mounts `Store.Handler` behind the same
  authentication as every route (without a record:
  `agent_requests_unavailable`). `GET /api/agent-requests` answers
  `{"requests":[{"id","agent","session_id","kind","tool","created_at","input":[{"label","value","code"?}],"suggestions":[{"id","label"}],"questions":[{"question","header"?,"multi_select","options":[{"label","description"?}]}],"altered"?}],"messages":[{"agent","session_id","text","queued_at"}],"settings":{"codex_approvals"}}`,
  oldest request first, message text masked. `altered` is true when the
  request's tool, input, questions or options are not shown exactly as the
  agent sent them (a value cut at its bound, cleaned of control or format
  characters or folded blanks, or with a secret masked); the MCP server's
  `answer_request` then never proposes allowing it. `POST
  /api/agent-requests/{id}/answer` takes
  `{"behavior","suggestion"?,"answers"?}` (answers keyed by the question and
  option labels shown, several labels joined by ", ") and answers
  `{"ok":true}`, 409 `request_gone` for a request no longer pending, 400
  `bad_request` for one that does not fit. `PUT /api/agent-requests/settings`
  takes `{"codex_approvals"}`; `POST /api/agent-messages` takes
  `{"agent","session_id","text"}` (at most 4000 characters, not blank; 404
  `session_not_running`); `DELETE /api/agent-messages/{agent}/{id}` is
  idempotent. Every change publishes `live`, which the app already rereads
  the sessions on.

### Decision capture

`internal/decisionlog` extracts the decisions of the tracked sessions (see
"Live sessions") into `<state>/app.sqlite`, with the published
prompt (`decisions-public-v1`) and the `decisions` package's renderer,
parser and validator. Its scheduler reads `Tracker.Sessions` every 30 seconds
and keeps no per-session timers: a session that ended, or is not running and
has been quiet for two minutes, is queued once per last activity; one running
20 minutes since it last entered running or was queued for it, or that sends
`pre-compact`, is queued at once; and every five minutes so is every settled
session. One worker captures the
queue, deduplicated. The gate is the automatic master switch, the decisions
switch (`preferences.decisions.enabled`) and the repository's
`settings.decisions`, all on by default.

A capture locates the transcript with `livesessions.Locate` (the agent's own
store, symbolic links resolved, the mapped root session the event's), skips
files over 64 MiB, baum's own generation runs and prompts over 272,000
estimated tokens, and reuses, extends or replaces the session's head
extraction as hosted extraction does. Its structured completion goes through
the data extraction backend's `agent.Completer`, the reviews' unless the
preferences choose another (`preferences.decisions.backend`; default models
`claude-haiku-5-5` and `gpt-6-luna`); cursor-agent has none. A model the price list cannot price
runs, reserved at 0, and every call is recorded in a ledger that throttles a session (5
minutes apart, 10 after a failure, 12 a day unless it ended). The reply is masked before it is parsed; decisions keep
their IDs across extractions and are marked obsolete, never deleted, when a
newer one drops them, and nothing is pruned. The app reads it through `GET /api/decisions` (a session, or the
sessions of a pull request: those the tracker links to it and those its saved
review links, with decisions newer than that review marked provisional) and
rereads on the `decisions` event the store's writes publish. See [its
README](internal/decisionlog/README.md).

### Feature log

`decisionlog.FeatureWorker` runs in the daemon beside the capture scheduler,
on the same store, every minute and on a settings change, for each picked
repository with a clone that passes the capture gate. It lists the repository's pull requests through the REST API
(`github.ListPullRequests`, `state=all`, sorted by update, to the stored
cursor, the first page conditional on its ETag, a page too large read again
at half the size), fetches the missing heads (from `refs/pull/<N>/head`) and
bases (by id) of open and merged non-bot pull requests (`gitlocal.FetchPulls`,
100 refspecs a `git fetch`; after a failed one, an `ls-remote` probe, then
one at a time), records each changed one's commits and paths from the
clone's own objects (a failed read is retried after 15 minutes, and after 3
failures at one head its paths are unknown until the daily re-link), and
links commits to this machine's sessions with `locallink.LinkCommits`, the
per-commit matching `Resolve` uses. It then places pending pull requests
and sessions in features (`FeatureWorker.Assign`: hosted triggers, a Go port
of the hosted weighted candidate ranking, `assign.Public`) and writes the
text of features whose members changed (`FeatureWorker.Summarize`,
`summary.Public`, reusing text and generated pull request summaries whose
input hash is unchanged), and at most weekly, when the features'
fingerprint changed, consolidates them (`FeatureWorker.Consolidate`,
`consolidate.Public`: one merge call, at most three split calls, every call
made before a write, each write checked against the membership versions it
read). A first build (`FeatureWorker.Build`) lists a repository's whole
history once (`FeatureWorker.ListHistory`), reads its paths, seeds a new
`building` version from a feature taxonomy (`featureseed.go`), running
feature discovery first when there is none newer than the version
(`featurediscover.go`: the data extraction backend and model through
`generate.RunTask`, `discover.PublicPrompt`, a head-only checkout of
`refs/baum/default` and `discover.HistoryDigest`), and places the history
in the version with the batch prompt (`assign.Public.RenderBatch`), its
seeds offered by path score (`discover.PathScores`), reusing placement's
candidate ranking and writes and the text stage, then deletes the seeds no
pull request joined and
switches it to current in one transaction, after which the weekly pass
consolidates it on its next run; it runs
as a job started at once (`jobs.KindFeatureBuild`, through `jobs.FeatureBuilder`)
through the desktop app. Every stage calls through the same
helper as capture (`caller` in `model.go`: backend and model resolution,
reservation and the `capture_attempt` ledger, with the call's stage and
item). The constants
are in `featureconfig.go` and the feature version a stage uses is chosen in
one place (`featureVersion`: the live stages' current one, a build's
building one). See
[its README](internal/decisionlog/README.md#feature-log).

### MCP server for agents

`internal/agentcontext` is an MCP server agents use to read what the app
shows: the app's chat, and actions working on one pull request. The app
server routes `POST /api/mcp` to it (`uiserver.Options.MCP`, set through
`daemon.App.MCP`). It speaks JSON-RPC 2.0 by hand, MCP revision
`2025-06-18`: `initialize`, `ping`, `tools/list` (each tool carries
annotations: `readOnlyHint` true and `openWorldHint` false, or, for a tool with
`Tool.Proposes`, `readOnlyHint` false and `destructiveHint` false, so a client
can approve reading tools unasked) and `tools/call`, one JSON
response per request, no batches and no event stream. A notification gets
202 with no body. Malformed JSON is -32700, an invalid request (a batch
included) -32600, an unknown method -32601, and missing or invalid params,
or an unknown tool, -32602. A tool that fails returns a result with
`isError: true` and its explanation, not a protocol error; so do arguments
that do not match the tool's JSON Schema.

Agent CLIs reach the route directly over HTTP; there is no bridge process.
`agent.Request.MCP` (`agent.MCPServer{URL, Token}`) names it for a run: URL
is `<daemon origin>/api/mcp`, and `Validate` accepts only `http` on a
loopback address with a port. Claude keeps `--strict-mcp-config` and names
only `baum` in `--mcp-config`, an `http` server whose `Authorization` header
is `Bearer ${BAUM_MCP_TOKEN}` (Claude Code expands it from its environment),
and adds `--allowedTools mcp__baum`, since `--tools` only selects built-in
tools and a `--print` run cannot ask permission. Codex adds
`-c mcp_servers.baum.url` and `.bearer_token_env_var="BAUM_MCP_TOKEN"` under
`--ignore-user-config`. Both put `BAUM_MCP_TOKEN` last in the CLI's
environment, after `agent_env`, so the token is in no argument or
configuration. Only the Claude `User` and Codex `Local` profiles accept it;
the others return an error. With no `MCP` the arguments are what they always
were. No job sets it yet.

What the CLIs send (checked by `internal/agentcontext/livecheck` against the
latest and floor releases): Claude Code asks `initialize` for a newer revision
and accepts `2025-06-18`, opens a `GET` for an event stream and accepts the
405, and recent releases first try `server/discover` with a newer
`MCP-Protocol-Version`, take the 400 and fall back to `initialize`. Codex asks
for `2025-06-18`. With a revoked token both get 401, try no OAuth discovery,
and finish the run without baum's tools. Tokens die with the daemon, so a CLI
that outlives a daemon restart has nothing to reconnect to.

The route takes only an MCP token as `Authorization: Bearer`. The app
server checks the exact Host, asks the MCP server whether the token is live,
refuses a foreign Origin and needs none; the MCP server takes only `POST`
(405 otherwise), `application/json` (415), at most 1 MiB (413) and an
`MCP-Protocol-Version` header that is absent or `2025-06-18` (400 otherwise).
The launch token and the cookie are refused there, and
`/s/<namespace>/api/mcp` is not served. An MCP token is not the launch
token, so it opens no other route.

`Registry` mints tokens in memory: `Mint(Consumer)` returns a token of 32
random bytes, an opaque consumer id and a revoke function, and refuses any
consumer but a `chat` (bound to nothing) or an `action` with an `owner/name`
repository and a pull request number. Nothing about tokens is persisted or
logged. A chat may read any page and propose changes to the page it has
open; an action only reads, within its repository: its tools default to its
pull request and may read the repository's other pull requests. `SetPage(id, PageContext)` records a chat's
page: its name (`inbox`, `pulls`, `timeline`, `sessions`, `analytics`,
`jobs`, `repositories`, `settings`, or a detail page: `pull_request`,
`group`, `session`, `job`, `feature`), the repository, pull request, group,
session, job or feature it shows, the open section, its filters and the focus
the user picked. `section` is one of the Settings page's sections (`general`,
`reviews`, `fixes`, `connections`, `computer`, `prompts`) on `settings`, or of
a repository's page (`reviews`, `prompts`, `registration`) on `repositories`
with a repository; any other section, or a section on another page or on the
Repositories page without a repository, is refused, by the same check `link`
builds a section's address with (`checkSection`).
An action's page is its pull request and does not change. Callers hold the
id, not the token, to set the page.

Tools are registered with a name, description, JSON Schema, a scope, the
route prefixes they read (`Reads`) and a call that gets a `CallContext`:
the consumer, its page, a reader and `Propose`. A `ScopeGlobal` tool,
the zero value, reads across repositories or the app's own state; actions
neither list nor call it (a call is an unknown tool). A `ScopeRepository`
tool reads one repository, which it takes only from
`CallContext.Target`: the repository and pull request asked for, else the
action's, else the open page's; an action is refused any other repository.
`RegisterPage` lists the tools that read a page and the proposer that takes
its changes. Two repository-scoped tools are built in: `current_page`,
which returns the page context and the tools of that page the consumer may
call (on a page with an open section, `section_tools` are those that read
it; the Repositories page lists a repository's tools only while it shows
one), and `read_more`.

Tools read through `Reader`, which serves an in-process `GET` against the
`LocalAPI` handler, below the app server's authentication and with no
credentials. `Get` is its only method, so nothing but `GET` can be sent,
and it refuses the `refresh` and `more` parameters, which make some `GET`
routes do work beyond reading. A tool's reader takes only clean paths under
the prefixes it declared, a whole segment at a time, and never the
unreadable ones (`/api/settings`, `/api/debug`, `/api/github` and
`/api/github-auth`, `/api/diagnostics`, `/api/plan-usage`, `/api/events`,
`/api/mcp`), which no tool may declare. It fails a body over 8 MiB, except
`/api/document` and `/api/facts`, which the daemon serves up to
`viewserver.MaxDocumentBytes` (32 MiB) and which are read up to that (a test
keeps the two equal); a tool projects what it needs, so its result is bounded
as before.

The unreadable routes have one narrow exception, `projectedReads`
(`reader.go`): a fixed map from a tool's name to the exact routes it may read
though they are unreadable. `Tool.Projected` declares them and `compile`
refuses a name or route the map does not give that tool, a route that is not
whole (`/api/diagnostics/x` is not `/api/diagnostics`) and a tool that is not
`ScopeGlobal`; declaring an unreadable route in `Reads` stays refused for
everyone. A route below an unreadable prefix is unreadable too
(`/api/settings/appearance` is below `/api/settings`), so each Settings route
a tool reads is its own pair. Such a tool returns a typed Go projection of the answer, never the
answer: a struct of the fields the app shows on that page, filled from a
decoded copy, so a field the route adds later is not passed on. The pairs are
`diagnostics_summary` with `/api/diagnostics` (check id, label, status,
version and source, each scrubbed with `doctor.Scrub` as the public issue
summary scrubs them; a GitHub Enterprise token check is `other host`, and the
token and backend checks give no source; never `home`, a check's path, detail
or fix, the environment or the issue the Report a problem dialog drafts), and
the Settings tools below: `settings_general` with `/api/settings/appearance`,
`settings_reviews` and `settings_fixes` with `/api/settings`,
`settings_connections` with `/api/github-auth`, `settings_computer` with
`/api/settings` and `/api/diagnostics`, and `prompts` and `prompt` with
`/api/settings/prompts`. `/api/settings` is read whole in process and decoded
into a private struct whose agent environment keeps only the names (the
values decode into `discarded`, which copies nothing), and no tool returns its
budgets or caps. The raw body of a read is in memory for the call's length,
as any read's is; it is never kept, logged or quoted in an error. A test lists the
allowed pairs exactly and fails when any other tool declares an unreadable
route; the reader gives a projected route only to a chat's reader, so an
action's call is refused whatever its tool declares, and the tools are global
besides. Three Settings routes are not exceptions either, because reading
them does work a tool call must not repeat: `/api/settings/plan-usage` reads
the plans to work out the default, so `settings_general` reads
`Options.PlanChoices`, wired to `LocalAPI.CachedPlanChoices` (the same
entries, the default from the plan usage cache alone);
`/api/agent-hooks/codex` starts Codex when its cache is stale or holds a
warning, so `settings_connections` reads `Options.CodexHooks`, wired to
`uiserver.LastHookTrust`, which remembers the newest answer the daemon's check
gave the app and never checks; and `/api/settings/decisions` resolves an auto
backend by running the harnesses' version checks on every read, so
`settings_reviews` reads `Options.DecisionSettings`, wired to
`uiserver.DecisionViews`, which answers the same body, shares one resolution
between concurrent calls and reuses it for 45 seconds while the preferences
(hashed whole) are unchanged; the app's route still resolves on every read.
Tests count the provider's reads, the Codex checks and the decision
resolutions.
`/api/plan-usage` is not an exception: its handler fetches from
Claude or Codex when its cache is stale, so `plan_usage` reads
`Options.PlanUsage`, an accessor the daemon wires to the plan usage cache
(`planusage.Service.Cached`, filtered by the plans the person shows), which
cannot read a provider; a test counts the provider's reads and finds none, and
`Cached` never waits for a read in progress (the snapshot has its own lock).
The picker's list is not an exception either: `/api/github/repositories`
contacts GitHub (a new query starts a search, an expired cache reloads), so
`github_repositories` reads `Options.PickList`, wired to
`githubrepos.Service.Cached`, which narrows the top list the service already
holds by the query's words and calls no lister; when no list is loaded yet the
result says so. A test counts the lister's calls and finds none from the tool.

An action's reader also reads only the routes in `actionRoutes`
(`reader.go`). Each entry is a path pattern, the query parameters it allows
and how the route is bound to the action's repository: by the `repository`
parameter its handler reads, by a `clone` parameter resolved through
`Options.Clone`, by `{owner}/{name}` in the path, and for a route that finds
a record by id in one of three ways: by a check that reads the repository
from the response and returns nothing on a mismatch (`byResponse`); for a
route whose response names no repository, by reading the record the id names
first and checking its repository before the route is read (`byLookup`); and
for a route that answers for every repository, by filtering the response to
the action's repository inside the reader, so the rest never reaches the agent
(`byFilter`); and for the person's saved UI state, which a key names and whose
handler cannot tell whose it is, by requiring the key to be the action's own
pull request's (`byKey`, with the action's pull request in the reader). Any other route, and any parameter its entry does not list, is
refused. The table holds:

- by `repository` (only the parameters listed, no `pr`): `GET /api/timeline/decisions`
  (`q`, `who`, `category`, `file`, `person`, `superseded`, `from`, `to`, `limit`,
  `cursor`), `/api/timeline/features` (`q`, `status`, `file`, `person`, `from`,
  `to`, `limit`, `cursor`), `/api/timeline/build` and `/api/timeline/files`
  (`prefix`, `limit`), whose handler reads every `repository` value and serves
  only those, and exactly one value is allowed;
- by `repository` (parameters `pr`, and as listed): `GET /api/jobs`,
  `/api/prs/description`, `/api/prs/checks` (`head_sha`),
  `/api/prs/failing-checks` (`head_sha`), `/api/prs/review-threads`
  (`unresolved`), `/api/prs/conflicts`, `/api/prs/merge-options`,
  `/api/prs/update-branch-options` and `/api/prs/rerun-checks-options`;
- by `clone` (parameter `pr`, and as listed): `GET /api/versions`,
  `/api/prs/chat`, `/api/document` and `/api/commits` (`version`), `/api/patch` (`version`,
  `path`), `/api/sessions` and `/api/sessions/{id}` (`version`), `/api/facts`,
  `/api/facts/commits`, `/api/facts/sessions` and `/api/facts/sessions/{id}`
  (`head`), `/api/facts/patch` (`head`, `path`), `/api/facts/commit` (`head`,
  `oid`) and `/api/facts/commit-patch` (`head`, `oid`, `path`), and the files
  and commits a saved review cites: `/api/file` (`version`, `path`),
  `/api/commit` (`version`, `oid`) and `/api/commit-patch` (`version`, `oid`,
  `path`);
- by `repository` and `pr` (the form that names a pull request; the form that
  names a session, which names no repository, is not allowed): `GET
  /api/decisions`;
- by key (the repository's lower-case name, `#` and the action's pull request
  number, and for the walkthrough `@v` and a version number, with `key` the
  only parameter): `GET /api/ui-state/review` (the person's draft review) and
  `/api/ui-state/walkthrough` (where they are in the review); another pull
  request's key, or another repository's, is refused though the repository is
  the action's, and no other kind of saved state (the inbox, the filters, the
  tour) is read;
- by response: `GET /api/jobs/{id}` and `/api/groups/{id}`, checked by the
  record's `repository`, and `GET /api/timeline/decisions/{id}` and
  `/api/timeline/features/{id}`, checked by the `repository.full_name` the
  detail names (an archived feature names none, so no action reads it);
- by lookup: `GET /api/jobs/{id}/transcript` (`after`) and
  `/api/jobs/{id}/draft`, after `/api/jobs/{id}`, and `/api/groups/{id}/document`
  (`version`), `/api/groups/{id}/versions`, `/api/groups/{id}/patch` (`version`,
  `path`, `hunk`) and `/api/groups/{id}/file` (`version`, `path`), after
  `/api/groups/{id}`;
- by filter: `GET /api/index` (`history`), which keeps the entries of the
  action's repository and drops the problems; `GET /api/groups`, which keeps
  the groups of its repository; and `GET /api/inbox`, which keeps the items of
  its repository by their `owner/name#number` keys, drops each item's
  saved-inbox ids and the saved inboxes themselves (their queries are the
  user's own), and recounts `count` over what is kept by the daemon's own rule
  (items queued for the user, not being fixed, snoozed or archived), and
  `GET /api/live-sessions`, which keeps the sessions of its repository;
  `GET /api/repositories`, rebuilt from its repository's row alone (its
  settings, each clone's id and availability, its earlier names: no clone's
  folder or git directory); and `GET /api/automatic`, rebuilt from its
  repository's status row alone (without the global switch and choice of pull
  requests, which are Settings'). A filtered
  route that answers an error, or `{"available":false}`, is refused as
  unavailable, which the tools report apart from a pull request that is not in
  the index;
- by path: `GET /api/repositories/{owner}/{name}/prompts`, with no parameter.

Routes are added beside the tools that need them.

The pull request and group tools are all repository-scoped and read the routes
the app calls (the pull request page's own: the review index with its
`clone_id` and `version_count`, the facts before a review exists and the saved
review after), compacting the answer. Each takes `repository` and `pr` (a
group tool `repository` and `id`) defaulting to the consumer's page, for an
action its pull request, and only through `CallContext.Target`:

| Tool | Reads |
| --- | --- |
| `pull_request` | the header and review status from `/api/index` (of the clone with a saved review, else one with a clone; the latest review is only its version, heads, backend, model, `generated_at` and cost, never the session, accounting, digests or policy), `/api/prs/description`, its inbox state (`kind`, `fixing`, `snoozed`, `archived`) from `/api/inbox`, and the groups and stacks it is a member of (`id`, `name`, `stack`) from `/api/groups` |
| `diff` | the changed files from `/api/document` (a saved review) or `/api/facts` (none), and each file's patch from `/api/patch` or `/api/facts/patch`, one file by `path`, else patches up to 100 files and about 1 MiB of patch text, the files left out listed by path |
| `commits` | `/api/commits`, or `/api/facts/commits` at the head the index shows, building the facts (`/api/facts`) only when the daemon has none |
| `checks` | `/api/prs/checks` at the head the index shows, its failing checks marked apart, and `/api/prs/failing-checks` only when `/api/prs/checks` is not found, as the page does |
| `review_threads` | `/api/prs/review-threads`, with the unresolved count discovery has |
| `merge_state` | the three `-options` routes, and `/api/prs/conflicts` when the pull request conflicts; nothing is changed |
| `jobs` | `/api/jobs` of the pull request, less each job's accounting |
| `job_transcript` | `/api/jobs/{id}` to check that the job is the repository's and pull request's, then its transcript; a job with none says only fix jobs keep one |
| `sessions`, `session_transcript` | `/api/sessions` or `/api/facts/sessions`, and one session's transcript, which the viewer serves only for a session the pull request's commits link |
| `focus` | no route: the focus of the consumer's page, or `none` |
| `group`, `group_versions` | `/api/groups/{id}` and `/api/groups/{id}/versions`; a group of another repository is refused. The group defaults to the open group page's; for an action, to the one group its pull request is in (`/api/groups`), and none or several must be named |

Labels, reviewer lists, check log tails and the inbox's Up next sentence are
not returned, as the app shows none of them.

The review tools read a review in parts, one per call, so the document is
never sent whole; none marshals the document it read, and a document that
cannot be read is an error that quotes none of it. Each takes `repository` and
`pr` as above.
The tools `review_outline`, `review_section` and `review_context`
also take `version` (a saved version, the latest by default); `review_versions`
lists every version and takes none. `review_outline`, `review_section`,
`review_context` and `review_versions` take `group` (a group id, `this` for
the open group page's or, for an action, the one group its pull request is in,
or none: the pull request's own review; a chat on a group page defaults to its
group). `facts`, `review_draft` and `review_conversations` take neither: they
need a pull request, so the group page does not list them. A group's tools read
its combined review through `/api/groups/{id}/document` and
`/api/groups/{id}/versions`, after `/api/groups/{id}` has shown the group is
the repository's. `/api/jobs/{id}/draft` is read up to the 32 MiB document
bound, like the documents.

| Tool | Reads |
| --- | --- |
| `facts` | `/api/facts` at the head the index shows (the facts, which exist before any review) |
| `review_outline` | `/api/document` (or the group's): the overview and each section's id, title, kind, hunks, files, assessment and `details` state (`pending`, `failed` or absent), by `review.ReadOutline`, the projection the staged outline stage writes (`review.OutlineOf` without its fallback to the document), never a section's details |
| `review_section` | one section by `id`: all it holds (summary, risks, evidence, citations), or for a `failed` or `pending` section its outline and that state in words |
| `review_context` | `summary.context`, or that it is absent, or failed (from the job's `context_failed`) |
| `review_draft` | `/api/jobs` to find the review running now, then `/api/jobs/{id}/draft` (drafts are never stored): `part` `outline` (default), `section` (with `id`) or `context`, projected as the stored tools; "No running review of this pull request." otherwise |
| `review_versions` | `/api/versions` or the group's versions: version, heads, `generated_at`, backend, model and recorded cost, never the session, accounting, digests or policy |
| `review_conversations` | `/api/prs/chat`: a list (id, version, times, turn count, the first question cut at 200 characters), or with an `id` that conversation's turns (question, focus, answer, citations, state, reason, note), never cost or job ids |

Four tools work on every page, and are in the tools every page lists in
`current_page` (`commonToolNames`: `New` gives each page them, and
`RegisterPage` adds them to the tools of a page it registers). `search` and
`daemon_status` are global, so an action neither lists nor calls them; `link`
and `app_help` read nothing, so an action may use them, and `link` refuses
another repository to an action through `CallContext.Target`.

| Tool | Reads |
| --- | --- |
| `search` | words (`query`, `kinds`, `limit` 1 to 50, default 20) over pull requests (`/api/index`, which Go to lists), review groups (`/api/groups`), running sessions (`/api/live-sessions`), repositories (`/api/repositories`) and, only while the hits so far leave room, features and decisions (`/api/timeline/features` and `/api/timeline/decisions`, matched by the daemon). Every word must be in a title, repository, kind or keyword, ignoring case; titles starting with the words rank first, as in Go to. Each hit has its kind, title, detail and the page it opens (the `PageContext` fields). A route that cannot be read, or answers `{"available":false}`, is named in `unavailable`; all of them failing is an error |
| `link` | no route: the app's address for a page and its subject, the query strings of `app/src/pages.ts` and the detail pages' hrefs (`?page=pulls`, `?repository=owner%2Fname&pr=7`, escaped as the app's `encodeURIComponent` does, `?group=id`, `?page=sessions&session=agent:id`, `?page=jobs&job=id`, `?page=timeline&feature=id`, `?page=inbox&inbox=id` for a saved inbox (its own `inbox` argument, in the store's id shape), a settings or repository section), relative so the chat panel makes it a link. `repository` is also accepted, unused, on group, session, job and feature so a `search` hit's page fields feed `link`. Filters are list pages' own parameters: ones that name a page or subject (`repository`, `pr`, `group`, `section`, `inbox`...) are refused, and a `repo` filter for an action goes through `Target`. Unknown pages, missing subjects, a field the page does not take and unknown sections are refused |
| `app_help` | no route: the embedded `help.md`: each page, the words the app uses and its keyboard shortcuts; `topic` returns one section or word. A test reads `pages.ts` and `shortcuts.tsx` and fails when a page has no section or `link` mapping, or a key is not in the help |
| `daemon_status` | `/api/background` (each repository's last and next refresh, failure count, whether the latest refresh failed and its problem count, never the error text) and `/api/jobs` (jobs queued and running), with the daemon's version. `/api/health` is answered by the app server before the API the tools read through, so `health` is `ok` when the call is answered. No paths, environment, diagnostics or tokens |

The Inbox, Pull requests, Sessions and Timeline pages list the tools below (the
`Server` registers them with `listTools`, `sessionTools` and `timelineTools`; the
pages `inbox`, `pulls`, `sessions`, `session`, `timeline` and `feature` list
them first, then the common four). A tool's default comes from its page's
`Filters`, the page's own address and saved filters, which an argument
replaces. Lists are bounded (`limit`, 50 by default and 200 at most; the
Timeline's 25 and 100, `file_history`'s 20 and 50, `test_query`'s 100 matches)
and say how many more there are. A transcript tool returns at most `limit`
prompts (20 by default, 100 at most) with only their turns' activity. The tools marked
global read across repositories or the person's own state, so an action
neither lists nor calls them; the repository-scoped ones read one repository,
which for an action is always its own, and every route they read has an entry in
`actionRoutes` above. The Timeline's results leave out each person's address
and avatar.

| Tool | Scope | Reads |
| --- | --- | --- |
| `inbox` | global | `/api/inbox` and `/api/index`: the pull requests the Inbox lists, as the queue holds them: `kind` (review, clone, fix, merge, update, other, waiting), whether an agent is fixing it, snoozed or archived, the saved inboxes holding it, the risk score and the review, merge and check state. Defaults to the page's `inbox` filter, else the queue waiting on the person; `query` keeps what `/api/inbox?query=` says an inbox filter holds |
| `inboxes` | global | `/api/inboxes` (id, name, colour, what, owners, risk, repositories, query, bots, badge, revision) with each inbox's own count and folded filter from `/api/inbox` |
| `test_query` | global | `/api/inbox?query=`: the daemon's draft of a query (the matches over the queue, with the kind each waits for) joined with `/api/index`, keeping the listed items as the app's preview does (the queue and others' pull requests, neither snoozed nor archived, so the total is what a saved inbox would count); at most 100 matches and how many more. A query `prfilter.Problem` refuses is returned as `valid: false` with its message, without reading the daemon |
| `pull_requests` | global | `/api/index?history=1`, filtered by view (open or closed), saved tab and filter query with `prfilter.Compile`, as the page does; the tab and the page's draft filter come from `/api/ui-state/filters`, the page's `filter` filter names the tab |
| `live_sessions` | repository | `/api/live-sessions`: state, what it waits for and since when, its latest steps and message, repository, branch and pull request; not the head, upstream or the hooks' record. An action's list is its repository's by the filter above |
| `live_session_transcript` | global | `/api/live-sessions/{agent}/{id}/transcript`: the prompts from `from_turn`, `limit` at a time, with the next turn named; no images (the answer's `images`, and any key beginning `image`, are dropped, and `/transcript/images/` is never readable by any reader), no working directory and no address |
| `agent_requests` | global | the `requests` of `/api/agent-requests`: pending permission requests and questions; never its `settings` |
| `agent_messages` | global | the `messages` of `/api/agent-requests`: the messages queued for running sessions |
| `decisions` | repository | `/api/timeline/decisions`, filtered by `who`, `category`, `file`, `person`, `q`, `superseded`, dates and `repository`; the page's `repo`, `who`, `agent=hide` and other filters apply unless given; `cursor` is the result's `next_cursor` |
| `decision` | repository | `/api/timeline/decisions/{id}` |
| `features` | repository | `/api/timeline/features`, by `status`, `file`, `person`, `q`, dates and `repository` |
| `feature` | repository | `/api/timeline/features/{id}`; defaults to the feature page's |
| `feature_build` | repository | `/api/timeline/build` |
| `file_history` | repository | `/api/timeline/files` |
| `timeline_transcript` | global | `/api/timeline/sessions/{agent:id}/transcript`, projected as `live_session_transcript` is; it names no repository, so no action reads it |

The pull request and group pages list more tools that open what their views
open (`pullExtraTools`; each takes `repository` and `pr` like the page's other
tools, and is repository-scoped, so an action reads its own repository's):

| Tool | Reads |
| --- | --- |
| `file` | `/api/file`: a file of the repository at the saved review's head, text cut at 512 KiB with `truncated`, or no content for a directory, a binary file or a path the head lacks; only a path the review cites, and only a pull request with a saved review |
| `commit`, `commit_patch` | `/api/commit` and `/api/commit-patch`, or the `/api/facts` forms before a review exists, as `commits` reads: one commit's message body and changed files, and one file's patch in it; the commit must be one `commits` lists |
| `pull_request_decisions` | `/api/decisions?repository=&pr=`: the linked sessions and their captured decisions, and which are provisional (the Timeline's `decisions` lists across a repository) |
| `update_branch_options`, `rerun_options` | `/api/prs/update-branch-options` and `/api/prs/rerun-checks-options`; nothing is changed |
| `pending_review` | `/api/ui-state/review?key=<repository lower-case>#<number>`, projected: each comment's target, text, place and head, the summary note and the reviews sent from baum; not their ids or the record's revision |
| `walkthrough_position` | `/api/ui-state/walkthrough?key=<repository>#<number>@v<version>` (the latest version by default, from the index): the stops seen, the notes and the overall comment |
| `group_patch`, `group_file` | `/api/groups/{id}/patch` and `/file`, after `/api/groups/{id}` has shown the group is the repository's; the path must be one the group's review cites |

The Analytics, Jobs and Repositories pages, a job's page and the Report a
problem summary list these (`analyticsTools`, `repositoryTools`). Analytics
and the job list are global; one job's tools read one repository's job through
a binding by response:

| Tool | Scope | Reads |
| --- | --- | --- |
| `analytics` | global | `/api/analytics` over `days` (7, 30 or 90), `repository` and `agent`, the page's own filters by default: the report as the page shows it (summary and cost, by agent, model and repository, the longest sessions, commits, baum's own work, delivery and flow), less the points of the plan usage series and each pull request's time to merge |
| `plan_usage` | global | no route: `Options.PlanUsage`, the daemon's cached plans (name, windows with percent used and reset, `fetched_at`), never a fetch |
| `job_list` | global | `/api/jobs?limit=&before=`, newest first, narrowed by `state` and `repository` while reading older pages (at most 10 per call), each job as the page shows it (type, state, phase, repository, pull request or group, reason, who started it, backend, model, tokens, cost, times), never its accounting or digests; `next` is the cursor for older jobs. `jobs` lists one pull request's |
| `job` | repository | `/api/jobs/{id}`, the open job page's by default; an action's job must be its repository's |
| `job_failure` | repository | `/api/jobs/{id}` of a failed, cancelled or interrupted job: the reason code and failure in words, the sections or context a review was published without, the backend, model and the most tokens and calls it was allowed (`max_tokens`, `max_calls`); the effort and time limits are settings, not the job's. A failure's words (here, in `job`, `job_list` and `jobs`) go through `doctor.Scrub`, since git's output can quote the home folder |
| `repositories` | global | `/api/repositories`: each repository's automatic choice, decision capture choice, earlier names and its clones as a count, how many are available and each clone's id and availability, never a clone's folder or git directory |
| `clones_found` | global | `/api/clones/found`: per repository how many clones were found, their sources and last activity, the repositories attached automatically and when the search ran, never a folder |
| `github_repositories` | global | no route: `Options.PickList`, the picker's list as the daemon already holds it (never a GitHub call; `query` narrows it by name, it does not search), with a note when no list is loaded |
| `clone_target` | global | `/api/repositories/{owner}/{name}/clone-target`: the suggested folder written `~/...` (a folder outside home is only its last folder's name) |
| `diagnostics_summary` | global | `/api/diagnostics` (projected, see above); listed by the Settings page |

The Settings page lists one or more tools per section (`settingsTools`,
`settingsSectionTools`) and `diagnostics_summary`; a repository's page lists
one per section (`repositoryPageToolList`, `repositorySectionTools`). The
Settings tools are global and read their Settings routes only as the projected
pairs above; the repository page's tools are repository-scoped, read through
`CallContext.Target` and the action routes above, and default to the open
page's repository. Each returns a typed struct, and none returns an agent
environment variable's value, the GitHub token, a sign-in's user code or
verification address, a check's folder, detail or fix, a clone's folder or the
home folder:

| Tool | Scope | Section | Reads |
| --- | --- | --- | --- |
| `settings_general` | global | General | `/api/settings/appearance` (theme, key hints, page shortcut modifier) and `Options.PlanChoices` (each plan's shown, default and explicit) |
| `settings_reviews` | global | Reviews | `/api/automatic` (enabled, `pull_requests`), the review backend, model and effort of `/api/settings`, and decision capture as chosen and as resolved from `Options.DecisionSettings` (above: one resolution of an auto backend's harness checks per 45 seconds for unchanged preferences) |
| `settings_fixes` | global | Fixes | the fix backend, model and effort of `/api/settings`, and whether they follow the reviews' |
| `settings_connections` | global | Connections | `/api/github-auth`: state, host (`github.com` or `other host`), login, the kind of place the token comes from (`baum`, `environment`, `config_file`), override, saved, workflow, the error code and message (scrubbed) and the organizations the sign-in cannot reach; a waiting sign-in is decoded for its presence alone, so its user code and address are never copied out of the body; the read may start the background check of those organizations, as the section's does (at most every 30 seconds, from a five-minute cache). The Codex hooks state and when it was read, from `Options.CodexHooks`. The desktop app's agent panel is the desktop shell's, not the daemon's |
| `settings_computer` | global | This computer | `/api/settings` (job concurrency, time limits, the agent environment's names, sorted, and its source), the `settings` of `/api/agent-requests` (`codex_approvals`), and `/api/diagnostics`: the agent and harness checks (`claude`, `codex`, `cursor-agent`, `backend-auto`, `backend-configured`, `anthropic-api-key`; id, label, status, version, scrubbed as `diagnostics_summary` scrubs them, without the source) and how many problems the report has. Updates, zoom, the command line tool and agent sign-in are the desktop shell's |
| `prompts` | global | Prompts | `/api/settings/prompts`, or with `repository` `/api/repositories/{owner}/{name}/prompts`: each entry's fields but the texts (`custom` says whether it has its own) |
| `prompt` | global | Prompts | the same routes: one entry by `id` with every `PromptEntry` field |
| `repository_reviews` | repository | Reviews | the repository's `settings.automatic` and `settings.decisions` from `/api/repositories`, and its status row of `/api/automatic` (effective switch, notice, pending) |
| `repository_prompts` | repository | Prompts | `/api/repositories/{owner}/{name}/prompts`: the list as `prompts`, or with `id` one entry as `prompt`. An action is not given `inherited_text` (the person's global custom text, or the built-in one) nor, for a prompt whose `source` is `global`, `missing_safety_rules` (null: they describe the global text); `withheld` says so. A chat gets every field |
| `repository_registration` | repository | Registration | the repository's `/api/repositories` row: whether it is registered, its clones as a count, how many are available, each one's id and availability, and its earlier names |

`/api/repositories` lists every picked repository with each clone probed for
availability (`Registry.Repositories`: git runs in every registered clone, and
the clones' configuration is read for the earlier names), so
`repository_reviews` and `repository_registration` do that local work on each
call, for an action too, before the reader keeps the action's row; it reads
nothing outside this computer.

`prompts` and `prompt` stay global, as they read the global prompts' projected
route; a repository's page uses `repository_prompts`, which an action may call
for its own repository. Prompts have no template variables (a run's data is
appended after the prompt, and the submit block's `{part}` is baum's own), so
there is no tool that lists them, and no Go code renders a prompt before a run,
so none previews one.

Every page a consumer can be on has a page context here that lists the common
tools and at least one of its own. A test reads `app/src/pages.ts` and every address parameter
`app.tsx`, `location.ts` and `timeline-filter.ts` read, and fails when a
parameter is neither mapped to the page it opens (pull request, group,
session, job, feature, repository) nor listed as not opening one, when a
mapped page has no registered page with a tool of its own, or when a page is
registered that the app lacks. Another reads `settingsSections` and
`repositorySections` from `app/src/settings.tsx` and fails when a section has
no tool in `settingsSectionTools` or `repositorySectionTools`, when a mapped
tool is not on that page or has the wrong scope, or when the maps name a
section the app does not have.

Every result goes through one envelope. It masks the text with
`bundle.NewRedactor`, cuts it at most 64 KiB in at a UTF-8 boundary, and
places the part between `BEGIN UNTRUSTED DATA <tag>` and
`END UNTRUSTED DATA <tag>` lines with a fresh random tag removed from the data
first (`internal/untrusted`, which the chat and fix prompts use too; tests
pin their bytes). The rest is kept masked in memory under a random cursor,
and the result ends with a line naming it; `read_more` with the cursor
returns the next part, with a new cursor while more remains. A cursor gives
its part once, only to its consumer, for 15 minutes; a consumer keeps at
most eight, the oldest (by the order they were kept) dropped first.
Revoking a token drops its cursors and proposals, and a call still running
then keeps neither: both are stored only after checking, under their lock,
that the consumer is live.

`Proposals` hold what a chat proposes, of two kinds (`Proposal.Kind`): a
`diff`, a change to something the page edits, which the person saves; and an
`operation`, something the page does with a button (merge, cancel a job,
answer an agent's request), which the person confirms (see "Operations"
below). Both kinds are held per consumer in memory, share the limit of 32
open proposals, are listed and dismissed the same way and are dropped when
the token is revoked; `Save` refuses an operation and `Confirm` a diff, and
`Propose` records only diffs. A diff holds page, target, the value before and
after, the target's revision and the page context it was made on. A
`Proposer` reads a target (`Current`), validates a change (`Validate`) and
saves it (`Apply`), each given the page context so it refuses a target the
page does not show. `Propose` refuses an action, a page other than the open
one and a page with no proposer; it reads the target's value and revision
itself, ignoring the agent's revision, refuses with the bare
`ErrNotRecorded` a `Before` the agent sent that no longer matches, and
records the proposal; nothing changes. A tool gets only a `Receipt` (id,
page, target, kind): the current value is for the person, through the Go API,
never for the agent. `List` returns deep copies. `Save(ctx, consumer, id,
input)` first checks that the person still has the page the proposal was
made on open, showing the same repository, pull request, group, session, job
and feature (`ErrNotCurrentPage` once they navigated away); it reads the
target again against the page as it is now, which refuses a target of
another section, one that left the page and a changed revision; then,
under the lock a revoke takes to drop proposals, it checks that the
consumer is live and the proposal open and calls `Apply` with the recorded
revision and the `SaveInput`. The proposer compares and writes in one
transaction, as `appdb.Update` does, and returns `ErrStale` without writing
when the target moved meanwhile. `Dismiss` drops a proposal. The app reaches
proposals only through the Go API for now.

`SaveInput` is what the person adds when saving, from the app's own save and
never through an agent: the value of an agent environment variable the agent
proposed adding. It goes to `Apply` alone, is never kept in the proposal, and
prints and logs as withheld. Every proposer but the agent environment's
refuses one.

The `propose_*` tools record proposals. Each is global (an action neither
lists nor calls one), reads nothing, names its page and target, passes the
change asked for as `After`, and answers the receipt and a fixed status. A
tool's schema bounds types and sizes only, so a value the page refuses
reaches the proposer and is refused with the page's own words. Pages list
their tools, and `current_page` lists a section's with its `section_tools`.
`Options.Proposers` gives each page its proposer; the daemon passes
`uiserver.Proposers.Pages()`:

| Page (section) | Tool | Target | Saved as |
| --- | --- | --- | --- |
| `settings` (`general`; also an unnamed section) | `propose_appearance` | `appearance` | `PUT /api/settings/appearance` (theme, key hints, page modifier) |
| `settings` (`general`) | `propose_plan_usage` | `plan_usage/<provider>` | `PUT /api/settings/plan-usage` |
| `settings` (`reviews`) | `propose_reviews` | `reviews` | `PUT /api/automatic` (`automatic.Scheduler.SavePreferences`): enabled, pull requests, backend, model, effort |
| `settings` (`reviews`) | `propose_decision_capture` | `decisions` | `PUT /api/settings/decisions`, the fields left out as the section shows them |
| `settings` (`fixes`) | `propose_fixes` | `fixes` | `PUT /api/automatic`: fix backend, model, effort |
| `settings` (`computer`) | `propose_time_limits` | `time_limits` | `PUT /api/settings/time-limits`, a limit left out as the section shows it |
| `settings` (`computer`) | `propose_job_concurrency` | `job_concurrency` | `PUT /api/settings/job-concurrency`, then the queue's concurrency |
| `settings` (`computer`) | `propose_codex_approvals` | `codex_approvals` | `PUT /api/agent-requests/settings` (`agentrequests.Store.SetSettingsIf`) |
| `settings` (`computer`) | `propose_agent_env` | `agent_env/<NAME>` | `PUT /api/settings/agent-env` of the environment in force with the name added (the value from `SaveInput`) or removed |
| `settings` (`prompts`) | `propose_prompt`, `propose_prompt_reset` | `prompt/<id>` | `PUT /api/settings/prompts/{id}` |
| `repositories` with a repository (`reviews`) | `propose_repository_settings` | `<owner/name>:settings` | `PUT /api/repositories/{owner}/{name}/settings`, the field left out kept |
| `repositories` with a repository (`prompts`) | `propose_prompt`, `propose_prompt_reset` | `<owner/name>:prompt/<id>` | `PUT /api/repositories/{owner}/{name}/prompts/{id}` |
| `inbox` | `propose_inbox` | `inbox:new` or `inbox:<id>` | `PUT /api/inboxes` (`inboxes.API.Save`) of the list with the inbox added at the end or changed: name, colour, query, badge |
| `group` | `propose_group` | `group:<id>`, the page's group | `PATCH /api/groups/{id}` (`reviewgroups.API.Update`): name, members added and removed |
| `pull_request` | `propose_review_comment` | `<owner/name>#<n>:review_comment`, the page's pull request | `PUT /api/ui-state/review?key=` of the pending review with the comment appended, the other comments, note and sent reviews kept |

A target of another section than the open one (General when none is named;
Reviews on a repository's page), another repository, group or pull request is
refused, at `Propose` and again at `Save`. Each proposer validates as the
page's route does, with the same functions: `repos.ValidateTimeLimit`,
`config.ParseBackend`, `config.ParseEffort`, `Preferences.ValidateChange`
(through `automatic.EditPreferences` and the decisions route's edit),
`config.ValidateAgentEnv`, the concurrency bound, the appearance and plan
checks, `prompt.Validate`, `inboxes` normalisation (`State.Check`) and the
app's query check (`prfilter.Problem`), `reviewgroups.CheckUpdate` and
`uistate.CheckReviewComment`. Each saves through the function the route's
handler calls and does what the route does after a save: the decisions event,
the queue's concurrency, cancelling disallowed automatic jobs, waking the
scheduler and the automatic notice, the settings and decisions events after a
repository's settings or prompts, and `Changed` after an inbox or group. Beyond
the page, a prompt's text, or a reset to the inherited text, that leaves out
the text of a safety rule the prompt in use has is refused, at `Propose` and
again inside the save's transaction against the rows it reads (the person
can still remove one in the editor, which warns). The check finds each
rule's text only; it cannot judge wording elsewhere that undercuts a rule, so
the person reads the diff. Adding an agent environment variable that is set,
or removing one that is not, is refused, and so is a change after which the
environment in force would differ from the proposed one by more than that
name: removing the app's last variable brings back the config file's
`agent_env`. An inbox is proposed and saved as the Inbox holds it: it folds
every saved inbox's older fields (what, owners, risk, repositories, bots)
into its query (`app/src/inboxes.ts`'s `folded`, here with
`prfilter.FoldedQuery` and `prfilter.Problem`) and saves the list so, and the
diff's `Before` is the folded inbox.

A target's revision is HMAC-SHA256, under a key made per daemon, of the
stored state its save reads: the preference fields of its route, the agent
environment in force (values included, which the key keeps from being
guessed from the hash) and whether the app sets it, the Codex setting, a
prompt's own and global rows, a repository's settings, a group's name,
members, stack flag and landed members. Saved inboxes and pending reviews use
their store's own revision. `Apply` checks the revision inside the writing
transaction: `repos.Registry.UpdatePreferencesIf`, `UpdateRepositoryIf` and
`PutPromptOverrideIf`, `reviewgroups.Store.UpdateWith` and
`agentrequests.Store.SetSettingsIf` hand the check the state as the
transaction (or the settings' save lock) reads it, and an error from it
writes nothing. Saved inboxes and pending reviews are read before the write
and saved with `inboxes.Store.Replace` and `uistate.Store.Replace`, which
compare the revision read in their own transaction. Tests save each proposal
and send the page's request to a twin fixture and compare the stores and
what followed each save, and change each target through its route before the
save, and after `Save` checked the revision, to see the save refused.

#### Operations

An operation proposal (`agentcontext.Operation`, `operations.go`, the list in
`operationlist.go`) is a button of a page an agent suggests the person press.
The agent never runs anything: nothing runs before the person confirms, and
on `Confirm` the daemon runs, in process, the route the app's button calls,
with that route's own validation and status. Each operation names the pages
that offer it, a fixed method (`POST` or `DELETE`) and route template
(`{name}` is one whole segment), an input schema, the GET routes it reads
(`Reads`, and `Projected` for the paid runs' model and effort from
`/api/settings`, a `projectedReads` pair), `Build`, which turns the agent's
arguments and the page into the request (the `{name}` values, the query and
the JSON body, as the button's code builds them) and names the target, and
`Describe`, which checks the target belongs to the page and writes the
summary the person reads (`OperationSummary`: what will happen, on what, and
for a paid run the harness, model and effort; the app computes no cost
estimate before a run, so none is shown). `Paid` marks an operation that
starts an agent run, `Irreversible` one that cannot be undone.

A summary's `Action` is baum's own sentence, naming things only by
identifiers (a repository and number, an agent and session id, a job id). All
else is a `SummaryField` in `Details`: baum's own facts, or `Quoted` text
that is not baum's, shown in full and never cut. Quoted text read from a
route (a title, a thread, a tool's input, a question) is made plain (`plain`:
control characters (C0, C1, DEL), invalid UTF-8 and the default-ignorable
characters (format characters such as bidirectional overrides and isolates,
zero-width characters, the soft hyphen and tags; variation selectors; the
other default-ignorables, the Hangul fillers and U+034F, U+17B4 and U+17B5
among them) are dropped, and line breaks, tabs and the line and paragraph
separators become spaces unless the field is `Multiline`, which keeps line
breaks and tabs). Text the agent wrote (a message, an answer, a group's name;
`ByAgent`) is sent as it is, so `Build` refuses it when it holds any such
character, a carriage return or a line or paragraph separator
(`checkAgentText`), and its schema's `maxLength` (in characters) refuses one
longer than the route takes: the field is exactly what runs. A clone's target
folder that `plain` would change is refused the same way. An identifier is
named in the sentence, or in a field's label, only when it passes its own
check (`validRepository`, `validSegment`, a hex head, a known merge method or
job kind); otherwise the sentence names it generically ("a pull request", "an
agent session") and the value is a quoted field. `refresh` and
`register_repository` send, and name, the registry's spelling of the
repository, not the agent's. A paid run is refused, at `Propose` and at
`Confirm`, rather than shown with a guess when its settings cannot be read,
or when `GET /api/settings` could read only the config file and not the
saved preferences (it then answers `preferences_unavailable`).

`ProposeOperation(ctx, consumer, name, args)` refuses any consumer but a
chat, a page that does not offer the operation, and a target `Build` or
`Describe` refuses (another page's pull request, a session, request, job,
group or repository the page does not list); `Build` also refuses what the
button's own code refuses (a draft or closed pull request, a head that moved
since the page loaded, a merge method GitHub does not allow, a fork's branch
update, a review or fix already running, a session that is not at its
prompt). It records the operation, its request and its summary; nothing
runs. Each operation has a `propose_<name>` tool, global (an action neither
lists nor calls it), listed on the pages that offer it, whose answer is the
`Receipt` (`{id, page, target, kind}`) and a fixed status, never the summary,
the request or anything a route answered.

`Confirm(ctx, consumer, id, seen)` checks that the consumer is live, the person
still has the page the proposal was made on open, showing the same thing (as
`Save` does), and runs `Describe` again against the page as it is now (the
request still pending on the page, the job still on it, the clone still
registered); a refusal leaves the proposal open. Each operation proposal
holds its summary's revision (`OperationProposal.Revision`, the SHA-256 of
the summary's JSON), which `List` returns and the app sends back as `seen`:
under the store's lock, `seen` must be the stored summary's revision and that
must be the revision of the summary `Describe` just wrote. When either
differs (another title, harness, model or effort; or a summary that another
`Confirm` replaced meanwhile, as a double click's second confirm finds),
nothing runs, `Confirm` returns `ErrOperationChanged` and the proposal stays
open holding the new summary and revision, for the person to read and confirm
again. It then takes the proposal
out of the store under the lock a revoke and every other `Confirm` take, so
of two confirms only one runs it and a revoke either comes first and nothing
runs or comes after the person's confirm took it; and runs its request once,
through the writer, with a context that the caller leaving does not cancel.
The writer is the GET `Reader`'s counterpart: it sends only the operation's
own method to the path its route template makes from the `{name}` values,
each a plain segment (`[A-Za-z0-9._~@+:-]`, not `.` or `..`, no `/`, `%`,
`?` or `#`), checks that the request's method and path are those and that
nothing was escaped, and keeps at most 1 MiB of the answer. `Confirm` returns
an `OperationOutcome` (the route's status and a short message from its body)
to the app; an error means nothing ran, and a status other than 2xx is the
route's own refusal. There is no app route for listing or confirming
proposals yet.

The chat's `Consumer.Session` (`agent:id`, the agent `claude-code` or
`codex` as the live sessions and agent requests name them, the id one plain
segment) is the agent session it runs in, set at `Mint` or once afterwards
with `Registry.SetSession` (Codex reports its session id late); setting
another session after one is refused, so no proposal can come to target the
chat's own session after it was made. A chat never proposes answering a
request of its own session, nor messaging, queuing for, ending or bringing
forward its own session; while its session is unknown it proposes none of
these for any session (fail closed). These are checked at `Propose` and at
`Confirm`. `answer_request` carries only allow or deny and a question's
answers: a permission's suggestions (always allow, a permission mode) can
change the agent's permissions for good, so only the person applies them, in
the app. Allowing (or answering) a request the store marks `altered`, or one
holding text `plain` would change, is refused: the person answers it in the
app or the terminal. Denying it may be proposed.

| Operation (tool `propose_<name>`) | Pages | Route | Paid | Irreversible |
| --- | --- | --- | --- | --- |
| `merge` (method from `/api/prs/merge-options`, default GitHub's) | `pull_request` | `POST /api/prs/merge` | | yes |
| `update_branch` | `pull_request` | `POST /api/prs/update-branch` | | |
| `rerun_checks` | `pull_request` | `POST /api/prs/rerun-checks` | | |
| `rerun_failed_job` (a failing check's `job_id`) | `pull_request` | `POST /api/prs/rerun-job` | | |
| `resolve_thread` (an unresolved thread the account can resolve) | `pull_request` | `POST /api/prs/resolve-thread` | | |
| `fix` (`only: checks` for Fix CI) | `pull_request` | `POST /api/jobs` (`kind: fix`, mode `single`) | yes | |
| `review` (no isolation confirmation: the route refuses a fork's and the person starts it in the app) | `pull_request`, `inbox`, `pulls` | `POST /api/jobs` (a review item, mode `single`) | yes | |
| `refresh` (by repository, else a registered clone of it) | `inbox`, `pulls` | `POST /api/prs/refresh` | | |
| `answer_request` (allow or deny, a question's answers; no suggestion) | `sessions`, `session` (its own requests) | `POST /api/agent-requests/{id}/answer` | | |
| `send_message` (a session at its prompt) | `sessions`, `session` | `POST /api/live-sessions/{agent}/{id}/message` | | |
| `queue_message` (a running session) | `sessions`, `session` | `POST /api/agent-messages` | | |
| `end_session` (an endable session) | `sessions`, `session` | `POST /api/live-sessions/{agent}/{id}/end` | | yes |
| `focus_session` | `sessions`, `session` | `POST /api/live-sessions/{agent}/{id}/focus` | | |
| `cancel_job` (one `GET /api/jobs` lists as queued or running) | `jobs`, `job` (its own) | `POST /api/jobs/{id}/cancel` | | |
| `create_group` (2 to 8 pull requests of one repository the list shows) | `pulls` | `POST /api/groups` | | |
| `delete_group` | `group` (its own) | `DELETE /api/groups/{id}` | | yes |
| `register_repository` | `repositories` | `POST /api/repositories` | | |
| `remove_repository` | `repositories` (a repository's page: its own) | `DELETE /api/repositories/{owner}/{name}` | | yes |
| `remove_clone` | `repositories` (a repository's page: its clones) | `DELETE /api/repos/{id}` | | yes |
| `clone_repository` (into the app's default target) | `repositories` (a repository's page: its own) | `POST /api/repositories/{owner}/{name}/clone` | | |

A clone goes where the app's Clone dialog would put it by default: `Build`
reads `GET /api/repositories/{owner}/{name}/clone-target` and sends that folder,
which the summary names, so the clone goes where the person saw. The agent
cannot name a folder; to clone elsewhere the person uses the app's dialog.
Not offered, because the app has no such button or route: marking a pull
request seen in the Inbox (the Inbox's seen state is the app's own UI state,
`PUT /api/ui-state/inbox`; `POST /api/jobs/{id}/seen` is the tray's), rerunning
or starting a job from the Jobs pages (reviews and fixes start from the pages
above), registering a local folder (the person's file picker), and running a
review with a draft prompt (no route takes draft prompt text).

Tests run each operation against the daemon's real routes
(`uiserver/operations_test.go`): the registry, review groups, jobs queue (an
executor that runs nothing), agent requests and live sessions (recording
senders, enders and focuser) are real; the merge, update, rerun, thread and
refresh handlers are real over a recording GitHub fake, and the clone route
over a recording clone function. They check nothing is written before
`Confirm`, one write of the operation's own method and route after it, its
effect, that a second `Confirm` runs nothing, that a route's refusal comes back
and changes nothing, and that crafted arguments (traversal, encoded slashes,
ids with `/`, queries) never reach another route.

### Transports and control protocol

The app transport is `internal/uiserver` on `127.0.0.1` (a free port unless
`--port`), unchanged: launch-token exchange, the cookie scoped to the secret
`/s/<namespace>/` path, exact Host and Origin checks; `POST /api/mcp` takes
only an MCP token and no Origin (see "MCP server for agents"). Its token is written to
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
returning a `daemon.Error`. `event` (see "Hook events") and
`agent.take-message` (see "Agent requests and messages") are the ones it
has; hook events that may move a head reach the automatic scheduler through
its `Trigger` (see "Refresh on hook events"). A method that may wait for as
long as its client stays connected is registered in `Options.Waits` or
`App.Waits` instead (`agent.request` is the one): the daemon clears the
connection's idle deadline and runs it without the 30-second method timeout,
its context ends when the client disconnects (a reader on the connection
sees its end, or anything more it sends) or the daemon stops, and the
connection closes after the answer. Other connections are served meanwhile;
clients send it with `Client.Wait`, which dials within the usual request
timeout and then waits until its context ends. Remote attach would be
another transport beside these two; none exists.

### Desktop app interface

The desktop app talks to the daemon only through the bundled `baum` binary
and two files:

1. `baum daemon ensure --json` starts the daemon if needed (replacing an older
   one), waits until it serves, and prints one JSON line on stdout:
   `{"protocol":1,"version":"v0.3.0","pid":4242,"started_at":"2026-10-19T16:00:00Z","app":"http://127.0.0.1:53124","socket":"/run/user/1000/baum/daemon.sock","state":"/home/user/.local/state/baum/daemon"}`.
   `stopping` appears only while it shuts down, and `restart_pending` when an
   older daemon was kept because it has jobs queued or running and will
   restart as this version once the running ones finish (see Lifecycle). On failure it exits non-zero
   with the reason on stderr. `--port` applies only when it starts the daemon.
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
   prints `{"agents":[{"agent","name","detected","signed_in","cli_path","cli_version","minimum_version","supported","recorded","plugin","installed","enabled","version","error"}]}`
   from native plugin registries and the detected CLI's `--version`
   (`supported` is absent when it could not be read; `recorded` says `baum
   desktop-setup` recorded its directory), with executable detection and installation/
   enablement separate from the agent's hook trust decision. It installs and
   removes `baum@baum` through native CLIs with `baum hooks install --user`
   and `uninstall --user`, and `update --user` refreshes the marketplace and
   brings an installed plugin to its latest version. Native status queries have a 10-second bound and
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
`app/` to it like any loopback client, including the PR page's facts before
any review exists (see "Pull request facts") and the draft of a review being
written in stages (see "Persistent generation jobs"). Besides `ensure` and `stop`, the tray
reads `GET /api/jobs`, `/api/automatic` and `/api/diagnostics`, sets
`PUT /api/automatic` and, once when first-run setup finishes, sends
`POST /api/automatic/catch-up` (see "Setup catch-up"), with the launch token
as a bearer, over the app API the window uses. It also owns first-run setup
(`src/onboarding.rs`, `src/install.rs`, `../app/src/desktop/setup.tsx`): a bundled screen,
with its own narrow capability, from which the user installs the bundled
`baum` as the `baum` command on the login shell's `PATH` (a symbolic link to a
stable path of the bundled binary; an AppImage and Windows run and link a
copy in the app's data directory, which Windows adds to the user `PATH`;
the password prompt only for root-owned system directories) and the
native `baum@baum` marketplace plugins; after that the window opens the
daemon's diagnostics page. The `setup` section of the native shell's
`desktop.json` records onboarding and the command links/copies it installed;
its other sections hold notification settings, zoom and failed updates.
Section updates share one lock and atomic file replacement, and the file is
readable before the daemon connects. Agent registries own plugin installation
and enablement. Plugin status/install/removal use the resolved login shell PATH,
with an inherited-PATH fallback warning if resolution fails. Command removal
uses its saved command records; plugin removal targets only `baum@baum`. See [`desktop/README.md`](desktop/README.md).

### Local linkage freshness

Successful local linkage results, including empty results, expire after one
minute. Session-end events invalidate desktop
viewer evidence independently of document revisions. An invalidation during a
read waits for it to finish and coalesces callers onto one fresh computation;
its stale result is not served as current evidence. Open PR pages reread linked
sessions every 30 seconds, with one request in flight, to pick up late transcript
writes and checkpoint arrivals even without another lifecycle event.

### Recorded live work

Live links retain the observed branch, upstream and HEAD plus the selection
method and status. Multiple matching PRs remain ambiguous. Existing links are
rechecked every sweep, including quiet sessions whose PR has closed. Ended
sessions reconcile from saved observations only, so a later branch switch or
removed worktree cannot rewrite their historical work. Replay without a saved
observation recovers clone identity only and reports `unknown_history`.
Active and ended observations survive restarts; notification message text and
local paths are not persisted. Stale or unavailable discovery is explicit.

Live PR selection compares the canonical head repository (including host) as
well as branch names. Discovery retains fork head identity; local Git records
the origin and configured upstream identities. Thus equal branch names in the
base, a fork and another GitHub host cannot alias one another. Older cache
entries lacking fork head identity remain unlinked until discovery refreshes.
