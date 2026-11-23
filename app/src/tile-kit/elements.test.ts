import { afterEach, describe, expect, it } from "vitest";
import { citationKeys, frame, type Frame } from "./harness";

const HUNK = { type: "hunk", id: "h_0123456789ab", lines: [12, 14] };

/** Synthetic payloads, keyed as the page keys them. */
const REFS = {
  '["hunk","h_0123456789ab",[12,14]]': {
    status: "ok",
    label: "store.go:12–14",
    navigable: true,
    path: "internal/widgets/store.go",
    change: "modified",
    role: "code",
    range: [10, 30],
    additions: 2,
    deletions: 1,
    excerpt: {
      header: "@@ -10,6 +10,7 @@ func Open()",
      omitted_before: 2,
      omitted_after: 5,
      lines: [
        { kind: "context", old: 12, new: 12, text: "db := open()" },
        { kind: "remove", old: 13, text: "db.Exec(`CREATE TABLE widgets`)" },
        {
          kind: "add",
          new: 13,
          text: 'db.Exec("CREATE TABLE widgets (id INTEGER)")',
          tokens: [
            ["db", "parameter"],
            [".", "punctuation"],
            ["Exec", "function"],
            ["(", "punctuation"],
            ['"CREATE TABLE widgets (id INTEGER)"', "string"],
            [")", "evil;background:url(x)"],
          ],
        },
        { kind: "add", new: 14, text: "return db", tokens: [["return", "keyword"]] },
      ],
    },
  },
  '["hunk","h_aaaaaaaaaaaa",null]': {
    status: "unavailable",
    label: "api.go",
    why: "comparing",
  },
  '["file","docs/api.md"]': {
    status: "ok",
    label: "api.md",
    navigable: false,
    path: "docs/api.md",
    hunks: 2,
  },
  '["commit","0123456789abcdef"]': {
    status: "ok",
    label: "0123456",
    navigable: true,
    sha: "0123456789abcdef",
    subject: "Add widgets",
  },
  '["risk","rsk_widgets"]': {
    status: "ok",
    label: "Widget rows can be lost",
    navigable: true,
    tier: "high",
    category: "data_loss",
    state: "open",
    text: "Dropping the column loses rows.",
    change: "sec_store",
  },
  '["evidence","ev_tests"]': {
    status: "ok",
    label: "Tests cover the migration",
    navigable: true,
    evidence_kind: "test",
    evidence_status: "verified",
    claim: "The migration test opens an old store.",
  },
  '["check","ci","unit"]': {
    status: "ok",
    label: "ci / unit",
    navigable: true,
    name: "unit",
    conclusion: "failure",
    required: true,
  },
  '["prompt","ses_1",3]': { status: "ok", label: "Prompt 3", navigable: true },
};

let current: Frame | null = null;
async function shown(face: "glance" | "detail", draw: string) {
  current = frame(`baum.tile({ glance: ${draw}, detail: ${draw} });`);
  current.init({ face, refs: REFS, data: { hunk: HUNK } });
  await current.next("ready");
  return current;
}
afterEach(() => {
  current?.remove();
  current = null;
});

const chip = (f: Frame, index = 0) =>
  f.root().querySelectorAll("baum-cite")[index] as HTMLElement & { firstElementChild: HTMLElement };

