# Review groups

A review group is a named set of open pull requests of one repository: either
chosen by a person in the inbox ("Review together"), when the members need not
be stacked or related by branch, or made by baum for a stack of pull requests
(a **stack group**, below). Generating a group a person made writes one
combined `baum.review/1` document across every member: how the changes fit
together, risks that cross members (the same file or function changed twice,
conflicting assumptions, merge order), and each member's own findings in
sections attributed to the members whose hunks they cover. A stack group has
no combined review: its page is an overview of its members' own reviews (see
Stack overview).

This package owns the group definitions, the group review versions, the stack
summaries and the `/api/groups` routes. Generation itself runs through the
job queue (`internal/jobs`) and `generate.Publish`, like a pull request
review.

## Rules

- A group has a name (1 to 80 characters, the schema's `stack.title` bound)
  and at least two members, all pull requests of **one repository**. A group
  a person makes has at most 8 (`MaxMembers`), since its combined review reads
  every one; a stack group has no bound.
  A request naming pull requests of several repositories is refused with
  `cross_repository`; groups across repositories are a later step.
- Members are kept in ascending number order. Renaming, adding and removing
  members and deleting the group are allowed at any time; a group never has
  fewer than two members. A stack group is the exception: it may have one
  open member while the rest of its stack merged (`landed`, below).
- Deleting a group removes it from every list. It never deletes a saved
  review: the members' own reviews stay in their clone's store and the
  group's combined versions stay in `app.sqlite`, unreachable.
- Generating a combined review is for a group a person made. One of a stack
  group is refused (`ErrStackOverview`, a not-found), and a job queued for one
  by an earlier release is cancelled when it would start, as for a group that
  went away.
- Generating needs a registered, available clone of the repository and a
  saved review of every member (any version). The member reviews are input
  to the group run and the schema records, per member, the review version
  the group review read (`stack.members[].review_version`, required). A
  member without a saved review is refused before any model call with
  `members_need_reviews`, naming them; the app offers to generate those
  reviews first.
- A group's review runs under a longer time limit than a member's: the
  review time limit plus half of it for each member after the first, at
  most three times it (15 minutes for two members with the default 10). It
  is written in one pass, and writing its document alone takes minutes.
- A group review is **outdated** when its latest version covered a different
  set of members, or a member's head (from discovery) differs from the head
  the version reviewed. An unknown current head is not outdated.

## Stack groups

Stacked pull requests are grouped by default. After every complete discovery
poll the daemon finds the repository's stacks (`Stacks`) and keeps its stack
groups in step (`Store.SyncStacks`):

- A **stack** follows the hosted platform's rules, shared in
  [`stacks`](../../stacks/README.md) (`stacks.Plan`): pull requests by one
  author, each based on another's head branch (`base_ref` = `head_ref`), as a
  chain or a tree; forks take no part. The open pull requests are planned over
  the live stack groups, which keeps a group's identity when its stack
  changes. A stack of any size gets a group: with no combined review there
  is nothing to bound.
- A member that **merged or closed stays in its stack group**, in its place,
  in `landed` with its state (`merged`, `closed` or `unknown`): it is no
  longer in `members`, so no summary or risk reads it, and the stack page
  lists these first, marked Merged, Closed or No longer open. Discovery lists only open pull requests, so the group keeps
  each member's parent, branches and author (`groups.chain`, written by every
  sync) and plans a member discovery no longer lists from that. The state
  comes from discovery's observations and is kept once known. A member
  with no observation yet (or a stale open one) is `unknown` until the next
  poll after its merged or closed state is observed, a brief window, and a
  stack group saved before `chain` existed has no saved branches, so a
  member that merges before its first sync leaves it. The group goes on
  while one member is open, even if only one.
- A stack without a group gets one (`stack` true), named after its bottom
  pull request's title as "Review together" names a group after the first.
  A stack group that a combined review of an earlier release renamed keeps
  that name. A stack that gained or lost members (a parent merged, a pull
  request added on top) gives its group the new members; one that is gone
  (no open member left, or the stack split apart) dissolves its group,
  which leaves every list like a deleted one and keeps its versions and
  summary.
- The person's choices win. A pull request in a group the person made, or in
  a stack group the person deleted, takes no part in stacks. Renaming a
  stack group or changing its members makes it the person's (`stack` false),
  so sync no longer changes or dissolves it, and it becomes a group with a
  combined review, generated on request.
