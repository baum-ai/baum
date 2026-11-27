import { describe, expect, it } from "vitest";
import { type Host, Notifier } from "./notifier.ts";
import type { Platform, ShowResult } from "./platform.ts";
import { type Permission, Store } from "./settings.ts";
import { KINDS, type Route, type Toast } from "./toast.ts";

class FakeHost implements Host {
  focused: [string, number] | undefined;
  focusedSessionKey: string | undefined;
  opened: Route[] = [];

  focusedPr() {
    return this.focused;
  }
  focusedSession() {
    return this.focusedSessionKey;
  }
  open(route: Route) {
    this.opened.push(route);
  }
}

class FakePlatform implements Platform {
  asked = 0;
  shown: Toast[] = [];
  clicks: (() => void)[] = [];

  constructor(
    // The answer to the permission request.
    private readonly permission: Permission,
    // Refuse at show time, as an OS that revoked permission would.
    private readonly refuse = false,
  ) {}

  click() {
    const click = this.clicks.pop();
    if (!click) throw new Error("a shown toast");
    click();
  }
  requestPermission() {
    this.asked++;
    return this.permission;
  }
  show(toast: Toast, onClick: () => void): ShowResult {
    if (this.refuse) return { ok: false, error: "denied" };
    this.shown.push(toast);
    this.clicks.push(onClick);
    return { ok: true };
  }
}

function setup(permission: Permission) {
  const host = new FakeHost();
  const platform = new FakePlatform(permission);
  const notifier = new Notifier(host, platform, Store.inMemory());
  return { notifier, host, platform };
}

function notice(kind: string, extra: string): string {
  return `{"kind":"${kind}","repository":"acme/widgets","pr":7,"clone":"c1","title":"Fix it"${extra}}`;
}

function sessionNotice(kind: string): string {
  return `{"kind":"${kind}","repository":"acme/widgets","pr":7,"agent":"claude-code","session":"s-1","branch":"feature","message":"Claude needs your permission to use Bash"}`;
}

