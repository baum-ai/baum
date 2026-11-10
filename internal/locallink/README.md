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

Git reflogs supply creation evidence too, for commits, amends, cherry-picks,
reverts, applied patches and rebase picks (`pull --rebase` included). A reflog
entry made while one session's shell tool call ran, in the same working tree,
establishes that session as the creator, but only when that call's command
writes Git: its first Git subcommand (after global options such as `-C` and
`-c`) is `commit`, `rebase`, `cherry-pick`, `revert`, `am` or `pull`. A call
that merely overlaps, such as a long test run, a poll of a running command or a
call waiting for permission, establishes nothing. Commits made through a wrapper
script (`make release`) get no reflog evidence and fall back to the other
evidence. Subagent calls count for their top-level session.

The HEAD logs (the main working tree's and each linked worktree's) are
authoritative: a commit one records is placed only by a call in that working
tree, and a HEAD log of unknown working tree places nothing. Only a commit no
HEAD log records comes from the branch logs, which know no working tree, so only
calls in working trees that no longer exist may match. This covers plain
commits, amends, cherry-picks and reverts of a removed worktree, not its rebase
picks (a branch log records only the end of a rebase) and not once the branch
is deleted. Reflogs expire with Git's gc (90 days by default).

A call is placed by its `workdir`, else the session's `cwd` attribute, else its
working directory; a `cd` or `git -C` to an absolute path moves it, and one that
cannot be followed (`~`, a variable) leaves the call unplaced. Directories map
to a working tree by path, and a removed worktree kept in an agent's
conventional folder inside the main working tree still maps. When several
sessions fit, or none, the commit stays unresolved. Merges are never reflog
evidence. A reflog match outranks a hook observation of the same commit, which
guesses the session from the process tree; hook observations still decide when
the reflog has no single Git-writing creator. A background command that commits
after its tool call returned lies outside every window. Entries are read from
the newest 8 MiB of each reflog. `ReflogFingerprint` hashes the creation entries
of the pull request's commits so the evidence and feature caches notice a new
one and ignore other reflog activity.

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
