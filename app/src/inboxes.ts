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

/** What the inbox holds, in a sentence: "Anything waiting on me in any
 *  repository, whatever the risk." `login` is the signed-in GitHub account,
 *  whose own repositories read as such. */
export function sentence(inbox: Inbox, login?: string): string {
  const where =
    inbox.owners.length === 0
      ? "in any repository"
      : inbox.owners.length === 1 && login && inbox.owners[0]!.toLowerCase() === login.toLowerCase()
        ? "in my own repositories"
        : `in ${joinOr(inbox.owners)}`;
  const only =
    inbox.repositories.length === 0
      ? ""
      : `, only in ${
          inbox.repositories.length === 1
            ? inbox.repositories[0]
            : `${inbox.repositories.length} repositories`
        }`;
  const text = `${WHAT_WORDS[inbox.what] ?? WHAT_WORDS.any} ${where}, ${RISK_WORDS[inbox.risk] ?? RISK_WORDS.any}${only}${inbox.no_bots ? ", without bots" : ""}`;
  return `${text.charAt(0).toUpperCase()}${text.slice(1)}.`;
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
  const saver = useRef<ReturnType<typeof createInboxSaver>>(undefined);
  saver.current ??= createInboxSaver({
    put: (revision, inboxes) =>
      request<unknown>("api/inboxes", { revision, inboxes }, "PUT").then(parseInboxes),
    get: () => request<unknown>("api/inboxes").then(parseInboxes),
    show: setList,
    fail: setError,
  });
  const { adopt, save } = saver.current;
  return {
    available: list !== null,
    inboxes: list?.inboxes ?? [],
    error: error || readError,
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
