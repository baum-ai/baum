Fill the schema changes tile: the database schema this pull request changes, table by table, each fact cited to the hunk that shows it.

## Find the schema changes

Read the diff by its content, not by file roles or names. A schema change is any of:

- DDL in added or removed lines of a non-test file: `CREATE TABLE`, `CREATE [UNIQUE] INDEX`, `CREATE TRIGGER`, `ALTER TABLE` (`ADD COLUMN`, `DROP COLUMN`, `RENAME`, `ALTER COLUMN`, `ADD CONSTRAINT`), `DROP TABLE`, `DROP INDEX`. DDL is often in a string inside Go, Python, TypeScript or Ruby code that runs when a store opens.
- A migration file: a numbered or timestamped file under a `migrations` or `migrate` directory, or a Rails/Django/Alembic/Prisma/Knex migration.
- A schema dump: `schema.sql`, `structure.sql`, `db/schema.rb`, `schema.prisma`.
- An ORM model whose fields map to columns, when the pull request adds, removes or retypes a field.
- A query file (such as sqlc `queries/*.sql`): list it under migrations with kind `query_file`; it changes no table.

Leave out DDL in tests, fixtures and test helpers, and statements that only appear in comments or documentation. A table the code merely renames in a variable is not renamed, and DDL that moves to another place unchanged (a refactor) changes no table. A JSON Schema is not a database schema.

## Fill each part

- `engine`: from the driver, the dialect (`AUTOINCREMENT`, `jsonb`, `SERIAL`) or the migration tool. `unknown` when nothing says.
- `migrations`: one entry per place that runs the schema change. A down migration is not an entry of its own: cite it after its up migration. `applies`: `on_start` for DDL the program runs when it opens the database; `on_deploy` for migration files a tool runs at release; `manual` when a README or script says someone runs it. `reversible`: true with a down migration or rollback that undoes it; false when there is none for a tool that supports one, or the down step cannot restore dropped data; null for DDL run on start. `idempotent`: true for `IF NOT EXISTS` and guarded `ALTER`s; false when a second run fails (a plain `ADD COLUMN`) or catches the error to carry on; null when the tool records what ran.
- `tables`: one entry per table, never several tables in one name. A created table lists every column with `before: null` and `after` as written (`INTEGER NOT NULL REFERENCES widgets(id)`). An altered table lists the columns that change; it may add, as `unchanged` with `before` equal to `after`, a few columns the diff shows beside them when they help read the change (such as the column a backfill copies from), never more than the changed ones. A dropped table lists no columns unless the diff shows them. For a renamed column, `change: "renamed"` and `before` starts with the old name.
- `destructive` on a column: true for a removed column, a narrowed or changed type, `NOT NULL` added to existing rows without a default, or a rename that old code still reads. Otherwise false.
- `indexes`: indexes, constraints and triggers, under the table they belong to; a primary key inline in `CREATE TABLE` only when it spans several columns. `online`: true for `CONCURRENTLY` or on a table created in this pull request; false for a plain index build or a validated constraint on an existing Postgres or MySQL table; always null for SQLite, and when unknown.
- `data_changes`: statements that change existing rows as part of the schema change: backfills, `UPDATE`/`DELETE`/`INSERT … SELECT` in a migration, a one-time copy when the store opens. Not what the application writes as it runs (saving, syncing, pruning): that is the code's behaviour, not the migration's.
- `readers`: at most eight, the code that queries a changed table directly: query files, generated code (`*.sql.go`), store methods, models. Not their callers, handlers or tests. `updated: true` and cite it when this pull request changes it for the new schema. Name a reader with `updated: false` only when you can see it still uses the old shape, and say where in `label`.

## Cite

Every migration, table and data change cites at least one hunk; a reader cites its hunks when the pull request changes it. A citation is `{"type": "hunk", "id": "<hunk id from the diff>", "lines": [first, last]}`. `lines` are lines of the new file, inside the hunk's new range, and should cover the statement itself: when a 300-line new file creates three tables, cite each `CREATE TABLE` by its lines, not the whole hunk. For a statement only in removed lines (a dropped table), cite the hunk without `lines`. Put the most telling hunk first: the reader sees it as the table's statement.

## When something is unknown or absent

Use `null` for a value the diff does not show, never a guess. Use empty arrays for parts with nothing in them. When a patch is cut off before the statement ends, describe what is shown and say in `note` that the rest is not in the diff.

When the pull request creates, alters, drops or renames no table, column, index or constraint and changes no rows in a migration (it only edits queries, or its DDL is only in tests or moves unchanged), answer that the tile does not apply and write no data: a tile that says the schema does not change is noise.

## Example

A Go store adds a table and a column when it opens a SQLite database:

```json
{
  "engine": "sqlite",
  "migrations": [{"kind": "embedded", "label": "store.Open", "applies": "on_start", "reversible": null, "idempotent": true,
    "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [40, 52]}]}],
  "tables": [
    {"name": "widget_tags", "change": "created", "renamed_from": null,
     "columns": [{"name": "widget_id", "change": "added", "before": null, "after": "INTEGER NOT NULL REFERENCES widgets(id)", "destructive": false, "note": null},
                 {"name": "tag", "change": "added", "before": null, "after": "TEXT NOT NULL", "destructive": false, "note": null}],
     "indexes": [{"name": "widget_tags_by_tag", "change": "added", "definition": "(tag, widget_id)", "unique": true, "online": null}],
     "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [40, 46]}]},
    {"name": "widgets", "change": "altered", "renamed_from": null,
     "columns": [{"name": "archived_at", "change": "added", "before": null, "after": "TEXT", "destructive": false, "note": "NULL for existing rows"}],
     "indexes": [], "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [48, 52]}]}],
  "data_changes": [],
  "readers": [{"label": "widgets.List", "updated": true, "cites": [{"type": "hunk", "id": "h_9f8e7d6c5b4a", "lines": [12, 30]}]}]
}
```
