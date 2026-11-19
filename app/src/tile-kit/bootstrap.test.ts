import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { frame, manifest, tick, type Frame } from "./harness";

let frames: Frame[] = [];
function open(author: string, before?: Parameters<typeof frame>[1]) {
  const f = frame(author, before);
  frames.push(f);
  return f;
}
afterEach(() => {
  for (const f of frames) f.remove();
  frames = [];
});

const TILE = `baum.tile({
  glance: ({ data }) => baum.html\`<p class="kit-lede">\${data.title}</p>\`,
  detail: ({ data, root }) => { root.append(baum.md(data.text)); },
});`;
const kinds = (f: Frame) => f.sent.map((message) => message.kind);

describe("the handshake", () => {
  it("says hello once the author's scripts have run", async () => {
    const f = open(TILE);
    expect(await f.next("hello")).toEqual({
      protocol: "baum.tile/1",
      kind: "hello",
      kit: 1,
      registered: true,
    });
  });

  it("says when the tile did not register", async () => {
    const f = open("");
    expect(await f.next("hello")).toMatchObject({ registered: false });
  });

  it("renders the face init asks for and reports when it is ready", async () => {
    const f = open(TILE);
    await f.next("hello");
    f.init({
      face: "glance",
      size: "third",
      theme: "dark",
      lang: "de",
      data: { title: "Two tables" },
    });
    const ready = await f.next("ready");
    expect(f.root().textContent).toBe("Two tables");
    const html = f.document.documentElement;
    expect([html.dataset.face, html.dataset.size, html.dataset.theme, html.lang]).toEqual([
      "glance",
      "third",
      "dark",
      "de",
    ]);
    expect(ready.metrics).toMatchObject({
      text_chars: "Two tables".length,
      elements: 1,
      media: 0,
      empty_state: false,
      failed: false,
      unresolved: 0,
      overflow_x: false,
      overflow_y: false,
    });
  });

  it("follows theme changes", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" } });
    await f.next("ready");
    f.send({ protocol: "baum.tile/1", kind: "theme", theme: "dark" });
    expect(f.document.documentElement.dataset.theme).toBe("dark");
    f.send({ protocol: "baum.tile/1", kind: "theme", theme: "sepia" });
    expect(f.document.documentElement.dataset.theme).toBe("dark");
  });

  it("draws again into a fresh root on update", async () => {
    const f = open(TILE);
    f.init({ data: { text: "first" } });
    await f.next("ready");
    const before = f.root();
    f.send({ protocol: "baum.tile/1", kind: "update", data: { text: "second" }, refs: {} });
    await f.next("ready", 2);
    expect(f.root()).not.toBe(before);
    expect(f.root().textContent).toBe("second");
    expect(f.document.querySelectorAll("#baum-root")).toHaveLength(1);
  });

  it("waits for an asynchronous render", async () => {
    const f = open(
      `baum.tile({ glance: () => null, detail: async () => { await new Promise((r) => setTimeout(r, 30)); return "late"; } });`,
    );
    f.init();
    await f.next("ready");
    expect(f.root().textContent).toBe("late");
  });
});

