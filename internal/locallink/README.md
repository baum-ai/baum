# Local session linkage

The desktop app, local PR viewer, review bundles and feature log share this
resolver. It reads the clone and local session stores without fetching.

Entire checkpoints discover candidates. Each commit exposes their session IDs
as `checkpoint_sessions`, separately from `links`. Membership survives missing
transcripts, but does not add linked session cards, transcript access, review
context or feature attribution.

Links require independent evidence: a successful recorded commit creation
(`commit_ref`) or timed edit overlap (`files_overlap`). Creation does not prove
that the session authored all committed edits. The starting-parent shortcut is
disabled locally because concurrent sessions commonly share it. Exact edit-score
ties remain unresolved. Contributors outside a checkpoint remain eligible, and
checkpoint membership never breaks a tie. The hosted resolver retains its own
policy; this package does not change it.

The daemon durably records session-owned Git hooks in the application database
(`commitattribution`, bucket `attribution/commits`). The shared resolver reads
post-commit observations as `commit_ref` evidence, scoped to the clone's remote
repository identities and exact commit SHA. These observations take precedence
over transcript guesses about commit creation. Their provenance includes agent,
session, hook event and observation time. A missing transcript keeps the link
and metadata-only review context; it does not invent conversation or usage.
Push observations are retained separately and never establish creation.

Quiet commits followed immediately by `git log` on a new line also supply
creation evidence when the tool succeeded and its output has no explicit
commit-failure diagnostic. The command parser excludes quoted commands,
heredoc text, background/negated commands and intervening checkout commands.

Local matching still uses path overlap rather than content equivalence. A
matched path set does not establish that every contributor was discovered.
Checkpoint IDs provide the narrower pool for later evidence inspection or
adjudication without asserting those candidates as contributors.

`BuildSessionEvidence` maps only the sessions that changed since the process
last read them. It keeps a digest per session: what linking reads from the
graph and, for a linked session, the evidence built for it with what that
evidence depended on besides the transcript. The digest is reused only when
all of it is unchanged, so the evidence is the same as a full rebuild.
[Local session evidence](../../ARCHITECTURE.md#local-session-evidence)
describes the keys. `Resolve` and
`LinkCommits` map every session.

Run `just oss-go test ./internal/locallink/...` from the monorepo root. The
tests cover concurrent readers/editors, stale sessions, matching-parent false
positives, ambiguous edits, outside contributors, checkpoint discovery and
offline behavior.
