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

Git reflogs supply creation evidence too, for commits, amends, rebase picks,
cherry-picks and reverts. The resolver reads the clone's HEAD logs (the main
working tree's and each linked worktree's) and its branch logs, which outlive a
removed worktree. An entry made while one session's shell tool call ran, in the
same working tree, establishes that session as the creator; subagent calls
count for their top-level session. Calls are placed by their `workdir`, else
the session's `cwd` attribute, else its working directory, and map to a working
tree by path (a removed worktree kept in an agent's conventional folder inside
the main working tree still maps). Branch-log entries know no working tree, so a session matched
through a HEAD-log entry outranks one matched only through them. When several
sessions match, those whose command writes Git (`commit`, `rebase`,
`cherry-pick`, `revert`, `am`) are kept; if that does not leave exactly one the
commit stays unresolved. Merges are never reflog evidence. A reflog match
outranks a hook observation of the same commit, which guesses the session from
the process tree. Limits: a background command that commits after its tool call
returned lies outside every window, and reflogs expire with Git's gc (90 days by
default). `ReflogFingerprint` lets the evidence and feature caches notice a new
entry.

Quiet commits followed immediately by `git log` on a new line also supply
creation evidence when the tool succeeded and its output has no explicit
commit-failure diagnostic. The command parser excludes quoted commands,
heredoc text, background/negated commands and intervening checkout commands.

Local matching still uses path overlap rather than content equivalence. A
matched path set does not establish that every contributor was discovered.
Checkpoint IDs provide the narrower pool for later evidence inspection or
adjudication without asserting those candidates as contributors.

Run `just oss-go test ./internal/locallink/...` from the monorepo root. The
tests cover concurrent readers/editors, stale sessions, matching-parent false
positives, ambiguous edits, outside contributors, checkpoint discovery and
offline behavior.
