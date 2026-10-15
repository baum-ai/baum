import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { App } from "./app";
import { cost, filtered, type Entry } from "./api";
const entry = (number: number, status = "no_review", historical = false): Entry => ({
  pull_request: {
    repository: "acme/demo",
    number,
    title: `Change ${number}`,
    url: `https://github.com/acme/demo/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: number === 2,
    review_requested_from_me: number === 1,
    author_has_write: true,
    team_membership_known: true,
  },
  clone_id: "clone",
  status,
  version_count: status === "no_review" ? 0 : 1,
  latest: null,
  risk_score: { score: 42, status: "ready" },
  historical,
  stale: false,
});
let root: Root;
let node: HTMLDivElement;
let fail = false;
let entries: Entry[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/");
  fail = false;
  entries = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (fail) return new Response("unauthorized", { status: 401 });
      return Response.json(
        path.includes("/api/index")
          ? { entries, problems: [] }
          : path === "/api/repos"
            ? []
            : path === "/api/prs"
              ? []
              : path === "/api/jobs"
                ? { jobs: [] }
                : {},
      );
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
it("shows first-run registration rather than a blank inbox", async () => {
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Register a local clone");
  expect(node.textContent).toContain("Repositories & settings");
});
it("shows actionable launch authorization failure", async () => {
  fail = true;
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Open the URL printed by baum ui");
  expect(node.textContent).not.toContain("Generate selected");
});
it("renders review states and unknown costs without inventing zero", async () => {
  entries = [entry(1, "outdated"), entry(2, "failed"), entry(3, "current"), entry(4, "generating")];
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("outdated");
  expect(node.textContent).toContain("failed");
  expect(node.textContent).toContain("current");
  expect(node.textContent).toContain("generating");
  expect(node.textContent).toContain("—");
  expect(node.textContent).not.toContain("$0.0000");
});
it("filters by requested, authored, review state, repository and saved history", () => {
  const rows = [entry(1, "outdated"), entry(2), entry(3, "current", true)];
  expect(filtered(rows, "requested", "").map((e) => e.pull_request.number)).toEqual([1]);
  expect(filtered(rows, "mine", "").map((e) => e.pull_request.number)).toEqual([2]);
  expect(filtered(rows, "outdated", "").length).toBe(1);
  expect(filtered(rows, "no_review", "").length).toBe(1);
  expect(filtered(rows, "all", "other/repo")).toEqual([]);
  expect(filtered(rows, "history", "").map((e) => e.pull_request.number)).toEqual([3]);
  expect(cost(null)).toBe("—");
  expect(cost(0)).toBe("$0.0000");
});
it("selects multiple rows before generation", async () => {
  entries = [entry(1), entry(2)];
  await act(async () => root.render(<App />));
  const box = node.querySelector<HTMLInputElement>(
    'input[aria-label="Select all visible pull requests"]',
  )!;
  await act(async () => box.click());
  expect(node.textContent).toContain("Generate selected (2)");
});

it("shows an unpriced model exclusion and keeps generation disabled", async () => {
  entries = [entry(1)];
  const original = globalThis.fetch;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, ...args: unknown[]) =>
      path === "/api/jobs/estimate"
        ? Response.json({
            id: "quote",
            estimate_usd: null,
            price_version: "test",
            expires_at: "2026-10-15T15:00:00Z",
            items: [
              {
                clone: "clone",
                pr: 1,
                head_sha: "head",
                backend: "claude",
                model: "",
                estimate_usd: null,
                requires_isolation_confirmation: false,
                blocked_reason: "unknown_model_price",
              },
            ],
          })
        : original(path, ...(args as [])),
    ),
  );
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  await act(async () => root.render(<App />));
  await act(async () =>
    node
      .querySelector<HTMLInputElement>('input[aria-label="Select all visible pull requests"]')!
      .click(),
  );
  await act(async () =>
    Array.from(node.querySelectorAll("button"))
      .find((b) => b.textContent?.includes("Generate selected"))!
      .click(),
  );
  expect(node.textContent).toContain("Choose a priced model in repository settings.");
  expect(
    Array.from(node.querySelectorAll("button")).find(
      (b) => b.textContent === "Confirm estimate and generate",
    )!.disabled,
  ).toBe(true);
});
