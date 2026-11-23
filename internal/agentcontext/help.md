# baum app help

baum is a desktop app and local daemon that helps a person review pull requests and keep track of what their coding agents decided and built. The sidebar lists its pages; each page below is a section, then the words the app uses and its keyboard shortcuts. Ask for one section with the topic argument: a page name, a word, or shortcuts. Use the link tool for the address of a page.

## inbox

The Inbox is the first page. It shows the saved inboxes, each a query over pull requests, and for the open inbox its Up next queue: the pull requests that wait on the person, with what each waits for (a review, a fix, a merge). Rows can be reviewed, fixed, merged, snoozed or archived from the keys. Inboxes are made, edited and managed from the page.

## pulls

Pull requests lists every pull request of the picked repositories in tabs: Open, any saved tabs the person made (each a saved filter), and Closed. A filter narrows the list. Opening a row opens the pull request page.

## timeline

The Timeline shows what was decided and built, from the person's agent sessions. Its Features tab lists features, each a group of related pull requests, sessions and decisions; its All decisions tab lists every decision. It opens on human decisions. The filter takes repo, who (human or agent), category, person, file, date, status (Features tab only) and words.

## sessions

Sessions lists the agent sessions (Claude Code, Codex and others) running or recently ended on this computer, grouped by those that need an answer, wait, run or ended. A session can be peeked at, replied to, ended, brought forward in its terminal, and its transcript opened. The 1 to 9 keys answer a question or permission a session waits on.

## analytics

Analytics charts agent activity, usage and the pull request flow over a window of 7, 30 or 90 days, filtered by repository and agent.

## jobs

Jobs lists the background runs of baum: reviews, fixes, chats and feature builds, with their state (queued, running, completed or failed), cost and transcript. A job opens on its own page.

## repositories

Repositories lists the picked repositories and lets the person add more from GitHub. A repository's own page has the sections Reviews (automatic or on request, decisions), Prompts (the review prompts for that repository) and Registration (its local clones).

## settings

Settings has eight sections: General (keyboard and plan usage), Appearance (the theme mode and the light and dark colour themes, with a thumbnail of each), Reviews (the review agent and automation), Fixes (the fix agent), Connections (GitHub and agent hooks), This computer (diagnostics, command line, updates, environment), Prompts (the prompts baum sends agents, customisable) and Tile library (the person's saved tile types). The 1 to 8 keys go to a section.

Colour themes can be changed from any page, not only Appearance, and every change is a proposal the app previews at once: the person saves or dismisses it in the session panel. propose_theme makes a custom theme; when it names a custom theme the person has (theme_id), saving updates that theme in place, keeping its slots, and the card also offers Save as new theme. propose_theme_choice switches to an existing theme (built-in or custom) and/or the theme mode: it fills the light or dark theme in use unless the person names the other one, which then only shows as a thumbnail.

The Tile library section lists the tile types the person saved, each marked Custom, with where it came from (made in chat or imported), its use-when line, the repositories it is pinned to and when a review last used it. A type opens in a pane (the page context's focus then names it as type): previews of its samples, its fill instructions, paths and versions. The person renames a type (its title only), pins it to repositories (a pinned type is always filled in that repository's generated reviews), restores an earlier version, exports it as a file, imports one and deletes it with Undo, all on the page. Edit in chat opens the session panel about the open type: read it with tile_type, change it with propose_tile_type, which the person saves.

## pull_request

The pull request page shows one pull request: its description, checks, conflicts and review threads, and baum's review of it, a saved version per head commit. A rail on the left leads through the review: Overview (the summary, verdict, risk, screenshots, main diagram and what was asked), How it works (Where this fits, then each section: its gist, a body of text, points and tiles, and its risks), Files changed (every changed file, grouped by type, folder or section, beside its diff), and the pull request's Decisions, Sessions and Commits. A review written before section bodies shows its own summary and section pages in the same rail. The page context's focus names the view, the place and, on How it works, the section the person is reading (context for Where this fits). The person can comment, ask an agent about it in the session panel (a, or Ask), merge, fix, and send a GitHub review (approve, comment or request changes). The agent uses propose_review_comment for a comment in the person's pending review. Nothing changes until the person clicks Save in the panel; the comment then joins their draft, without sending anything to GitHub. Questions asked of the review's agent before the session panel are listed read-only under Earlier questions.

On the review page, the person can add tiles to the review from the session panel, at once, with no Save: this is the one exception to proposals. Use a built-in or library tile type whenever one can show what the person asks for; make a custom tile only when none makes sense. add_tile adds a built-in type, drawn by the page itself (flow, columns, sequence, table, code, and images for screenshots), or one of baum's library tile types (such as api_change, "Interface changes") on a section of the page: the section the person is reading by default, or the one you name (a section id, or context for Where this fits). Read tile_type first and write its data yourself from the diff and checkout you have, then pass it to add_tile as data: the tile shows at once. baum checks the data against the type's schema and this version's hunks and, when it does not fit, says where and changes nothing; fix it and call again. A built-in type always needs data and keeps it for later versions. For images, save each image to a file (a screenshot you took, using the way the repository's agent instructions describe if they do, or one downloaded from the description) and pass its absolute path: baum reads, downscales and stores it, so never pass image bytes. A library type without data shows as being filled while baum fills it in a separate run; baum fills it again for each later version of the pull request. When the person asks for a tile no built-in or library type gives, make one: start_tile shows a pending card on the section where it will stand; read tile_guide (the guide and the starter); write the type in your own scratch directory, never in the repository's tree; check it with check_tile, with data for this pull request that cites its hunks (the diff tool gives each hunk's id) and synthetic samples written for a made-up repository; fix what fails and check again; then add_custom_tile with the check that passed. To show a screenshot in a custom tile, put {"image_file": its absolute path} in the data where the schema has a string for it: baum stores the image and the tile gets it as a data:image URL, so never write image bytes or base64. The tile shows at once as "Custom · <title>". Saving it to the person's tile library is a proposal (propose_tile_type, with a check that passed on a synthetic sample), which "Save to library…" on its menu asks for. An added tile shows on every version of this pull request, on this computer only, and never changes the review's content; the person takes it off with Remove on its menu, which has Undo. Say what was added and where.

