import { expect, it, vi } from "vitest";
import { LocalRequestError, type Entry } from "./api";
import {
  createInboxSaver,
  inboxBadgeCount,
  inboxesOf,
  matches,
  parseInboxes,
  sentence,
  type Inbox,
  type InboxList,
} from "./inboxes";
import type { UpNextKind } from "./up-next";

const inbox = (overrides: Partial<Inbox> = {}): Inbox => ({
  id: "i1",
  name: "Inbox one",
  color: "blue",
  what: "any",
  owners: [],
  risk: "any",
  repositories: [],
  no_bots: false,
  badge: true,
  ...overrides,
});

const item = (
  kind: UpNextKind,
  pr: Partial<Entry["pull_request"]> = {},
  risk: Entry["risk_score"] = null,
) => ({
  kind,
  entry: {
    risk_score: risk,
    pull_request: { repository: "Acme/demo", authored_by_me: false, ...pr },
  } as Entry,
});

it("matches what the pull request waits on", () => {
  const kinds: UpNextKind[] = ["review", "clone", "merge", "fix", "update"];
  const of = (what: Inbox["what"]) => kinds.filter((kind) => matches(inbox({ what }), item(kind)));
  expect(of("any")).toEqual(kinds);
  expect(of("review")).toEqual(["review", "clone"]);
  expect(of("merge")).toEqual(["merge"]);
  expect(of("agent")).toEqual(["fix", "update"]);
  expect(of("mine")).toEqual([]);
  expect(matches(inbox({ what: "mine" }), item("merge", { authored_by_me: true }))).toBe(true);
});

it("matches owners and repositories ignoring case", () => {
  expect(matches(inbox({ owners: ["acme"] }), item("review"))).toBe(true);
  expect(matches(inbox({ owners: ["other", "ACME"] }), item("review"))).toBe(true);
  expect(matches(inbox({ owners: ["acm"] }), item("review"))).toBe(false);
  expect(matches(inbox({ repositories: ["acme/DEMO"] }), item("review"))).toBe(true);
  expect(matches(inbox({ repositories: ["acme/other"] }), item("review"))).toBe(false);
  expect(matches(inbox({ owners: ["acme"], repositories: ["acme/other"] }), item("review"))).toBe(
    false,
  );
});

it("matches a risk threshold only for a pull request that has a score", () => {
  const scored = (score: number | null, status = "assessed") =>
    item("review", {}, { score, status });
  expect(matches(inbox({ risk: "3" }), scored(3))).toBe(true);
  expect(matches(inbox({ risk: "3" }), scored(2))).toBe(false);
  expect(matches(inbox({ risk: "4" }), scored(3))).toBe(false);
  expect(matches(inbox({ risk: "4" }), scored(5))).toBe(true);
  // A lower bound counts as its number.
  expect(matches(inbox({ risk: "4" }), scored(4, "lower_bound"))).toBe(true);
  // Unscored, unreviewed or still generating: never matches a threshold.
  expect(matches(inbox({ risk: "3" }), scored(null, "generating"))).toBe(false);
  expect(matches(inbox({ risk: "3" }), item("review"))).toBe(false);
  expect(matches(inbox({ risk: "any" }), item("review"))).toBe(true);
});

it("leaves out bots when asked", () => {
  expect(matches(inbox({ no_bots: true }), item("merge", { author_bot: true }))).toBe(false);
  expect(matches(inbox({ no_bots: true }), item("merge", { author_bot: false }))).toBe(true);
  expect(matches(inbox({ no_bots: true }), item("merge"))).toBe(true);
  expect(matches(inbox({ no_bots: false }), item("merge", { author_bot: true }))).toBe(true);
});

it("says what an inbox holds", () => {
  expect(sentence(inbox())).toBe("Anything waiting on me in any repository, whatever the risk.");
  expect(sentence(inbox({ what: "review", risk: "3" }))).toBe(
    "Reviews I’m asked for in any repository, when risk is 3 or more.",
  );
  expect(sentence(inbox({ what: "merge", risk: "4" }))).toBe(
    "Things ready to merge in any repository, when risk is 4 or 5.",
  );
  expect(sentence(inbox({ what: "agent", owners: ["baum-ai"] }))).toBe(
    "Things an agent can fix in baum-ai, whatever the risk.",
  );
  expect(sentence(inbox({ what: "mine", owners: ["dan"] }), "Dan")).toBe(
    "My own pull requests in my own repositories, whatever the risk.",
  );
  // Only the signed-in account's alone is "my own".
  expect(sentence(inbox({ owners: ["dan", "acme"] }), "dan")).toBe(
    "Anything waiting on me in dan or acme, whatever the risk.",
  );
  expect(sentence(inbox({ owners: ["a", "b", "c"] }))).toContain("in a, b or c, whatever");
  expect(sentence(inbox({ repositories: ["acme/demo"], no_bots: true }))).toBe(
    "Anything waiting on me in any repository, whatever the risk, only in acme/demo, without bots.",
  );
  expect(sentence(inbox({ repositories: ["a/b", "a/c", "a/d"] }))).toContain(
    ", only in 3 repositories.",
  );
});

it("lists every inbox a pull request is in, in order", () => {
  const list = [
    inbox({ id: "a", what: "review" }),
    inbox({ id: "b", what: "merge" }),
    inbox({ id: "c", owners: ["acme"] }),
  ];
  expect(inboxesOf(item("review"), list).map((i) => i.id)).toEqual(["a", "c"]);
  expect(inboxesOf(item("merge", { repository: "x/y" }), list).map((i) => i.id)).toEqual(["b"]);
});