describe("notifier", () => {
  it("each event kind is shown", async () => {
    const { notifier, platform } = setup("granted");
    const events = [
      notice("review_finished", ',"risk":4,"risk_status":"complete"'),
      notice("review_failed", ',"reason":"timeout"'),
      notice("review_requested", ""),
      notice("review_automatic", ""),
      notice("checks_failed", ""),
      notice("conflict", ""),
      notice("changes_requested", ""),
      notice("ready_to_merge", ""),
      notice("fix_pushed", ',"commit":"0123456"'),
      notice("fix_failed", ',"reason":"push_failed"'),
      sessionNotice("session_permission"),
      sessionNotice("session_input"),
    ];
    for (const data of events) {
      expect(await notifier.handleEvent("notice", data), data).toBe("shown");
    }
    expect(platform.shown.map((t) => t.kind)).toEqual(KINDS);
    expect(platform.shown[0]?.title).toBe("Review finished: risk 4 of 5");
    expect(platform.shown[1]?.title).toBe("Review failed: timeout");
  });

  it("other events are ignored", async () => {
    const { notifier, platform } = setup("granted");
    expect(await notifier.handleEvent("index", "{}")).toBeUndefined();
    expect(await notifier.handleEvent("reset", "{}")).toBeUndefined();
    // An automatic generation status is not news, an older daemon's daily
    // cap pause included.
    expect(
      await notifier.handleEvent("automatic", '{"repository":"acme/widgets","notice":"daily_cap"}'),
    ).toBeUndefined();
    expect(platform.shown).toEqual([]);
  });

  it("clicking a pull request notification opens its page", async () => {
    const { notifier, host, platform } = setup("granted");
    await notifier.handleEvent("notice", notice("review_finished", ""));
    platform.click();
    expect(host.opened).toEqual([{ type: "pr", clone: "c1", number: 7 }]);
  });

  it("a focused window on the same pull request is not interrupted", async () => {
    const { notifier, host, platform } = setup("granted");
    host.focused = ["c1", 7];
    const same = notice("review_finished", "");
    expect(await notifier.handleEvent("notice", same)).toBe("focused");
    // Another pull request, or another clone of it, still notifies.
    const other = same.replace('"pr":7', '"pr":8');
    expect(await notifier.handleEvent("notice", other)).toBe("shown");
    const otherClone = same.replace("c1", "c2");
    expect(await notifier.handleEvent("notice", otherClone)).toBe("shown");
    expect(platform.shown).toHaveLength(2);
    // Once the window loses focus the same pull request notifies again.
    host.focused = undefined;
    expect(await notifier.handleEvent("notice", same)).toBe("shown");
  });

  it("clicking a session notification opens its transcript", async () => {
    const { notifier, host, platform } = setup("granted");
    await notifier.handleEvent("notice", sessionNotice("session_permission"));
    platform.click();
    expect(host.opened).toEqual([{ type: "session", key: "claude-code:s-1" }]);
  });

  it("a window focused on the session's transcript is not interrupted", async () => {
    const { notifier, host, platform } = setup("granted");
    const waiting = sessionNotice("session_permission");
    host.focusedSessionKey = "claude-code:s-1";
    expect(await notifier.handleEvent("notice", waiting)).toBe("focused");
    // Another session's transcript, or the pull request the session is on,
    // does not count.
    host.focusedSessionKey = "claude-code:s-2";
    expect(await notifier.handleEvent("notice", waiting)).toBe("shown");
    host.focusedSessionKey = undefined;
    host.focused = ["acme/widgets", 7];
    expect(await notifier.handleEvent("notice", sessionNotice("session_input"))).toBe("shown");
    expect(platform.shown).toHaveLength(2);
  });

  it("a kind switched off is not shown and the others are", async () => {
    const { notifier, platform } = setup("granted");
    notifier.setKind("failed", false);
    const failed = notice("review_failed", "");
    expect(await notifier.handleEvent("notice", failed)).toBe("off");
    expect(await notifier.handleEvent("notice", notice("review_finished", ""))).toBe("shown");
    notifier.setKind("failed", true);
    expect(await notifier.handleEvent("notice", failed)).toBe("shown");
    expect(platform.shown).toHaveLength(2);
  });

  it("every kind can be switched off", async () => {
    const { notifier, platform } = setup("granted");
    for (const kind of KINDS) notifier.setKind(kind, false);
    const events = [
      notice("review_finished", ""),
      notice("review_failed", ""),
      notice("review_requested", ""),
      notice("review_automatic", ""),
      notice("checks_failed", ""),
      notice("conflict", ""),
      notice("changes_requested", ""),
      notice("ready_to_merge", ""),
      notice("fix_pushed", ""),
      notice("fix_failed", ""),
      sessionNotice("session_permission"),
      sessionNotice("session_input"),
    ];
    for (const data of events) {
      expect(await notifier.handleEvent("notice", data), data).toBe("off");
    }
    expect(platform.shown).toEqual([]);
    // Switching off asks the operating system for nothing.
    expect(platform.asked).toBe(0);
  });

  it("permission is asked on first use only", async () => {
    const { notifier, platform } = setup("granted");
    expect(platform.asked).toBe(0);
    await notifier.handleEvent("notice", notice("review_finished", ""));
    await notifier.handleEvent("notice", notice("review_failed", ""));
    expect(platform.asked).toBe(1);
    expect(notifier.settings().permission).toBe("granted");
  });

  it("denied permission shows nothing and is not asked again", async () => {
    const { notifier, platform } = setup("denied");
    const finished = notice("review_finished", "");
    expect(await notifier.handleEvent("notice", finished)).toBe("denied");
    expect(await notifier.handleEvent("notice", finished)).toBe("denied");
    expect(platform.shown).toEqual([]);
    expect(platform.asked).toBe(1);
    expect(notifier.settings().permission).toBe("denied");
  });

  it("switching a kind on again asks again after a refusal", async () => {
    const { notifier, platform } = setup("denied");
    const finished = notice("review_finished", "");
    await notifier.handleEvent("notice", finished);
    notifier.setKind("failed", false);
    expect(notifier.settings().permission).toBe("denied");
    notifier.setKind("failed", true);
    expect(notifier.settings().permission).toBe("unknown");
    await notifier.handleEvent("notice", finished);
    expect(platform.asked).toBe(2);
  });

  // A permission request that waits on a system prompt must not lose a change
  // the menu makes meanwhile.
  it("the menu can change a kind while permission is being asked", async () => {
    let answer: (permission: Permission) => void = () => {};
    const pending = new Promise<Permission>((resolve) => {
      answer = resolve;
    });
    let asked = 0;
    const platform: Platform = {
      requestPermission() {
        asked++;
        return pending;
      },
      show: () => ({ ok: true }),
    };
    const notifier = new Notifier(new FakeHost(), platform, Store.inMemory());
    const delivering = notifier.handleEvent("notice", notice("review_finished", ""));
    // Let the delivery reach the request, then act like the menu.
    await Promise.resolve();
    expect(asked).toBe(1);
    notifier.setKind("failed", false);
    answer("granted");
    expect(await delivering).toBe("shown");
    const settings = notifier.settings();
    expect(settings.permission).toBe("granted");
    expect(settings.kinds.failed).toBe(false);
  });

  it("a refusal when showing is remembered", async () => {
    const host = new FakeHost();
    const platform = new FakePlatform("granted", true);
    const notifier = new Notifier(host, platform, Store.inMemory());
    const finished = notice("review_finished", "");
    expect(await notifier.handleEvent("notice", finished)).toBe("denied");
    expect(notifier.settings().permission).toBe("denied");
    expect(await notifier.handleEvent("notice", finished)).toBe("denied");
  });
});
