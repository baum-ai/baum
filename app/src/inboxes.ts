import { useEffect, useRef, useState } from "react";
import { LocalRequestError, request, type Entry } from "./api";
import type { QueueKind } from "./up-next";
import { andQuery, matchesQuery, parseQuery, queryProblem, quote } from "./pr-filter";

export const INBOX_COLORS = ["blue", "green", "red", "amber", "purple", "grey"] as const;
export type InboxColor = (typeof INBOX_COLORS)[number];
export function unusedInboxColor(inboxes: Inbox[], preferred: InboxColor = "blue"): InboxColor {
  const used = new Set(inboxes.map((inbox) => inbox.color));
  if (!used.has(preferred)) return preferred;
  return INBOX_COLORS.find((color) => !used.has(color)) ?? preferred;
}

export const COLOR_NAMES: Record<InboxColor, string> = {
  blue: "Blue",
  green: "Green",
  red: "Red",
  amber: "Amber",
  purple: "Purple",
  grey: "Grey",
};
export type InboxWhat = "any" | "review" | "merge" | "agent" | "mine";

/** A saved filter over the queue, as the daemon stores it (see its
 *  `internal/inboxes` README). The daemon also matches it (`Held`). */
export type Inbox = {
  id: string;
  name: string;
  color: InboxColor;
  what: InboxWhat;
  owners: string[];
  risk: "any" | "3" | "4";
  repositories: string[];
  /** A filter query on top of the rest (see `pr-filter.ts`); "" for none. */
  query: string;
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
  // Its parts read as one query from here on, and save so.
  return folded({
    id: value.id,
    name: value.name,
    color: INBOX_COLORS.includes(value.color as InboxColor) ? (value.color as InboxColor) : "grey",
    what: (["any", "review", "merge", "agent", "mine"] as const).includes(value.what as InboxWhat)
      ? (value.what as InboxWhat)
      : "any",
    owners: strings(value.owners),
    risk: value.risk === "3" || value.risk === "4" ? value.risk : "any",
    repositories: strings(value.repositories),
    query: typeof value.query === "string" ? value.query : "",
    no_bots: value.no_bots === true,
    badge: value.badge !== false,
  });
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
export async function readInboxes(
  signal?: AbortSignal,
): Promise<InboxList | null | { failed: string }> {
  try {
    return parseInboxes(await request<unknown>("api/inboxes", undefined, "GET", signal));
  } catch (e) {
    if (e instanceof LocalRequestError && e.status === 404) return null;
    return {
      failed: `Couldn't read your inboxes: ${e instanceof Error ? e.message : "Local request failed."}`,
    };
  }
}

/** What `matches` needs of a pull request: a queue item, `other` for someone
 *  else's that waits on nothing from the person, or without `kind` one of
 *  the person's own that waits on others. A pull request an agent is fixing
 *  now is one of kind `fix`. */
export type Matchable = { kind?: QueueKind; entry: Entry };

/** The default inbox's query, as the daemon creates it: what the person
 *  wrote, is assigned or was asked to review. */
export const FOR_ME = "author=@me OR assignee=@me OR review-requested=@me";

/** Quick filters on an inbox's edit page: each puts its query into the
 *  inbox's (or takes it out again), as the person could type it. */
export const QUICK_FILTERS: { label: string; query: string }[] = [
  { label: "For me", query: FOR_ME },
  { label: "Risky", query: "risk>=4" },
  { label: "Unclaimed", query: "assignee=none review-requested=none" },
  { label: "No drafts", query: "is!=draft" },
  { label: "No bots", query: "is!=bot" },
];

/** An inbox's parts as one query: what it holds (`waiting=` or, for the
 *  person's own, `author=@me`), owners (`org=`), risk (`risk>=`),
 *  repositories (`repo=`) and bots (`is!=bot`), then its own query. */
export function foldedQuery(inbox: Inbox): string {
  const parts = [
    inbox.what === "mine"
      ? "author=@me"
      : inbox.what !== "any"
        ? `waiting=${inbox.what === "agent" ? "agent" : inbox.what}`
        : "",
    inbox.owners.length ? `org=${inbox.owners.map(quote).join(",")}` : "",
    inbox.risk !== "any" ? `risk>=${inbox.risk}` : "",
    inbox.repositories.length ? `repo=${inbox.repositories.map(quote).join(",")}` : "",
    inbox.no_bots ? "is!=bot" : "",
  ].filter(Boolean);
  // A query that does not read narrows nothing, and must not undo the parts.
  return andQuery(parts.join(" "), parseQuery(inbox.query).error ? "" : inbox.query);
}

/** The inbox with its parts folded into its query, which alone then says
 *  what it holds. One whose parts would not fit the daemon's limit, or whose
 *  query does not read, stays as it is and still matches by `foldedQuery`. */
export function folded(inbox: Inbox): Inbox {
  const query = foldedQuery(inbox);
  if (query === inbox.query || queryProblem(query) || queryProblem(inbox.query)) return inbox;
  return {
    ...inbox,
    what: "any",
    owners: [],
    risk: "any",
    repositories: [],
    no_bots: false,
    query,
  };
}

/** The inbox holding only `query`, its parts cleared: the query says it all. */
export function withQuery(inbox: Inbox, query: string): Inbox {
  return {
    ...inbox,
    what: "any",
    owners: [],
    risk: "any",
    repositories: [],
    no_bots: false,
    query,
  };
}

/** What the daemon answered about which pull requests queries hold
 *  (`GET api/inbox`, see its `internal/inboxqueue` README): each pull
 *  request's kind as it matched it, by key, and the keys each query holds.
 *  The daemon matches, so the page does not run every inbox's query over
 *  every pull request. */
export type Held = {
  kinds: ReadonlyMap<string, string>;
  byQuery: ReadonlyMap<string, ReadonlySet<string>>;
};

/** The daemon's queue as `GET api/inbox` answers it: null for an older
 *  daemon or an answer that does not read, and no queries when it could not
 *  read the saved inboxes. */
export function parseHeld(body: unknown): Held | null {
  const value = body as { items?: unknown; inboxes?: unknown } | null;
  if (!value || typeof value !== "object" || !Array.isArray(value.items)) return null;
  const kinds = new Map<string, string>();
  const ids = new Map<string, Set<string>>();
  for (const raw of value.items) {
    const item = raw as { key?: unknown; kind?: unknown; inboxes?: unknown } | null;
    if (!item || !isString(item.key) || !isString(item.kind)) continue;
    kinds.set(item.key, item.kind);
    for (const id of strings(item.inboxes)) {
      let keys = ids.get(id);
      if (!keys) ids.set(id, (keys = new Set()));
      keys.add(item.key);
    }
  }
  const byQuery = new Map<string, Set<string>>();
  for (const raw of Array.isArray(value.inboxes) ? value.inboxes : []) {
    const inbox = raw as { id?: unknown; query?: unknown } | null;
    if (inbox && isString(inbox.id) && isString(inbox.query))
      byQuery.set(inbox.query, ids.get(inbox.id) ?? new Set());
  }
  return { kinds, byQuery };
}

/** Whether `query` holds the item. The daemon's answer decides when it has
 *  one for the query and matched the pull request as the same kind; else
 *  (an older daemon, a query not saved or not asked yet, a read in between)
 *  the page matches it itself. */
export function holds(held: Held | null | undefined, query: string, item: Matchable): boolean {
  const keys = held?.byQuery.get(query);
  const pr = item.entry.pull_request;
  // As `upNextKey` and the daemon name it.
  const key = `${pr.repository.toLowerCase()}#${pr.number}`;
  if (keys && held!.kinds.get(key) === (item.kind ?? "")) return keys.has(key);
  return matchesQuery(query, item.entry, item.kind);
}

/** `held` with the daemon's answers for queries not saved, as `useHeld`
 *  read them. */
function withAnswers(held: Held, answers: ReadonlyMap<string, ReadonlySet<string>>): Held {
  if (answers.size === 0) return held;
  return { kinds: held.kinds, byQuery: new Map([...answers, ...held.byQuery]) };
}

/** `held`, with the daemon also asked which pull requests each of `queries`
 *  holds, for those it has no answer for: a filter being typed, or an inbox
 *  not saved yet. Each is asked a moment after it last changed, and again
 *  whenever `held` is read again; until it answers the page matches. */
export function useHeld(held: Held | null, queries: readonly string[]): Held | null {
  const [answers, setAnswers] = useState<{
    from: Held | null;
    byQuery: Map<string, ReadonlySet<string>>;
  }>({ from: null, byQuery: new Map() });
  const wanted = [...new Set(queries)].filter((query) => held && !held.byQuery.has(query));
  const signature = wanted.join("\n");
  useEffect(() => {
    if (!held || wanted.length === 0) return;
    const abort = new AbortController();
    const timer = setTimeout(() => {
      void Promise.all(
        wanted.map((query) =>
          request<unknown>(
            `api/inbox?query=${encodeURIComponent(query)}`,
            undefined,
            "GET",
            abort.signal,
          )
            .then((body) => {
              const matches = (body as { matches?: unknown } | null)?.matches;
              return Array.isArray(matches) ? ([query, new Set(strings(matches))] as const) : null;
            })
            .catch(() => null),
        ),
      ).then((read) => {
        if (abort.signal.aborted) return;
        setAnswers({
          from: held,
          byQuery: new Map(read.flatMap((one) => (one ? [one] : []))),
        });
      });
    }, 150);
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [held, signature]);
  if (!held) return null;
  // Answers about an older read of the queue no longer hold.
  return answers.from === held ? withAnswers(held, answers.byQuery) : held;
}

export function matches(inbox: Inbox, item: Matchable, held?: Held | null): boolean {
  return holds(held, foldedQuery(inbox), item);
}

/** Every inbox the item is in, in the sidebar's order. */
export function inboxesOf(item: Matchable, inboxes: Inbox[], held?: Held | null): Inbox[] {
  return inboxes.filter((inbox) => matches(inbox, item, held));
}

const WHAT_WORDS: Record<InboxWhat, string> = {
  any: "every open pull request",
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
function sentenceParts(inbox: Inbox, login?: string) {
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

/** What the inbox holds, in a sentence: "Every open pull request in any
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
  /** What its tile says, in place of the sentence its parts make. */
  description?: string;
  /** A query on top of its parts. */
  query?: string;
};

/** The seven starting points. `owners` is every owner of the person's
 *  repositories except their login (see `ownersOf`). Custom starts with no
 *  filter, so it holds every open pull request. */
export function templates(owners: string[], login?: string): Template[] {
  const base = { what: "any", risk: "any", owners: [] as string[], badge: true } as const;
  return [
    {
      ...base,
      name: "Custom",
      color: "blue",
      description: "Every open pull request, until you add filters.",
    },
    {
      ...base,
      name: "Urgent",
      color: "red",
      risk: "4",
      query: FOR_ME,
      description: "Mine, assigned to me or asking for my review, at risk 4 or 5.",
    },
    {
      ...base,
      name: "Work",
      color: "blue",
      owners,
      query: FOR_ME,
      description: `Mine, assigned to me or asking for my review, ${owners.length ? `in ${joinOr(owners)}` : "in any repository"}.`,
    },
    { ...base, name: "Personal", color: "green", owners: login ? [login] : [], badge: false },
    { ...base, name: "Quick merges", color: "amber", what: "merge" },
    { ...base, name: "For an agent", color: "grey", what: "agent" },
    {
      ...base,
      name: "Unclaimed",
      color: "purple",
      query: "waiting=other assignee=none review-requested=none is!=draft is!=bot",
      description: "Others' pull requests with no one assigned and no review requested.",
    },
  ];
}

/** A template as an inbox of its parts, as its tile describes it. */
export function templateInbox(template: Template, inboxes: Inbox[], id = newInboxId()): Inbox {
  const { description: _, query = "", ...parts } = template;
  return {
    id,
    ...parts,
    name: freeName(template.name, inboxes),
    repositories: [],
    query,
    no_bots: false,
  };
}

/** A template as an inbox of its own, named so no other inbox has the name,
 *  its parts one query. */
export function fromTemplate(template: Template, inboxes: Inbox[], id = newInboxId()): Inbox {
  return folded({
    ...templateInbox(template, inboxes, id),
    color: unusedInboxColor(inboxes, template.color),
  });
}

/** How many of `items` the inbox holds. */
export function countOf(inbox: Inbox, items: Matchable[], held?: Held | null): number {
  return items.filter((item) => matches(inbox, item, held)).length;
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

/** Whether the first read has answered: `loading` before it, `ready` with a
 *  list, `older` for a daemon without inboxes (404 or `available: false`) and
 *  `failed` for a first read that failed some other way. */
export type InboxesStatus = "loading" | "ready" | "older" | "failed";

/** The person's inboxes, `null` while unread or when the daemon is older. */
export function useInboxes() {
  const [list, setList] = useState<InboxList | null>(null);
  const [status, setStatus] = useState<InboxesStatus>("loading");
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
    status,
    inboxes: list?.inboxes ?? [],
    error: error || readError,
    /** How many saves the daemon has accepted, for a "Saved" mark. */
    saves,
    /** Takes what a read answered. A failed read keeps the list shown and
     *  the saver's state, and is reported; an older daemon hides the inboxes. */
    read(result: InboxList | null | { failed: string }) {
      if (result && "failed" in result) {
        setReadError(result.failed);
        // Once a read has answered, a later failure keeps what it said.
        setStatus((s) => (s === "loading" || s === "failed" ? "failed" : s));
        return;
      }
      setReadError("");
      setStatus(result ? "ready" : "older");
      adopt(result);
    },
    save: (inboxes: Inbox[]) => {
      setError("");
      save(inboxes);
    },
  };
}
