import type { WebContents } from "electron";
import { afterEach, describe, expect, it } from "vitest";
import { answer, type BridgeState, type RoleInput, resolveRole } from "./bridge.ts";
import { clearCommands, registerCommand } from "./commands.ts";

const daemon = "http://127.0.0.1:4100";
const main = (url: string, extra: Partial<RoleInput> = {}): RoleInput => ({
  window: "main",
  url,
  daemon,
  setupOpen: false,
  development: false,
  ...extra,
});

describe("role resolution", () => {
  it("gives the bundled screens their roles in the main window", () => {
    expect(resolveRole(main("app://bundle/"))).toBe("connecting-screen");
    expect(resolveRole(main("app://bundle/index.html"))).toBe("connecting-screen");
    expect(resolveRole(main("app://bundle/index.html?x#y"))).toBe("connecting-screen");
    expect(resolveRole(main("app://bundle/setup.html", { setupOpen: true }))).toBe("setup-screen");
  });

  it("gives the setup screen its role only while it shows", () => {
    expect(resolveRole(main("app://bundle/setup.html"))).toBeNull();
  });

  it("refuses other bundled paths and spellings", () => {
    for (const path of ["/setup", "/setup.html/", "/setup%2Ehtml", "/tray.html", "/assets/x.js"]) {
      expect(resolveRole(main(`app://bundle${path}`, { setupOpen: true })), path).toBeNull();
    }
    for (const url of [
      "app://bundle:1/index.html",
      "app://user@bundle/index.html",
      "app://other/index.html",
    ]) {
      expect(resolveRole(main(url, { setupOpen: true })), url).toBeNull();
    }
  });

  it("gives the attached daemon's pages, and only its port, their role", () => {
    expect(resolveRole(main("http://127.0.0.1:4100/s/abc/?page=jobs"))).toBe("daemon-pages");
    for (const url of [
      "http://127.0.0.1:4200/",
      "https://127.0.0.1:4100/",
      "http://localhost:4100/",
      "http://user@127.0.0.1:4100/",
      "https://example.com/",
      "about:blank",
      "data:text/html,hi",
      "not a url",
    ]) {
      expect(resolveRole(main(url)), url).toBeNull();
    }
    expect(resolveRole(main("http://127.0.0.1:4100/", { daemon: null }))).toBeNull();
  });

  it("gives the tray panel its role only at its page in its window", () => {
    expect(resolveRole({ ...main("app://tray/tray.html"), window: "tray-panel" })).toBe(
      "tray-panel",
    );
    expect(resolveRole(main("app://tray/tray.html"))).toBeNull();
    expect(resolveRole({ ...main("app://bundle/tray.html"), window: "tray-panel" })).toBeNull();
    expect(resolveRole(main("app://bundle/tray.html"))).toBeNull();
    for (const url of [
      "app://tray/index.html",
      "app://bundle/index.html",
      "app://bundle/setup.html",
      daemon,
    ]) {
      expect(resolveRole({ ...main(url, { setupOpen: true }), window: "tray-panel" }), url).toBe(
        null,
      );
    }
  });

  it("gives Vite's setup screen its role only under development, while setup shows", () => {
    const vite = "http://localhost:1420/setup.html";
    expect(resolveRole(main(vite, { development: true, setupOpen: true }))).toBe("setup-screen");
    expect(resolveRole(main(`${vite}#x`, { development: true, setupOpen: true }))).toBe(
      "setup-screen",
    );
    expect(resolveRole(main(vite, { development: false, setupOpen: true }))).toBeNull();
    expect(resolveRole(main(vite, { development: true, setupOpen: false }))).toBeNull();
    for (const url of [
      "http://localhost:1420/setup.html?x=1",
      "http://localhost:1420/",
      "http://localhost:1421/setup.html",
      "http://127.0.0.1:1420/setup.html",
    ]) {
      expect(resolveRole(main(url, { development: true, setupOpen: true })), url).toBeNull();
    }
    // The development proxy is the attached daemon's origin there.
    expect(
      resolveRole(
        main("http://127.0.0.1:1420/", { development: true, daemon: "http://127.0.0.1:1420" }),
      ),
    ).toBe("daemon-pages");
  });
});

describe("the guard", () => {
  afterEach(() => clearCommands());

  const mainFrame = { processId: 1, routingId: 1, parent: null, url: "app://bundle/index.html" };
  const contents = { mainFrame } as unknown as WebContents;
  const state: BridgeState = {
    kindOf: (c) => (c === contents ? "main" : undefined),
    windowOf: () => "the main window",
    daemon: () => daemon,
    setupOpen: () => false,
    development: false,
  };
  const event = (frame: object | null, sender: WebContents = contents) =>
    ({ sender, senderFrame: frame }) as never;

  it("runs an allowed command from the main frame and passes its context", async () => {
    registerCommand("connection", (args, context) => ({ args, context }));
    expect(await answer(event(mainFrame), state, "connection", { a: 1 })).toEqual({
      ok: true,
      value: {
        args: { a: 1 },
        context: { window: "the main window", role: "connecting-screen", origin: "app://bundle" },
      },
    });
  });

  it("refuses sub-frames, other windows, other roles and unknown commands", async () => {
    let ran = 0;
    registerCommand("connection", () => ran++);
    registerCommand("setup_state", () => ran++);
    const child = { ...mainFrame, routingId: 2, parent: mainFrame };
    const other = { mainFrame } as unknown as WebContents;
    for (const [e, name, args] of [
      [event(child), "connection", {}],
      [event({ ...mainFrame, routingId: 2 }), "connection", {}],
      [event(null), "connection", {}],
      [event(mainFrame, other), "connection", {}],
      [event(mainFrame), "setup_state", {}],
      [event(mainFrame), "no_such_command", {}],
      [event(mainFrame), "__proto__", {}],
      [event(mainFrame), "connection", ["not", "an", "object"]],
      [event(mainFrame), "connection", "string"],
    ] as const) {
      const result = await answer(e, state, name, args);
      expect(result.ok, String(name)).toBe(false);
    }
    expect(ran).toBe(0);
  });

  it("answers a failing command with its message", async () => {
    registerCommand("reconnect", () => {
      throw new Error("Cannot reach baum.");
    });
    expect(await answer(event(mainFrame), state, "reconnect", undefined)).toEqual({
      ok: false,
      error: "Cannot reach baum.",
    });
  });

  it("says when an allowed command has no handler yet", async () => {
    expect(await answer(event(mainFrame), state, "step_zoom", {})).toEqual({
      ok: false,
      error: "command step_zoom is not available",
    });
  });
});
