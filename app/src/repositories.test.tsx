import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import {
  RepositoryPicker,
  CloneActions,
  cloneDirectory,
  type RepositoryCall,
} from "./repositories";
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
it("leaves found clones unpicked until the person picks them", async () => {
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
  expect(calls).not.toContain("pick");
  expect(node.querySelector<HTMLInputElement>("input[type=checkbox]")!.checked).toBe(false);
  await act(async () => node.querySelector<HTMLInputElement>("input[type=checkbox]")!.click());
  expect(picked.has("picker/found")).toBe(true);
});
it("shows the first repositories and picked ones, and searches the rest", async () => {
  const names = Array.from({ length: 12 }, (_, i) => `many/repo-${i}`);
  const call: RepositoryCall = async (action) =>
    (action === "list"
      ? { repositories: names.map((repository) => ({ repository, clone: "", description: "" })) }
      : action === "picked"
        ? [{ repository: "many/repo-11" }]
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  const shown = () =>
    [...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent);
  expect(shown()).toEqual([...names.slice(0, 8), "many/repo-11"]);
  expect(node.querySelector(".repository-more")!.textContent).toBe(
    "3 more repositories. Search to find one.",
  );
  const search = node.querySelector<HTMLInputElement>("input[type=search]")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      search,
      "repo-10",
    );
    search.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(shown()).toEqual(["many/repo-10"]);
  expect(node.querySelector(".repository-more")).toBeNull();
});
it("puts exact and leading matches first, so a name buried in longer ones stays reachable", async () => {
  const names = [
    ...Array.from({ length: 8 }, (_, i) => `acme/api-client-${i}`),
    "acme/web-api",
    "acme/api",
  ];
  const call: RepositoryCall = async (action) =>
    (action === "list"
      ? { repositories: names.map((repository) => ({ repository, clone: "", description: "" })) }
      : action === "picked"
        ? []
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  const search = node.querySelector<HTMLInputElement>("input[type=search]")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      search,
      "acme/api",
    );
    search.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const shown = [...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent);
  expect(shown).toEqual(["acme/api", ...names.slice(0, 7)]);
  expect(node.querySelector(".repository-more")!.textContent).toBe(
    "1 more match. Keep typing to narrow the list.",
  );
});
it("keeps loading, not empty, while the GitHub listing for a new connection is pending", async () => {
  let finish!: () => void;
  const call: RepositoryCall = async (action) => {
    if (action === "list") {
      await new Promise<void>((resolve) => {
        finish = resolve;
      });
      return { repositories: [{ repository: "late/one", clone: "", description: "" }] } as never;
    }
    return (action === "picked" ? [] : {}) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} connected={false} />));
  await act(async () => root.render(<RepositoryPicker call={call} connected />));
  expect(node.textContent).toContain("Loading your GitHub repositories…");
  expect(node.textContent).not.toContain("No repositories match.");
  await act(async () => finish());
  expect(node.textContent).not.toContain("Loading your GitHub repositories…");
  expect(node.querySelector(".repository-name strong")!.textContent).toBe("late/one");
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
it("maps needs_clone conflicts from generation and other requests", async () => {
  history.replaceState(null, "", "/s/test-ns/");
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ needs_clone: true }, { status: 409 })),
  );
  expect(await generateReviews([{ clone: "gone", pr: 1 }], "single")).toBe("needs_clone");
  await expect(request("api/jobs", { items: [] })).rejects.toEqual(
    expect.objectContaining({ message: "Needs a local clone", code: "needs_clone" }),
  );
  expect(LocalRequestError).toBeDefined();
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

it("clones into the repository's own folder inside the folder picked", () => {
  expect(cloneDirectory("/home/user/code", "acme/widgets")).toBe("/home/user/code/widgets");
  expect(cloneDirectory("/home/user/code/", "acme/widgets")).toBe("/home/user/code/widgets");
  // A folder already named after the repository is the clone's folder.
  expect(cloneDirectory("/home/user/code/widgets", "acme/widgets")).toBe("/home/user/code/widgets");
  expect(cloneDirectory("C:\\Users\\me\\code", "acme/widgets")).toBe(
    "C:\\Users\\me\\code\\widgets",
  );
});
