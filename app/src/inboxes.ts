import { useRef, useState } from "react";
import { LocalRequestError, request, type Entry } from "./api";
import type { UpNextKind } from "./up-next";

export const INBOX_COLORS = ["blue", "green", "red", "amber", "purple", "grey"] as const;
export type InboxColor = (typeof INBOX_COLORS)[number];
export type InboxWhat = "any" | "review" | "merge" | "agent" | "mine";

/** A saved filter over the queue, as the daemon stores it (see its
 *  `internal/inboxes` README). Matching lives here, not in the daemon. */
export type Inbox = {
  id: string;
  name: string;
  color: InboxColor;
  what: InboxWhat;
  owners: string[];
  risk: "any" | "3" | "4";
  repositories: string[];
  no_bots: boolean;
  badge: boolean;
};
export type InboxList = { revision: number; inboxes: Inbox[] };

const isString = (value: unknown): value is string => typeof value === "string";
const strings = (value: unknown) => (Array.isArray(value) ? value.filter(isString) : []);

/** One entry as the page can rely on it: a missing list is empty and an
 *  unknown colour, `what` or risk is the plain one. Null without a string id
 *  and name, so one malformed entry cannot crash the page. */
function parseInbox(raw: unknown): Inbox | null {
  const value = raw as Record<string, unknown> | null;
  if (!value || typeof value !== "object" || !isString(value.id) || !isString(value.name))
    return null;
  return {
    id: value.id,
    name: value.name,
    color: INBOX_COLORS.includes(value.color as InboxColor) ? (value.color as InboxColor) : "grey",
    what: (["any", "review", "merge", "agent", "mine"] as const).includes(value.what as InboxWhat)
      ? (value.what as InboxWhat)
      : "any",
    owners: strings(value.owners),
    risk: value.risk === "3" || value.risk === "4" ? value.risk : "any",
    repositories: strings(value.repositories),
    no_bots: value.no_bots === true,
    badge: value.badge !== false,
  };
}

/** An older daemon answers 404 or `available: false`: no inboxes at all, not
 *  even an empty list. */
export function parseInboxes(body: unknown): InboxList | null {
  const value = body as { available?: unknown; revision?: unknown; inboxes?: unknown } | null;
  if (!value || value.available === false || !Array.isArray(value.inboxes)) return null;
  return {
    revision: typeof value.revision === "number" ? value.revision : 0,
    inboxes: value.inboxes.flatMap((raw) => parseInbox(raw) ?? []),
  };
}

/** What a read of the inboxes came to: the list, `null` for an older daemon
 *  (404), or a failure, which says nothing about the daemon's version. */
export async function readInboxes(): Promise<InboxList | null | { failed: string }> {
  try {
    return parseInboxes(await request<unknown>("api/inboxes"));
  } catch (e) {
    if (e instanceof LocalRequestError && e.status === 404) return null;
    return {
      failed: `Couldn't read your inboxes: ${e instanceof Error ? e.message : "Local request failed."}`,
    };
  }
}

/** What `matches` needs of a queue item. A pull request an agent is fixing
 *  now is one of kind `fix`. */
export type Matchable = { kind: UpNextKind; entry: Entry };

const WHAT: Record<InboxWhat, (kind: UpNextKind, entry: Entry) => boolean> = {
  any: () => true,
  review: (kind) => kind === "review" || kind === "clone",
  merge: (kind) => kind === "merge",
  agent: (kind) => kind === "fix" || kind === "update",
  mine: (_, entry) => entry.pull_request.authored_by_me,
};

export function matches(inbox: Inbox, { kind, entry }: Matchable): boolean {
  const pr = entry.pull_request;
  if (!(WHAT[inbox.what] ?? WHAT.any)(kind, entry)) return false;
  const repository = pr.repository.toLowerCase();
  const owner = repository.split("/")[0];
  if (inbox.owners.length > 0 && !inbox.owners.some((o) => o.toLowerCase() === owner)) return false;
  if (inbox.risk !== "any") {
    // A pull request with no score yet is never riskier than a threshold.
    const score = entry.risk_score?.score;
    if (typeof score !== "number" || score < Number(inbox.risk)) return false;
  }
  if (
    inbox.repositories.length > 0 &&
    !inbox.repositories.some((r) => r.toLowerCase() === repository)
  )
    return false;
  return !(inbox.no_bots && pr.author_bot);
}

/** Every inbox the item is in, in the sidebar's order. */
export function inboxesOf(item: Matchable, inboxes: Inbox[]): Inbox[] {
  return inboxes.filter((inbox) => matches(inbox, item));
}

/** What the Inbox badge counts: queue items that are in no inbox, or in at
 *  least one that counts in the badge. */
export function inboxBadgeCount(items: Matchable[], inboxes: Inbox[]): number {
  return items.filter((item) => {
    const own = inboxesOf(item, inboxes);
    return own.length === 0 || own.some((inbox) => inbox.badge);
  }).length;
}

