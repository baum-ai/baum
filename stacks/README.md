# stacks

Detects stacked pull requests: a chain or tree of a repository's pull
requests, each based on another's head branch (A ← B ← C). `Plan` is a pure
function from the candidate pull requests and the stored stacks to the
changes that bring the stacks in line. The hosted platform stores stacks in
PostgreSQL; the local app keeps them as review
groups (`internal/reviewgroups`, "Stack groups").

## Detection

- B's parent is A when both are in the same repository, neither comes from a
  fork (`is_cross_repository`), both have the same author, and B's
  `base_ref` equals A's `head_ref`. The author rule keeps a shared branch's
  own pull request (develop into main) from gathering everyone's pull
  requests based on that branch.
  Branch chaining is the only signal: no Graphite, ghstack or spr markers, and
  stacks that each target the default branch are not detected.
- A stack is a connected group of at least two such pull requests. A new
  stack forms only when at least two of them are open. Two children of one
  parent give one stack shaped as a tree.
- A recompute considers the repository's open pull requests and the current
  members of its stacks that have not ended, or that have an open member. A
  merged or closed pull request that is not already a member never takes
  part, so a single open pull request based on the branch of one merged
  before detection forms no stack.
- When several pull requests share the head branch an open one is based on,
  its stored parent wins, then the open one with the lowest number, then the
  lowest number. Branches naming each other in a loop lose the parent link of
  the loop's lowest-numbered pull request.
- `plan.go` holds the rules as a pure function (`Plan`): from the
  candidates and the stored stacks to the rows to write. Planning again after
  applying them gives no change.

## Identity and membership

As the hosted platform stores stacks, with every member and its parent. The
local app plans only open pull requests over its stack groups, which store no
parents, and dissolves a group where a hosted stack ends.

- Members are recorded by pull request number with their parent's number
  (`NULL` for the root). A pull request is a current member (`left_at IS
  NULL`) of at most one stack; leaving keeps the row, and joining again clears
  `left_at` and restarts `joined_at`.
- A merged or closed member stays, with its parent. When the bottom pull
  request merges and GitHub retargets the next one to the bottom's base, the
  next one keeps its merged parent and stays in the stack. If several
  descendants are automatically retargeted to the merged ancestor's base,
  they keep their stored chain, even when the descendant updates arrive
  before their parents' updates.
- Any other retarget of an open member loses its parent: without a merged
  ancestor explaining the new base, after the parent was closed without
  merging, or onto a branch other than the merged ancestor's base. If that
  leaves the member outside the part that keeps the stack, it gets `left_at`;
  the rest go on.
- Each stack goes to at most one recomputed group, oldest stack first:
  - a group joining several stacks keeps the oldest id, and the others end
    with every member left;
  - a stack that split goes to the part sharing the most of its members (then
    the larger part, then the part with the lowest number). The other part
    leaves it and forms a new stack if at least two of its pull requests are
    open;
  - a stack left with fewer than two members ends with every member left.
- `ended_at` is set when every current member is merged or closed and cleared
  when one reopens. Such a stack keeps its members, so its page stays
  readable.
