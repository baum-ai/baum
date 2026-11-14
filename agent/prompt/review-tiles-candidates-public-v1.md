## Exhibit candidates

Answer `exhibits` too: the exhibits a reviewer of this pull request would
need, which another call then writes from the hunks you name. Most pull
requests need none, and an empty list is then the right answer. Propose one
when both of these hold:

- A reviewer would otherwise have to open three or more places (hunks,
  files or both) to assemble it.
- It bears on whether the change is correct or safe: what the code answers,
  refuses or does in each case, not how it is written.

Tables of that kind earn a place, for example:

- what each request, input or failure gets now: status, message, redirect,
  fallback or cost estimate, branch by branch;
- where each step or link sends the reader, before and after;
- what happens for each stored state when the program opens, reads or
  migrates it, or which binary can read what;
- which writes or events trigger an effect (a cache invalidation, a
  revision, a background read) and which do not;
- which check the client makes and which the server makes, condition by
  condition;
- every reason a request is refused and the stage that refuses it;
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
gives and why it matters to whether the change is right; `hunks` are the
hunks it draws on. At most three.
