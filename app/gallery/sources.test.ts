// @vitest-environment node
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { cacheSizes, limits, scanSources } from "./sources.ts";

const flow = { id: "dg_flow", type: "flow", title: "Request path", data: { nodes: [] } };
const table = { id: "dg_table", type: "table", title: "Limits", data: { rows: [] } };
const review = { schema: "baum.review/2", tiles: [flow] };

function composition(extra: Record<string, unknown> = {}) {
  return {
    schema: "baum.composition/2",
    overview: { diagram: "dg_flow" },
    sections: [{ id: "sec_api", tiles: ["dg_table"] }],
    diagrams: [flow, table],
    ...extra,
  };
}

let root = "";
const write = (path: string, value: unknown) => {
  mkdirSync(dirname(join(root, path)), { recursive: true });
  writeFileSync(join(root, path), typeof value === "string" ? value : JSON.stringify(value));
};

type Row = [string, number, number, unknown, string | null];

/** A store as the desktop app keeps it; `without` drops a column, as in an older store. */
function store(path: string, rows: Row[], options: { wal?: boolean; without?: string } = {}) {
  mkdirSync(dirname(join(root, path)), { recursive: true });
  const database = new DatabaseSync(join(root, path));
  if (options.wal) database.exec("PRAGMA journal_mode=WAL");
  const columns = ["document BLOB NOT NULL", "composition TEXT"].filter(
    (column) => !column.startsWith(`${options.without} `),
  );
  database.exec(
    `CREATE TABLE versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, ${columns.join(", ")}, PRIMARY KEY(repository,pr,version))`,
  );
  for (const [repository, pr, version, document, stored] of rows) {
    const values = { document: JSON.stringify(document), composition: stored };
    const names = columns.map((column) => column.split(" ")[0] as "document" | "composition");
    database
      .prepare(
        `INSERT INTO versions (repository, pr, version, metadata, ${names.join(", ")}) VALUES (?, ?, ?, '{}', ${names.map(() => "?").join(", ")})`,
      )
      .run(
        repository,
        pr,
        version,
        ...names.map((name) =>
          name === "document" ? Buffer.from(values.document) : values.composition,
        ),
      );
  }
  return database;
}

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "diagram-gallery-"));
});
afterEach(() => {
  vi.useRealTimers();
  rmSync(root, { recursive: true, force: true });
});

