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

Settings has six sections: General (appearance and keyboard), Reviews (the review agent and automation), Fixes (the fix agent), Connections (GitHub and agent hooks), This computer (diagnostics, command line, updates, environment) and Prompts (the prompts baum sends agents, customisable). The 1 to 6 keys go to a section.

## pull_request

The pull request page shows one pull request: its description, checks, conflicts and review threads, and baum's review of it, a saved version per head commit. The review has a walkthrough (chapters of behaviours) and a full review (sections of risks, tiles and files). The person can comment, ask an agent about it in the session panel (a, or Ask), merge, fix, and send a GitHub review (approve, comment or request changes). Questions asked of the review's agent before the session panel are listed read-only under Earlier questions.

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
- `c`: comment. `v`: send your review. `a`: ask an agent in the session panel.
- `]`: toggle the right-hand sidebar; `h`, `l`: Details, Activity and Comments in it.
- `u`, `Escape`: back to the list.

The walkthrough:

- `j`, `k`: next and previous behaviour. `→`, `←`: next and previous chapter.
- `o`: show the behaviour's code. `c`: comment. `v`: full review. `a`: ask an agent in the session panel.

Sessions and transcripts:

- `p`: open the session's pull request. `r`: reply. `f`: bring its terminal forward.
- `1–9`: answer the focused question or permission; `Enter` submits several.
- In a transcript, `j`, `k` move between messages, `Space` shows all of one and `Enter` opens it.

Repositories: `a`: add repositories, or finish adding; `/`: search GitHub repositories while adding.

Timeline: `/`: search the features or decisions; `Enter` opens or closes one.

Settings:

- `1–6`: go to a section. `j`, `k`, `↓`, `↑`: move. `Enter`, `l`, `→`: go into the section; `Space`, `Enter`: change the focused control.
- `Escape`, `h`, `←`: back to the sections.
