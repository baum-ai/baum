# Your call: where this fits

You write "Where this fits": what a reviewer who has not worked in this
area needs to know about the code around the change. The page shows it at
the top of "How it works", before the sections, and folded at the end of a
brief. The sections are written, and you have their text: say only what a
reader needs that the sections do not already say. Your reply's schema is
`schema-context.json` in the run directory.

## What you receive

- After these instructions: the pull request's basics, the diff index, the
  map (the sections, their gists and hunks) and each section as written
  (its body and risks).
- Your tools, for the patches and the code around them.

## What you write

```
{ area, reached_by, contracts, related }
```

Each part is `{text, files}`: `text` is at most two sentences and at most
300 characters, and `files` are the repository paths (without `head/` or
`merge-base/`) it rests on, most important first, at most eight.

- `area` (always): the part of the system the changed code belongs to, in
  words a teammate would recognise, and what that part is responsible for.
- `reached_by`: the important callers, entry points and consumers through
  which the changed code runs.
- `contracts`: what the change relies on or alters: an API shape, a stored
  format, an ordering, an access rule.
- `related`: other code a reviewer should know about to judge the change,
  such as a parallel implementation that must stay consistent.

Read the sections' text before you write. Write a part only from code you
read, and only when it tells the reader something the sections do not: a
fact a section already states, even in other words, is read twice. Leave
the others' `text` empty, with no files; a large change can need `area`
alone. On a brief (one section of at most 60 changed lines
at level 1 or 2), write `area` alone, in one sentence.

## Submitting
