// The tests of src/lifecycle.rs's quitting, and of the flow around them.
import { describe, expect, it } from "vitest";
import { Failure } from "./daemon.ts";
import { type QuitDeps, Quitter, stopConfirmation, stopFailureText, stopPrompt } from "./quit.ts";

describe("stopping background reviews", () => {
  it("needs no confirmation with nothing running", () => {
    expect(stopPrompt(0, 0)).toBeNull();
    expect(stopPrompt(0, 4)).toBeNull();
  });

  it("says a running review will be interrupted", () => {
    const one = stopPrompt(1, 0) ?? "";
    expect(one.startsWith("1 review is running.")).toBe(true);
    expect(one).toContain("will interrupt the running review");
    const many = stopPrompt(2, 3) ?? "";
    expect(many.startsWith("2 reviews are running and 3 queued.")).toBe(true);
    expect(many).toContain("interrupt the running reviews");
  });

  it("asks when the job count is unknown", () => {
    const text = stopConfirmation(undefined) ?? "";
    expect(text).toContain("couldn't check whether reviews are running");
    expect(text).toContain("will interrupt any that are");
    expect(stopConfirmation({ running: 0, queued: 3 })).toBeNull();
    expect(stopConfirmation({ running: 1, queued: 0 })).not.toBeNull();
  });

  it("says after a failed stop that hooks will not restart the daemon", () => {
    const text = stopFailureText("baum daemon stop failed (exit status: 1).", ["timeout"]);
    expect(text).toContain("Quit baum anyway?");
    expect(text).toContain("Hooks will not restart");
    expect(text).toContain("baum daemon ensure");
    expect(text.endsWith("timeout")).toBe(true);
  });
});

function quitter(over: Partial<QuitDeps> = {}) {
  const calls: string[] = [];
  const deps: QuitDeps = {
    jobCounts: async () => ({ running: 0, queued: 0 }),
    confirm: async (title) => {
      calls.push(`ask ${title}`);
      return true;
    },
    misplaced: () => null,
    stopDaemon: async () => {
      calls.push("stop");
    },
    exit: () => calls.push("exit"),
    ...over,
  };
  return { quitter: new Quitter(deps), calls };
}

describe("quitting", () => {
  it("Quit leaves the daemon", () => {
    const { quitter: q, calls } = quitter();
    q.quit();
    expect(calls).toEqual(["exit"]);
  });

  it("stops the daemon without asking when nothing runs", async () => {
    const { quitter: q, calls } = quitter();
    await q.quitAndStop();
    expect(calls).toEqual(["stop", "exit"]);
  });

  it("asks before interrupting a review, and stays on Cancel", async () => {
    const { quitter: q, calls } = quitter({
      jobCounts: async () => ({ running: 1, queued: 0 }),
      confirm: async (title) => {
        calls.push(`ask ${title}`);
        return false;
      },
    });
    await q.quitAndStop();
    expect(calls).toEqual(["ask Stop background reviews?"]);
  });

  it("offers to quit anyway when the daemon will not stop", async () => {
    let shown = "";
    const { quitter: q, calls } = quitter({
      stopDaemon: async () => {
        throw new Failure("baum daemon stop failed (exit status: 1).", ["log line"]);
      },
      confirm: async (title, text) => {
        calls.push(`ask ${title}`);
        shown = text;
        return true;
      },
    });
    await q.quitAndStop();
    expect(calls).toEqual(["ask Cannot stop background reviews", "exit"]);
    expect(shown.endsWith("log line")).toBe(true);
  });

  it("has no daemon to stop when it cannot run baum", async () => {
    const { quitter: q, calls } = quitter({ misplaced: () => "on a disk image" });
    await q.quitAndStop();
    expect(calls).toEqual(["exit"]);
  });

  it("shows one question at a time", async () => {
    let answer: (yes: boolean) => void = () => {};
    const { quitter: q, calls } = quitter({
      confirm: (title) => {
        calls.push(`ask ${title}`);
        return new Promise((resolve) => {
          answer = resolve;
        });
      },
    });
    const first = q.confirmQuit();
    await q.confirmQuit();
    answer(true);
    await first;
    expect(calls).toEqual(["ask Quit baum?", "exit"]);
  });
});