Beyond what the page shows, the pull request's tools read its whole history: review_threads with state resolved or all (with replies), pull_request_timeline (pushes and force-pushes, review submissions, comments, labels, assignments, ready for review, and what mentions it), session_transcript a turn range at a time (from_turn, limit) and transcript_search for words in a session, review_version_diff (what changed between two saved review versions), related_pull_requests (its stack, what mentions it, and open or recently merged pull requests touching the same files), git_log and git_blame on the clone (at the head, the merge base or a commit, with the pull request each commit came from where baum knows it), file for any file of the clone at the head or the base, issue for a GitHub issue the description or commits reference, and validation_runs for the checks the linked sessions ran. Issues are GitHub's only: baum has no Linear or other tracker integration. The agent that generates a review reads all of these, and every other tool that only reads, with a read-only token; it never proposes, edits or saves anything, and never reads the person's draft review or their conversations.

## group

A group page shows a review group: pull requests reviewed together, as one combined review. baum makes a group for a stack of pull requests; people make their own. It has its members, the ones that landed, and whether its review is outdated.

## session

A session page shows one agent session's transcript, turn by turn, with its pull request when it has one, and a reply box for a session waiting at its prompt.

## job

A job page shows everything kept about one job and its transcript, for a fix or chat the person can watch while it runs.

## feature

A feature is open in the Timeline's side panel: its title, summary, status, people, pull requests, sessions and key decisions.

## words

- **inbox**: a saved query over pull requests with its own queue; the Inbox page shows one.
- **up next**: the queue of pull requests waiting on the person in an inbox, in the order to act on them.
- **group**: pull requests reviewed together as one combined review; a stack is a group baum makes.
- **review**: baum's saved, generated review of a pull request at one head commit; a push makes a new version.
- **fix**: an agent run that changes a pull request's branch to resolve review threads, failing checks or conflicts; it is a job.
- **session**: one run of a coding agent on this computer, seen through its hooks.
- **job**: a background run by baum: a review, fix, chat or feature build.
- **timeline**: the page of features and decisions drawn from sessions.
- **decision**: a choice made in an agent session, with who made it (human or agent), its category and summary.
- **feature**: what a set of related pull requests, sessions and decisions built, kept in the feature log.
- **prompt**: the instructions baum gives an agent for a task; Settings and each repository can customise them.
- **tile type**: the definition of a custom tile: its component, the schema of its data, how to fill it and when to use it. baum's own are built in; a chat can propose saving one to the person's tile library, and tile_type reads one in full.
- **tile library**: the person's own saved tile types, on this computer, each with versions, synthetic samples and Undo for deleting. propose_tile_type adds or changes one, and nothing is saved until the person clicks Save in the panel; saved reviews keep the definitions they were made with. Types come from a tile's check and are never changed from repository content.
- **tile**: a diagram, table, code excerpt, images or custom tile on the review page: the overview's main diagram, or one placed on a section of How it works.

