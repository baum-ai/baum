import { afterEach, expect, it, vi } from "vitest";
import { UIState } from "./ui-state";

type Value = { seen: string[]; notes: Record<string, string>; note: string };
const empty = (): Value => ({ seen: [], notes: {}, note: "" });
const path = "api/ui-state/walkthrough?key=acme%2Fdemo%237%40v1";
function deferred() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}
function daemon(initial: Value | null = null) {
  let record = { revision: initial ? 1 : 0, value: initial };
  const writes = new Set<string>();
  const handler = vi.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method !== "PUT") return Response.json(record);
    const input = JSON.parse(String(init.body));
    if (writes.has(input.mutation_id)) return Response.json(record);
    if (input.revision !== record.revision) return Response.json(record, { status: 409 });
    writes.add(input.mutation_id);
    record = { revision: record.revision + 1, value: input.value };
    return Response.json(record);
  });
  vi.stubGlobal("fetch", handler);
  return {
    handler,
    get: () => record,
    change: (change: (value: Value) => Value) => {
      record = { revision: record.revision + 1, value: change(record.value ?? empty()) };
    },
  };
}
const settled = (state: UIState<Value>) => vi.waitFor(() => expect(state.get().saving).toBe(false));
afterEach(() => vi.unstubAllGlobals());

it("reads without writing defaults, then preserves edits made while hydration waits", async () => {
  const remote = daemon({ seen: ["remote"], notes: { old: "existing draft" }, note: "overall" });
  const gate = deferred();
  const handler = remote.handler.getMockImplementation()!;
  remote.handler.mockImplementation(async (path, init) => {
    if (init?.method !== "PUT") await gate.promise;
    return handler(path, init);
  });
  const state = new UIState(path, empty);
  const loading = state.load();
  state.update((current) => ({ ...current, notes: { ...current.notes, mine: "new draft" } }));
  expect(remote.handler.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  gate.release();
  await loading;
  await settled(state);
  expect(remote.get().value).toEqual({
    seen: ["remote"],
    notes: { old: "existing draft", mine: "new draft" },
    note: "overall",
  });
  const readOnly = new UIState(path, empty);
  const count = remote.handler.mock.calls.filter(([, init]) => init?.method === "PUT").length;
  await readOnly.load();
  expect(remote.handler.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(
    count,
  );
});

it("rebases concurrent changes to different fields instead of replacing a stale document", async () => {
  const remote = daemon(empty());
  const first = new UIState(path, empty);
  const second = new UIState(path, empty);
  await Promise.all([first.load(), second.load()]);
  first.update((current) => ({ ...current, notes: { ...current.notes, one: "first" } }));
  second.update((current) => ({ ...current, notes: { ...current.notes, two: "second" } }));
  await Promise.all([settled(first), settled(second)]);
  expect(remote.get().value?.notes).toEqual({ one: "first", two: "second" });
});

it("retries a committed append with its mutation id after response loss and another window's edit", async () => {
  const remote = daemon(empty());
  const handler = remote.handler.getMockImplementation()!;
  let lose = true;
  remote.handler.mockImplementation(async (path, init) => {
    const response = await handler(path, init);
    if (init?.method === "PUT" && lose) {
      lose = false;
      remote.change((current) => ({ ...current, notes: { remote: "another window" } }));
      throw Error("Response lost");
    }
    return response;
  });
  const state = new UIState(path, empty);
  await state.load();
  state.update((current) => ({ ...current, note: `${current.note}The answer.` }));
  await vi.waitFor(() => expect(state.get().error).toBe("Response lost"));
  state.update((current) => ({ ...current, notes: { ...current.notes, local: "later edit" } }));
  await state.retry();
  expect(remote.get().value).toEqual({
    seen: [],
    note: "The answer.",
    notes: { remote: "another window", local: "later edit" },
  });
  const ids = remote.handler.mock.calls
    .filter(([, init]) => init?.method === "PUT")
    .map(([, init]) => JSON.parse(String(init?.body)).mutation_id);
  expect(ids[0]).toBe(ids[1]);
  expect(ids[2]).not.toBe(ids[1]);
  const first = ids[0].split(":");
  const next = ids[2].split(":");
  expect(first[0]).toBe(next[0]);
  expect(Number(next[1])).toBe(Number(first[1]) + 1);
});

it("holds unsaved drafts after failed writes and resumes only when explicitly retried", async () => {
  const remote = daemon(empty());
  const handler = remote.handler.getMockImplementation()!;
  let fail = true;
  remote.handler.mockImplementation(async (path, init) =>
    init?.method === "PUT" && fail
      ? new Response("unavailable", { status: 500 })
      : handler(path, init),
  );
  const state = new UIState(path, empty);
  await state.load();
  state.update((current) => ({ ...current, note: "Keep my text" }));
  await vi.waitFor(() => expect(state.get().error).toBe("Could not save your changes."));
  expect(state.get().value.note).toBe("Keep my text");
  expect(remote.get().value?.note).toBe("");
  fail = false;
  await state.retry();
  expect(state.get().error).toBe("");
  expect(remote.get().value?.note).toBe("Keep my text");
});

it("never overwrites data after a failed initial read and can retry with pending edits", async () => {
  const remote = daemon({ seen: [], notes: { old: "remote draft" }, note: "" });
  const handler = remote.handler.getMockImplementation()!;
  let fail = true;
  remote.handler.mockImplementation(async (path, init) =>
    fail ? new Response("unavailable", { status: 500 }) : handler(path, init),
  );
  const state = new UIState(path, empty);
  await state.load();
  state.update((current) => ({ ...current, note: "local draft" }));
  expect(state.get().ready).toBe(false);
  expect(remote.handler.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  fail = false;
  await state.retry();
  expect(remote.get().value).toEqual({
    seen: [],
    notes: { old: "remote draft" },
    note: "local draft",
  });
});

it("serializes an edit behind an in-flight refresh so an old read cannot undo its save", async () => {
  const remote = daemon(empty());
  const state = new UIState(path, empty);
  await state.load();
  const handler = remote.handler.getMockImplementation()!;
  const gate = deferred();
  remote.handler.mockImplementation(async (path, init) => {
    const response = await handler(path, init);
    if (init?.method !== "PUT") await gate.promise;
    return response;
  });
  const refresh = state.load();
  state.update((current) => ({ ...current, note: "During refresh" }));
  expect(remote.handler.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(false);
  gate.release();
  await refresh;
  await settled(state);
  expect(remote.get().value?.note).toBe("During refresh");
  expect(state.get().value.note).toBe("During refresh");
});
