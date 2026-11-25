import { expect, it } from "vitest";
import type { Entry } from "./api";
import type { ConversationSummary } from "./console-api";
import { activityCount, activityText, advance, KEEP, sumActivity } from "./pr-activity";

const entry = (pr: Partial<Entry["pull_request"]> = {}, state: Partial<Entry> = {}): Entry =>
  ({
    review: "",
    unresolved: 0,
    version_count: 1,
    historical: false,
    ...state,
    pull_request: {
      repository: "acme/demo",
      number: 7,
      head_sha: "aaa1111",
      state: "OPEN",
      updated_at: "2026-11-25T10:00:00Z",
      ...pr,
    },
  }) as Entry;

const conversation = (
  state: ConversationSummary["state"],
  updated_at: string,
  id = "c1",
): ConversationSummary =>
  ({ id, state, updated_at, repository: "acme/demo", pull_request: 7 }) as ConversationSummary;

const KEY = "acme/demo#7";
const start = advance({ prs: {} }, [entry()], [], [], 1)!;
const unread = (stored: ReturnType<typeof advance>) => stored!.prs[KEY]!.unread;

it("takes a pull request seen the first time as read, once the conversations are read", () => {
  expect(advance({ prs: {} }, [entry()], null, [], 1)).toBeNull();
  expect(unread(start)).toEqual({});
  expect(advance(start, [entry()], [], [], 2)).toBeNull();
});

it("counts pushes, reviews, threads and decisions since the page was open", () => {
  let stored = advance(start, [entry({ head_sha: "bbb2222" })], [], [], 2)!;
  stored = advance(stored, [entry({ head_sha: "ccc3333" })], [], [], 3)!;
  stored = advance(
    stored,
    [entry({ head_sha: "ccc3333" }, { version_count: 3, unresolved: 2, review: "approved" })],
    [],
    [],
    4,
  )!;
  expect(unread(stored)).toEqual({ pushes: 2, reviews: 2, threads: 2, decisions: 1 });
  expect(activityCount(unread(stored))).toBe(7);
  expect(activityText(unread(stored))).toBe(
    "2 new reviews, 2 pushes, 2 new threads and 1 review on GitHub",
  );
});

it("counts a finished turn of a panel conversation as one answer", () => {
  // Started: no turn yet.
  let stored = advance(start, [entry()], [conversation("idle", "2026-11-25T10:01:00Z")], [], 2)!;
  expect(unread(stored)).toEqual({});
  stored = advance(stored, [entry()], [conversation("running", "2026-11-25T10:02:00Z")], [], 3)!;
  expect(unread(stored)).toEqual({});
  stored = advance(stored, [entry()], [conversation("idle", "2026-11-25T10:04:00Z")], [], 4)!;
  expect(unread(stored)).toEqual({ answers: 1 });
  // A turn that ran between two reads counts too; closing one does not.
  stored = advance(stored, [entry()], [conversation("failed", "2026-11-25T10:09:00Z")], [], 5)!;
  expect(unread(stored)).toEqual({ answers: 2 });
  stored = advance(stored, [entry()], [conversation("closed", "2026-11-25T10:10:00Z")], [], 6)!;
  expect(unread(stored)).toEqual({ answers: 2 });
  // Unread conversations keep what was seen of them.
  expect(advance(stored, [entry()], null, [], 7)).toBeNull();
});

it("counts an update on GitHub only when nothing else explains it", () => {
  const later = "2026-11-25T11:00:00Z";
  expect(unread(advance(start, [entry({ updated_at: later })], [], [], 2))).toEqual({ github: 1 });
  expect(
    unread(advance(start, [entry({ head_sha: "bbb2222", updated_at: later })], [], [], 2)),
  ).toEqual({ pushes: 1 });
  expect(
    unread(advance(start, [entry({ updated_at: later }, { version_count: 2 })], [], [], 2)),
  ).toEqual({ reviews: 1 });
});

it("reads a pull request listed by several clones once", () => {
  const clones = [entry({}, { version_count: 3 }), entry({}, { version_count: 0 })];
  const stored = advance({ prs: {} }, clones, [], [], 1)!;
  expect(advance(stored, clones, [], [], 2)).toBeNull();
  expect(advance(stored, [...clones].reverse(), [], [], 2)).toBeNull();
});

it("keeps the review and threads while a new head's details are unknown", () => {
  const seen = advance({ prs: {} }, [entry({}, { review: "approved", unresolved: 3 })], [], [], 1)!;
  const pushed = advance(
    seen,
    [entry({ head_sha: "bbb2222" }, { review: "", unresolved: null })],
    [],
    [],
    2,
  )!;
  const fetched = advance(
    pushed,
    [entry({ head_sha: "bbb2222" }, { review: "approved", unresolved: 3 })],
    [],
    [],
    3,
  );
  expect(unread(fetched ?? pushed)).toEqual({ pushes: 1 });
});

it("clears the pull requests whose page is open", () => {
  const pushed = advance(start, [entry({ head_sha: "bbb2222" })], [], [], 2)!;
  const read = advance(pushed, [entry({ head_sha: "bbb2222" })], [], [KEY], 3)!;
  expect(unread(read)).toEqual({});
  // What happens while it is open is read too.
  expect(unread(advance(read, [entry({ head_sha: "ccc3333" })], [], [KEY], 4))).toEqual({});
});

it("leaves closed pull requests alone and keeps the latest changed", () => {
  expect(advance({ prs: {} }, [entry({ state: "MERGED" })], [], [], 1)).toBeNull();
  const many = {
    prs: Object.fromEntries(
      Array.from({ length: KEEP }, (_, i) => [
        `acme/demo#${i + 100}`,
        { ...start.prs[KEY]!, at: i },
      ]),
    ),
  };
  const next = advance(many, [entry()], [], [], KEEP + 1)!;
  expect(Object.keys(next.prs)).toHaveLength(KEEP);
  expect(next.prs[KEY]).toBeDefined();
  expect(next.prs["acme/demo#100"]).toBeUndefined();
});

it("adds up a group's members", () => {
  expect(sumActivity([{ pushes: 1 }, undefined, { pushes: 2, answers: 1 }])).toEqual({
    pushes: 3,
    answers: 1,
  });
  expect(sumActivity([undefined, {}])).toBeUndefined();
});
