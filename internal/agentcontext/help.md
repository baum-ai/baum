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

Settings has seven sections: General (appearance and keyboard), Reviews (the review agent and automation), Fixes (the fix agent), Connections (GitHub and agent hooks), This computer (diagnostics, command line, updates, environment), Prompts (the prompts baum sends agents, customisable) and Tile library (the person's saved tile types). The 1 to 7 keys go to a section.

The Tile library section lists the tile types the person saved, each marked Custom, with where it came from (made in chat or imported), its use-when line, the repositories it is pinned to and when a review last used it. A type opens in a pane (the page context's focus then names it as type): previews of its samples, its fill instructions, paths and versions. The person renames a type (its title only), pins it to repositories (a pinned type is always filled in that repository's generated reviews), restores an earlier version, exports it as a file, imports one and deletes it with Undo, all on the page. Edit in chat opens the session panel about the open type: read it with tile_type, change it with propose_tile_type, which the person saves.

## pull_request

The pull request page shows one pull request: its description, checks, conflicts and review threads, and baum's review of it, a saved version per head commit. The review is laid out as sections of risks and files; with the Tiles (preview) review page (Settings, General) it opens on one screen of tiles instead, the page context's focus then naming the view, the tile open and the item in it. The person can comment, ask an agent about it in the session panel (a, or Ask), merge, fix, and send a GitHub review (approve, comment or request changes). The agent uses propose_review_comment for a comment in the person's pending review. Nothing changes until the person clicks Save in the panel; the comment then joins their draft, without sending anything to GitHub. Questions asked of the review's agent before the session panel are listed read-only under Earlier questions.

The tiles page's layout is the person's to change, and the session panel changes it at once, with no Save: this is the one exception to proposals. tile_layout lists the tiles as the page lays them out (rows from the top; the first 2 rows are the first screen, the first 3 the landing page), each tile's id, size, allowed sizes and whether it can be removed, and the focus: the tile the person pointed at (Ask about this tile, or `a` on a card) or has open, which "this", "it" or "that tile" mean; with no focus, ask which tile. edit_tiles moves tiles (to the top, the end, before or after another tile), resizes them (full, half or third width), hides them (they move to the Hidden tiles group at the end, with Show), shows them again, pins them (pinned tiles lead) and sets how many changes the changes group shows; one call is one undo step. add_tile adds a library tile type the review does not show yet (tile_layout lists them as addable, such as api_change, "Interface changes"), placed after the overview unless asked otherwise, or brings a removed tile back. For a library type, read tile_type first and write its data yourself from the diff and checkout you have, then pass it to add_tile as data: the tile shows at once. baum checks the data against the type's schema and this version's hunks and, when it does not fit, says where and changes nothing; fix it and call again. Without data the tile shows as being filled while baum fills it in a separate run; baum fills it again for each later version of the pull request. When the person asks for a tile no library type gives, make one: start_tile shows a pending card where it will stand; read tile_guide (the guide and the starter); write the type in your own scratch directory, never in the repository's tree; check it with check_tile, with data for this pull request that cites its hunks (the diff tool gives each hunk's id) and synthetic samples written for a made-up repository; fix what fails and check again; then add_custom_tile with the check that passed. The tile shows at once as "Custom · <title>" with Undo, like any layout edit. Saving it to the person's tile library is a proposal (propose_tile_type, with a check that passed on a synthetic sample), which "Save to library…" on its menu asks for. undo_tiles undoes the last change, whoever made it, and reset_tiles returns to the generated layout (and can itself be undone). The overview, and the risks tile while it lists a critical or high risk, always stay on the landing page. Edits change only the person's own layout of this pull request, on this computer, for every version of it; never the review's content. Say what changed, using the result's own words, and that Undo on the page takes it back; when something was refused, say why.

A repository's standing tile preferences are different, because they outlive this pull request and instruct every later review in the repository: tile_preferences reads them (text, and pins, the tiles that lead the page) and propose_tile_preferences proposes a change the person saves, which applies to the next review generated in the repository, with tile reviews on, never to the page open. A pin is a single tile type (overview, risks, evidence, intent, decisions, context, media, everything_else) or a library type such as schema_change. When the person asks to "always" do something with the tiles, propose the preference and, to change this page too, also call edit_tiles. The repository's own page (Repositories, Reviews) has the same Tile layout box and Pinned tiles, which the person saves directly.

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
- **layout**: the order and sizes of a review's tiles; the person may change it from the session panel or the page, for that pull request, and reset it to the generated one.
- **tile type**: the definition of a custom tile: its component, the schema of its data, how to fill it and when to use it. baum's own are built in; a chat can propose saving one to the person's tile library, and tile_type reads one in full.
- **tile library**: the person's own saved tile types, on this computer, each with versions, synthetic samples and Undo for deleting. propose_tile_type adds or changes one, and nothing is saved until the person clicks Save in the panel; saved reviews keep the definitions they were made with. Types come from a tile's check and are never changed from repository content.
- **tile**: one card of a review's tiles page (the overview, the risks, a change, evidence, context and so on), with an id the layout tools name it by.

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

- `j`, `↓`: next row. `k`, `↑`: previous row.
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
- `c`: comment. `v`: send your review. `a`: ask an agent in the session panel (on the tiles page, about the card the keys are on).
- On the tiles page, `Mod+Z`: undo the last change to the tiles' layout.
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

- `1–7`: go to a section. `j`, `k`, `↓`, `↑`: move. `Enter`, `l`, `→`: go into the section; `Space`, `Enter`: change the focused control.
- `Escape`, `h`, `←`: back to the sections.