describe("scanSources", () => {
  it("reads replay compositions with their placements, and v2 documents whole", () => {
    write("runs/20261114T101500Z/cases/case_a/composition.json", composition());
    write("runs/20261114T101500Z/cases/case_a/document.json", { schema: "baum.review/1" });
    write("runs/20261114T101500Z/cases/case_b/composition.json", {
      schema: "baum.composition/1",
      layout: { entries: [] },
    });
    write("runs/20261114T101500Z/cases/case_c/composition.json", "{ not json");
    write("runs/20261115T090000Z/cases/case_a/document.json", review);
    const scan = scanSources({ runs: join(root, "runs") });
    expect(scan.entries).toEqual([
      {
        key: "replay:20261114T101500Z/cases/case_a/composition.json:dg_flow",
        source: "replay",
        origin: "20261114T101500Z · case_a",
        id: "dg_flow",
        type: "flow",
        title: "Request path",
        placement: "overview",
        data: flow.data,
      },
      expect.objectContaining({ id: "dg_table", placement: "sec_api" }),
    ]);
    expect(scan.documents).toEqual([
      {
        key: "replay:20261115T090000Z/cases/case_a/document.json",
        source: "replay",
        origin: "20261115T090000Z · case_a",
        document: review,
      },
    ]);
    expect(scan.problems).toEqual([
      "replay 20261114T101500Z/cases/case_c/composition.json: not valid JSON",
      "replay: 1 skipped, baum.composition/1, not baum.composition/2",
    ]);
  });

  it("reads each pull request's latest version in a store: its v2 document or composition", () => {
    const v1 = { schema: "baum.review/1" };
    const database = store("code/baum/.git/baum/reviews/versions.sqlite", [
      ["acme/api", 11, 1, v1, JSON.stringify(composition({ diagrams: [table] }))],
      ["acme/api", 11, 2, v1, JSON.stringify(composition())],
      ["acme/api", 12, 1, v1, JSON.stringify(composition())],
      ["acme/api", 12, 2, review, null],
      ["acme/api", 13, 1, v1, "{ secret content"],
      ["acme/api", 14, 1, v1, null],
    ]);
    const scan = scanSources({ stores: root });
    expect(scan.entries.map((entry) => [entry.key, entry.origin])).toEqual([
      ["store:code/baum:acme/api#11 v2:dg_flow", "code/baum · acme/api#11 v2"],
      ["store:code/baum:acme/api#11 v2:dg_table", "code/baum · acme/api#11 v2"],
    ]);
    expect(scan.documents).toEqual([
      {
        key: "store:code/baum:acme/api#12 v2",
        source: "store",
        origin: "code/baum · acme/api#12 v2",
        document: review,
      },
    ]);
    expect(scan.problems).toEqual(["store code/baum acme/api#13 v1: not valid JSON"]);

    // A new version replaces the one shown.
    database
      .prepare("INSERT INTO versions VALUES ('acme/api', 12, 3, '{}', ?, NULL)")
      .run(Buffer.from(JSON.stringify({ ...review, tiles: [table] })));
    database.close();
    expect(scanSources({ stores: root }).documents.map((entry) => entry.key)).toEqual([
      "store:code/baum:acme/api#12 v3",
    ]);
  });

  it("drops what it keeps for files and stores that are gone", () => {
    write("runs/r1/cases/a/document.json", review);
    write("files/a.json", flow);
    store("clone/.git/baum/reviews/versions.sqlite", [["acme/api", 1, 1, review, null]]).close();
    const directories = { runs: join(root, "runs"), files: join(root, "files"), stores: root };
    scanSources(directories);
    expect(cacheSizes()).toEqual({ files: 2, stores: 1 });
    rmSync(join(root, "runs/r1"), { recursive: true });
    rmSync(join(root, "files/a.json"));
    rmSync(join(root, "clone/.git/baum/reviews/versions.sqlite"));
    scanSources(directories);
    expect(cacheSizes()).toEqual({ files: 0, stores: 0 });
  });

  it("walks replay runs newest first, so its bound leaves out the oldest", () => {
    write("runs/20261101T000000Z/cases/a/document.json", review);
    write("runs/20261115T000000Z/cases/a/document.json", review);
    const files = limits.files;
    limits.files = 1;
    try {
      const scan = scanSources({ runs: join(root, "runs") });
      expect(scan.documents.map((entry) => entry.origin)).toEqual(["20261115T000000Z · a"]);
      expect(scan.problems).toEqual(["replay: stopped after 1 files in 4 directories"]);
    } finally {
      limits.files = files;
    }
  });

  it("finds a store in the app's fallback location, labelled by its directory", () => {
    store("share/baum/reviews/0a1b2c/versions.sqlite", [["acme/api", 1, 1, review, null]]).close();
    expect(
      scanSources({ stores: join(root, "share") }).documents.map((entry) => entry.origin),
    ).toEqual(["baum/reviews/0a1b2c · acme/api#1 v1"]);
  });

  // Root ignores the directory's read-only mode, so the plain open succeeds.
  it.skipIf(process.getuid?.() === 0)(
    "does not read a store as it is while a rollback journal is beside it",
    () => {
      const directory = join(root, "clone/.git/baum/reviews");
      store("clone/.git/baum/reviews/versions.sqlite", [["acme/api", 1, 1, review, null]], {
        wal: true,
      }).close();
      writeFileSync(join(directory, "versions.sqlite-journal"), "");
      chmodSync(directory, 0o555);
      try {
        const scan = scanSources({ stores: root });
        expect(scan.documents).toEqual([]);
        expect(scan.problems).toEqual([
          expect.stringMatching(/^store clone: ERR_SQLITE_ERROR \(.+\)$/),
        ]);
      } finally {
        chmodSync(directory, 0o755);
      }
    },
  );

  it("opens a WAL store on a read-only directory, with and without its -wal file", () => {
    const directory = join(root, "clone/.git/baum/reviews");
    store("clone/.git/baum/reviews/versions.sqlite", [["acme/api", 1, 1, review, null]], {
      wal: true,
    }).close();
    chmodSync(directory, 0o555);
    try {
      const closed = scanSources({ stores: root });
      expect(closed.problems).toEqual([]);
      expect(closed.documents).toHaveLength(1);
    } finally {
      chmodSync(directory, 0o755);
    }
    // An open writer keeps the -wal and -shm files, as a running app does.
    const writer = new DatabaseSync(join(directory, "versions.sqlite"));
    writer.exec(
      `INSERT INTO versions VALUES ('acme/api', 1, 2, '{}', '${JSON.stringify(review)}', NULL)`,
    );
    try {
      expect(scanSources({ stores: root }).documents.map((entry) => entry.key)).toEqual([
        "store:clone:acme/api#1 v2",
      ]);
    } finally {
      writer.close();
    }
  });

  it("reads what an older store has and names the missing column", () => {
    store("old/.git/baum/reviews/versions.sqlite", [["acme/api", 1, 1, review, null]], {
      without: "composition",
    }).close();
    const scan = scanSources({ stores: root });
    expect(scan.documents).toHaveLength(1);
    expect(scan.problems).toEqual(["store old: versions has no composition column"]);
  });

  it("finds stores by clone, without descending into clones or hidden directories", () => {
    const one: Row[] = [["acme/api", 1, 1, review, null]];
    store("code/a/.git/baum/reviews/versions.sqlite", one).close();
    store("code/group/b/.git/baum/reviews/versions.sqlite", one).close();
    store("code/a/vendor/c/.git/baum/reviews/versions.sqlite", one).close();
    store("code/node_modules/d/.git/baum/reviews/versions.sqlite", one).close();
    store("code/.hidden/e/.git/baum/reviews/versions.sqlite", one).close();
    expect(scanSources({ stores: root }).documents.map((entry) => entry.origin)).toEqual([
      "code/a · acme/api#1 v1",
      "code/group/b · acme/api#1 v1",
    ]);
    // A store's own directory, mounted directly.
    expect(
      scanSources({ stores: join(root, "code/a/.git/baum/reviews") }).documents.map(
        (entry) => entry.origin,
      ),
    ).toEqual([". · acme/api#1 v1"]);
  });

  it("walks the stores' directory again only after a while", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const one: Row[] = [["acme/api", 1, 1, review, null]];
    store("a/.git/baum/reviews/versions.sqlite", one).close();
    expect(scanSources({ stores: root }).documents).toHaveLength(1);
    store("b/.git/baum/reviews/versions.sqlite", one).close();
    expect(scanSources({ stores: root }).documents).toHaveLength(1);
    vi.advanceTimersByTime(31_000);
    expect(scanSources({ stores: root }).documents).toHaveLength(2);
  });

  it("reads loose files: a review, a composition, one diagram or an array", () => {
    write("files/composition.json", { diagrams: [flow] });
    write("files/single.json", table);
    write("files/many.json", [flow, flow]);
    write("files/review.json", review);
    write("files/other.json", { hello: "world" });
    write("files/notes.txt", "ignored");
    const scan = scanSources({ files: join(root, "files") });
    expect(scan.entries.map((entry) => entry.key)).toEqual([
      "file:composition.json:dg_flow",
      "file:many.json:dg_flow",
      "file:many.json:dg_flow#2",
      "file:single.json:dg_table",
    ]);
    expect(scan.entries[0]?.placement).toBeUndefined();
    expect(scan.documents).toEqual([
      { key: "file:review.json", source: "file", origin: "review", document: review },
    ]);
    expect(scan.problems).toEqual([
      "file other.json: expected a composition, a diagram or an array of diagrams",
    ]);
  });

  it("treats missing directories as empty and changes version only with the files", () => {
    const empty = scanSources({ runs: join(root, "none"), stores: join(root, "none") });
    expect(empty.entries).toEqual([]);
    expect(empty.documents).toEqual([]);
    expect(empty.problems).toEqual([]);

    write("files/a.json", flow);
    const first = scanSources({ files: join(root, "files") });
    expect(scanSources({ files: join(root, "files") }).version).toBe(first.version);
    write("files/a.json", { ...flow, title: "Changed path, longer" });
    const second = scanSources({ files: join(root, "files") });
    expect(second.version).not.toBe(first.version);
    expect(second.entries[0]?.title).toBe("Changed path, longer");
  });
});
