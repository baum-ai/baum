import { expect, it } from "vitest";
import type { Entry } from "./api";
import { countOf, holds, inboxesOf, matches, parseHeld, type Inbox } from "./inboxes";

const inbox = (overrides: Partial<Inbox> = {}): Inbox => ({
  id: "i1",
  name: "Inbox one",
  color: "blue",
  what: "any",
  owners: [],
  risk: "any",
  repositories: [],
  query: "",
  no_bots: false,
  badge: true,
  ...overrides,
});

const entry = (number: number, repository = "Acme/demo"): Entry =>
  ({
    risk_score: null,
    pull_request: { repository, number, title: `Change ${number}`, authored_by_me: false },
  }) as Entry;

// What the daemon answers: #1 a review in "reviews", #2 someone else's in no
// inbox, and an inbox whose query it matched as written.
const answer = {
  count: 1,
  items: [
    { key: "acme/demo#1", kind: "review", inboxes: ["reviews", "all"] },
    { key: "acme/demo#2", kind: "other", inboxes: ["all"] },
  ],
  inboxes: [
    { id: "reviews", query: "waiting=review", count: 1 },
    { id: "all", query: "", count: 2 },
  ],
};

it("reads the daemon's queue, and nothing from an older daemon", () => {
  const held = parseHeld(answer)!;
  expect([...held.kinds]).toEqual([
    ["acme/demo#1", "review"],
    ["acme/demo#2", "other"],
  ]);
  expect([...held.byQuery.get("waiting=review")!]).toEqual(["acme/demo#1"]);
  expect([...held.byQuery.get("")!]).toEqual(["acme/demo#1", "acme/demo#2"]);
  expect(parseHeld({ available: false, code: "inbox_unavailable" })).toBeNull();
  expect(parseHeld(null)).toBeNull();
  // Unreadable inboxes: the kinds, and no query answered.
  expect(parseHeld({ ...answer, inboxes: null })!.byQuery.size).toBe(0);
});

it("takes the daemon's answer over matching on the page", () => {
  const held = parseHeld(answer)!;
  // The daemon says #1 is not in the inbox whose query is "", though the
  // page would match it: its answer decides.
  const narrowed = parseHeld({
    ...answer,
    items: [{ key: "acme/demo#1", kind: "review", inboxes: [] }],
  })!;
  expect(holds(narrowed, "", { kind: "review", entry: entry(1) })).toBe(false);
  expect(holds(held, "", { kind: "review", entry: entry(1) })).toBe(true);
  expect(holds(held, "waiting=review", { kind: "other", entry: entry(2) })).toBe(false);
  // An inbox goes by its query, whatever its id.
  expect(
    matches(
      inbox({ id: "x", query: "waiting=review" }),
      { kind: "review", entry: entry(1) },
      narrowed,
    ),
  ).toBe(false);
});

it("matches on the page where the daemon has no answer", () => {
  const held = parseHeld(answer)!;
  // A query not saved yet.
  expect(holds(held, "Change 2", { kind: "other", entry: entry(2) })).toBe(true);
  // A pull request the daemon has not read yet.
  expect(holds(held, "waiting=review", { kind: "review", entry: entry(3) })).toBe(true);
  // One the daemon matched as another kind: a read in between.
  const fixed = parseHeld({
    ...answer,
    items: [{ key: "acme/demo#1", kind: "fix", inboxes: [] }],
  })!;
  expect(holds(fixed, "waiting=review", { kind: "review", entry: entry(1) })).toBe(true);
  // An older daemon.
  expect(holds(null, "waiting=review", { kind: "review", entry: entry(1) })).toBe(true);
});

it("counts and tags inboxes from the daemon's answer", () => {
  const held = parseHeld({
    ...answer,
    items: [
      { key: "acme/demo#1", kind: "review", inboxes: ["all"] },
      { key: "acme/demo#2", kind: "other", inboxes: ["all"] },
    ],
  })!;
  const items = [
    { kind: "review" as const, entry: entry(1) },
    { kind: "other" as const, entry: entry(2) },
  ];
  const reviews = inbox({ id: "reviews", query: "waiting=review" });
  const all = inbox({ id: "all" });
  expect(countOf(reviews, items, held)).toBe(0);
  expect(countOf(all, items, held)).toBe(2);
  expect(inboxesOf(items[0]!, [reviews, all], held).map((one) => one.id)).toEqual(["all"]);
});
