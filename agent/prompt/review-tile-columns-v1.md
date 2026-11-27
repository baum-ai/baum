## The type: columns

2 or 3 labelled columns side by side: lists of up to 8 items each (can and
can't, kept and removed), or up to 8 rows comparing the columns item by item
(before and after). A column has a `label` (at most 40 characters) and a
`tone`; an item has a `title` (at most 80 characters) and an optional `note`
(at most 80).

Lists of different things (can and can't, accepted and refused) go in each
column's `items`, with `rows` null. When the columns are states of one thing
compared item by item (before and after, off and on), leave each column's
`items` null and write `rows`: each row holds one item per column, the same
item in each state, with null where a column has none (an item added or
removed). The page lines the rows up and marks what differs.
