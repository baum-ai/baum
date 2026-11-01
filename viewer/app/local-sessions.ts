import { harnessName } from "../src/agent-names";
import type {
  CommitDetailView,
  CommitFileView,
  CommitListState,
  CommitLoaders,
  CommitView,
} from "../src/components/commit-list";
import type { SessionListState, SessionView } from "../src/components/session-list";
import type { ReviewDocument } from "../src/review-document";

/** What recorded the sessions to fall back on: a review, or a pull
 *  request's facts. */
type SessionSource = { index: Pick<ReviewDocument["index"], "sessions"> };

/** What `baum internal view` knows about this pull request's sessions and commits on
 *  the user's machine: the answer of `GET /api/sessions`, checked. */

export type LocalSession = {
  id: string;
  vendor: string;
  title?: string;
  branch?: string;
  startedAt?: string;
  endedAt?: string;
  outcome?: string;
  editedFiles?: number;
};

export type LocalCommit = {
  oid: string;
  subject: string;
  author?: string;
  committedAt?: string;
  links: { session: string; method: string; confidence: number }[];
};

export type SessionsAnswer =
  | { available: true; sessions: LocalSession[]; commits: LocalCommit[] }
  | { available: false; reason: string };

/** Where the request stands: not answered yet, answered, or failed (it did not
 *  complete, or what it returned is not an answer). */
export type SessionsLoad =
  | { state: "loading" }
  | { state: "answered"; answer: SessionsAnswer }
  | { state: "request-failed" };

const MAX_REASON = 400;
const REQUEST_FAILED = "the sessions on this machine could not be read";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Whether a field that may be left out is, when present, a string. */
function isOptionalText(value: unknown): boolean {
  return value === undefined || value === null || typeof value === "string";
}

