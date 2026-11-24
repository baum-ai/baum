import { afterEach, expect, it, vi } from "vitest";
import type { LiveSession } from "./api";
import { pullRequestSessions } from "./review";

afterEach(() => history.replaceState(null, "", "/"));

const listed = { agent: "codex", session_id: "live-1", state: "running" } as LiveSession;
const slot = () =>
  pullRequestSessions(
    {
      status: "ready",
      sessions: [
        { id: "linked-1", vendor: "claude_code" },
        { id: "other-1", vendor: "gemini" },
      ],
    },
    [listed],
    () => null,
  );

it("opens a listed or linked session on its Sessions page, at the turn asked for", () => {
  const popstate = vi.fn();
  addEventListener("popstate", popstate);
  expect(slot().open!("live-1")).toBe(true);
  expect(new URLSearchParams(location.search).get("session")).toBe("codex:live-1");
  expect(slot().open!("linked-1", 3)).toBe(true);
  const search = new URLSearchParams(location.search);
  expect(search.get("page")).toBe("sessions");
  expect(search.get("session")).toBe("claude-code:linked-1");
  expect(search.get("turn")).toBe("3");
  expect(popstate).toHaveBeenCalledTimes(2);
  removeEventListener("popstate", popstate);
});

it("leaves a session of an agent the Sessions page does not know to the review's pane", () => {
  expect(slot().open!("other-1")).toBe(false);
  expect(slot().open!("missing")).toBe(false);
  expect(location.search).toBe("");
});
