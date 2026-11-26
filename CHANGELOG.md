# Changelog

One `## vX.Y.Z` section per release, newest first, written for users and
without internal tracking identifiers. A release candidate's notes are its
version's section: `v0.2.0-rc.1` and `v0.2.0` both use `## v0.2.0`.

## v0.3.0

baum 0.3.0 moves the desktop app to Electron, adds a session panel on every page that works with your own Claude Code or Codex session, and lays the review page out as an Overview, How it works and Files changed.

### Highlights

- **Electron.** The desktop app now runs on Electron instead of Tauri, with Chromium's engine and codecs on every platform. The Linux packages no longer need WebKitGTK or GStreamer.
- **Session panel.** A panel that drops down over any page runs a conversation with your own Claude Code or Codex session, with the page you are on as its context. It replaces Ask on pull requests. It can read what the app shows, including a failing check's log, and propose changes to settings, prompts, inboxes and review comments that only happen when you save or confirm them. Messages sent while it works are queued, and an ended session can be continued in it.
- **Review page.** A rail with Overview, How it works, Files changed, Decisions, Sessions and Commits, in every state of a review. The Overview gives the summary, a verdict only when something must happen before merging, the risk with its reason, screenshots, the description's video clips and a main diagram. Each section of How it works has views for its code, tests, docs and session.
- **Automatic reviews per inbox.** Review modes Never, On open, On change and Always, a customisable On change prompt, and a switch per inbox to review its pull requests automatically.

### Reviews

- A new version of a review revises the sections that changed instead of rewriting the whole review.
- Reviews write plainer prose and add code recommendations.
- Stacks get an overview page instead of one combined review.
- The review of a merged or closed pull request can be generated again.
- Local reviews capture the videos in a pull request's description. Images open in a larger, resizable viewer, and videos can go full screen.
- Files changed puts each file's tests in its diff header. "Where this fits" opens its files whole, beside the context or in a dialog.
- Keys: `[` for the section list, `h` and `l` for a section's views, and `j` and `k` across the whole Overview and a section's files.
- Review groups get a time limit that grows with their members.
- Experimental: **Generate reviews as tiles** (Settings, Reviews; off by default) writes reviews in levels with diagrams: flows, sequence diagrams with lifelines, tables and columns that mark what changed, word-by-word before and after in schema and interface tiles, and code as its hunk's diff. Custom tile types can show images by file path.

### Inbox and pull requests

- Sort inboxes and Pull requests by several keys, saved with the inbox.
- Each inbox row shows an unread count, and each pull request a dot for its review state.
- Inbox and Pull requests load page by page.
- Snooze a whole stack at once.
- Merge is offered whenever GitHub can merge, and Enter retries a merge GitHub refused. Merges in read-only repositories are left out of the actionable Inbox.
- A failing check's log tail opens on the pull request page. Cancelled checks are counted and can be re-run.
- Agent sessions waiting for you appear under an Up next review.
- Activity is counted only once a pull request has been opened.

### Sessions and agents

- Use several Claude Code and Codex accounts, chosen per agent in Settings.
- The Sessions page opens a pull request's sessions and reads sessions that have ended. Sessions running in Herdr can be focused.
- A session running in a terminal is never continued or written to from the app.
- Close or archive any session from the session panel's picker.
- Agents baum runs get baum's own tools for reading the app's pages and proposing changes you confirm.
- Fix agents run in a sandboxed shell with their own Git repository. A fix push that adds credentials, nested repositories or untrusted workflow changes is refused. The fix agent's session is linked to the commit baum pushes.
- Local commits are linked to sessions only by their recorded creation. Commits still count when the configured Git email changes case.
- Expired plan sign-ins are shown, and a permission wait ends once the transcript shows it was turned down.
- Git hook dispatchers are installed whether or not a repository uses Entire. Worktrees are listed on Git older than 2.36.

### Desktop app

- Installed copies move to Electron with their next update, as with any release: the macOS app and the Linux AppImage replace themselves and keep their settings, setup, command line, menu entry and start at login. Saved view preferences in the app's pages, such as a folded inbox or the review's file grouping, go back to their defaults once. A deb install updates by installing the new deb.
- Every update is still checked against baum's release signing key before it is installed.
- Custom colour themes, with built-in themes, in a new Appearance tab.
- The sidebar folds and opens under the pointer, and folds when the pointer leaves the window.
- The shortcuts sheet is searchable and opens from Go to. `g`, `G`, Home and End jump to a list's first or last row; Shift+J and Shift+K move between Settings sections; ⌘1 returns to the last inbox you saw.
- The Walkthrough is gone; the tour ends at the session panel.
- A left click on the Linux tray icon opens the app, and the macOS tray icon fits the menu bar.
- A failed GitHub refresh is retried within seconds.

### Fixes

- Many small layout, wording, keyboard and accessibility fixes from a full audit of the app, including controls that stayed hidden in narrow or short windows.
- Retry starts a new read instead of joining one that may never answer.
- A long job transcript scrolls inside its panel, and long transcripts render progressively.
- Codex recovery turns keep their activity.

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
