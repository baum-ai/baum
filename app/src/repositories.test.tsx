import { mockStateFetch } from "./ui-state-test-helper";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import {
  RepositoryPicker,
  CloneActions,
  cloneDirectory,
  type RepositoryCall,
  repositoryCall,
  setupRepositoryCall,
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
  vi.useRealTimers();
});
async function searchRepositories(query: string) {
  const search = node.querySelector<HTMLInputElement>("input[type=search]")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, query);
    search.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => new Promise((resolve) => setTimeout(resolve, 300)));
}
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
  const call: RepositoryCall = async (action, args) =>
    (action === "list"
      ? {
          total_count: 147,
          repositories: names
            .filter((name) => name.includes(args?.query ?? ""))
            .map((repository) => ({ repository, clone: "", description: "" })),
        }
      : action === "picked"
        ? [{ repository: "many/repo-11" }]
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  const shown = () =>
    [...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent);
  expect(shown()).toEqual([...names.slice(0, 8), "many/repo-11"]);
  // Without another GitHub page, only the loaded rows count.
  expect(node.querySelector(".repository-more")!.textContent).toBe(
    "3 more repositories. Show more",
  );
  await searchRepositories("repo-10");
  expect(shown()).toEqual(["many/repo-10"]);
  expect(node.querySelector(".repository-more")).toBeNull();
});
it("shows more rows of the current list, then asks GitHub for its next page", async () => {
  const page = (from: number, to: number) =>
    Array.from({ length: to - from }, (_, i) => ({
      repository: `many/repo-${String(from + i).padStart(2, "0")}`,
      clone: "",
      description: "",
    }));
  let loaded = 30;
  const calls: string[] = [];
  const call: RepositoryCall = async (action, args) => {
    calls.push(`${action}:${args?.query ?? ""}`);
    if (action === "more-list") loaded = 40;
    return (
      action === "list" || action === "more-list"
        ? {
            total_count: 40,
            has_more: loaded < 40,
            repositories: page(0, loaded).filter((r) => r.repository.includes(args?.query ?? "")),
          }
        : action === "picked"
          ? []
          : {}
    ) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  const shown = () => node.querySelectorAll(".repository-name strong").length;
  const more = () => node.querySelector<HTMLButtonElement>(".repository-show-more")!;
  expect(shown()).toBe(8);
  // The count includes the rows GitHub has not returned yet.
  expect(node.querySelector(".repository-more")!.textContent).toBe(
    "32 more repositories. Show more",
  );
  await act(async () => more().click());
  expect(shown()).toBe(28);
  expect(calls).not.toContain("more-list:");
  await act(async () => more().click());
  expect(calls).toContain("more-list:");
  expect(shown()).toBe(40);
  expect(node.querySelector(".repository-more")).toBeNull();
  // A new search starts from the first rows again.
  await searchRepositories("repo-1");
  expect(shown()).toBe(8);
  expect(node.querySelector(".repository-more")!.textContent).toBe("2 more matches. Show more");
});
it("puts exact and leading matches first, so a name buried in longer ones stays reachable", async () => {
  const names = [
    ...Array.from({ length: 8 }, (_, i) => `acme/api-client-${i}`),
    "acme/web-api",
    "acme/api",
  ];
  const call: RepositoryCall = async (action, args) =>
    (action === "list"
      ? {
          repositories: names
            .filter((name) => name.includes(args?.query ?? ""))
            .map((repository) => ({ repository, clone: "", description: "" })),
        }
      : action === "picked"
        ? []
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await searchRepositories("acme/api");
  const shown = [...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent);
  expect(shown).toEqual(["acme/api", ...names.slice(0, 7)]);
  expect(node.querySelector(".repository-more")!.textContent).toBe("1 more match. Show more");
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
it.each([
  ["the app's Add repositories", repositoryCall],
  ["desktop setup", setupRepositoryCall],
] as const)("searches beyond suggestions and picks the result in %s", async (_name, call) => {
  history.replaceState(null, "", "/s/test-ns/");
  const picked = new Set<string>();
  const queries: string[] = [];
  const repository = "new-org/never-contributed";
  const backend = (action: string, args: { query?: string; repository?: string } = {}) => {
    if (action === "list") {
      queries.push(args.query ?? "");
      return {
        repositories: args.query ? [{ repository, description: "", clone: "" }] : [],
        total_count: 200,
      };
    }
    if (action === "pick") picked.add(args.repository!);
    if (action === "picked") return [...picked].map((repository) => ({ repository }));
    return {};
  };
  vi.stubGlobal("__TAURI_INTERNALS__", {
    invoke: async (
      _command: string,
      args: { action: string; query?: string; repository?: string },
    ) => backend(args.action, args),
  });
  vi.stubGlobal(
    "fetch",
    mockStateFetch(async (input, init) => {
      const url = new URL(String(input), location.origin);
      const action = url.pathname.endsWith("/github/repositories")
        ? "list"
        : url.pathname.endsWith("/repositories")
          ? init?.method === "POST"
            ? "pick"
            : "picked"
          : "found";
      return Response.json(
        backend(action, {
          query: url.searchParams.get("query") ?? "",
          ...(init?.body ? JSON.parse(String(init.body)) : {}),
        }),
      );
    }),
  );
  const onPicked = vi.fn();
  await act(async () => root.render(<RepositoryPicker call={call} onPicked={onPicked} />));
  expect(node.textContent).toContain("No repositories match.");
  await searchRepositories("new-org never");
  const checkbox = node.querySelector<HTMLInputElement>(`input[aria-label="Pick ${repository}"]`)!;
  expect(checkbox).not.toBeNull();
  await act(async () => checkbox.click());
  expect(picked.has(repository)).toBe(true);
  expect(onPicked).toHaveBeenLastCalledWith(1);
  expect(queries).toContain("new-org never");
  await searchRepositories("");
  expect(queries.at(-1)).toBe("");
  expect(
    node.querySelector<HTMLInputElement>(`input[aria-label="Pick ${repository}"]`)!.checked,
  ).toBe(true);
});
it("debounces search and ignores an older search response", async () => {
  vi.useFakeTimers();
  const pending = new Map<string, (value: unknown) => void>();
  const call: RepositoryCall = async (action, args) => {
    if (action === "list" && args?.query) {
      return (await new Promise<unknown>((resolve) => pending.set(args.query!, resolve))) as never;
    }
    return (action === "list" ? { repositories: [] } : action === "picked" ? [] : {}) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  const type = async (query: string) =>
    act(async () => {
      const input = node.querySelector<HTMLInputElement>("input[type=search]")!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, query);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  await type("old");
  expect(pending.size).toBe(0);
  expect(node.textContent).toContain("Searching your GitHub repositories…");
  await act(async () => vi.advanceTimersByTimeAsync(250));
  await type("new");
  await act(async () => vi.advanceTimersByTimeAsync(250));
  const result = (repository: string) => ({
    repositories: [{ repository, clone: "", description: "" }],
  });
  await act(async () => pending.get("new")!(result("acme/new")));
  await act(async () => pending.get("old")!(result("acme/old")));
  expect(node.textContent).toContain("acme/new");
  expect(node.textContent).not.toContain("acme/old");
});
it("shows cached top matches immediately before the debounce or GitHub response", async () => {
  vi.useFakeTimers();
  const queries: string[] = [];
  const call: RepositoryCall = async (action, args) => {
    if (action === "list") {
      queries.push(args?.query ?? "");
      return (
        args?.query
          ? { repositories: [], searching: true }
          : {
              repositories: [
                { repository: "acme/widget-tools", description: "", clone: "", top: true },
              ],
            }
      ) as never;
    }
    return (action === "picked" ? [] : {}) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await act(async () => {
    const input = node.querySelector<HTMLInputElement>("input[type=search]")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      input,
      "widget",
    );
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(queries).toEqual([""]);
  expect(node.querySelector('[aria-label="Pick acme/widget-tools"]')).not.toBeNull();
  await act(async () => vi.advanceTimersByTimeAsync(250));
  expect(queries).toEqual(["", "widget"]);
  expect(node.querySelector('[aria-label="Pick acme/widget-tools"]')).not.toBeNull();
});
it("sorts exact matches before top matches, then sorts by latest push", async () => {
  const repositories = [
    { repository: "acme/widget-tools", top: true, pushed_at: "2026-10-05T00:00:00Z" },
    { repository: "other/widget-new", top: false, pushed_at: "2026-10-10T00:00:00Z" },
    { repository: "other/widget", top: false, pushed_at: "2026-01-01T00:00:00Z" },
    { repository: "acme/widget-old", top: true, pushed_at: "2026-10-01T00:00:00Z" },
  ].map((r) => ({ ...r, description: "", clone: "", picked: false }));
  const call: RepositoryCall = async (action, args) =>
    (action === "list"
      ? { repositories: args?.query ? repositories : repositories.filter((r) => r.top) }
      : action === "picked"
        ? []
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await searchRepositories("widget");
  expect([...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent)).toEqual([
    "other/widget",
    "acme/widget-tools",
    "acme/widget-old",
    "other/widget-new",
  ]);
});
it("preserves the top repository order when search is empty or cleared", async () => {
  const repositories = [
    { repository: "acme/widget-z", pushed_at: "2026-01-01T00:00:00Z" },
    { repository: "acme/widget-a", pushed_at: "2026-10-01T00:00:00Z" },
    { repository: "acme/widget-m", pushed_at: "2026-05-01T00:00:00Z" },
  ].map((r) => ({ ...r, description: "", clone: "", picked: false, top: true }));
  const call: RepositoryCall = async (action) =>
    (action === "list" ? { repositories } : action === "picked" ? [] : {}) as never;
  const shown = () =>
    [...node.querySelectorAll(".repository-name strong")].map((n) => n.textContent);
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(shown()).toEqual(repositories.map((r) => r.repository));
  await searchRepositories("widget");
  expect(shown()).toEqual(["acme/widget-a", "acme/widget-m", "acme/widget-z"]);
  await searchRepositories("");
  expect(shown()).toEqual(repositories.map((r) => r.repository));
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
it("shows and picks cached results while GitHub search is pending", async () => {
  vi.useFakeTimers();
  let page = 0;
  const picked = new Set<string>();
  const onPicked = vi.fn();
  const call: RepositoryCall = async (action, args) => {
    if (action === "pick") picked.add(args!.repository!);
    return (
      action === "list"
        ? {
            repositories:
              args?.query && page > 0
                ? [{ repository: "acme/early", description: "", clone: "" }]
                : [],
            searching: Boolean(args?.query) && page < 2,
          }
        : action === "picked"
          ? [...picked].map((repository) => ({ repository }))
          : {}
    ) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} onPicked={onPicked} />));
  await act(async () => {
    const input = node.querySelector<HTMLInputElement>("input[type=search]")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "early");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => vi.advanceTimersByTimeAsync(250));
  expect(node.textContent).toContain("Searching your GitHub repositories…");
  expect(node.textContent).not.toContain("No repositories match.");
  page = 1;
  await act(async () => vi.advanceTimersByTimeAsync(300));
  const checkbox = node.querySelector<HTMLInputElement>('input[aria-label="Pick acme/early"]')!;
  expect(checkbox).not.toBeNull();
  expect(checkbox.disabled).toBe(false);
  await act(async () => checkbox.click());
  expect(onPicked).toHaveBeenLastCalledWith(1);
  expect(node.textContent).toContain("Searching your GitHub repositories…");
  // With rows shown, the search reports itself below them, not over them.
  const status = node.querySelector(".repository-results [role=status]")!;
  expect(status.closest(".repository-more")).not.toBeNull();
  expect(status.classList.contains("loading-block")).toBe(false);
  expect(
    node.querySelector(".repository-choices")!.compareDocumentPosition(status) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  page = 2;
  await act(async () => vi.advanceTimersByTimeAsync(300));
  expect(node.textContent).not.toContain("Searching your GitHub repositories…");
  expect(node.querySelector<HTMLInputElement>('input[aria-label="Pick acme/early"]')!.checked).toBe(
    true,
  );
});
it("keeps cached results when GitHub search fails and retries explicitly", async () => {
  let failed = true;
  const actions: string[] = [];
  const call: RepositoryCall = async (action, args) => {
    actions.push(action);
    if (action === "retry-list") failed = false;
    return (
      action === "list" || action === "retry-list"
        ? {
            repositories: args?.query
              ? [{ repository: "acme/early", description: "", clone: "" }]
              : [],
            search_error: args?.query && failed ? "GitHub could not be reached. Try again." : "",
          }
        : action === "picked"
          ? []
          : {}
    ) as never;
  };
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await searchRepositories("early");
  expect(node.querySelector('[aria-label="Pick acme/early"]')).not.toBeNull();
  expect(node.textContent).toContain("GitHub could not be reached.");
  await act(async () =>
    [...node.querySelectorAll("button")]
      .find((button) => button.textContent === "Try again")!
      .click(),
  );
  expect(actions).toContain("retry-list");
  expect(node.textContent).not.toContain("GitHub could not be reached.");
  expect(node.querySelector('[aria-label="Pick acme/early"]')).not.toBeNull();
});
it("only announces no matches after GitHub search completes", async () => {
  vi.useFakeTimers();
  let searching = true;
  const call: RepositoryCall = async (action, args) =>
    (action === "list"
      ? { repositories: [], searching: Boolean(args?.query) && searching }
      : action === "picked"
        ? []
        : {}) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  await act(async () => {
    const input = node.querySelector<HTMLInputElement>("input[type=search]")!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
      input,
      "missing",
    );
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => vi.advanceTimersByTimeAsync(250));
  expect(node.textContent).not.toContain("No repositories match.");
  searching = false;
  await act(async () => vi.advanceTimersByTimeAsync(300));
  expect(node.textContent).toContain("No repositories match.");
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
    mockStateFetch(async () => Response.json({ needs_clone: true }, { status: 409 })),
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
it("warns about a picked repository whose clone uses an earlier name", async () => {
  const call: RepositoryCall = async (action) =>
    (action === "list"
      ? {
          repositories: [
            {
              repository: "new/repo",
              clone: "/code/repo",
              earlier_names: ["old/repo"],
              picked: true,
              description: "",
            },
          ],
        }
      : action === "picked"
        ? [{ repository: "new/repo", clones: [], settings: {} }]
        : { running: false, repositories: {}, attached: [] }) as never;
  await act(async () => root.render(<RepositoryPicker call={call} />));
  expect(node.querySelector(".repository-warning")!.textContent).toBe("Pointing to old/repo.");
});
