import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
import type { Entry, ReviewGroup } from "./api";
import { inboxGroups } from "./inbox";
import {
  compareEntries,
  describeSort,
  dropLastSortKey,
  parseSort,
  sortBy,
  toggleSortField,
  withoutSortField,
} from "./pr-sort";
import { SortButton } from "./sort-menu";
import { withGroups } from "./up-next";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const entry = (
  number: number,
  {
    opened,
    risk = null,
    minutes = null,
    repository = "acme/demo",
    author = "sam",
    requested = false,
  }: {
    opened?: string;
    risk?: number | null;
    minutes?: number | null;
    repository?: string;
    author?: string;
    requested?: boolean;
  } = {},
): Entry => ({
  pull_request: {
    repository,
    number,
    title: `Change ${number}`,
    url: `https://github.com/${repository}/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    author,
    authored_by_me: false,
    review_requested_from_me: requested,
    author_has_write: true,
    team_membership_known: true,
    review: "",
    checks: "passing",
    merge: "blocked",
    unresolved: 0,
    unresolved_capped: false,
    ...(opened ? { created_at: opened } : {}),
  },
  clone_id: "clone",
  status: "current",
  version_count: 1,
  latest: null,
  risk_score: risk === null ? null : { status: "assessed", score: risk },
  historical: false,
  stale: false,
  review: "",
  checks: "passing",
  merge: "blocked",
  unresolved: 0,
  unresolved_capped: false,
  review_minutes: minutes,
});
const numbers = (entries: Entry[]) => entries.map((one) => one.pull_request.number);

it("reads a sort, skipping unknown and repeated fields", () => {
  expect(parseSort("opened, -risk,bogus,risk,-")).toEqual([
    { field: "opened", reversed: false },
    { field: "risk", reversed: true },
  ]);
  expect(parseSort("")).toEqual([]);
  expect(parseSort(undefined)).toEqual([]);
});

it("adds a field, turns it round, drops it and the last key", () => {
  let sort = toggleSortField("", "opened");
  expect(sort).toBe("opened");
  sort = toggleSortField(sort, "risk");
  expect(sort).toBe("opened,risk");
  sort = toggleSortField(sort, "opened");
  expect(sort).toBe("-opened,risk");
  expect(dropLastSortKey(sort)).toBe("-opened");
  expect(withoutSortField(sort, "opened")).toBe("risk");
  expect(describeSort(sort)).toBe("Opened, newest first, then Risk, highest first");
  expect(describeSort("")).toBe("");
});

it("sorts by each key in turn, ties keeping their order, missing values last", () => {
  const rows = [
    entry(1, { opened: "2026-11-03T00:00:00Z", risk: 2 }),
    entry(2, { opened: "2026-11-01T00:00:00Z", risk: 4 }),
    entry(3, { risk: 5 }),
    entry(4, { opened: "2026-11-01T00:00:00Z", risk: 1 }),
    entry(5, { opened: "2026-11-01T00:00:00Z" }),
  ];
  // Opened oldest first, then risk highest first; no risk goes last.
  expect(numbers(sortBy(rows, parseSort("opened,risk"), (one) => one))).toEqual([2, 4, 5, 1, 3]);
  // Reversed, a missing date still goes last.
  expect(numbers(sortBy(rows, parseSort("-opened"), (one) => one))).toEqual([1, 2, 4, 5, 3]);
  // Risk lowest first, ties (none) in their own order.
  expect(numbers(sortBy(rows, parseSort("-risk"), (one) => one))).toEqual([4, 1, 2, 3, 5]);
  // No keys: the same array.
  expect(sortBy(rows, [], (one) => one)).toBe(rows);
  expect(compareEntries(parseSort("time"), entry(1, { minutes: 3 }), entry(2))).toBeLessThan(0);
});

it("sorts text fields A to Z, numbers in names in order", () => {
  const rows = [
    entry(1, { repository: "acme/app10" }),
    entry(2, { repository: "Acme/app2" }),
    entry(3, { repository: "acme/api" }),
  ];
  expect(numbers(sortBy(rows, parseSort("repo"), (one) => one))).toEqual([3, 2, 1]);
  expect(numbers(sortBy(rows, parseSort("-repo"), (one) => one))).toEqual([1, 2, 3]);
});

it("sorts Pull requests within each bucket, groups by their first-ranked member", () => {
  const group: ReviewGroup = {
    id: "g",
    repository: "acme/demo",
    name: "Pair",
    members: [7, 8],
  } as ReviewGroup;
  const rows = [
    entry(1, { risk: 1, requested: true }),
    entry(2, { risk: 3, requested: true }),
    entry(3, { risk: 2 }),
    entry(4, { risk: 4 }),
    entry(7, { risk: 1 }),
    entry(8, { risk: 5 }),
  ];
  const groups = inboxGroups(rows, [group], parseSort("risk"));
  expect(groups.map((one) => [one.id, numbers(one.rows)])).toEqual([
    ["requested", [2, 1]],
    ["other", [4, 3]],
  ]);
  // The group falls among the rows by its riskiest member (8, risk 5).
  const line = (one: { group?: { group: ReviewGroup }; entry?: Entry }) =>
    one.group ? one.group.group.id : one.entry!.pull_request.number;
  expect(groups[1]!.lines.map(line)).toEqual(["g", 4, 3]);
  // Without a sort, the buckets keep the order they were given, groups first.
  const plain = inboxGroups(rows, [group]);
  expect(numbers(plain[0]!.rows)).toEqual([1, 2]);
  expect(plain[1]!.lines.map(line)).toEqual(["g", 3, 4]);
});

it("puts a group after a lone pull request that ranks higher", () => {
  // The group's riskiest member scores 1; the lone pull request scores 5,
  // and the one with no score goes last.
  const group = { id: "g", repository: "acme/demo", name: "Pair", members: [7, 8] } as ReviewGroup;
  const rows = [entry(3), entry(4, { risk: 5 }), entry(7, { risk: 1 }), entry(8)];
  const [other] = inboxGroups(rows, [group], parseSort("risk"));
  expect(
    other!.lines.map((one) => (one.group ? one.group.group.id : one.entry.pull_request.number)),
  ).toEqual([4, "g", 3]);
});

it("places a sorted stack at its first-ranked member, not its base", () => {
  const stack = {
    id: "s",
    repository: "acme/demo",
    name: "Stack",
    members: [1, 2],
    stack: true,
  } as unknown as ReviewGroup;
  const row = (number: number) => ({ key: `acme/demo#${number}`, entry: entry(number) });
  // Sorted, the tip (2) ranks before 3 and the base (1) last.
  const section = { id: "eyes", rows: [row(2), row(3), row(1)] };
  const keys = (lines: { key: string }[]) => lines.map((line) => line.key);
  expect(keys(withGroups([section], [stack], true)[0]!.lines)).toEqual(["group:s", "acme/demo#3"]);
  // Unsorted, a stack follows its base.
  expect(keys(withGroups([section], [stack])[0]!.lines)).toEqual(["acme/demo#3", "group:s"]);
});

it("opens with Shift+S and takes digits, Backspace and Escape", async () => {
  const node = document.createElement("div");
  document.body.append(node);
  const root = createRoot(node);
  let sort = "";
  const onChange = vi.fn((next: string) => {
    sort = next;
  });
  const render = () => act(async () => root.render(<SortButton sort={sort} onChange={onChange} />));
  const press = (key: string, target: EventTarget = document.activeElement ?? window) =>
    act(async () => {
      target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
    });
  try {
    await render();
    const details = node.querySelector("details")!;
    await press("S", document.body);
    expect(details.open).toBe(true);
    const panel = node.querySelector<HTMLElement>('[role="menu"]')!;
    expect(document.activeElement).toBe(panel);
    await press("1", panel);
    expect(sort).toBe("opened");
    await render();
    await press("3", panel);
    expect(sort).toBe("opened,risk");
    await render();
    await press("1", panel);
    expect(sort).toBe("-opened,risk");
    await render();
    // The fields in the sort show their place and direction.
    expect(
      [...panel.querySelectorAll('[aria-checked="true"]')].map((row) => row.textContent),
    ).toEqual([
      expect.stringContaining("OpenedNewest first1"),
      expect.stringContaining("RiskHighest first2"),
    ]);
    await press("Backspace", panel);
    expect(sort).toBe("-opened");
    await render();
    // Nothing reads 0 any more; Backspace drops the last key.
    await press("0", panel);
    expect(sort).toBe("-opened");
    await press("Backspace", panel);
    expect(sort).toBe("");
    await render();
    await press("Escape", panel);
    expect(details.open).toBe(false);
  } finally {
    act(() => root.unmount());
    node.remove();
  }
});