/** An optional string field's text, an empty one counting as absent. */
function text(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function parseSession(value: unknown): LocalSession | null {
  if (!isRecord(value) || typeof value.id !== "string" || value.id === "") return null;
  const optional = [
    value.vendor,
    value.source,
    value.title,
    value.branch,
    value.started_at,
    value.ended_at,
    value.outcome,
  ];
  if (!optional.every(isOptionalText)) return null;
  const edited = value.edited_files;
  if (edited !== undefined && edited !== null && typeof edited !== "number") return null;
  return {
    id: value.id,
    vendor: text(value.vendor) ?? text(value.source) ?? "",
    title: text(value.title),
    branch: text(value.branch),
    startedAt: text(value.started_at),
    endedAt: text(value.ended_at),
    outcome: text(value.outcome),
    editedFiles: typeof edited === "number" ? edited : undefined,
  };
}

function parseCommit(value: unknown): LocalCommit | null {
  if (!isRecord(value) || typeof value.oid !== "string" || value.oid === "") return null;
  if (typeof value.subject !== "string") return null;
  if (!isOptionalText(value.author) || !isOptionalText(value.committed_at)) return null;
  const rawLinks = value.links ?? [];
  if (!Array.isArray(rawLinks)) return null;
  const links: LocalCommit["links"] = [];
  for (const link of rawLinks) {
    if (
      !isRecord(link) ||
      typeof link.session !== "string" ||
      typeof link.method !== "string" ||
      typeof link.confidence !== "number" ||
      !Number.isFinite(link.confidence)
    ) {
      return null;
    }
    links.push({ session: link.session, method: link.method, confidence: link.confidence });
  }
  return {
    oid: value.oid,
    subject: value.subject,
    author: text(value.author),
    committedAt: text(value.committed_at),
    links,
  };
}

/** A count of lines: null when left out, undefined when it is not a count. */
function lineCount(n: unknown): number | null | undefined {
  if (n === undefined || n === null) return null;
  return typeof n === "number" && Number.isInteger(n) && n >= 0 ? n : undefined;
}

function parseCommitFile(value: unknown): CommitFileView | null {
  if (!isRecord(value) || typeof value.path !== "string" || value.path === "") return null;
  if (typeof value.status !== "string" || !isOptionalText(value.old_path)) return null;
  const additions = lineCount(value.additions);
  const deletions = lineCount(value.deletions);
  if (additions === undefined || deletions === undefined) return null;
  // A binary file has neither count.
  const binary = additions === null || deletions === null;
  return {
    path: value.path,
    oldPath: text(value.old_path) ?? null,
    status: value.status,
    additions: binary ? null : additions,
    deletions: binary ? null : deletions,
  };
}

/** The detail in a `GET /api/commit` response body: the commit's message
 *  body and changed files. Null when the daemon could not read the commit
 *  (it answers `"files": null` and a `code`) or the body is not an answer. */
export function parseCommitDetail(body: unknown): CommitDetailView | null {
  if (!isRecord(body) || !Array.isArray(body.files) || !isOptionalText(body.body)) return null;
  const files = body.files.map(parseCommitFile);
  if (files.includes(null)) return null;
  return {
    body: typeof body.body === "string" ? body.body : "",
    files: firsts(files as CommitFileView[], (file) => file.path),
  };
}

/** Reads one of the daemon's commit routes, `GET /api/commit` or
 *  `GET /api/commit-patch`, with `params` added to the host's own query.
 *  It resolves to the response body, to undefined for a 404, and rejects on
 *  any other failure. */
export type CommitRouteReader = (
  route: "commit" | "commit-patch",
  params: Record<string, string>,
) => Promise<unknown>;

/** The answer cached under key, else start's, kept while it holds: a
 *  failed read is forgotten, so the next asks again. */
function once<T>(cache: Map<string, Promise<T>>, key: string, start: () => Promise<T>) {
  let pending = cache.get(key);
  if (!pending) {
    const started = start();
    started.catch(() => {
      if (cache.get(key) === started) cache.delete(key);
    });
    cache.set(key, started);
    pending = started;
  }
  return pending;
}

/** `commitsSlot`'s loaders over a host's reader. Each commit and each file is
 *  read once; a failed read is forgotten, so a retry asks again. A commit or
 *  file the daemon does not answer for (404) has no detail or no diff. */
export function createCommitLoaders(read: CommitRouteReader): Required<CommitLoaders> {
  const commits = new Map<string, Promise<CommitDetailView | null>>();
  const patches = new Map<string, Promise<string | null>>();
  return {
    loadCommit: (oid) => once(commits, oid, () => read("commit", { oid }).then(parseCommitDetail)),
    loadPatch: (oid, path) =>
      once(patches, `${oid}\n${path}`, () =>
        read("commit-patch", { oid, path }).then((body) =>
          isRecord(body) && typeof body.patch === "string" && body.patch !== "" ? body.patch : null,
        ),
      ),
  };
}

/** The first of each key: an id or oid listed twice would repeat a React key. */
function firsts<T>(items: T[], key: (item: T) => string): T[] {
  const seen = new Set<string>();
  return items.filter((item) => !seen.has(key(item)) && seen.add(key(item)));
}

/** The answer in a response body, or null when it is not one: the wrong
 *  shape or a field of the wrong type anywhere in it. Fields it does not know
 *  are ignored. */
export function parseSessionsAnswer(body: unknown): SessionsAnswer | null {
  if (!isRecord(body)) return null;
  if (body.available === false) {
    const reason = typeof body.reason === "string" ? body.reason.trim() : "";
    return {
      available: false,
      reason: reason
        ? reason.slice(0, MAX_REASON)
        : "the sessions on this machine are not available",
    };
  }
  if (body.available !== true || !Array.isArray(body.sessions) || !Array.isArray(body.commits)) {
    return null;
  }
  const sessions = body.sessions.map(parseSession);
  const commits = body.commits.map(parseCommit);
  if (sessions.includes(null) || commits.includes(null)) return null;
  return {
    available: true,
    sessions: firsts(sessions as LocalSession[], (session) => session.id),
    commits: firsts(commits as LocalCommit[], (commit) => commit.oid),
  };
}

/** Reads `/api/sessions`. A request that fails, answers with another status or
 *  returns something that is not an answer resolves to null; it is never
 *  thrown, except for the abort that ends it. */
export async function fetchSessions(signal: AbortSignal): Promise<SessionsAnswer | null> {
  try {
    const response = await fetch("/api/sessions", { cache: "no-store", signal });
    if (!response.ok) return null;
    return parseSessionsAnswer(await response.json());
  } catch {
    if (signal.aborted) throw new DOMException("Aborted", "AbortError");
    return null;
  }
}

function localLabel(session: LocalSession): string {
  return session.title || harnessName(session.vendor);
}

function localSessionViews(sessions: LocalSession[], commits: LocalCommit[]): SessionView[] {
  return sessions.map((session) => ({
    id: session.id,
    vendor: session.vendor,
    title: session.title,
    branch: session.branch,
    startedAt: session.startedAt,
    endedAt: session.endedAt,
    outcome: session.outcome,
    editedFiles: session.editedFiles,
    commits: commits
      .filter((commit) => commit.links.some((link) => link.session === session.id))
      .map((commit) => ({ id: commit.oid, subject: commit.subject })),
  }));
}

function localCommitViews(sessions: LocalSession[], commits: LocalCommit[]): CommitView[] {
  const known = new Map(sessions.map((session) => [session.id, session]));
  return commits.map((commit) => ({
    id: commit.oid,
    subject: commit.subject,
    author: commit.author,
    committedAt: commit.committedAt,
    // A link to a session the list lacks would open nothing.
    links: commit.links
      .filter((link) => known.has(link.session))
      .map((link) => ({
        session: link.session,
        label: localLabel(known.get(link.session)!),
        method: link.method,
        confidence: link.confidence,
      })),
  }));
}

/** The sessions the review document (or the facts) recorded, as the pane lists them. */
function documentSessionViews(document: SessionSource): SessionView[] | null {
  const recorded = document.index?.sessions;
  if (!Array.isArray(recorded)) return null;
  return recorded
    .filter((session) => typeof session?.id === "string")
    .map((session) => ({
      id: session.id,
      vendor: typeof session.vendor === "string" ? session.vendor : "",
      // A restricted session carries no title the reader may see; say so.
      title: session.restricted ? "Details restricted" : session.title,
      branch: session.restricted ? null : session.git_branch,
      startedAt: session.started_at,
      endedAt: session.ended_at,
      outcome: session.outcome,
    }));
}

/** What the Sessions and Commits groups hold. With the sessions on this
 *  machine, both; without them, the sessions the review recorded and no
 *  Commits group, and a note saying why. A request that failed with nothing
 *  in the review to show instead is the only failure. */
export function sessionLists(
  load: SessionsLoad,
  document: SessionSource,
): { sessions: SessionListState; commits: CommitListState | null } {
  if (load.state === "loading") {
    return { sessions: { status: "loading" }, commits: { status: "loading" } };
  }
  if (load.state === "answered" && load.answer.available) {
    const { sessions, commits } = load.answer;
    return {
      sessions: { status: "ready", sessions: localSessionViews(sessions, commits) },
      commits: { status: "ready", commits: localCommitViews(sessions, commits) },
    };
  }
  const recorded = documentSessionViews(document);
  const reason = load.state === "answered" && !load.answer.available ? load.answer.reason : null;
  if (reason === null && (recorded === null || recorded.length === 0)) {
    return { sessions: { status: "failed" }, commits: null };
  }
  return {
    sessions: {
      status: "ready",
      sessions: recorded ?? [],
      note: `Showing the sessions recorded in the review — ${reason ?? REQUEST_FAILED}`,
    },
    commits: null,
  };
}
