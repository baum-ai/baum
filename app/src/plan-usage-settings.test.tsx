import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { PlanUsageSettings, type PlanChoice } from "./plan-usage-settings";
import { PLAN_USAGE_CHANGED } from "./plan-usage";

let root: Root;
let host: HTMLElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

const plans = (codex: Partial<PlanChoice> = {}): { plans: PlanChoice[] } => ({
  plans: [
    { provider: "claude", name: "Claude Code", shown: true, default: true, explicit: false },
    { provider: "codex", name: "Codex", shown: false, default: false, explicit: false, ...codex },
  ],
});
const flush = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const input = (provider: string) =>
  host.querySelector<HTMLInputElement>(`#plan-usage-${provider}`) as HTMLInputElement;

it("lists each plan with its state and saves a choice, telling the sidebar", async () => {
  const calls: { method: string; body: string }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      calls.push({ method, body: String(init?.body ?? "") });
      const body = method === "PUT" ? plans({ shown: true, explicit: true }) : plans();
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
  const changed = vi.fn();
  window.addEventListener(PLAN_USAGE_CHANGED, changed);
  act(() => root.render(<PlanUsageSettings />));
  await flush();
  expect(input("claude").checked).toBe(true);
  expect(input("codex").checked).toBe(false);
  expect(host.textContent).toContain("Shown because baum uses it.");
  expect(host.textContent).toContain("Hidden because baum does not use it.");
  expect(host.querySelector('label[for="plan-usage-codex"]')?.textContent).toBe(
    "Show Codex plan usage",
  );
  act(() => input("codex").click());
  await flush();
  expect(changed).toHaveBeenCalledTimes(1);
  expect(calls.at(-1)).toEqual({
    method: "PUT",
    body: JSON.stringify({ provider: "codex", shown: true }),
  });
  expect(input("codex").checked).toBe(true);
  const reset = [...host.querySelectorAll("button")].find((b) =>
    b.textContent?.startsWith("Use baum's default (hidden)"),
  );
  expect(reset).toBeTruthy();
  act(() => reset?.click());
  await flush();
  expect(changed).toHaveBeenCalledTimes(2);
  expect(calls.at(-1)?.body).toBe(JSON.stringify({ provider: "codex", shown: null }));
  window.removeEventListener(PLAN_USAGE_CHANGED, changed);
});

it("reports a failed save and keeps the choice as it was", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) =>
      init?.method === "PUT"
        ? new Response("no", { status: 500 })
        : new Response(JSON.stringify(plans()), { status: 200 }),
    ),
  );
  act(() => root.render(<PlanUsageSettings />));
  await flush();
  act(() => input("codex").click());
  await flush();
  expect(host.querySelector('[role="alert"]')).toBeTruthy();
  expect(input("codex").checked).toBe(false);
});