describe("baum.cite", () => {
  it("labels a hunk by its file and lines, with the path in the title", async () => {
    const f = await shown("glance", "({ data }) => baum.cite(data.hunk)");
    const cite = chip(f);
    expect(cite.dataset.kind).toBe("hunk");
    expect(cite.dataset.state).toBe("ok");
    expect(cite.textContent).toBe("store.go:12–14");
    expect(cite.firstElementChild.getAttribute("title")).toBe("internal/widgets/store.go:12–14");
  });

  it("is plain text in a glance and a button that jumps in a detail", async () => {
    const glance = await shown("glance", "({ data }) => baum.cite(data.hunk)");
    expect(chip(glance).querySelector("button")).toBeNull();
    glance.remove();
    const detail = await shown("detail", "({ data }) => baum.cite(data.hunk)");
    const button = chip(detail).querySelector("button")!;
    expect(button.type).toBe("button");
    button.click();
    expect(detail.sent.at(-1)).toEqual({
      protocol: "baum.tile/1",
      kind: "jump",
      key: '["hunk","h_0123456789ab",[12,14]]',
    });
  });

  it("labels each kind of citation", async () => {
    const f = await shown(
      "detail",
      `() => [
        baum.cite({ type: "file", path: "docs/api.md" }),
        baum.cite({ type: "commit", sha: "0123456789abcdef" }),
        baum.cite({ type: "risk", id: "rsk_widgets" }),
        baum.cite({ type: "evidence", id: "ev_tests" }),
        baum.cite({ type: "check", id: "ci", test: "unit" }),
        baum.cite({ type: "prompt", session: "ses_1", index: 3 }),
        baum.cite(data => 0),
      ]`,
    );
    const cites = [...f.root().querySelectorAll("baum-cite")] as HTMLElement[];
    expect(cites.map((cite) => cite.textContent)).toEqual([
      "api.md",
      "0123456",
      "Widget rows can be lost",
      "Tests cover the migration",
      "ci / unit",
      "Prompt 3",
      "Citation unavailable",
    ]);
    expect(cites[0]!.querySelector("button")).toBeNull();
    expect(cites[1]!.firstElementChild!.getAttribute("title")).toBe("Add widgets");
    expect(cites[2]!.querySelector<HTMLElement>(".baum-mark")!.dataset.tier).toBe("high");
    expect(cites[2]!.firstElementChild!.getAttribute("title")).toBe(
      "Dropping the column loses rows.",
    );
    expect(cites[3]!.querySelector<HTMLElement>(".baum-mark")!.dataset.tone).toBe("positive");
    expect(cites[4]!.querySelector<HTMLElement>(".baum-mark")!.dataset.tone).toBe("critical");
  });

  it("takes the author's label, keeping the resolved one as the title", async () => {
    const f = await shown(
      "detail",
      `() => baum.cite({ type: "risk", id: "rsk_widgets" }, "Data loss")`,
    );
    expect(chip(f).textContent).toBe("Data loss");
    expect(chip(f).firstElementChild.getAttribute("title")).toBe("Dropping the column loses rows.");
  });

  it("draws an unresolved citation dashed and counts it", async () => {
    const f = await shown("detail", `() => baum.cite({ type: "risk", id: "rsk_missing" }, "Mine")`);
    expect(chip(f).dataset.state).toBe("unavailable");
    expect(chip(f).textContent).toBe("Citation unavailable");
    expect(chip(f).querySelector("button")).toBeNull();
    const ready = f.sent.find((message) => message.kind === "ready")!;
    expect((ready.metrics as { unresolved: number }).unresolved).toBe(1);
  });

  it("draws an unavailable citation with its label and reason, uncounted", async () => {
    const f = await shown("detail", `() => baum.cite({ type: "hunk", id: "h_aaaaaaaaaaaa" })`);
    expect(chip(f).dataset.state).toBe("unavailable");
    expect(chip(f).textContent).toBe("api.go");
    expect(chip(f).firstElementChild.getAttribute("title")).toBe("Unavailable: comparing versions");
    const ready = f.sent.find((message) => message.kind === "ready")!;
    expect((ready.metrics as { unresolved: number }).unresolved).toBe(0);
  });

  it("keys every citation as the shared fixture says", async () => {
    const f = await shown("detail", "() => null");
    for (const { citation, key } of citationKeys) {
      const refs = key ? { [key]: { status: "ok", label: key, navigable: true } } : {};
      f.send({ protocol: "baum.tile/1", kind: "update", data: {}, refs });
      const payload = f.window.baum.refs.get(f.window.JSON.parse(JSON.stringify(citation)));
      expect(payload?.label ?? null, JSON.stringify(citation)).toBe(key);
    }
  });
});

