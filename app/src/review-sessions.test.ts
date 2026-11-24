import { afterEach, expect, it, vi } from "vitest";
import type { LiveSession } from "./api";

const answers = vi.hoisted(() => new Map<string, unknown>());
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  request: vi.fn(async (path: string) => {
    if (!answers.has(path)) throw Error("not found");
    return answers.get(path);
  }),
}));

const { pullRequestSessions } = await import("./review");

afterEach(() => history.replaceState(null, "", "/"));

const listed = { agent: "codex", session_id: "live-1", state: "running" } as LiveSession;
const slot = () =>
  pullRequestSessions(
    {
      status: "ready",
      sessions: [
        { id: "here-1", vendor: "claude_code" },
        { id: "elsewhere-1", vendor: "claude_code" },
        { id: "other-1", vendor: "gemini" },
      ],
    },
    [listed],
    () => null,
  );
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));
const search = () => new URLSearchParams(location.search);

it("opens a listed session on its Sessions page at once", () => {
  expect(slot().open!("live-1", 2)).toBe(true);
  expect(search().get("page")).toBe("sessions");
  expect(search().get("session")).toBe("codex:live-1");
  expect(search().get("turn")).toBe("2");
});

it("opens a linked session there once the daemon has its transcript, else leaves it to the pane", async () => {
  answers.set("api/live-sessions/claude-code/here-1/transcript", {
    available: true,
    version: "v1",
  });
  answers.set("api/live-sessions/claude-code/elsewhere-1/transcript", {
    available: false,
    message: "Claude Code did not say where this session's transcript is.",
  });
  // Asked first, while the pane shows it.
  expect(slot().open!("here-1", 3)).toBe(false);
  await settle();
  expect(search().get("session")).toBe("claude-code:here-1");
  expect(search().get("turn")).toBe("3");
  // Known since, it opens at once.
  history.replaceState(null, "", "/");
  expect(slot().open!("here-1")).toBe(true);
  expect(search().get("session")).toBe("claude-code:here-1");

  history.replaceState(null, "", "/");
  expect(slot().open!("elsewhere-1")).toBe(false);
  await settle();
  expect(location.search).toBe("");
  expect(slot().open!("other-1")).toBe(false);
  expect(slot().open!("missing")).toBe(false);
});