const WHAT_WORDS: Record<InboxWhat, string> = {
  any: "anything waiting on me",
  review: "reviews I’m asked for",
  merge: "things ready to merge",
  agent: "things an agent can fix",
  mine: "my own pull requests",
};
const RISK_WORDS = {
  any: "whatever the risk",
  "3": "when risk is 3 or more",
  "4": "when risk is 4 or 5",
};

/** "a", "a or b", "a, b or c". */
function joinOr(parts: string[]) {
  return parts.length < 2
    ? (parts[0] ?? "")
    : `${parts.slice(0, -1).join(", ")} or ${parts[parts.length - 1]}`;
}

/** The three parts of an inbox's sentence, in lower case: what it holds,
 *  where, and at what risk. `login` is the signed-in GitHub account, whose
 *  own repositories read as such. */
export function sentenceParts(inbox: Inbox, login?: string) {
  return {
    what: WHAT_WORDS[inbox.what] ?? WHAT_WORDS.any,
    where:
      inbox.owners.length === 0
        ? "in any repository"
        : inbox.owners.length === 1 &&
            login &&
            inbox.owners[0]!.toLowerCase() === login.toLowerCase()
          ? "in my own repositories"
          : `in ${joinOr(inbox.owners)}`,
    risk: RISK_WORDS[inbox.risk] ?? RISK_WORDS.any,
  };
}

/** What the inbox holds, in a sentence: "Anything waiting on me in any
 *  repository, whatever the risk." */