describe("messages from the page", () => {
  const init = {
    protocol: "baum.tile/1",
    kind: "init",
    face: "detail",
    size: "full",
    theme: "light",
    lang: "en",
    data: { text: "x" },
    refs: {},
  };

  it.each([
    ["no protocol", { ...init, protocol: undefined }],
    ["another protocol", { ...init, protocol: "baum.tile/2" }],
    ["an unknown face", { ...init, face: "side" }],
    ["an unknown size", { ...init, size: "huge" }],
    ["an unknown theme", { ...init, theme: "blue" }],
    ["a bad language", { ...init, lang: "en_US<" }],
    ["no data", { ...init, data: undefined }],
    ["refs that are not an object", { ...init, refs: [] }],
    ["a check without an id", { ...init, check: { contrast: true } }],
    ["too much text", { ...init, data: { text: "x".repeat(1_600_000) } }],
    ["too deep", { ...init, data: JSON.parse(`${"[".repeat(80)}${"]".repeat(80)}`) as unknown }],
    ["a string", "init"],
    ["null", null],
  ])("ignores an init with %s", async (_, message) => {
    const f = open(TILE);
    f.send(message);
    await tick(60);
    expect(kinds(f)).toEqual(["hello"]);
    f.init({ data: { text: "fine" } });
    await f.next("ready");
  });

  it("ignores values that are not JSON", async () => {
    const f = open(TILE);
    await f.next("hello");
    const data = new f.window.MessageEvent("message", {
      data: { ...init, data: { when: new f.window.Date() } },
    });
    Object.defineProperty(data, "source", { value: f.window.parent });
    f.window.dispatchEvent(data);
    await tick(60);
    expect(kinds(f)).toEqual(["hello"]);
  });

  it("ignores messages from anything but its parent", async () => {
    const f = open(TILE);
    f.send(init, {});
    f.send(init, f.window);
    f.send(init, null);
    await tick(60);
    expect(kinds(f)).toEqual(["hello"]);
  });

  it("takes init once", async () => {
    const f = open(TILE);
    f.init({ data: { text: "first" } });
    await f.next("ready");
    f.init({ data: { text: "second" }, face: "glance" });
    await tick(60);
    expect(f.root().textContent).toBe("first");
    expect(kinds(f).filter((kind) => kind === "ready")).toHaveLength(1);
  });

  it("ignores update and theme before init", async () => {
    const f = open(TILE);
    f.send({ protocol: "baum.tile/1", kind: "update", data: { text: "x" }, refs: {} });
    f.send({ protocol: "baum.tile/1", kind: "theme", theme: "dark" });
    await tick(60);
    expect(kinds(f)).toEqual(["hello"]);
    expect(f.document.documentElement.dataset.theme).toBe("light");
  });

  it("drops resolved citations that are not payloads", async () => {
    const f = open(TILE);
    f.init({
      refs: {
        '["risk","a"]': { status: "ok", label: "A" },
        '["risk","b"]': { status: "maybe", label: "B" },
        '["risk","c"]': "C",
      },
    });
    await f.next("ready");
    expect(f.window.baum.refs.get({ type: "risk", id: "a" })).toMatchObject({ label: "A" });
    expect(f.window.baum.refs.get({ type: "risk", id: "b" })).toBeNull();
    expect(f.window.baum.refs.get({ type: "risk", id: "c" })).toBeNull();
  });
});

describe("registration", () => {
  it("throws on a second baum.tile and reports it", async () => {
    const f = open(`${TILE}\nbaum.tile({ glance() {}, detail() {} });`);
    const error = await f.next("error");
    expect(error).toMatchObject({ code: "script_error" });
    expect(String(error.message)).toContain("already called");
    expect(await f.next("hello")).toMatchObject({ registered: true });
  });

  it.each([
    ["nothing", ""],
    ["one face", "{ glance() {} }"],
    ["strings", '{ glance: "x", detail: "y" }'],
  ])("refuses %s", async (_, spec) => {
    const f = open(`baum.tile(${spec});`);
    expect(String((await f.next("error")).message)).toContain("baum.tile() takes");
    expect(await f.next("hello")).toMatchObject({ registered: false });
  });

  it("renders a tile that registers after init", async () => {
    const f = open("");
    f.init({ data: { text: "late" } });
    await tick(20);
    f.run(TILE);
    await f.next("ready");
    expect(f.root().textContent).toBe("late");
  });
});

