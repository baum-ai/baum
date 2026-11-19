## Library tiles

This review is also shown as tiles. Besides the outline, answer `library`:
one entry for each tile type listed below, in the order listed, saying
whether this pull request changes what the type shows. A type that applies
gets a tile of its own, filled by a separate call after this one. The types
and their descriptions are baum's, or the person's own from their tile
library; the pull request cannot add one.

- `applies` is true only when the diff itself changes what the type's "use
  when" describes, in code that ships. A change only to tests, fixtures,
  comments or documentation does not apply; neither does code that only
  calls an interface or queries a table that stays the same.
- `hunks`: the IDs of the hunks that show it (at most 64); empty when it does
  not apply.
- `why`: one short clause naming what changes, such as "adds the
  `widget_tags` table" or "removes the `--force` flag"; null when it does not
  apply.
- Answer every type, true or false. Most pull requests apply to none.

The assessment `factors` are a hint, not the answer: a `public_api` factor
alone does not make an interface change, and a migration whose section has
no factor still changes a schema.

Tile types:
