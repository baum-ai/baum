# Review groups

A review group is a named set of open pull requests of one repository that a
person chose in the inbox ("Review together") to review as one. The members
need not be stacked or related by branch. Generating the group writes one
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
  fewer than two members.
- Deleting a group removes it from every list. It never deletes a saved
  review: the members' own reviews stay in their clone's store and the
  group's combined versions stay in `groups.sqlite`, unreachable.
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

## Storage

`groups.sqlite` (0600) in the daemon's state directory (`jobs.Directory()`,
beside `jobs.json`), not in a clone: a group outlives any one clone.

| Table | Holds |
| --- | --- |
| `groups(id, repository, name, members, created, updated, deleted)` | definitions; `members` is a JSON array of numbers; `deleted` is a soft-delete time, 0 while live |
| `versions(group_id, version, metadata, members, document)` | immutable combined reviews keyed by group ID and version; `metadata` is `store.Metadata`; `members` is the reviewed `stack.members` copied out of the document for listing |

Group versions are keyed by group ID, never by `{repository, number}`, so
they never collide with a member's own versions in the clone's
`versions.sqlite`. `Store.Versions(id)` adapts a group to the
`generate.Versions` interface `generate.Publish` takes: `Put` appends only
while the expected version is still the latest. Run leases are not
recorded for groups; the job queue shows a running group job.

## Generation

A job item names `{clone, group}` instead of `{clone, pr}`; its `pr` is 0, so
no pull request logic (automatic scheduling, duplicate heads, notices)
matches a group job. Preparation reads every member's head, state and
author permission from GitHub like a pull request job: a member from a fork
or an author without known write access needs the isolation confirmation
for the whole group. The estimate is one run's budget, as for a pull
request. A confirmed job is rechecked before running: every member head must
be the confirmed one.

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
   projection of its saved review (summary, sections and risks).
4. Runs the agent loop with the public group prompt
   (`agent/prompt` `GroupPrompts`, version `group-review-public-v1`).
   Before validation the host fills what the model never writes: the
   document's `stack` (`title` = group name, `members` = number, head SHA and
   member review version) and each section's `pull_requests`, the members
   whose hunks the section lists. A previous group version's sections keep
   only the members still in the group.

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

`Group` is `{id, repository, name, members, created_at, updated_at,
version_count, latest, risk_score, reviewed, outdated}`: `latest` is the
latest version's metadata (or null), `reviewed` the members it covered with
their heads, and `outdated` as above. Every mutation publishes an `index`
event.