## shortcuts

Mod is Cmd on a Mac and Ctrl elsewhere; the Settings page can choose another modifier for the page keys. Lists work with the keys below; `Escape` clears the row focus.

Anywhere:

- `Mod+K`: Go to a page, pull request or session.
- `Mod+.`: actions for the focused row, as a right click.
- `Mod+R`: refresh the page.
- `⌃N`, `⌃P` on a Mac, `Ctrl+N`, `Ctrl+P` elsewhere: down and up in any list or menu.
- `Mod+1` to `Mod+8`: the sidebar's pages in order: Inbox, Sessions, Timeline, Pull requests, Analytics, Repositories, Jobs, Settings. `Mod+,` opens Settings.
- `⌘[` and `⌘]` on a Mac, `Alt+←` and `Alt+→` elsewhere: back and forward.
- `?`: the help menu, then the shortcuts list, or hide it.
- `⌥ Option` on a Mac, `Alt` elsewhere: hold to show the page's keys.
- `Escape`: close the list.

Lists (Inbox, Pull requests, Sessions, Jobs, Repositories, Timeline):

- `j`, `↓`: next row. `k`, `↑`: previous row. `g`, `Home`: first row. `G`, `End`: last row.
- `Enter`, `o`: open the row (the Inbox's Up next takes `Enter` only).
- `Escape`: clear the row focus.

Pull requests (and, for `m`, `f` and `t`, the Inbox):

- `x`: select or deselect. `Space`: peek at the review below the row, or show or hide a group's pull requests.
- `m`: merge, where offered. `f`: fix, where offered. `t`: watch the fix agent or read its transcript. `r`: refresh from GitHub (Pull requests only).
- `/`: open the filter (Inbox and Pull requests).
- `h`, `←` and `l`, `→`: previous and next tab on Pull requests.
- `Mod+Shift+G`: open the focused pull request on GitHub (Inbox, Pull requests and a pull request page).

Inbox (Up next):

- `v`: review: approve, comment or request changes.
- `s`: snooze, then a number key for until when.
- `e`: archive. `u`: undo a snooze or archive.
- `⇧J`, `⇧K`, `⌃↓`, `⌃↑` on a Mac, `Shift+J`, `Shift+K`, `Ctrl+↓`, `Ctrl+↑` elsewhere: next and previous inbox.

A pull request or group:

- `j`, `k`: next and previous item; `⇧J`, `⇧↓`, `→` (`Shift+J`, `Shift+↓` elsewhere): next section; `⇧K`, `⇧↑`, `←` (`Shift+K`, `Shift+↑`): previous section.
- `Space`: open or close an item's details. `Enter`: open the item.
- `c`: comment. `v`: send your review. `a`: ask an agent in the session panel.
- `]`: toggle the right-hand sidebar; `h`, `l`: Details, Activity and Comments in it.
- `u`, `Escape`: back to the list.

Sessions and transcripts:

- `p`: open the session's pull request. `r`: reply. `f`: bring its terminal forward.
- `1–9`: answer the focused question or permission; `Enter` submits several.
- In a transcript, `j`, `k` move between messages, `Space` shows all of one and `Enter` opens it.

The session panel:

- Control and the backtick key: show the panel, or write in it; from its composer, hide it. `↑` at the composer's start leaves it for the session, on its latest message.
- `j`, `k`: next and previous message; past the last, back to the composer. `g`: the session's start. `G`: its end.
- `Space`: show all of the message, or less again. `a`: open or close the message's agent activity.
- `Enter`, `i`: back to the composer. `Esc`: leave the composer for the session, then give the keys back to the page. `q`: hide the panel, out of its composer.
- `PageUp`, `PageDown`: scroll the session, from the composer too.

Repositories: `a`: add repositories, or finish adding; `/`: search GitHub repositories while adding.

Timeline: `/`: search the features or decisions; `Enter` opens or closes one.

Settings:

- `1–8`: go to a section. `j`, `k`, `↓`, `↑`: move. `Enter`, `l`, `→`: go into the section; `Space`, `Enter`: change the focused control.
- `Escape`, `h`, `←`: back to the sections.
