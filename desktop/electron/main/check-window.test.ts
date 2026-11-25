import { describe, expect, it } from "vitest";
import { CheckWindow, type CheckWindowHost, checkPageUrl, UNLOAD_GRACE } from "./check-window.ts";
import { decide } from "./navigation.ts";

const daemon = "http://127.0.0.1:4100";

describe("the check page", () => {
  it("is the namespace root of the main window's daemon page", () => {
    for (const main of [
      "http://127.0.0.1:4100/s/abc/",
      "http://127.0.0.1:4100/s/abc/?page=jobs#x",
      "http://127.0.0.1:4100/s/abc/sample/?page=review&id=7",
      "http://127.0.0.1:4100/s/abc/api/x",
    ]) {
      expect(checkPageUrl(main, daemon), main).toBe("http://127.0.0.1:4100/s/abc/?page=tile-check");
    }
  });

  it("is none without a daemon page", () => {
    for (const main of [
      "http://127.0.0.1:4100/",
      "http://127.0.0.1:4100/#token=x",
      "http://127.0.0.1:4100/s/",
      "http://127.0.0.1:4100/s//",
      "http://127.0.0.1:4100/s/abc",
      "http://127.0.0.1:4200/s/abc/",
      "http://user@127.0.0.1:4100/s/abc/",
      "https://example.com/s/abc/",
      "app://bundle/index.html",
      "app://bundle/setup.html",
      "not a url",
    ]) {
      expect(checkPageUrl(main, daemon), main).toBeNull();
    }
    expect(checkPageUrl("http://127.0.0.1:4100/s/abc/", null)).toBeNull();
  });

  it("passes the main window's rules", () => {
    const page = checkPageUrl("http://127.0.0.1:4100/s/abc/", daemon) as string;
    expect(decide(page, daemon, false)).toBe("allow");
    expect(decide(page, null, false)).toBe("deny");
  });
});

interface FakeWindow {
  id: number;
  url: string;
  gone: boolean;
  /** Whether the page answers a request to close. */
  answers: boolean;
  pids: number[];
}

function fakeHost() {
  const events: string[] = [];
  const windows: FakeWindow[] = [];
  let answers = true;
  const host: CheckWindowHost<FakeWindow> = {
    create(url) {
      const window = { id: windows.length + 1, url, gone: false, answers, pids: [100, 101] };
      windows.push(window);
      events.push(`create ${window.id}`);
      return window;
    },
    isGone: (window) => window.gone,
    ownProcesses: (window) => window.pids,
    askToClose(window) {
      events.push(`ask ${window.id}`);
      if (window.answers) window.gone = true;
    },
    destroy(window) {
      events.push(`destroy ${window.id}`);
      window.gone = true;
    },
    endProcess: (pid) => void events.push(`end ${pid}`),
    async sleep(ms) {
      events.push(`sleep ${ms}`);
      await Promise.resolve();
    },
  };
  return {
    host,
    events,
    windows,
    spinning: () => {
      answers = false;
    },
  };
}

describe("the check window", () => {
  it("opens once, however often it is asked", async () => {
    const { host, events } = fakeHost();
    const check = new CheckWindow(host);
    await Promise.all([check.open("a"), check.open("a")]);
    await check.open("a");
    expect(events).toEqual(["create 1"]);
    expect(check.window?.id).toBe(1);
  });

  it("asks the page to close, then ends the window's processes", async () => {
    const { host, events } = fakeHost();
    const check = new CheckWindow(host);
    await check.open("a");
    await check.close();
    expect(events).toEqual(["create 1", "ask 1", `sleep ${UNLOAD_GRACE}`, "end 100", "end 101"]);
    expect(check.window).toBeNull();
  });

  it("destroys a window whose page never yields", async () => {
    const { host, events, spinning } = fakeHost();
    spinning();
    const check = new CheckWindow(host);
    await check.open("a");
    await check.close();
    expect(events).toEqual([
      "create 1",
      "ask 1",
      `sleep ${UNLOAD_GRACE}`,
      "destroy 1",
      "end 100",
      "end 101",
    ]);
  });

  it("does nothing when closed while closed", async () => {
    const { host, events } = fakeHost();
    const check = new CheckWindow(host);
    await check.close();
    await check.closeSoon();
    expect(events).toEqual([]);
  });

  it("builds a new window for an open right after a close", async () => {
    const { host, events } = fakeHost();
    const check = new CheckWindow(host);
    await check.open("a");
    await Promise.all([check.close(), check.open("b")]);
    expect(events.at(-1)).toBe("create 2");
    expect(check.window?.url).toBe("b");
  });

  it("builds nothing for an open the shell's own close overtook", async () => {
    const { host, events } = fakeHost();
    const check = new CheckWindow(host);
    await check.open("a");
    // A page's close is running when the page asks to open again, and the
    // main window then loads another page.
    const closing = check.close();
    const opening = check.open("b");
    const leaving = check.closeSoon();
    await Promise.all([closing, opening, leaving]);
    expect(events.filter((e) => e.startsWith("create"))).toEqual(["create 1"]);
    expect(check.window).toBeNull();
    // A page asking after that opens it again.
    await check.open("c");
    expect(check.window?.url).toBe("c");
  });

  it("forgets a window that went away on its own", async () => {
    const { host, events, windows } = fakeHost();
    const check = new CheckWindow(host);
    await check.open("a");
    (windows[0] as FakeWindow).gone = true;
    await check.open("b");
    expect(events).toEqual(["create 1", "create 2"]);
  });

  it("keeps working after a window could not be built", async () => {
    const { host } = fakeHost();
    const create = host.create;
    let fail = true;
    host.create = (url) => {
      if (fail) throw new Error("no window");
      return create(url);
    };
    const check = new CheckWindow(host);
    await expect(check.open("a")).rejects.toThrow("no window");
    fail = false;
    await check.open("b");
    expect(check.window?.url).toBe("b");
  });
});