describe("what goes wrong is reported", () => {
  it("reports a render that throws, and no ready", async () => {
    const f = open(`baum.tile({ glance() {}, detail() { throw new RangeError("no rows"); } });`);
    f.init();
    const error = await f.next("error");
    expect(error).toMatchObject({ code: "script_error", message: "RangeError: no rows" });
    await tick(150);
    expect(kinds(f)).not.toContain("ready");
  });

  it("reports a render that rejects", async () => {
    const f = open(`baum.tile({ glance() {}, async detail() { throw new Error("late"); } });`);
    f.init();
    expect(await f.next("error")).toMatchObject({ code: "script_error", message: "Error: late" });
  });

  it("reports a value the render can't draw", async () => {
    const f = open(`baum.tile({ glance() {}, detail() { return { rows: 1 }; } });`);
    f.init();
    expect(String((await f.next("error")).message)).toContain("can't show an object");
  });

  it("reports an uncaught error in the author's script", async () => {
    const f = open("undefinedFunction();");
    expect(await f.next("error")).toMatchObject({ code: "script_error" });
  });

  it("reports console.error and still logs it", async () => {
    const f = open(`console.error("bad row", { id: 3 }, new Error("x"));`);
    expect(await f.next("error")).toMatchObject({
      code: "console_error",
      message: 'bad row {"id":3} Error: x',
    });
  });

  it("reports a policy violation by scheme and host only", async () => {
    const f = open("");
    const event = new f.window.Event("securitypolicyviolation", { bubbles: true });
    Object.assign(event, {
      blockedURI: "https://tracker.example.invalid/pixel?id=secret",
      effectiveDirective: "img-src",
      lineNumber: 0,
    });
    f.document.body.dispatchEvent(event);
    expect(await f.next("error")).toMatchObject({
      code: "csp_violation",
      blocked: "https://tracker.example.invalid",
      message: "The frame's img-src policy blocked https://tracker.example.invalid.",
    });
  });

  it("reports baum.fail and draws its state", async () => {
    const f = open(
      `baum.tile({ glance() {}, detail() { return baum.fail("The data has no tables."); } });`,
    );
    f.init();
    expect(await f.next("error")).toMatchObject({
      code: "render_failed",
      message: "The data has no tables.",
    });
    const ready = await f.next("ready");
    expect(ready.metrics).toMatchObject({ failed: true });
    expect(f.root().querySelector(".kit-fail")!.textContent).toBe("The data has no tables.");
  });

  it("counts baum.empty as content", async () => {
    const f = open(
      `baum.tile({ glance() {}, detail() { return baum.empty("No schema changes."); } });`,
    );
    f.init();
    const ready = await f.next("ready");
    expect(ready.metrics).toMatchObject({ empty_state: true });
    expect(f.root().querySelector(".kit-empty")!.textContent).toBe("No schema changes.");
  });

  it("clips long messages and keeps room for every code", async () => {
    const f = open(
      `for (let i = 0; i < 80; i++) console.error("x".repeat(900));\nundefinedFunction();`,
    );
    await f.next("error", 11);
    await tick(20);
    const errors = f.sent.filter((message) => message.kind === "error");
    expect(errors.filter((message) => message.code === "console_error")).toHaveLength(10);
    expect(errors.filter((message) => message.code === "script_error")).toHaveLength(1);
    expect(String(errors[0]!.message).length).toBe(500);
  });

  it("logs values that can't be described without throwing", async () => {
    const f =
      open(`const bad = { toJSON() { throw new Error("no"); }, toString() { throw new Error("no"); } };
      console.error(bad);
      window.__survived = true;`);
    expect(await f.next("error")).toMatchObject({ code: "console_error" });
    expect((f.window as unknown as { __survived?: boolean }).__survived).toBe(true);
  });
});

describe("height", () => {
  /** A frame whose content height is whatever `height.value` says, with a
   *  ResizeObserver the test triggers. */
  function sized() {
    const height = { value: 300 };
    const observers: (() => void)[] = [];
    const f = open(TILE, (win) => {
      win.ResizeObserver = class {
        constructor(callback: () => void) {
          observers.push(callback);
        }
        observe() {}
        disconnect() {
          observers.length = 0;
        }
      } as unknown as typeof ResizeObserver;
      win.document.documentElement.getBoundingClientRect = () =>
        ({ height: height.value }) as DOMRect;
    });
    const resize = async (value: number) => {
      height.value = value;
      // A copy: disconnecting empties the list.
      for (const observer of observers.slice()) observer();
      await tick(25);
    };
    return { f, resize };
  }

  it("reports a detail's height as it changes", async () => {
    const { f, resize } = sized();
    f.init({ data: { text: "x" } });
    await f.next("ready");
    await resize(420);
    await resize(420);
    await resize(380);
    const heights = f.sent
      .filter((message) => message.kind === "resize")
      .map((message) => message.height);
    expect(heights).toEqual([300, 420, 380]);
  });

  it("stops a height that grows by the same amount ten times", async () => {
    const { f, resize } = sized();
    f.init({ data: { text: "x" } });
    await f.next("ready");
    for (let step = 1; step <= 14; step++) await resize(300 + step * 40);
    const error = await f.next("error");
    expect(error).toMatchObject({ code: "layout_loop" });
    const heights = f.sent
      .filter((message) => message.kind === "resize")
      .map((message) => message.height);
    expect(heights).toEqual([300, 340, 380, 420, 460, 500, 540, 580, 620, 660]);
    await resize(2000);
    expect(f.sent.filter((message) => message.kind === "resize")).toHaveLength(10);
  });

  it("allows growth that is not a loop", async () => {
    const { f, resize } = sized();
    f.init({ data: { text: "x" } });
    await f.next("ready");
    for (let step = 1; step <= 14; step++) await resize(300 + step * 40 + (step % 3));
    expect(kinds(f)).not.toContain("error");
  });

  it("reports no height for a glance", async () => {
    const { f, resize } = sized();
    f.init({ face: "glance", data: { title: "x" } });
    await f.next("ready");
    await resize(500);
    expect(kinds(f)).not.toContain("resize");
  });
});

