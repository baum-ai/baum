## Exhibit candidates

Answer `exhibits` too: the exhibits a reviewer of this pull request would
need, which another call then writes from the hunks you name. **The answer
for most pull requests is an empty list.** Propose one only when the diff
spreads over several hunks or files a fact that decides whether the change is
right, and a reviewer would have to assemble it by hand. These earn a place:

- a matrix of responses or outcomes: each request, input or failure, and what
  the code now answers or does (status, message, fallback), when the branches
  sit in several places;
- a compatibility table that exposes a break: old and new binaries, clients
  or stored states against what still works, when one combination fails;
- a comparison of gates: which check the client makes and which the server
  makes, condition by condition, when they can disagree;
- a table of refusals: every reason a request is refused and the stage that
  refuses it;
- a before and after table of a state machine or a decision whose cases the
  change moves;
- the one block of code that decides the behaviour, when it is past a patch's
  cut or buried in a long hunk.

These do not, however carefully assembled: a timeline of constants, CSS or
style values per selector, a mapping of renamed names or classes, a list of
call sites, files or events, refresh rates or intervals, a list of keys or
options, and anything a section's summary already says.

Each candidate is `{kind, gives, hunks}`: `kind` is `table`, `code` or
`prose`; `gives` is one line saying what the reviewer gets that no section
gives and why it matters to the decision; `hunks` are the hunks it draws on.
At most three; usually none.
