import { expect, it } from "vitest";
import type { Entry } from "./api";
import { sentence, type Inbox } from "./inboxes";
import {
  chips,
  inWhere,
  matchesCriteria,
  NO_CRITERIA,
  parseCriteria,
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

it("treats owners and repositories as one tree", () => {
  const where = (c: Partial<Criteria>, repository: string) => inWhere(criteria(c), repository);
  expect(where({}, "acme/demo")).toBe(true);
  // An owner alone is all of its repositories.
  expect(where({ owners: ["ACME"] }, "acme/demo")).toBe(true);
  expect(where({ owners: ["acme"] }, "other/demo")).toBe(false);
  // An owner with some of its repositories named is only those.
  expect(where({ owners: ["acme"], repositories: ["acme/web"] }, "acme/demo")).toBe(false);
  expect(where({ owners: ["acme"], repositories: ["acme/web"] }, "acme/web")).toBe(true);
  // Another owner's repository named is in as well, and leaves the owner whole.
  expect(where({ owners: ["acme"], repositories: ["other/x"] }, "other/x")).toBe(true);
  expect(where({ owners: ["acme"], repositories: ["other/x"] }, "acme/demo")).toBe(true);
  expect(where({ repositories: ["other/x"] }, "acme/demo")).toBe(false);
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
  const all = criteria({
    owners: ["acme", "other"],
    repositories: ["acme/web"],
    involvement: ["assigned"],
    no_drafts: true,
    risk: "4",
  });
  const list = chips(all);
  // acme stands for its named repository, so only other is a chip of its own.
  expect(list.map((chip) => chip.label)).toEqual([
    "other",
    "acme/web",
    "Assigned to me",
    "No drafts",
    "Risk 4 or 5",
  ]);
  // Removing an owner's last named repository removes the owner too.
  expect(list[1]!.without.owners).toEqual(["other"]);
  expect(list[1]!.without.repositories).toEqual([]);
  expect(list[2]!.without.involvement).toEqual([]);
  expect(chips(NO_CRITERIA)).toEqual([]);
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
