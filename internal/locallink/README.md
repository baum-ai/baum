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

Local matching still uses path overlap rather than content equivalence. A
matched path set does not establish that every contributor was discovered.
Checkpoint IDs provide the narrower pool for later evidence inspection or
adjudication without asserting those candidates as contributors.

Run `just oss-go test ./internal/locallink/...` from the monorepo root. The
tests cover concurrent readers/editors, stale sessions, matching-parent false
positives, ambiguous edits, outside contributors, checkpoint discovery and
offline behavior.