describe("baum.diff", () => {
  it("draws a cited hunk's lines in a detail", async () => {
    const f = await shown("detail", "({ data }) => baum.diff(data.hunk)");
    const diff = f.root().querySelector("baum-diff") as HTMLElement;
    expect(diff.dataset.face).toBe("detail");
    expect(diff.dataset.state).toBe("ok");
    const head = diff.querySelector(".baum-diff-head")!;
    expect(head.querySelector("button")!.textContent).toBe("internal/widgets/store.go:12–14");
    expect(head.querySelector(".baum-diff-stat")!.textContent).toBe("+2 −1");
    const rows = [...diff.querySelectorAll("tbody tr")] as HTMLElement[];
    expect(rows[0]!.textContent).toBe("@@ -10,6 +10,7 @@ func Open()");
    expect(rows[1]!.textContent).toBe("⋯ 2 lines above");
    const lines = rows.filter((row) => row.dataset.kind);
    expect(lines.map((row) => row.dataset.kind)).toEqual(["context", "remove", "add", "add"]);
    expect([...lines[1]!.children].map((cell) => cell.textContent)).toEqual([
      "13",
      "",
      "−",
      "db.Exec(`CREATE TABLE widgets`)",
    ]);
    expect([...lines[2]!.children].slice(0, 3).map((cell) => cell.textContent)).toEqual([
      "",
      "13",
      "+",
    ]);
    const tokens = [...lines[2]!.querySelectorAll(".baum-code span")] as HTMLElement[];
    expect(tokens.map((token) => token.style.color)).toEqual([
      "var(--syntax-token-parameter)",
      "var(--syntax-token-punctuation)",
      "var(--syntax-token-function)",
      "var(--syntax-token-punctuation)",
      "var(--syntax-token-string)",
    ]);
    expect(lines[2]!.querySelector(".baum-code")!.textContent).toBe(
      'db.Exec("CREATE TABLE widgets (id INTEGER)")',
    );
    // Tokens that do not spell the line are dropped for its plain text.
    expect(lines[3]!.querySelectorAll(".baum-code span")).toHaveLength(0);
    expect(lines[3]!.querySelector(".baum-code")!.textContent).toBe("return db");
    expect(rows.at(-1)!.textContent).toBe("⋯ 5 lines below");
    head.querySelector("button")!.click();
    expect(f.sent.at(-1)).toMatchObject({ kind: "jump", key: '["hunk","h_0123456789ab",[12,14]]' });
  });

  it("folds lines past maxLines behind a toggle", async () => {
    const f = await shown("detail", "({ data }) => baum.diff(data.hunk, { maxLines: 2 })");
    const tail = f.root().querySelectorAll("tbody")[1] as HTMLElement;
    const more = f.root().querySelector(".baum-more") as HTMLButtonElement;
    expect(tail.hidden).toBe(true);
    expect(more.textContent).toBe("Show 2 more lines");
    more.click();
    expect(tail.hidden).toBe(false);
    expect(more.textContent).toBe("Show fewer lines");
    expect(more.getAttribute("aria-expanded")).toBe("true");
  });

  it("draws one line in a glance", async () => {
    const f = await shown("glance", "({ data }) => baum.diff(data.hunk)");
    const diff = f.root().querySelector("baum-diff") as HTMLElement;
    expect(diff.dataset.face).toBe("glance");
    expect(diff.querySelector("table, button")).toBeNull();
    expect(diff.textContent).toBe("internal/widgets/store.go:12–14+2 −1");
  });

  it.each([
    [`{ type: "hunk", id: "h_aaaaaaaaaaaa" }`, "Diff unavailable (comparing versions)"],
    [`{ type: "hunk", id: "h_missing" }`, "Diff unavailable (the citation isn't in the review)"],
    [`{ type: "risk", id: "rsk_widgets" }`, "Diff unavailable (not code)"],
  ])("says why %s has no diff", async (citation, why) => {
    const f = await shown("detail", `() => baum.diff(${citation})`);
    const diff = f.root().querySelector("baum-diff") as HTMLElement;
    expect(diff.dataset.state).toBe("unavailable");
    expect(diff.querySelector(".baum-diff-note")!.textContent).toBe(why);
  });

  it("says when a detail has no excerpt", async () => {
    const f = frame(
      `baum.tile({ glance: () => null, detail: () => baum.diff({ type: "hunk", id: "h_1" }) });`,
    );
    current = f;
    f.init({
      refs: {
        '["hunk","h_1",null]': {
          status: "ok",
          label: "a.go",
          navigable: false,
          path: "a.go",
          range: [1, 2],
          additions: 1,
          deletions: 0,
        },
      },
    });
    await f.next("ready");
    expect(f.root().querySelector(".baum-diff-note")!.textContent).toBe(
      "Diff unavailable (no excerpt)",
    );
    expect(f.root().querySelector(".baum-diff-head span.baum-diff-path")).not.toBeNull();
  });
});