- Automatic generation never queues a combined review: a stack's members
  get their own automatic reviews, and its summary follows them (below).

## Stack overview

A stack group's page shows the stack and its pull requests, each opening its
own review page, with two parts worked out from the open members' saved
reviews:

- **Risk** is the riskiest member: the open member whose latest saved review
  has the highest `summary.risk_score.score`, the lowest in the stack on a
  tie. No model call; the API reads it from the review index on each
  request.
- **What changed** is the stack summary: two to four plain sentences about
  the whole stack, written by one short model call (`Summarizer`) from the
  members' saved reviews, never the code. The call reads each open member's
  review title, summary and up to 12 current section titles, in stack order,
  each member cut to an equal share of 64 KiB. The prompt is published as
  `prompts/stack-summary-public-v1.txt` and asks for plain sentences: no
  lists, no line counts, no reveal colons.

The `Summarizer` is woken after every finished job (a new saved review) and
every stack sync that changed a group (new membership). It compares each
stack group's open members' latest saved review versions in the review index
with those its stored summary read, and only when they differ, with at least
two members reviewed, reads those reviews from the clone and makes the call.
A stack with fewer than two reviewed members keeps whatever summary it has
and gets no new one. One call runs at a time. A failed call, or an unusable
reply, is retried after 2 and then 4 minutes, and after three attempts not
again until the members' reviews change; a daemon restart starts over.

The call goes through the decision log (`decisionlog.Calls`): the data
extraction backend and its cheap decision model unless the preferences name
others, the cost reserved and the call recorded in the ledger with stage
`stack_summary` and item `group:<id>`. The daemon runs the `Summarizer` only
when the decision log's store is open.

## Storage

The group tables share the private [`app.sqlite`](../appdb/README.md)
application database in the state directory, not a clone: a group outlives
any one clone.

| Table | Holds |
| --- | --- |
| `groups(id, repository, name, members, stack, chain, created, updated, deleted)` | definitions; `members` is a JSON array of numbers; `stack` marks a stack group; `chain` is a stack group's saved placements (parent, branches, author, merged flag) for every member, merged ones included; `deleted` is a soft-delete time, 0 while live |
| `versions(group_id, version, metadata, members, document)` | immutable combined reviews keyed by group ID and version; `metadata` is `store.Metadata`; `members` is the reviewed `stack.members` copied out of the document for listing |
| `stack_summaries(group_id, summary)` | one row per stack group, replaced when written again: `summary` is JSON with the text, the member reviews it read (`members`, each `{number, review_version}`), the model, the prompt and the time |

A stack summary is not a review version: it has no document and no
history, and keeping the member review versions it read in the row lets
the list tell whether it is current from the review index alone. Combined
review versions an earlier release wrote for a stack group stay in
`versions`, unlisted while the group is a stack group; they show again if
the person takes the group over.

The group list reads version counts and latest summaries in one query for
all live groups. The `groups(repository, deleted, created, id)` index bounds
stack synchronization reads to the repository being synchronized.

Group versions are keyed by group ID, never by `{repository, number}`, so
they never collide with a member's own versions in the clone's
`versions.sqlite`. `Store.Versions(id)` adapts a group to the
`generate.Versions` interface `generate.Publish` takes: `Put` appends only
while the expected version is still the latest. Run leases are not
recorded for groups; the job queue shows a running group job.

## Generation

Only a group a person made is generated. What follows says how a stack
group's review was named and queued automatically too; that applied before
stacks had overviews, and is how the combined versions stored for stack
groups were written.

A job item names `{clone, group}` instead of `{clone, pr}`; its `pr` is 0, so
no pull request logic (automatic scheduling of pull requests, notices)
matches a group job. An automatic group job's head is its members' heads
(`Prepared.Members`): it is busy or a duplicate only against jobs of the same
group. Preparation reads every member's head, state and
author permission from GitHub like a pull request job: a member from a fork
or an author without known write access needs the isolation confirmation
for the whole group. Its budgets are one run's, as for a pull request. A
queued job is rechecked before running: every member head must be the one it
started from.

`generate.Run` with `Options.Group`:

1. Assembles each member with the ordinary assembler (no description
   images), each into its own checkout archive. Each member's checkout gets
   an equal share of the checkout byte and file budgets.