export function sentence(inbox: Inbox, login?: string): string {
  const { what, where, risk } = sentenceParts(inbox, login);
  const only =
    inbox.repositories.length === 0
      ? ""
      : `, only in ${
          inbox.repositories.length === 1
            ? inbox.repositories[0]
            : `${inbox.repositories.length} repositories`
        }`;
  const text = `${what} ${where}, ${risk}${only}${inbox.no_bots ? ", without bots" : ""}`;
  return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`;
}

export const MAX_INBOXES = 20;
const NAME_LIMIT = 40;

/** An id in the daemon's shape (32 lower-case hex characters), which it keeps,
 *  so a new inbox's page can open before the save is answered. */
export function newInboxId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}

/** Why `name` cannot be saved for the inbox with id `own` (none for a new
 *  one): blank, too long or another inbox's, ignoring case. Empty when fine. */
export function nameProblem(name: string, inboxes: Inbox[], own?: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "Give the inbox a name.";
  if ([...trimmed].length > NAME_LIMIT)
    return `Keep the name to ${NAME_LIMIT} characters or fewer.`;
  if (inboxes.some((one) => one.id !== own && one.name.toLowerCase() === trimmed.toLowerCase()))
    return "Another inbox already has that name.";
  return "";
}

/** `name`, or `name 2`, `name 3`… when an inbox has it. */
export function freeName(name: string, inboxes: Inbox[]): string {
  const taken = new Set(inboxes.map((one) => one.name.toLowerCase()));
  let candidate = name;
  for (let n = 2; taken.has(candidate.toLowerCase()); n++) candidate = `${name} ${n}`;
  return candidate;
}

/** The owners of `repositories` ("owner/name"), without the person's own
 *  login, in alphabetical order, spelled as first met. */
export function ownersOf(repositories: string[], login?: string): string[] {
  const seen = new Map<string, string>();
  for (const repository of repositories) {
    const owner = repository.split("/")[0] ?? "";
    const key = owner.toLowerCase();
    if (owner && key !== login?.toLowerCase() && !seen.has(key)) seen.set(key, owner);
  }
  return [...seen.values()].sort((a, b) => a.localeCompare(b));
}

/** What a new inbox starts from. */
export type Template = Pick<Inbox, "color" | "what" | "risk" | "owners" | "badge"> & {
  name: string;
};

/** The six starting points. `owners` is every owner of the person's
 *  repositories except their login (see `ownersOf`). */
export function templates(owners: string[], login?: string): Template[] {
  const base = { what: "any", risk: "any", owners: [] as string[], badge: true } as const;
  return [
    { ...base, name: "Urgent", color: "red", risk: "4" },
    { ...base, name: "Work", color: "blue", owners },
    { ...base, name: "Personal", color: "green", owners: login ? [login] : [], badge: false },
    { ...base, name: "Needs my review", color: "purple", what: "review" },
    { ...base, name: "Quick merges", color: "amber", what: "merge" },
    { ...base, name: "For an agent", color: "grey", what: "agent" },
  ];
}

/** A template as an inbox of its own, named so no other inbox has the name. */
export function fromTemplate(template: Template, inboxes: Inbox[], id = newInboxId()): Inbox {
  return {
    id,
    ...template,
    name: freeName(template.name, inboxes),
    repositories: [],
    no_bots: false,
  };
}

/** How many of `items` the inbox holds. */
export function countOf(inbox: Inbox, items: Matchable[]): number {
  return items.filter((item) => matches(inbox, item)).length;
}

export type Choice = { key: string; label: string; patch: Partial<Inbox>; on: boolean };

const sameSet = (a: string[], b: string[]) =>
  a.length === b.length && a.every((x) => b.some((y) => y.toLowerCase() === x.toLowerCase()));

/** The options of each part of the sentence, marking the one in force. */
export function choices(
  inbox: Inbox,
  owners: string[],
  login?: string,
): { what: Choice[]; where: Choice[]; risk: Choice[] } {
  const what: Choice[] = (
    [
      ["any", "Anything waiting on me"],
      ["review", "Reviews I’m asked for"],
      ["merge", "Things ready to merge"],
      ["agent", "Things an agent can fix"],
      ["mine", "My own pull requests"],
    ] as const
  ).map(([key, label]) => ({ key, label, patch: { what: key }, on: inbox.what === key }));
  const sets: { key: string; label: string; owners: string[] }[] = [
    { key: "any", label: "Any repository", owners: [] },
    ...(login ? [{ key: "own", label: "My own repositories", owners: [login] }] : []),
    ...owners.map((owner) => ({ key: `owner:${owner}`, label: owner, owners: [owner] })),
  ];
  // Owners the list does not offer: several at once, or one no longer there.
  if (!sets.some((set) => sameSet(set.owners, inbox.owners)))
    sets.push({ key: "set", label: joinOr(inbox.owners), owners: inbox.owners });
  const where: Choice[] = sets.map((set) => ({
    key: set.key,
    label: set.label,
    patch: { owners: set.owners },
    on: sameSet(set.owners, inbox.owners),
  }));
  const risk: Choice[] = (
    [
      ["any", "Any risk"],
      ["3", "Risk 3 or more"],
      ["4", "Risk 4 or 5"],
    ] as const
  ).map(([key, label]) => ({ key, label, patch: { risk: key }, on: inbox.risk === key }));
  return { what, where, risk };
}

type SaverIO = {
  put: (
    revision: number,
    inboxes: (Omit<Inbox, "id"> & { id?: string })[],
  ) => Promise<InboxList | null>;
  get: () => Promise<InboxList | null>;
  /** What the sidebar and the queue show now; null for an older daemon. */
  show: (list: InboxList | null) => void;
  fail: (message: string) => void;
  /** Called after each save the daemon accepted. */
  saved?: () => void;
};

/** Keeps the saved list and sends changes one at a time. A change shows at
 *  once; a PUT carries the whole list, so a later change waits for the
 *  running one and is then sent alone, with the newest revision, which keeps
 *  an older change from landing after a newer one. A conflict adopts the
 *  daemon's list; another failure puts the last saved list back. */
export function createInboxSaver(io: SaverIO) {
  let server: InboxList | null = null;
  let waiting: Inbox[] | null = null;
  let busy = false;
  const errorText = (e: unknown) => (e instanceof Error ? e.message : "Local request failed.");
  const drain = async () => {
    busy = true;
    while (waiting && server) {
      const next = waiting;
      waiting = null;
      try {
        const saved = await io.put(server.revision, next);
        if (!saved) throw new Error("The daemon cannot save inboxes.");
        server = saved;
        io.saved?.();
        if (!waiting) io.show(saved);
      } catch (e) {
        waiting = null;
        if (e instanceof LocalRequestError && e.status === 409) {
          server = (await io.get().catch(() => null)) ?? server;
          io.show(server);
          io.fail("Your inboxes changed somewhere else, so this change was not saved.");
        } else {
          io.show(server);
          io.fail(`Couldn't save your inboxes: ${errorText(e)}`);
        }
      }
    }
    busy = false;
  };
  return {
    /** Takes the list a read returned, unless a save is still in flight. */
    adopt(list: InboxList | null) {
      if (busy || waiting) return;
      // A read that began before a save can land after it.
      if (server && list && list.revision < server.revision) return;
      server = list;
      io.show(list);
    },
    save(inboxes: Inbox[]) {
      if (!server) return;
      io.show({ revision: server.revision, inboxes });
      waiting = inboxes;
      if (!busy) void drain();
    },
  };
}

/** The person's inboxes, `null` while unread or when the daemon is older. */
export function useInboxes() {
  const [list, setList] = useState<InboxList | null>(null);
  const [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [saves, setSaves] = useState(0);
  const saver = useRef<ReturnType<typeof createInboxSaver>>(undefined);
  saver.current ??= createInboxSaver({
    put: (revision, inboxes) =>
      request<unknown>("api/inboxes", { revision, inboxes }, "PUT").then(parseInboxes),
    get: () => request<unknown>("api/inboxes").then(parseInboxes),
    show: setList,
    fail: setError,
    saved: () => setSaves((n) => n + 1),
  });
  const { adopt, save } = saver.current;
  return {
    available: list !== null,
    inboxes: list?.inboxes ?? [],
    error: error || readError,
    /** How many saves the daemon has accepted, for a "Saved" mark. */
    saves,
    /** Takes what a read answered. A failed read keeps the list shown and
     *  the saver's state, and is reported; an older daemon hides the inboxes. */
    read(result: InboxList | null | { failed: string }) {
      if (result && "failed" in result) {
        setReadError(result.failed);
        return;
      }
      setReadError("");
      adopt(result);
    },
    save: (inboxes: Inbox[]) => {
      setError("");
      save(inboxes);
    },
  };
}
