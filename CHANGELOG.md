# Changelog

One `## vX.Y.Z` section per release, newest first, written for users and
without internal tracking identifiers. A release candidate's notes are its
version's section: `v0.2.0-rc.1` and `v0.2.0` both use `## v0.2.0`.

## v0.2.0

baum 0.2.0 makes the desktop app and daemon much lighter on an idle machine, reads the Codex sessions Codex has compressed, and adds custom prompts, inbox archiving and tighter links between reviews and code.

### Highlights

- **Compressed Codex sessions.** Codex compresses sessions to `.jsonl.zst` a few weeks after they end. baum now finds, links and shows them; before, most of a long-time Codex user's history was missing.
- **Quieter when idle.** The daemon and the app wake far less often, re-read and re-render only what changed, and pause polling while the window is hidden. Session, review and transcript work is cached and decoded incrementally.
- **Custom prompts** for reviews, fixes and data extraction.
- **Inbox:** archive pull requests, and saved inboxes are matched by the daemon.

### Reviews

- Hunk IDs in review prose link to their file and line, and show as chips in Up next.
- Review threads are read live in the review pane.
- Local reviews use screenshots from linked sessions.
- Huge pull requests are reviewed with a reduced diff index instead of failing.
- Up next says when there is no review and offers to generate one.
- Reading-time estimates are shorter and closer to reality.

### Desktop app

- ⇧⌘G opens the pull request in hand on GitHub.
- App updates download through downloads.usebaum.com.
- Linux packages are built on Ubuntu 24.04.
- A run's cost is shown during the run as well as after it.

### Agents and jobs

- Claude Code's output is no longer capped at 32,000 tokens, and Claude plan usage survives rate limits.
- Manual jobs run first, and busy pull requests keep their place in the queue.
- After an upgrade the daemon restarts once running jobs finish; queued jobs run on the new version.
- A fix that changed nothing is shown as an outcome, not a failure. The fix agent gets the failed CI step rather than the runner's cleanup.
- baum installs its hook dispatchers and names sessions from the agent's environment.

### GitHub

- Discovery finishes on repositories with hundreds of open pull requests, polls incrementally, and polls repositories concurrently.
- The GitHub access token is cached instead of launching the credential helper per request.
- Failing checks are kept past GitHub's 100-check page, and pull requests whose checks never started say "No checks reported".
- Setup explains organizations that restrict OAuth app access, and how to use an existing GitHub token.

### Removed

- The CLI's hosted commands (`login`, `logout`, `whoami`), uploads and repository setup. The CLI now holds what the desktop app needs.
- Installing, configuring and delegating hooks to Entire. Checkpoints Entire records on its own are still read.

### Fixes

- Merge authorization and the Inbox's placement of stacked pull requests.
- Job workers keep running after a failed journal write.
- SQLite databases use the write-ahead log, with one handle per database.

## v0.1.0

First public release.