describe("keys", () => {
  async function ready(face = "detail") {
    const f = open(
      `${TILE.replace("detail: ({ data, root }) => { root.append(baum.md(data.text)); }", "detail: () => baum.html`<input><p>x</p>`")}`,
    );
    f.init({ face, data: { title: "x", text: "x" } });
    await f.next("ready");
    return f;
  }
  const press = (f: Frame, key: string, init: KeyboardEventInit = {}, target?: Element) => {
    const event = new f.window.KeyboardEvent("keydown", {
      key,
      bubbles: true,
      cancelable: true,
      ...init,
    });
    (target ?? f.document.body).dispatchEvent(event);
  };

  it("forwards the page's keys from a detail", async () => {
    const f = await ready();
    for (const key of ["Escape", "u", "j", "k", "ArrowLeft", "ArrowRight", "[", "]", "?"])
      press(f, key);
    press(f, "?", { shiftKey: true });
    const keys = f.sent.filter((message) => message.kind === "key");
    expect(keys.map((message) => message.key)).toEqual([
      "Escape",
      "u",
      "j",
      "k",
      "ArrowLeft",
      "ArrowRight",
      "[",
      "]",
      "?",
      "?",
    ]);
    expect(keys.at(-1)).toMatchObject({ shift: true });
  });

  it("keeps other keys, handled keys, typed keys and modified keys", async () => {
    const f = await ready();
    press(f, "x");
    press(f, "Enter");
    press(f, "j", { ctrlKey: true });
    press(f, "k", { metaKey: true });
    press(f, "u", { altKey: true });
    press(f, "j", {}, f.root().querySelector("input")!);
    f.document.addEventListener("keydown", (event) => event.preventDefault(), { once: true });
    press(f, "Escape");
    expect(kinds(f)).not.toContain("key");
  });

  it("forwards nothing from a glance", async () => {
    const f = await ready("glance");
    press(f, "Escape");
    expect(kinds(f)).not.toContain("key");
  });
});

describe("requests to the page", () => {
  it("jumps only to a navigable citation the page resolved", async () => {
    const f = open(TILE);
    f.init({
      refs: {
        '["risk","a"]': { status: "ok", label: "A", navigable: true },
        '["risk","b"]': { status: "ok", label: "B", navigable: false },
        '["risk","c"]': { status: "unavailable", label: "C", why: "comparing" },
      },
    });
    await f.next("ready");
    const jump = (id: string) =>
      f.window.baum.jump(f.window.JSON.parse(JSON.stringify({ type: "risk", id }))) as boolean;
    expect([jump("a"), jump("b"), jump("c"), jump("d")]).toEqual([true, false, false, false]);
    expect(f.sent.filter((message) => message.kind === "jump")).toEqual([
      { protocol: "baum.tile/1", kind: "jump", key: '["risk","a"]' },
    ]);
  });

  it("asks and opens the detail", async () => {
    const f = open(TILE);
    f.window.baum.ask();
    f.window.baum.openDetail();
    expect(kinds(f).slice(-2)).toEqual(["ask", "open"]);
  });
});

