# Review groups

A review group is a named set of open pull requests of one repository
reviewed as one: either chosen by a person in the inbox ("Review together"),
when the members need not be stacked or related by branch, or made by baum
for a stack of pull requests (a **stack group**, below). Generating the group writes one
combined `baum.review/1` document across every member: how the changes fit
together, risks that cross members (the same file or function changed twice,
conflicting assumptions, merge order), and each member's own findings in
sections attributed to the members whose hunks they cover.

This package owns the group definitions, the group review versions and the
`/api/groups` routes. Generation itself runs through the job queue
(`internal/jobs`) and `generate.Publish`, like a pull request review.

## Rules

- A group has a name (1 to 80 characters, the schema's `stack.title` bound)
  and 2 to 8 members (`MaxMembers`), all pull requests of **one repository**.
  A request naming pull requests of several repositories is refused with
  `cross_repository`; groups across repositories are a later step.
- Members are kept in ascending number order. Renaming, adding and removing
  members and deleting the group are allowed at any time; a group never has
  fewer than two members. A stack group is the exception: it may have one
  open member while the rest of its stack merged (`landed`, below).
- Deleting a group removes it from every list. It never deletes a saved
  review: the members' own reviews stay in their clone's store and the
  group's combined versions stay in `app.sqlite`, unreachable.
- Generating needs a registered, available clone of the repository and a
  saved review of every member (any version). The member reviews are input
  to the group run and the schema records, per member, the review version
  the group review read (`stack.members[].review_version`, required). A
  member without a saved review is refused before any model call with
  `members_need_reviews`, naming them; the app offers to generate those
  reviews first.
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
  changes. A stack of more than eight open pull requests gets no group.
- A member that **merged or closed stays in its stack group**, in its place,
  in `landed` with its state (`merged`, `closed` or `unknown`): it is no
  longer in `members`, so no review, automatic job or outdated check reads
  it, and the group page lists these first, marked Merged, Closed or No
  longer open. Discovery lists only open pull requests, so the group keeps
  each member's parent, branches and author (`groups.chain`, written by every
  sync) and plans a member discovery no longer lists from that. The state
  comes from discovery's observations and is kept once known. A member
  with no observation yet (or a stale open one) is `unknown` until the next
  poll after its merged or closed state is observed, a brief window, and a
  stack group saved before `chain` existed has no saved branches, so a
  member that merges before its first sync leaves it. The group goes on
  while one member is open, even if only one; automatic generation needs two
  open members.
- A stack without a group gets one (`stack` true), named after its bottom
  pull request's title as "Review together" names a group after the first,
  until its review names it: a stack group's name is baum's, so every
  stored version of its review renames it to the name the model wrote for
  the group (see Generation). The app shows a stack group, and its automatically generated review, as it
  shows a group a person made and generated, marked as a stack: the hosted
  list's stack icon and count on its row, and "Stack" instead of "Review
  group" on its page. A stack that gained or lost members (a parent merged,
  a pull request added on top) gives its group the new members; one that is
  gone (no open member left, or the stack split apart) dissolves its group,
  which leaves every list like a deleted one and keeps its versions.
- The person's choices win. A pull request in a group the person made, or in
  a stack group the person deleted, takes no part in stacks. Renaming a
  stack group or changing its members makes it the person's (`stack` false),
  so sync no longer changes or dissolves it.
- With automatic generation on for the repository, a stack group's review
  follows its members' (see `internal/automatic`): it is queued once every
  member is eligible, has a saved review of its current head and no
  automatic job left to submit, unless the group's latest version already
  read every current member at that head (a merged member alone calls for
  none), and once automatic generation handled at least one member's current
  head: a stack reviewed before automatic generation was enabled, or only by
  hand, is not caught up. Otherwise, and for groups a person made, the group
  review is generated on request.

## Storage

The group tables share the private [`app.sqlite`](../appdb/README.md)
application database in the state directory, not a clone: a group outlives
any one clone.

| Table | Holds |
| --- | --- |
| `groups(id, repository, name, members, stack, chain, created, updated, deleted)` | definitions; `members` is a JSON array of numbers; `stack` marks a stack group; `chain` is a stack group's saved placements (parent, branches, author, merged flag) for every member, merged ones included; `deleted` is a soft-delete time, 0 while live |
| `versions(group_id, version, metadata, members, document)` | immutable combined reviews keyed by group ID and version; `metadata` is `store.Metadata`; `members` is the reviewed `stack.members` copied out of the document for listing |

Group versions are keyed by group ID, never by `{repository, number}`, so
they never collide with a member's own versions in the clone's
`versions.sqlite`. `Store.Versions(id)` adapts a group to the
`generate.Versions` interface `generate.Publish` takes: `Put` appends only
while the expected version is still the latest. Run leases are not
recorded for groups; the job queue shows a running group job.

## Generation

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
   (`agent/prompt` `GroupPrompts`, version `group-review-public-v4`).
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

The group page renders the combined review with the shared viewer (the
group name as heading, member labels on sections). Patches come from
`GET /api/groups/{id}/patch`, which picks the member whose comparison
(`index.file_comparisons`) changes the path and whose hunk header matches the
cited hunk's range. Sessions and commits are not listed for a group in v1;
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
version_count, latest, risk_score, reviewed, outdated}`: `latest` is the
latest version's metadata (or null), `reviewed` the members it covered with
their heads, and `outdated` as above. Every mutation publishes an `index`
event.
