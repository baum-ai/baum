import { describe, expect, it } from "vitest";
import {
  isMenuId,
  NotificationService,
  type Platform,
  prOf,
  sessionOf,
  Store,
  submenu,
} from "./index.ts";
import { KINDS } from "./toast.ts";

const origin = "http://127.0.0.1:4100";

function service() {
  const shown: string[] = [];
  const clicks: (() => void)[] = [];
  const opened: string[] = [];
  const platform: Platform = {
    requestPermission: () => "granted",
    show(toast, onClick) {
      shown.push(toast.title);
      clicks.push(onClick);
      return { ok: true };
    },
  };
  const notifications = new NotificationService({
    configDir: Store.inMemory(),
    platform,
    client: { appUrl: async () => new URL("http://127.0.0.1:4100/s/ns/") },
    openInWindow: (path) => opened.push(path),
  });
  return { notifications, shown, clicks, opened };
}

const finished = '{"kind":"review_finished","repository":"acme/widgets","pr":7,"clone":"c1"}';

describe("notifications module", () => {
  it("a page opens where the window shows the app", async () => {
    const { notifications, opened } = service();
    notifications.attach(origin);
    await notifications.openRoute({ type: "job", id: "6e28" });
    // The main process loads the path on the origin the window shows the
    // app on, the daemon's or under `desktop::dev` Vite's.
    expect(opened).toEqual(["/s/ns/?page=jobs&job=6e28"]);
    await notifications.openRoute({ type: "pr", clone: "c1", number: 7 });
    expect(opened[1]).toBe("/s/ns/?clone=c1&pr=7");
  });

  it("a click on a notification loads its page", async () => {
    const { notifications, clicks, opened } = service();
    notifications.attach(origin);
    await notifications.handleNotice(finished);
    clicks[0]?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(opened).toEqual(["/s/ns/?clone=c1&pr=7"]);
  });

  it("notices are dropped while no daemon is attached", async () => {
    const { notifications, shown, opened } = service();
    await notifications.handleNotice(finished);
    expect(shown).toEqual([]);
    notifications.attach(origin);
    await notifications.handleNotice(JSON.parse(finished));
    expect(shown).toEqual(["Review finished"]);
    notifications.detach();
    await notifications.handleNotice(finished);
    await notifications.openSession("claude-code:s-1");
    expect(shown).toHaveLength(1);
    expect(opened).toEqual([]);
  });

  it("the focused page suppresses its own notification", async () => {
    const { notifications, shown } = service();
    notifications.attach(origin);
    notifications.setFocusedPage(`${origin}/s/ns/?clone=c1&pr=7`);
    await notifications.handleNotice(finished);
    expect(shown).toEqual([]);
    notifications.setFocusedPage(null);
    await notifications.handleNotice(finished);
    expect(shown).toHaveLength(1);
  });

  it("the menu is one check item per kind and a click flips the saved state", () => {
    const { notifications } = service();
    expect(notifications.menu().items.map((i) => i.id)).toEqual(
      KINDS.map((k) => `notifications:${k}`),
    );
    expect(notifications.menu().items.every((i) => i.checked)).toBe(true);
    expect(notifications.onMenu("notifications:failed")).toBe(true);
    expect(notifications.menu().items.find((i) => i.id === "notifications:failed")?.checked).toBe(
      false,
    );
    expect(notifications.onMenu("notifications:failed")).toBe(true);
    expect(notifications.menu().items.every((i) => i.checked)).toBe(true);
    expect(notifications.onMenu("other")).toBe(false);
    expect(isMenuId("notifications:session_input")).toBe(true);
    expect(isMenuId("notifications:other")).toBe(false);
    expect(submenu(notifications.notifier.settings().kinds).label).toBe("Notifications");
  });

  it("the window shows a pull request when its url says so", () => {
    expect(prOf(`${origin}/s/ns/?clone=c1&pr=7`, origin)).toEqual(["c1", 7]);
    // Another page, a half-specified one, another origin or the bundled
    // screen is not a pull request.
    for (const other of [
      `${origin}/s/ns/?page=jobs`,
      `${origin}/s/ns/?pr=7`,
      `${origin}/s/ns/?clone=c1&pr=x`,
      "http://127.0.0.1:4200/s/ns/?clone=c1&pr=7",
      "file:///app/index.html",
      "not a url",
    ]) {
      expect(prOf(other, origin), other).toBeUndefined();
    }
  });

  it("the window shows a session when its url says so", () => {
    expect(sessionOf(`${origin}/s/ns/?page=sessions&session=claude-code%3As-1`, origin)).toBe(
      "claude-code:s-1",
    );
    for (const other of [
      `${origin}/s/ns/?page=sessions`,
      `${origin}/s/ns/?session=claude-code%3As-1`,
      `${origin}/s/ns/?page=jobs&session=claude-code%3As-1`,
      "http://127.0.0.1:4200/s/ns/?page=sessions&session=claude-code%3As-1",
    ]) {
      expect(sessionOf(other, origin), other).toBeUndefined();
    }
  });
});
