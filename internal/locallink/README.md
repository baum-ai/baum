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
establishes that session as the creator. The call must have succeeded (a
rejected, denied or failed call, such as `git commit` with nothing to commit,
establishes nothing, and a call that waited for permission counts only if it
then succeeded) and its command must write Git: the first Git subcommand (after
global options such as `-C` and `-c`) is `commit`, `rebase`, `cherry-pick`,
`revert`, `am` or `pull`. A call that merely overlaps, such as a long test run
or a poll of a running command, establishes nothing. Commits made through a
wrapper script (`make release`) get no reflog evidence and fall back to the
other evidence. Subagent calls count for their top-level session. Calls are
read from any tool the session graph marks as a command (the execute verb) that
records its start and end times; today that is Claude Code and Codex.

A commit object is made once, so only the earliest creation entry of a commit
counts. A later entry is a ref moving to an existing commit, and
`cherry-pick --ff` is never creation. The entry's time is the committer time
Git recorded, so `GIT_COMMITTER_DATE` or a script that preserves dates can move
it outside the call.

The HEAD logs (the main working tree's and each linked worktree's) are
authoritative: a commit one records is placed only by a call in that working
tree, and a HEAD log of unknown working tree places nothing. Only a commit no
HEAD log records comes from the branch logs, which know no working tree, so only
a call in a working tree that no longer exists, on the same branch (the
session's recorded branch at the call; unknown matches nothing), may match. This
covers plain commits, amends, cherry-picks and reverts of a removed worktree,
not its rebase picks (a branch log records only the end of a rebase) and not
once the branch is deleted. Reflogs expire with Git's gc (90 days by default).

A call starts in its `workdir`, else in the nested session's `cwd` attribute
and working directory, then the top-level session's. Its commands are read by
[agentcmd](https://github.com/baum-ai/agentcmd) without tree-sitter: Claude
Code's `Bash`, Codex's `exec_command` and `shell` with their `workdir`, and
Codex code-mode `exec` programs. A code-mode program is stored as its
JavaScript plus the command lines it runs; agentcmd reads the program itself,
so each `tools.exec_command({...})` call is a command with its own `workdir`
and no `cd` carried between them. A command or workdir the program builds at
run time (a template, a variable, a shorthand `{workdir}`, a spread or
computed key) gives no evidence. Another tool marked as a command is read as
shell from its `command` or `cmd` field. Only commands that run count, in a
command substitution or a script fed to a shell too, never quoted text or a
heredoc that is only data, and a script that does not parse gives no
evidence. A `cd` or `pushd` to a literal path moves the directory within its
scope (a subshell has its own), and `bash -c` scripts, shell functions and
wrappers (`env -C`, `timeout`, `nohup`, `xargs`) are followed. A dynamic path,
`~`, `cd -` or `pushd +N` leaves the directory unknown, and so does a `cd`
inside a conditional or loop. `git notes` and `git apply` make no commit in a
working tree's history. Unplaced, too:
- commands run on another host or in a container (`ssh`, `docker`, `kubectl`);
- commands run by `find`, since `-execdir` runs them in each match's directory;
- Git redirected by `--git-dir`, `--work-tree`, `core.worktree` or a config
  include, and options that only print (`--exec-path`, `--version`);
- a `--dry-run`;
- every command of a call that mentions `GIT_DIR`, `GIT_WORK_TREE`,
  `GIT_COMMON_DIR` or a `GIT_CONFIG` variable, which agentcmd does not follow,
  also when spelled with JSON, JavaScript or `$'...'` escapes;
- a Windows path in a `workdir`.

A directory inside a nested repository (a
submodule, or a clone within the working tree: a `.git` entry below the working
tree's root) is not placed, since its commits are not in this reflog.
Directories map to the deepest existing linked worktree, else a removed
worktree kept in an agent's conventional folder inside the main working tree,
else the main one.

A command that yielded while it ran (Codex `exec_command` with a short
`yield_time_ms`, whose result reports a process id as `"session_id":N` or
`session ID N`) can finish during later polls. Its window then extends to the
last successful `write_stdin` or `wait` call on the same process, following
polls that still find it running until one finds it finished. Where the result
does not report the process id (a program that does not print it), the window
is the call's own. Only a call that ran one command is extended this way: in a
code-mode program with several, a process id may be another command's (a test
run after the commit). A poll that finds the process exited with a non-zero
status means the command failed, and the call gives no evidence.

A code-mode program that completed says nothing about its commands. It gives
evidence only when its printed results show how each went (an exit status or
a process id per command) and none exited with a non-zero status. A Codex call
whose result reports a non-zero exit gives none either.

When several sessions fit, or none, the commit stays unresolved. Merges are
never reflog evidence. A reflog match outranks a hook observation of the same
commit, which guesses the session from the process tree; hook observations
still decide when the reflog has no single Git-writing creator. A background
command that commits after its tool call returned, and that no poll covers, lies
outside every window. Entries are read from the newest 8 MiB of each reflog.
`ReflogFingerprint` hashes the creation entries of the pull request's commits
so the evidence and feature caches notice a new one and ignore other reflog
activity.

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
