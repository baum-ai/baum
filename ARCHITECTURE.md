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
caps, input redaction, byte trimming and digest applied to a bundle, and
`review` holds the document model, validation, history, refresh, output
redaction and the tile catalogue; `agent` and `viewer` do not exist yet. Until a
package is complete, its section describes the intended shape.

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
| attachments | images referenced in the description | GitHub, with the user's token |
| previous version | the last stored document for this pull request | the local version store |

A source that has nothing returns an empty result, and the bundle records in
`sources` which inputs existed. Missing evidence is reported as missing,
never as zero.

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
its own, whole and from after every run of characters in it that are not
letters or digits, and masked in place; `MaskedClosed` counts the values
masked for a secret, and a host should reject the run when it is not zero. A
value longer than its schema's `maxLength` (128 characters for identifiers
and categories) cannot be valid and is masked whole without the scan. A value
whose schema sets no `maxLength`, such as a SHA, hunk ID or date-time, is
scanned at any length, but from after at most 16 runs; one with more fits none
of those patterns and is masked whole, so the scan stays linear in the
document's size. A value masked whole holds nothing detected and counts
nowhere.

Normalisation respells every identifier, category and tile-local name the
model writes in lowercase with one underscore for each run of other
characters, and a secret respelled that way (`sk-ant-api03-AbC…` as
`sk_ant_api03_abc…`) is one no rule detects. So a host runs the redactor's
`Identifiers` on the document as the model wrote it, before `Prepare`: the
values normalisation respells, taken from its own walk, are scanned the same
way, bounded by the length of their respelling, and masked in place with the
format-preserving mask, which normalisation then respells to `x`, `9` and
underscores; they count in `MaskedClosed` too. One whose respelling is over
its bound is masked whole without a count, as validation rejects it anyway.
The same secret written in another spelling, upper case or already
respelled, is not detected again, so every identifier whose respelling
contains a masked secret's respelling is masked whole and counted. The order
is `Identifiers`, `Prepare`, `Document` on
what `Prepare` returned, with one redactor, then the `MaskedClosed` check. Text
scattered across unrelated fields, a token wrapped mid-value, or a secret no
rule detects on its own, such as a bare hex string, which reads as a SHA, is
not caught. If redaction fails, the text is dropped rather than kept.

### Tiles

The tile catalogue (`catalogue.json`) and the tile guidance are public and
live here. Tiles are off by default; a host offers an allow-list of kinds,
and the model schema and prompt include only those kinds.

## `agent`

### Backends

```go
type Backend interface {
	Run(ctx context.Context, req Request) (Result, error)
}
```

A request carries the prompt, the model-facing schema, the checkout directory
and the call and token budgets. A result carries the document, usage and an
event log. Backends:

| Backend | Runs |
| --- | --- |
| `anthropic` | the Anthropic API directly, with read-only file tools over the checkout |
| `claude` | `claude -p` restricted to read-only tools, submitting through structured output |
| `codex` | `codex exec` in its read-only sandbox, with a strict-mode output schema |
| `cursor-agent` | `cursor-agent` confined to the checkout |

The CLI backends rely on each tool's own read-only mode; baum does not add a
container. [SECURITY.md](SECURITY.md#agent-isolation) describes what that
leaves open.

### Prompts

The review instructions reach the loop through an interface:

```go
type Prompts interface {
	// Instructions returns the review instructions for a backend, with the
	// version and digest recorded in the document's generator block.
	Instructions(ctx context.Context, backend string) (Prompt, error)
}
```

This module ships and maintains its own public prompt. A host may supply
another implementation; the hosted service does. The prompt is per backend
because each backend submits its result differently. The repair
instructions and tile guidance are part of the loop, not the prompt.

### Generate and repair

The loop asks the backend for a document, runs it through `review`, and on
rejection sends the findings back for a repair, up to the configured number
of attempts. Repairs count toward the call and token budgets. A run that
fails records one reason from a fixed list.

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
| hunk patch source | no | not used | the full patch for a hunk when the document does not carry it |
| file opener | no | not used | opens a cited file in the host's own diff view instead of following its link |
| row actions | no | not used | a slot beside each section and risk, for example a read mark or approval |
| side pane | no | not used | a slot for discussion or other host features |
| header slots | no | not used | statistics, generation controls, approval state, a back link |
| rail groups | no | linked sessions and commits from the local session stores | changed files, linked sessions and commits, each listed in the rail with the host's pane |
| version shown | no | not used | a callback with the versions on screen, for example to record that one was read |

Optional seams default to absent, and the viewer renders the document alone.
The viewer never decides who can see a document; that is the host's job.

The sans-serif font comes from the `--viewer-font-sans` CSS variable, which
defaults to an open font bundled with the package. A host overrides the
variable to restyle the viewer without forking it.

The components and their seams are in [`viewer/`](viewer/README.md); the
standalone app and the bundled font are not there yet, and until the font is
the variable falls back to the system's sans-serif face.

## Running locally

`baum generate --pr N`:

1. resolves the pull request with the user's GitHub token;
2. exports the head and merge-base trees with `git archive` from a private
   repository that borrows the clone's objects, and writes the checkout under the
   same limits as above;
3. collects sessions from the home directory and Entire checkpoints and
   links them to the pull request's commits;
4. fetches description images with the user's token and caches them by
   content hash beside the local version store;
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

`baum bundle` stops after step 5. `baum validate` checks a stored document with
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
