import { expect, it } from "vitest";
import type { Entry } from "./api";
import { sentence, type Inbox } from "./inboxes";
import {
  chips,
  inWhere,
  matchesCriteria,
  NO_CRITERIA,
  ownerOf,
  parseCriteria,
  savable,
  whereFor,
  type Criteria,
} from "./pr-filter";

const criteria = (overrides: Partial<Criteria> = {}): Criteria => ({
  ...NO_CRITERIA,
  ...overrides,
});
const entry = (pr: Partial<Entry["pull_request"]> = {}, state: Partial<Entry> = {}): Entry =>
  ({
    risk_score: null,
    review: "",
    checks: "",
    merge: "",
    ...state,
    pull_request: {
      repository: "acme/demo",
      draft: false,
      authored_by_me: false,
      review_requested_from_me: false,
      ...pr,
    },
  }) as Entry;

it("needs a repository to be in both the owners and the repositories named", () => {
  const where = (c: Partial<Criteria>, repository: string) => inWhere(criteria(c), repository);
  expect(where({}, "acme/demo")).toBe(true);
  expect(where({ owners: ["ACME"] }, "acme/demo")).toBe(true);
  expect(where({ owners: ["acme"] }, "other/demo")).toBe(false);
  expect(where({ owners: ["acme"], repositories: ["acme/web"] }, "acme/demo")).toBe(false);
  expect(where({ owners: ["acme"], repositories: ["acme/web"] }, "acme/web")).toBe(true);
  // A saved inbox naming another owner's repository keeps matching nothing there.
  expect(where({ owners: ["acme"], repositories: ["other/x"] }, "other/x")).toBe(false);
  // GitHub Enterprise names carry their host.
  expect(where({ owners: ["acme"] }, "ghe.example.com/acme/app")).toBe(true);
  expect(ownerOf("ghe.example.com/acme/app")).toBe("acme");
});

it("names owners while each is picked whole, else the repositories", () => {
  const known = ["acme/a", "acme/b", "other/x", "other/y"];
  expect(whereFor([], known)).toEqual({ owners: [], repositories: [] });
  expect(whereFor(known, known)).toEqual({ owners: [], repositories: [] });
  expect(whereFor(["acme/a", "acme/b"], known)).toEqual({ owners: ["acme"], repositories: [] });
  // A whole owner beside one repository of another: both by name.
  expect(whereFor(["acme/a", "acme/b", "other/x"], known)).toEqual({
    owners: ["acme", "other"],
    repositories: ["acme/a", "acme/b", "other/x"],
  });
  const many = Array.from({ length: 52 }, (_, i) => `acme/r${i}`);
  expect(whereFor(many.slice(1), [...many, "other/x"])).toBeNull();
  expect(whereFor(many, [...many, "other/x"])).toEqual({ owners: ["acme"], repositories: [] });
});

it("matches involvement with or, and every other part with and", () => {
  const assigned = entry({ assigned_to_me: true });
  const mine = entry({ authored_by_me: true });
  const requested = entry({ review_requested_from_me: true });
  const either = criteria({ involvement: ["assigned", "authored"] });
  expect([assigned, mine, requested].map((e) => matchesCriteria(either, e))).toEqual([
    true,
    true,
    false,
  ]);
  // An older daemon's entry has no assignee flag: it is not assigned.
  expect(matchesCriteria(criteria({ involvement: ["assigned"] }), entry())).toBe(false);
  expect(matchesCriteria(criteria({ involvement: ["authored"], owners: ["other"] }), mine)).toBe(
    false,
  );
});

it("matches drafts, review, checks, merge state and risk", () => {
  expect(matchesCriteria(criteria({ no_drafts: true }), entry({ draft: true }))).toBe(false);
  expect(matchesCriteria(criteria({ no_drafts: true }), entry())).toBe(true);
  const review = (state: Entry["review"]) =>
    matchesCriteria(criteria({ review: ["none"] }), entry({}, { review: state }));
  const states: Entry["review"][] = [
    "",
    "unreviewed",
    "commented",
    "approved",
    "changes_requested",
  ];
  expect(states.map(review)).toEqual([true, true, true, false, false]);
  expect(matchesCriteria(criteria({ checks: ["failing"] }), entry({}, { checks: "failing" }))).toBe(
    true,
  );
  expect(matchesCriteria(criteria({ checks: ["failing"] }), entry({}, { checks: "passing" }))).toBe(
    false,
  );
  expect(
    matchesCriteria(criteria({ merge: ["ready", "conflict"] }), entry({}, { merge: "conflict" })),
  ).toBe(true);
  expect(matchesCriteria(criteria({ merge: ["ready"] }), entry({}, { merge: "blocked" }))).toBe(
    false,
  );
  // A pull request with no score yet is never above a threshold.
  expect(matchesCriteria(criteria({ risk: "3" }), entry())).toBe(false);
  expect(
    matchesCriteria(
      criteria({ risk: "3" }),
      entry({}, { risk_score: { score: 3, status: "assessed" } }),
    ),
  ).toBe(true);
});

it("names each part in force as a chip that removes it", () => {
  const known = ["acme/web", "other/x", "other/y"];
  const all = criteria({
    owners: ["acme", "other"],
    repositories: ["acme/web", "other/x"],
    involvement: ["assigned"],
    no_drafts: true,
    risk: "4",
  });
  const list = chips(all, known);
  // Every repository of acme is named, so acme is one chip; other is not whole.
  expect(list.map((chip) => chip.label)).toEqual([
    "acme",
    "other/x",
    "Assigned to me",
    "No drafts",
    "Risk 4 or 5",
  ]);
  expect(list[0]!.without).toMatchObject({ owners: ["other"], repositories: ["other/x"] });
  // Removing an owner's last named repository removes the owner too.
  expect(list[1]!.without).toMatchObject({ owners: ["acme"], repositories: ["acme/web"] });
  expect(list[2]!.without.involvement).toEqual([]);
  expect(chips(criteria({ owners: ["acme"] })).map((chip) => chip.label)).toEqual(["acme"]);
  expect(chips(NO_CRITERIA)).toEqual([]);
});

it("keeps from the daemon what it would refuse", () => {
  expect(savable(criteria({ owners: ["acme"], repositories: ["ghe.example.com/acme/app"] }))).toBe(
    true,
  );
  expect(savable(criteria({ repositories: ["foo"] }))).toBe(false);
  expect(savable(criteria({ owners: ["bad owner"] }))).toBe(false);
  expect(savable(criteria({ repositories: ["acme/.."] }))).toBe(false);
  const many = Array.from({ length: 51 }, (_, i) => `acme/r${i}`);
  expect(savable(criteria({ repositories: many }))).toBe(false);
});

it("reads saved criteria defensively", () => {
  expect(parseCriteria(null)).toEqual(NO_CRITERIA);
  expect(
    parseCriteria({
      owners: ["acme", 3],
      risk: "9",
      involvement: ["assigned", "mentioned"],
      review: "approved",
      merge: ["draft", "ready"],
      no_drafts: "yes",
    }),
  ).toEqual(criteria({ owners: ["acme"], involvement: ["assigned"], merge: ["ready"] }));
});

it("says an inbox's pull request parts in its sentence", () => {
  const inbox: Inbox = {
    ...criteria({ involvement: ["assigned", "authored"], checks: ["failing"], no_drafts: true }),
    id: "i",
    name: "I",
    color: "blue",
    what: "any",
    no_bots: false,
    badge: true,
  };
  expect(sentence(inbox)).toBe(
    "Anything waiting on me in any repository, whatever the risk, assigned to me or created by me, with checks failing, without drafts.",
  );
});
