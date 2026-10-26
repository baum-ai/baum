import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { RepositoryPicker, CloneActions, type RepositoryCall } from "./repositories";
import { generateReviews, request, LocalRequestError } from "./api";
let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
it("preselects a found clone once and keeps a deliberate unpick across remounts", async () => {
  const picked = new Set<string>();
  const calls: string[] = [];
  const call: RepositoryCall = async (action, args) => {
    calls.push(action);
    const value =
      action === "list"
        ? {
            repositories: [
              {
                repository: "picker/found",
                clone: "/code/found",
                picked: false,
                description: "A found clone",
              },
            ],
          }
        : action === "picked"
          ? [...picked].map((repository) => ({ repository, clones: [], settings: {} }))
          : action === "found"
            ? { running: false, repositories: {}, attached: [] }
            : {};
    if (action === "pick") picked.add(args!.repository!);
    if (action === "unpick") picked.delete(args!.repository!);
    return value as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(picked.has("picker/found")).toBe(true);
  await act(async () => node.querySelector<HTMLInputElement>("input[type=checkbox]")!.click());
  expect(picked.size).toBe(0);
  await act(async () => root.render(<div />));
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(node.querySelector<HTMLInputElement>("input[type=checkbox]")!.checked).toBe(false);
  expect(calls.filter((a) => a === "pick")).toHaveLength(1);
});
it("does not pick a result arriving after the picker unmounts", async () => {
  let finish!: (value: unknown) => void;
  const calls: string[] = [];
  const call: RepositoryCall = async (action) => {
    calls.push(action);
    return (
      action === "list"
        ? await new Promise<unknown>((resolve) => {
            finish = resolve;
          })
        : action === "picked"
          ? []
          : {}
    ) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await act(async () => root.render(<div />));
  await act(async () => finish({ repositories: [{ repository: "picker/late", clone: "/late" }] }));
  expect(calls).not.toContain("pick");
});
it("retries an automatic pick after a transient failure, including after remount", async () => {
  let attempts = 0;
  let picked = false;
  const call: RepositoryCall = async (action) => {
    if (action === "list")
      return { repositories: [{ repository: "retry/found", clone: "/code/found" }] } as never;
    if (action === "picked")
      return (picked ? [{ repository: "retry/found", clones: [], settings: {} }] : []) as never;
    if (action === "pick") {
      attempts++;
      if (attempts < 3) throw Error("Registry temporarily unavailable");
      picked = true;
    }
    return {} as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(node.textContent).toContain("Registry temporarily unavailable");
  await act(async () =>
    [...node.querySelectorAll("button")]
      .find((button) => button.textContent === "Try again")!
      .click(),
  );
  expect(attempts).toBe(2);
  await act(async () => root.render(<div />));
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(attempts).toBe(3);
  expect(picked).toBe(true);
  expect(node.querySelector<HTMLInputElement>("input[type=checkbox]")!.checked).toBe(true);
  expect(JSON.parse(localStorage.getItem("baum-unpicked-repositories") ?? "[]")).not.toContain(
    "retry/found",
  );
});
it("shows an editable clone target and registers only after an explicit clone click", async () => {
  const calls: { action: string; args: unknown }[] = [];
  const call: RepositoryCall = async (action, args) => {
    calls.push({ action, args });
    return (
      action === "target"
        ? { dir: "/code/owner/repo" }
        : action === "status"
          ? { state: "failed", error: "Previous failure" }
          : { state: "running", dir: args?.dir }
    ) as never;
  };
  await act(async () =>
    root.render(<CloneActions repository="owner/repo" call={call} changed={async () => {}} />),
  );
  expect(node.textContent).toContain("Previous failure");
  await act(async () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Clone it")!.click(),
  );
  expect(node.querySelector<HTMLInputElement>("input")!.value).toBe("/code/owner/repo");
  expect(calls.some((c) => c.action === "clone")).toBe(false);
  await act(async () =>
    node
      .querySelector("form")!
      .dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })),
  );
  expect(calls).toContainEqual({
    action: "clone",
    args: { repository: "owner/repo", dir: "/code/owner/repo" },
  });
  expect(node.textContent).toContain("Cloning to");
});
it("maps needs_clone conflicts from generation and estimation", async () => {
  history.replaceState(null, "", "/s/test-ns/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ needs_clone: true }, { status: 409 })),
  );
  expect(await generateReviews([{ clone: "gone", pr: 1 }], "single")).toBe("needs_clone");
  await expect(request("api/jobs/estimate", { items: [] })).rejects.toEqual(
    expect.objectContaining({ message: "Needs a local clone", code: "needs_clone" }),
  );
  expect(LocalRequestError).toBeDefined();
});

it("stops preselecting further clones after unmounting during a pick", async () => {
  let finish!: () => void;
  const picked: string[] = [];
  const call: RepositoryCall = async (action) => {
    if (action === "list")
      return {
        repositories: [
          { repository: "during/one", clone: "/one" },
          { repository: "during/two", clone: "/two" },
        ],
      } as never;
    if (action === "picked") return [] as never;
    if (action === "pick") {
      picked.push("pick");
      await new Promise<void>((resolve) => {
        finish = resolve;
      });
      return {} as never;
    }
    return {} as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await act(async () => root.render(<div />));
  await act(async () => finish());
  expect(picked).toHaveLength(1);
});

it("offers the macOS Documents scan only through its explicit action", async () => {
  const original = Object.getOwnPropertyDescriptor(navigator, "platform");
  Object.defineProperty(navigator, "platform", { value: "MacIntel", configurable: true });
  const calls: { action: string; args: unknown }[] = [];
  const call: RepositoryCall = async (action, args) => {
    calls.push({ action, args });
    return (
      action === "list"
        ? {
            repositories: [
              { repository: "documents/missing", clone: "", picked: true, description: "" },
            ],
          }
        : action === "picked"
          ? [{ repository: "documents/missing" }]
          : action === "found"
            ? { running: false, repositories: {}, attached: [] }
            : {}
    ) as never;
  };
  try {
    await act(async () => root.render(<RepositoryPicker call={call} />));
    expect(calls.some((c) => c.action === "refresh")).toBe(false);
    await act(async () =>
      [...node.querySelectorAll("button")]
        .find((b) => b.textContent === "Also look in Documents")!
        .click(),
    );
    expect(calls).toContainEqual({ action: "refresh", args: { include_documents: true } });
  } finally {
    if (original) Object.defineProperty(navigator, "platform", original);
    else Reflect.deleteProperty(navigator, "platform");
  }
});