describe("elements written as tags", () => {
  it("say to use the factory, visibly and as a console error", async () => {
    const f = frame(
      `baum.tile({ glance: () => null, detail: (view) => { view.root.append(document.createElement("baum-cite")); } });`,
    );
    current = f;
    f.init();
    const error = await f.next("error");
    expect(error).toMatchObject({ code: "console_error" });
    expect(f.root().querySelector("baum-cite")!.textContent).toBe(
      "Use baum.cite() to make <baum-cite>.",
    );
  });
});

describe("baum.compare", () => {
  const compare = (was: string, now: string) => {
    current = frame();
    const result = current.window.baum.compare(was, now) as {
      was: HTMLElement;
      now: HTMLElement;
      same: boolean;
    };
    const marks = (node: HTMLElement, tag: string) =>
      [...node.querySelectorAll(tag)].map((mark) => [mark.className, mark.textContent]);
    return { ...result, del: marks(result.was, "del"), ins: marks(result.now, "ins") };
  };

  it("marks the words one side lacks, a run as one mark", () => {
    const out = compare("TEXT", "TEXT NOT NULL");
    expect(out.was.textContent).toBe("TEXT");
    expect(out.now.textContent).toBe("TEXT NOT NULL");
    expect(out.del).toEqual([]);
    expect(out.ins).toEqual([["kit-ins", "NOT NULL"]]);
    expect(out.same).toBe(false);
  });

  it("marks a removed word as deleted", () => {
    const out = compare("amount BIGINT", "BIGINT");
    expect(out.del).toEqual([["kit-del", "amount"]]);
    expect(out.ins).toEqual([]);
  });

  it("leaves a rewrite unmarked, reads spacing as the same and case as a change", () => {
    expect(compare("int64", "string, a user login")).toMatchObject({
      del: [],
      ins: [],
      same: false,
    });
    expect(compare("TEXT  NOT\nNULL", "TEXT NOT NULL")).toMatchObject({
      del: [],
      ins: [],
      same: true,
    });
  });

  it("marks a word whose case changed", () => {
    const out = compare("DEFAULT 'Draft'", "DEFAULT 'draft'");
    expect(out.same).toBe(false);
    expect(out.del).toEqual([["kit-del", "'Draft'"]]);
    expect(out.ins).toEqual([["kit-ins", "'draft'"]]);
  });

  it("keeps markup in the texts as text", () => {
    const out = compare("<b>x</b> y", "<img src=x onerror=alert(1)> y");
    expect(out.now.querySelector("img")).toBeNull();
    expect(out.now.textContent).toBe("<img src=x onerror=alert(1)> y");
  });
});