2. Merges them (`assemble.MergeGroup`) into one bundle: the group's name and
   a list of the members with their descriptions as the pull request header
   (number and branches of the lowest member), every member's hunks in member
   order with duplicates (the same change in two members) listed once,
   the union of checks, conversation, threads, sessions and references,
   and `FileComparisons` with one comparison per member. The merged bundle is
   fitted to the configured caps again and digested.
3. Unpacks each member's checkout at `members/<number>/head/` and
   `members/<number>/merge-base/` in the run directory, and writes
   `members.json`: each member's number, title, reviewed head and a compact
   projection of its saved review (summary, sections and risks). A section
   whose details are pending or failed has "Details for this section are
   unavailable." in place of its summary.
4. Runs the agent loop with the public group prompt
   (`agent/prompt` `GroupPrompts`, version `group-review-public-v7`).
   The model names the group: its schema asks for `stack` with only a
   `title` (`agent.Run.ModelSchema`). Before validation the host fills the
   rest of `stack` (`members` = number, head SHA and member review version)
   and each section's `pull_requests`, the members whose hunks the section
   lists. The `title` is the model's name, on one line and cut to 80
   characters, for a stack group, and the group's name for a group the
   person made or took over, or when the model wrote none. A previous group
   version's sections keep only the members still in the group.
5. `Store.Put` stores the version and, in the same transaction, renames a
   group that is still a stack group to the document's `stack.title` when
   the name rules accept it. A person's rename made the group theirs
   (`stack` false) and is never written over.

The stored document is a stack-shaped `baum.review/1`: `review.pull_request`
is the lowest member's number, as a hosted stack review holds its root
member, and `stack` names the members. No schema change.

## Viewing

A stack group's page is its overview (above). For a group a person made,
the group page renders the combined review with the shared viewer (the
group name as heading, member labels on sections). Patches come from
`GET /api/groups/{id}/patch`, which picks the member whose comparison
(`index.file_comparisons`) changes the path and whose hunk header matches the
cited hunk's range. The patch is masked as a stored hunk is
(`locallink.Patcher`, `bundle.RedactFilePatch`), as for a pull request's
`GET /api/patch`. Sessions and commits are not listed for a group in v1;
the viewer shows the sessions the document recorded.

## HTTP

Behind the daemon's authenticated host. JSON is decoded strictly; errors are
`{"code","message"}`.

| Route | Result |
| --- | --- |
| `GET /api/groups` | `{"available":true,"groups":[Group]}` |
| `POST /api/groups` `{"name","members":[{"repository","number"}]}` | create (201); `cross_repository`, `invalid_group`, `not_picked` (400) |
| `GET /api/groups/{id}` | one group (404 when missing or deleted) |
| `PATCH /api/groups/{id}` `{"name"?,"members"?:[number]}` | rename or set members |
| `DELETE /api/groups/{id}` | delete (204) |
| `GET /api/groups/{id}/versions` | `store.Metadata` newest first |
| `GET /api/groups/{id}/document?version=` | the latest or the named version |
| `GET /api/groups/{id}/patch?version=&path=&hunk=` | `{"patch": string or null}` |

`Group` is `{id, repository, name, members, stack, landed, created_at, updated_at,
version_count, latest, risk_score, reviewed, outdated, overview?}`: `latest` is the
latest version's metadata (or null), `reviewed` the members it covered with
their heads, and `outdated` as above. A stack group reports no versions
(`version_count` 0, `latest` null, `reviewed` empty, never `outdated`), its
`risk_score` is its riskiest member review's, and it has `overview`:
`{riskiest, reviewed, summary, current}`, the riskiest member's number (absent
when no review has a score), how many open members have a saved review, the
stack summary (or null) and whether that summary read every open member's
latest saved review. Its `versions` route answers `[]` and its `document`,
`patch` and `file` routes 404. Every mutation publishes an `index` event.

`PATCH` goes through `API.Update`, which a chat's proposal to rename a group
or add and remove its pull requests also saves with (see "MCP server for
agents" in `ARCHITECTURE.md`). It calls `Store.UpdateWith`, `Update` with the
name and members chosen from the group as the writing transaction reads it:
an error the choice returns (a proposal read at another state of the group)
writes nothing. `CheckUpdate` refuses what `Update` would refuse of a name
or members, without writing.
