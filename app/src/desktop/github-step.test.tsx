import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { GitHubConnection } from "../api";
import { GitHubStep } from "./github-step";

const disconnected: GitHubConnection = {
  state: "disconnected",
  host: "github.com",
  saved: false,
  override: false,
};
const pending: GitHubConnection = {
  ...disconnected,
  state: "pending",
  pending: {
    id: "attempt",
    user_code: "ABCD-EFGH",
    verification_uri: "https://evil.example/",
    expires_at: "2030-01-01T12:00:00Z",
    interval: 1,
  },
};
let root: Root;
let node: HTMLDivElement;
let status: GitHubConnection;
let invoke: ReturnType<typeof vi.fn>;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  status = disconnected;
  invoke = vi.fn(async (_command: string, args?: { action?: string }) =>
    args?.action === "open" ? null : status,
  );
  vi.stubGlobal("baumDesktop", { invoke });
  vi.stubGlobal("open", vi.fn());
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("asks the app to open GitHub's device page once the code has shown", async () => {
  vi.useFakeTimers();
  await act(async () => root.render(<GitHubStep onConnected={() => {}} />));
  status = pending;
  const signIn = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Sign in with GitHub",
  );
  await act(async () => signIn!.click());
  expect(node.textContent).toContain("ABCD-EFGH");
  expect(invoke).not.toHaveBeenCalledWith("github_connection", { action: "open" });
  await act(async () => vi.advanceTimersByTime(5000));
  // Through the app: the window's own navigations never open the browser.
  expect(invoke).toHaveBeenCalledWith("github_connection", { action: "open" });
  expect(window.open).not.toHaveBeenCalled();
  expect(location.href).not.toContain("github.com");
});
