This pull request changes a database schema. Check, and raise a risk anchored on the statement for each that holds:

- A dropped table or column, or a narrowed type, loses data; is it copied first, and does every reader stop using it in the same release?
- `NOT NULL` added to a column of an existing table without a default fails or rewrites existing rows.
- A rename breaks code, queries or older builds that still use the old name while both versions run.
- A plain `CREATE INDEX` on an existing Postgres or MySQL table blocks writes while it builds; large tables need `CONCURRENTLY` or an online tool.
- SQLite cannot drop or alter most constraints in place; a rebuild must copy every row and keep foreign keys.
- DDL run when the program opens the database must be safe to run twice and on a database an older or newer build already changed; a guarded `ADD COLUMN` that relies on catching a duplicate-column error hides other failures.
- A migration with no down step, or whose down step cannot restore dropped data, cannot be rolled back.
- A backfill in a migration runs inside the deploy; is it bounded, batched and safe to run again?
- Queries, generated code and models that read a changed table must change with it.
