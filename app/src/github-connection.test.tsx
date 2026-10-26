import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { GitHubConnectionSection } from "./github-connection";
import type { GitHubConnection } from "./api";

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
let calls: { path: string; options?: RequestInit }[];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/auth-test/?page=settings");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  status = { ...disconnected };
  calls = [];
  vi.stubGlobal("open", vi.fn());
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      calls.push({ path, options });
      return Response.json(status);
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
const render = () => act(async () => root.render(<GitHubConnectionSection />));
async function click(label: string) {
  const button = [...node.querySelectorAll("button")].find((b) => b.textContent === label);
  expect(button, label).toBeDefined();
  await act(async () => button!.click());
}
it("starts and cancels with namespaced JSON mutations; opens only the fixed GitHub URL", async () => {
  const open = vi.fn();
  vi.stubGlobal("open", open);
  await render();
  status = pending;
  await click("Sign in with GitHub");
  expect(open).toHaveBeenCalledWith("https://github.com/login/device", "_blank", "noreferrer");
  expect(node.textContent).toContain("ABCD-EFGH");
  expect(node.querySelector("a")!.href).toBe("https://github.com/login/device");
  expect(calls.at(-1)).toMatchObject({
    path: "/s/auth-test/api/github-auth/start",
    options: { method: "POST", body: "{}", headers: { "Content-Type": "application/json" } },
  });
  status = { ...disconnected, state: "cancelled" };
  await click("Cancel sign-in");
  expect(node.textContent).toContain("sign-in was cancelled");
  expect(node.textContent).not.toContain("ABCD-EFGH");
  expect(calls.at(-1)!.path).toBe("/s/auth-test/api/github-auth/cancel");
});
it("copies the new code as part of the sign-in click", async () => {
  const write = vi.fn(async (items: ClipboardItem[]) => {
    expect(await items[0]!.getType("text/plain").then((b) => b.text())).toBe("ABCD-EFGH");
  });
  vi.stubGlobal("navigator", { clipboard: { write } });
  vi.stubGlobal(
    "ClipboardItem",
    class {
      items: Record<string, Promise<string>>;
      constructor(items: Record<string, Promise<string>>) {
        this.items = items;
      }
      async getType(type: string) {
        return new Blob([await this.items[type]!]);
      }
    },
  );
  await render();
  status = pending;
  await click("Sign in with GitHub");
  expect(write).toHaveBeenCalledOnce();
  expect(node.textContent).toContain("Code copied. Paste it on GitHub.");
});
it("copies the displayed code and explains clipboard failure", async () => {
  status = pending;
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  await render();
  await click("Copy code");
  expect(writeText).toHaveBeenCalledWith("ABCD-EFGH");
  expect(node.textContent).toContain("Code copied.");
  writeText.mockRejectedValue(Error("blocked"));
  await click("Copy code");
  expect(node.textContent).toContain("Select and copy the code above");
});
it("observes daemon progress through GET and slows observation after approval", async () => {
  vi.useFakeTimers();
  status = pending;
  await render();
  status = { ...disconnected, state: "connected", saved: true, login: "octocat" };
  await act(async () => vi.advanceTimersByTimeAsync(1500));
  expect(node.textContent).toContain("octocat");
  const count = calls.length;
  await act(async () => vi.advanceTimersByTimeAsync(6000));
  expect(calls).toHaveLength(count);
  expect(calls.every((c) => c.options?.method === "GET")).toBe(true);
});
it("keeps the effective override distinct from the saved account on disconnect", async () => {
  status = {
    ...disconnected,
    state: "connected",
    override: true,
    source: "environment",
    saved: true,
    login: "saved-cat",
  };
  await render();
  expect(node.textContent).toContain("explicit credential from environment");
  expect(node.textContent).toContain("saved-cat on github.com (not currently used)");
  status = { ...disconnected, override: true, source: "environment" };
  await click("Disconnect");
  expect(node.textContent).toContain("explicit credential from environment");
  expect(node.textContent).not.toContain("saved-cat");
  expect(calls.at(-1)!.path).toBe("/s/auth-test/api/github-auth/disconnect");
});
it.each([
  ["credential_store_unavailable", "Unlock your operating system"],
  ["reconnect_required", "expired or was revoked"],
  ["insufficient_scopes", "repository and organization read access"],
  ["sso_required", "GitHub SSO"],
  ["permission_denied", "repository access"],
  ["auth_request_failed", "Check your connection"],
])("explains %s and retries the connection without starting OAuth", async (error, text) => {
  status = { ...disconnected, error };
  await render();
  expect(node.textContent).toContain(text);
  status = { ...disconnected, state: "connected", saved: true, login: "recovered" };
  await click("Check connection");
  expect(calls.at(-1)!.path).toBe("/s/auth-test/api/github-auth/retry");
  expect(node.textContent).toContain("recovered");
});
it.each([
  ["denied", "was denied"],
  ["expired", "code expired"],
])("offers a new attempt after %s", async (state, text) => {
  status = { ...disconnected, state };
  await render();
  expect(node.textContent).toContain(text);
  status = pending;
  await click("Sign in with GitHub");
  expect(node.textContent).toContain("ABCD-EFGH");
});
it("ignores an old GET that finishes after a new authorization starts", async () => {
  vi.useFakeTimers();
  status = pending;
  await render();
  let finish!: (response: Response) => void;
  vi.mocked(fetch).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await act(async () => vi.advanceTimersByTimeAsync(1500));
  status = { ...disconnected, state: "cancelled" };
  await click("Cancel sign-in");
  await act(async () => finish(Response.json(pending)));
  expect(node.textContent).not.toContain("ABCD-EFGH");
});
it("recovers from a local transport failure", async () => {
  vi.mocked(fetch).mockRejectedValueOnce(Error("offline"));
  await render();
  expect(node.textContent).toContain("Could not check the GitHub connection. offline.");
  await click("Check again");
  expect(node.textContent).toContain("Sign in with GitHub");
});

it("observes runtime authorization failures and refreshes parent state on recovery", async () => {
  vi.useFakeTimers();
  const onChanged = vi.fn();
  status = {
    ...disconnected,
    state: "connected",
    saved: true,
    login: "alice",
    source: "baum credential store",
  };
  await act(async () => root.render(<GitHubConnectionSection onChanged={onChanged} />));
  status = { ...status, error: "reconnect_required" };
  await act(async () => vi.advanceTimersByTimeAsync(10000));
  expect(node.textContent).toContain("expired or was revoked");
  expect(onChanged).toHaveBeenCalledTimes(1);
  status = { ...status, error: undefined };
  await act(async () => dispatchEvent(new Event("focus")));
  expect(node.textContent).not.toContain("expired or was revoked");
  expect(onChanged).toHaveBeenCalledTimes(2);
});
