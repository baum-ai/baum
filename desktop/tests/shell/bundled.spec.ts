// A frame inside a bundled screen (`app://bundle`) can call no command and
// sees no declaration, while the screen itself can. The setup screen shows
// on first run; the connecting screen while the app attaches, which a fake
// `baum` that cannot start the daemon keeps up. The tray panel's window (macOS and
// Windows) does not exist on Linux, so it is not checked here.
import { expect, type Page, test } from "@playwright/test";
import { invoke, launch, MockDaemon, type Shell, sleep, waitFor } from "./harness.ts";

const daemon = new MockDaemon();
let shell: Shell | undefined;

test.beforeAll(async () => {
  await daemon.listen();
});

test.afterEach(async () => {
  await shell?.close();
  shell = undefined;
});

test.afterAll(async () => {
  await daemon.close();
});

/** Adds a sandboxed, script-only frame to `page` and reports what it sees. */
async function probeFrame(page: Page): Promise<Record<string, string>> {
  await page.evaluate(() => {
    const frame = document.createElement("iframe");
    frame.name = "probe";
    frame.sandbox.add("allow-scripts");
    document.body.append(frame);
  });
  const frame = await waitFor("the frame", 10_000, () => page.frame({ name: "probe" }));
  return frame.evaluate(() => {
    const seen: Record<string, string> = {};
    const t = (k: string, f: () => unknown) => {
      try {
        seen[k] = String(f());
      } catch (e) {
        seen[k] = `throws ${(e as Error).name}`;
      }
    };
    const w = window as unknown as Record<string, unknown>;
    t("baumDesktop", () => typeof w.baumDesktop);
    t("__BAUM_DESKTOP__", () => typeof w.__BAUM_DESKTOP__);
    t(
      "parent.baumDesktop",
      () => typeof (parent as unknown as Record<string, unknown>).baumDesktop,
    );
    t("require", () => typeof w.require);
    t("process", () => typeof w.process);
    return seen;
  });
}

const NOTHING = {
  baumDesktop: "undefined",
  __BAUM_DESKTOP__: "undefined",
  "parent.baumDesktop": "throws SecurityError",
  require: "undefined",
  process: "undefined",
};

test("a frame in the setup screen can call no command", async () => {
  shell = await launch(daemon, { onboarded: false });
  await shell.main.waitForURL("app://bundle/setup.html");
  expect(await invoke(shell.main, "theme_state")).toMatch(/^ok /);
  const logs = shell.logs.length;
  expect(await probeFrame(shell.main)).toEqual(NOTHING);
  await sleep(500);
  // The screen's own calls run; nothing else asked.
  expect(shell.logs.slice(logs).filter((l) => l.includes("refused"))).toEqual([]);
  // The setup screen stays: the app does not attach while it shows.
  expect(shell.main.url()).toBe("app://bundle/setup.html");
});

test("a frame in the connecting screen can call no command", async () => {
  // A daemon that cannot start keeps the connecting screen up.
  shell = await launch(daemon, { failing: true });
  await shell.main.waitForURL("app://bundle/index.html");
  await waitFor("the connection to fail", 30_000, async () =>
    (await invoke((shell as Shell).main, "connection")).includes("failed"),
  );
  expect(await probeFrame(shell.main)).toEqual(NOTHING);
});