it("counts in the badge what is in no inbox or in one that counts", () => {
  const items = [
    item("review"),
    item("merge"),
    item("merge", { repository: "x/y" }),
    item("fix", { repository: "z/w" }),
  ];
  const quiet = inbox({ id: "q", what: "merge", badge: false });
  const loud = inbox({ id: "l", owners: ["acme"], badge: true });
  // Today's count with none.
  expect(inboxBadgeCount(items, [])).toBe(4);
  // x/y is only in the quiet inbox; the second merge is also in a counting one.
  expect(inboxBadgeCount(items, [quiet, loud])).toBe(3);
  expect(inboxBadgeCount(items, [{ ...quiet, badge: true }, loud])).toBe(4);
  expect(inboxBadgeCount(items, [{ ...loud, badge: false }, quiet])).toBe(1);
});

it("reads the daemon's list, and nothing from an older daemon", () => {
  const body = { available: true, revision: 4, inboxes: [inbox()] };
  expect(parseInboxes(body)).toEqual({ revision: 4, inboxes: [inbox()] });
  expect(parseInboxes({ available: true, revision: 0, inboxes: [] })).toEqual({
    revision: 0,
    inboxes: [],
  });
  expect(parseInboxes({ available: false })).toBeNull();
  expect(parseInboxes({})).toBeNull();
  expect(parseInboxes(null)).toBeNull();
});

const saver = () => {
  const puts: {
    revision: number;
    inboxes: unknown[];
    resolve: (list: InboxList) => void;
    reject: (error: unknown) => void;
  }[] = [];
  const shown: (InboxList | null)[] = [];
  const failures: string[] = [];
  let current: InboxList | null = null;
  const get = vi.fn(async () => current);
  const s = createInboxSaver({
    put: (revision, inboxes) =>
      new Promise((resolve, reject) => puts.push({ revision, inboxes, resolve, reject })),
    get,
    show: (list) => shown.push(list),
    fail: (message) => failures.push(message),
  });
  return {
    s,
    puts,
    shown,
    failures,
    server: (list: InboxList) => {
      current = list;
    },
  };
};
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

it("sends one save at a time, the later one with the newest revision", async () => {
  const { s, puts, shown } = saver();
  const a = inbox({ id: "a", name: "A" });
  const b = inbox({ id: "b", name: "B" });
  s.adopt({ revision: 1, inboxes: [a, b] });
  s.save([a]);
  s.save([]);
  await settle();
  // The second change waits for the first.
  expect(puts.map((p) => [p.revision, p.inboxes.length])).toEqual([[1, 1]]);
  puts[0]!.resolve({ revision: 2, inboxes: [a] });
  await settle();
  expect(puts.map((p) => [p.revision, p.inboxes.length])).toEqual([
    [1, 1],
    [2, 0],
  ]);
  // The first answer does not bring the removed inbox back on screen.
  expect(shown.at(-1)).toEqual({ revision: 1, inboxes: [] });
  puts[1]!.resolve({ revision: 3, inboxes: [] });
  await settle();
  expect(shown.at(-1)).toEqual({ revision: 3, inboxes: [] });
  // A read made before the saves and landing after them is stale.
  s.adopt({ revision: 1, inboxes: [a, b] });
  expect(shown.at(-1)).toEqual({ revision: 3, inboxes: [] });
});

it("sends only the newest of several waiting changes", async () => {
  const { s, puts } = saver();
  const a = inbox({ id: "a", name: "A" });
  s.adopt({ revision: 1, inboxes: [a] });
  s.save([{ ...a, badge: false }]);
  s.save([{ ...a, badge: true, name: "B" }]);
  s.save([{ ...a, badge: false, name: "C" }]);
  puts[0]!.resolve({ revision: 2, inboxes: [{ ...a, badge: false }] });
  await settle();
  expect(puts).toHaveLength(2);
  expect((puts[1]!.inboxes[0] as Inbox).name).toBe("C");
});

it("adopts the daemon's list on a conflict and restores the saved list on another failure", async () => {
  const one = saver();
  const a = inbox({ id: "a" });
  const elsewhere = { revision: 5, inboxes: [inbox({ id: "z", name: "Z" })] };
  one.s.adopt({ revision: 1, inboxes: [a] });
  one.server(elsewhere);
  one.s.save([]);
  one.puts[0]!.reject(new LocalRequestError("stale", "stale_revision", 409));
  await settle();
  expect(one.shown.at(-1)).toEqual(elsewhere);
  expect(one.failures).toHaveLength(1);
  // The next save goes out on the adopted revision.
  one.s.save([]);
  expect(one.puts[1]!.revision).toBe(5);

  const two = saver();
  two.s.adopt({ revision: 1, inboxes: [a] });
  two.s.save([]);
  expect(two.shown.at(-1)).toEqual({ revision: 1, inboxes: [] });
  two.puts[0]!.reject(new LocalRequestError("Boom", "invalid_inbox", 400));
  await settle();
  expect(two.shown.at(-1)).toEqual({ revision: 1, inboxes: [a] });
  expect(two.failures).toEqual(["Couldn't save your inboxes: Boom"]);
});

it("sends an inbox the daemon does not store without its id", async () => {
  const { s, puts } = saver();
  const a = inbox({ id: "a" });
  const gone = inbox({ id: "gone", name: "Gone" });
  s.adopt({ revision: 2, inboxes: [a] });
  s.save([a, gone]);
  expect(puts[0]!.inboxes[0]).toEqual(a);
  expect("id" in (puts[0]!.inboxes[1] as object)).toBe(false);
});