describe("window.baum", () => {
  it("is frozen", async () => {
    const f = open("");
    f.run(`
      "use strict";
      window.__results = [];
      for (const attempt of [
        () => { baum.html = () => 1; },
        () => { baum.refs.get = () => 1; },
        () => { window.baum = {}; },
        () => { delete window.baum; },
        () => { baum.extra = 1; },
      ]) { try { attempt(); window.__results.push("changed"); } catch { window.__results.push("refused"); } }
    `);
    expect((f.window as unknown as { __results: string[] }).__results).toEqual(
      Array(5).fill("refused"),
    );
  });

  it("has every API name in the manifest, and nothing else", () => {
    const f = open("");
    const names = new Set<string>();
    const walk = (value: Record<string, unknown>, prefix: string) => {
      for (const [key, item] of Object.entries(value)) {
        if (typeof item === "function") names.add(`${prefix}.${key}`);
        else if (item && typeof item === "object")
          walk(item as Record<string, unknown>, `${prefix}.${key}`);
      }
    };
    walk(f.window.baum, "baum");
    expect([...names].sort()).toEqual(manifest.api.map((entry) => entry.name).sort());
  });

  it("defines every element in the manifest", () => {
    const f = open("");
    for (const element of manifest.elements) {
      expect(f.window.customElements.get(element.name), element.name).toBeTypeOf("function");
      const factory = element.created_by.replace(/^baum\./, "");
      const made = f.window.baum[factory]({ type: "risk", id: "x" }) as HTMLElement;
      expect(made.localName).toBe(element.name);
    }
  });
});

describe("theme tokens", () => {
  const THEME = { protocol: "baum.tile/1", kind: "theme", theme: "light" };
  const style = (f: Frame) => f.document.documentElement.style;

  // The Go validator's cases (internal/themes), so the two agree.
  const cases = JSON.parse(
    readFileSync(
      resolve(import.meta.dirname, "../../../internal/themes/testdata/validate.json"),
      "utf8",
    ),
  ) as { token: string; value: string; ok: boolean }[];

  it("applies a token value iff the Go validator accepts it", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" } });
    await f.next("ready");
    const setProperty = vi.spyOn(style(f), "setProperty");
    for (const { token, value, ok } of cases) {
      setProperty.mockClear();
      f.send({ ...THEME, tokens: { [token]: value } });
      // Accepted: the one token is set to the value. Rejected: the whole
      // message is dropped, so nothing is set at all.
      const calls = setProperty.mock.calls.map(([name, v]) => [name, v]);
      expect({ token, value, calls }).toEqual({
        token,
        value,
        calls: ok ? [[token, value]] : [],
      });
      f.send(THEME);
      expect(style(f).getPropertyValue(token)).toBe("");
    }
    setProperty.mockRestore();
  });

  it("sets tokens from init", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" }, tokens: { "--surface-page": "#102030" } });
    await f.next("ready");
    expect(style(f).getPropertyValue("--surface-page")).toBe("#102030");
  });

  it("drops an init whose tokens are invalid, leaving the frame unstarted", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" }, tokens: { "--surface-page": "red" } });
    await tick(50);
    expect(kinds(f)).not.toContain("ready");
    f.init({ data: { text: "x" } });
    await f.next("ready");
  });

  it("clears earlier tokens a later message leaves out", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" } });
    await f.next("ready");
    f.send({ ...THEME, tokens: { "--surface-page": "#111111", "--text-primary": "#eeeeee" } });
    expect(style(f).getPropertyValue("--surface-page")).toBe("#111111");
    f.send({ ...THEME, tokens: { "--text-primary": "#dddddd" } });
    expect(style(f).getPropertyValue("--surface-page")).toBe("");
    expect(style(f).getPropertyValue("--text-primary")).toBe("#dddddd");
    f.send({ ...THEME, theme: "dark" });
    expect(style(f).getPropertyValue("--text-primary")).toBe("");
    expect(f.document.documentElement.dataset.theme).toBe("dark");
  });

  it("drops the whole message for a bad token set", async () => {
    const f = open(TILE);
    f.init({ data: { text: "x" }, tokens: { "--surface-page": "#111111" } });
    await f.next("ready");
    for (const tokens of [
      { "--surface-page": "#222222", "--not-a-token": "#333333" },
      { "--surface-page": "#222222", "--text-primary": "var(--x)" },
      { "--surface-page": 5 },
      ["--surface-page"],
      "#222222",
      null,
    ]) {
      f.send({ ...THEME, theme: "dark", tokens });
      expect(f.document.documentElement.dataset.theme).toBe("light");
      expect(style(f).getPropertyValue("--surface-page")).toBe("#111111");
    }
  });
});
