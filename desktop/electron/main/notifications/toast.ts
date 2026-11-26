// What a daemon event says, as the notification the user sees. Everything
// here is a pure function of the event: the text comes only from fields the
// daemon already redacted (the repository, the title as GitHub returned it,
// the stored risk score, a failure category, an agent session's branch and
// the masked line its agent showed) and is only stripped of control
// characters and bounded in length, never reworded.

/** A kind of notification, the unit of the per-kind settings. The names are
 *  the keys of desktop.json's `notifications.kinds`. */
export type Kind =
  | "finished"
  | "failed"
  | "requested"
  | "automatic"
  // The user's own pull request: its checks began failing.
  | "checks_failed"
  // ... it began conflicting with its base.
  | "conflict"
  // ... a review requested changes.
  | "changes_requested"
  // ... GitHub would merge it now.
  | "ready_to_merge"
  // A fix pushed a commit to the pull request's branch.
  | "fix_pushed"
  | "fix_failed"
  // An agent session asks to be allowed to run a tool.
  | "session_permission"
  // ... asks the user a question.
  | "session_input";

export const KINDS: readonly Kind[] = [
  "finished",
  "failed",
  "requested",
  "automatic",
  "checks_failed",
  "conflict",
  "changes_requested",
  "ready_to_merge",
  "fix_pushed",
  "fix_failed",
  "session_permission",
  "session_input",
];

const LABELS: Record<Kind, string> = {
  finished: "Review finished",
  failed: "Review failed",
  requested: "Review requested from me",
  automatic: "Review generated automatically",
  checks_failed: "My pull request: checks failing",
  conflict: "My pull request: merge conflict",
  changes_requested: "My pull request: changes requested",
  ready_to_merge: "My pull request: ready to merge",
  fix_pushed: "Fix pushed",
  fix_failed: "Fix failed",
  session_permission: "Agent needs my permission",
  session_input: "Agent waiting for my answer",
};

/** The menu entry's label. */
export function label(kind: Kind): string {
  return LABELS[kind];
}

/** The menu entry's identifier. */
export function menuId(kind: Kind): string {
  return `notifications:${kind}`;
}

export function fromMenuId(id: string): Kind | undefined {
  return KINDS.find((kind) => menuId(kind) === id);
}

/** Where a click opens the app. */
export type Route =
  // A pull request's page in the clone the daemon named.
  | { type: "pr"; clone: string; number: number }
  // A picked repository that has no clone yet.
  | { type: "repository_pr"; repository: string; number: number }
  // The jobs list.
  | { type: "jobs" }
  // One job's page.
  | { type: "job"; id: string }
  // The diagnostics page.
  | { type: "diagnostics" }
  // An agent session's transcript, by its `agent:id` key.
  | { type: "session"; key: string };

/** The URL under the daemon's app path (`base`, which ends in a slash). */
export function routeUrl(route: Route, base: URL): URL {
  const url = new URL(base);
  url.hash = "";
  const query = new URLSearchParams();
  switch (route.type) {
    case "pr":
      query.append("clone", route.clone);
      query.append("pr", String(route.number));
      break;
    case "repository_pr":
      query.append("repository", route.repository);
      query.append("pr", String(route.number));
      break;
    case "jobs":
      query.append("page", "jobs");
      break;
    case "job":
      query.append("page", "jobs");
      query.append("job", route.id);
      break;
    case "diagnostics":
      query.append("page", "diagnostics");
      break;
    case "session":
      query.append("page", "sessions");
      query.append("session", route.key);
      break;
  }
  url.search = query.toString();
  return url;
}

/** The pull request this route shows, if it is one. */
export function routePr(route: Route): [string, number] | undefined {
  switch (route.type) {
    case "pr":
      return [route.clone, route.number];
    case "repository_pr":
      return [route.repository, route.number];
    default:
      return undefined;
  }
}

/** The agent session this route shows, if it is one. */
export function routeSession(route: Route): string | undefined {
  return route.type === "session" ? route.key : undefined;
}

export interface Toast {
  kind: Kind;
  title: string;
  body: string;
  route: Route;
}

/** The daemon's `notice` event, validated field by field the way a typed
 *  deserialiser would: a field of the wrong type makes the event unreadable. */
interface Notice {
  kind: string;
  repository: string;
  pr: number;
  clone?: string;
  title?: string;
  risk?: number;
  riskStatus?: string;
  reason?: string;
  commit?: string;
  agent: string;
  session: string;
  branch?: string;
  message?: string;
}

export const MAX_TITLE = 200;
const MAX_FIELD = 120;

function parseNotice(value: unknown): Notice | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const o = value as Record<string, unknown>;
  let valid = true;
  const optional = (key: string): string | undefined => {
    const v = o[key];
    if (v === undefined || v === null) return undefined;
    if (typeof v === "string") return v;
    valid = false;
    return undefined;
  };
  const text = (key: string): string => {
    const v = o[key];
    if (v === undefined) return "";
    if (typeof v === "string") return v;
    valid = false;
    return "";
  };
  const integer = (key: string, max: number): number | undefined => {
    const v = o[key];
    if (v === undefined || v === null) return undefined;
    if (typeof v === "number" && Number.isInteger(v) && v >= 0 && v <= max) return v;
    valid = false;
    return undefined;
  };
  const kind = o.kind;
  if (typeof kind !== "string") return undefined;
  const notice: Notice = {
    kind,
    repository: text("repository"),
    pr: integer("pr", Number.MAX_SAFE_INTEGER) ?? 0,
    clone: optional("clone"),
    title: optional("title"),
    risk: integer("risk", 255),
    riskStatus: optional("risk_status"),
    reason: optional("reason"),
    commit: optional("commit"),
    agent: text("agent"),
    session: text("session"),
    branch: optional("branch"),
    message: optional("message"),
  };
  return valid ? notice : undefined;
}

/** The notification an event calls for, if any. Unknown events, kinds and
 *  malformed payloads call for none. `data` is the event's JSON text or its
 *  already parsed object. */
export function fromEvent(name: string, data: string | object): Toast | undefined {
  if (name !== "notice") return undefined;
  let parsed: unknown = data;
  if (typeof data === "string") {
    try {
      parsed = JSON.parse(data);
    } catch {
      return undefined;
    }
  }
  const notice = parseNotice(parsed);
  return notice && fromNotice(notice);
}

function kindOf(notice: string): Kind | undefined {
  switch (notice) {
    case "review_finished":
      return "finished";
    // A retry of a review's failed section details is a review failure in
    // the settings, with its own words.
    case "review_failed":
    case "details_failed":
      return "failed";
    case "review_requested":
      return "requested";
    case "review_automatic":
      return "automatic";
    case "checks_failed":
    case "conflict":
    case "changes_requested":
    case "ready_to_merge":
    case "fix_pushed":
    case "fix_failed":
    case "session_permission":
    case "session_input":
      return notice;
    default:
      return undefined;
  }
}

function headlineOf(kind: Kind, detailsFailed: boolean): string {
  switch (kind) {
    case "finished":
      return "Review finished";
    case "automatic":
      return "Review generated automatically";
    case "failed":
      return detailsFailed ? "Couldn't write the missing section details" : "Review failed";
    case "requested":
      return "Review requested from you";
    case "checks_failed":
      return "Checks failing";
    case "conflict":
      return "Merge conflict";
    case "changes_requested":
      return "Changes requested";
    case "ready_to_merge":
      return "Ready to merge";
    case "fix_pushed":
      return "Fix pushed";
    case "fix_failed":
      return "Fix failed";
    case "session_permission":
    case "session_input":
      throw new Error("a session notice is read by fromSessionNotice");
  }
}

function fromNotice(n: Notice): Toast | undefined {
  const kind = kindOf(n.kind);
  if (kind === undefined) return undefined;
  if (kind === "session_permission" || kind === "session_input") {
    return fromSessionNotice(kind, n);
  }
  if (n.pr === 0) return undefined;
  const repository = clean(n.repository, MAX_FIELD);
  let risk: string | undefined;
  if (n.risk !== undefined && n.risk >= 1 && n.risk <= 5) {
    risk = n.riskStatus === "lower_bound" ? `risk at least ${n.risk} of 5` : `risk ${n.risk} of 5`;
  }
  const headline = headlineOf(kind, n.kind === "details_failed");
  const reason = n.reason ? clean(n.reason.replace(/[_-]/g, " "), 60) : undefined;
  const reference = `${repository} #${n.pr}`;
  // News about the user's own pull request or a fix leads with what
  // happened and where; its body is the pull request's title. A review's
  // title keeps the outcome and puts the reference under the pull request's
  // title.
  const leadsWithReference =
    kind === "checks_failed" ||
    kind === "conflict" ||
    kind === "changes_requested" ||
    kind === "ready_to_merge" ||
    kind === "fix_pushed" ||
    kind === "fix_failed";
  let title: string;
  if ((kind === "finished" || kind === "automatic") && risk !== undefined) {
    title = `${headline}: ${risk}`;
  } else if (kind === "failed" && reason !== undefined) {
    title = `${headline}: ${reason}`;
  } else if (kind === "fix_pushed") {
    const commit = n.commit === undefined ? undefined : clean(n.commit, 12);
    title = commit ? `${headline} (${commit}) · ${reference}` : `${headline} · ${reference}`;
  } else if (kind === "fix_failed" && reason !== undefined) {
    title = `${headline}: ${reason} · ${reference}`;
  } else if (leadsWithReference) {
    title = `${headline} · ${reference}`;
  } else {
    title = headline;
  }
  const prTitle = n.title === undefined ? undefined : clean(n.title, MAX_TITLE);
  let body: string;
  if (prTitle && leadsWithReference) body = prTitle;
  else if (prTitle) body = `${prTitle}\n${reference}`;
  else body = reference;
  let route: Route;
  if (n.clone) route = { type: "pr", clone: n.clone, number: n.pr };
  else if (n.repository !== "") {
    route = { type: "repository_pr", repository: n.repository, number: n.pr };
  } else route = { type: "jobs" };
  return { kind, title, body, route };
}

/** The agent's name as the app writes it. */
function agentName(agent: string): string {
  switch (agent) {
    case "claude-code":
      return "Claude Code";
    case "codex":
      return "Codex";
    case "cursor":
      return "Cursor";
    default:
      return clean(agent, 32);
  }
}

/** An agent session waiting on the user, like "Claude Code needs your
 *  permission · acme/demo" over the line the agent showed and the branch. A
 *  click opens the session's transcript. The daemon announces each wait once. */
function fromSessionNotice(kind: Kind, n: Notice): Toast | undefined {
  if (n.agent === "" || n.session === "") return undefined;
  const agent = agentName(n.agent);
  const headline =
    kind === "session_permission"
      ? `${agent} needs your permission`
      : `${agent} is waiting for your answer`;
  const repository = clean(n.repository, MAX_FIELD);
  const title = repository === "" ? headline : `${headline} · ${repository}`;
  const shown = n.message === undefined ? "" : clean(n.message, MAX_TITLE);
  let message = shown;
  if (message === "") {
    message = kind === "session_permission" ? "It asks to run a tool." : "It asked you a question.";
  }
  const branch = n.branch === undefined ? "" : clean(n.branch, MAX_FIELD);
  let place = "";
  if (branch !== "" && n.pr > 0) place = `${branch} · #${n.pr}`;
  else if (branch !== "") place = branch;
  else if (n.pr > 0) place = `#${n.pr}`;
  const body = place === "" ? message : `${message}\n${place}`;
  return { kind, title, body, route: { type: "session", key: `${n.agent}:${n.session}` } };
}

/** Unicode format characters (category Cf): bidirectional overrides and
 *  isolates, zero-width characters, the byte order mark and tag characters.
 *  They render nothing but can reorder or hide the text around them. */
function isFormat(c: number): boolean {
  return (
    c === 0xad ||
    (c >= 0x600 && c <= 0x605) ||
    c === 0x61c ||
    c === 0x6dd ||
    c === 0x70f ||
    (c >= 0x890 && c <= 0x891) ||
    c === 0x8e2 ||
    c === 0x180e ||
    (c >= 0x200b && c <= 0x200f) ||
    (c >= 0x202a && c <= 0x202e) ||
    (c >= 0x2060 && c <= 0x2064) ||
    (c >= 0x2066 && c <= 0x206f) ||
    c === 0xfeff ||
    (c >= 0xfff9 && c <= 0xfffb) ||
    c === 0x110bd ||
    c === 0x110cd ||
    (c >= 0x13430 && c <= 0x1343f) ||
    (c >= 0x1bca0 && c <= 0x1bca3) ||
    (c >= 0x1d173 && c <= 0x1d17a) ||
    c === 0xe0001 ||
    (c >= 0xe0020 && c <= 0xe007f)
  );
}

function isControl(c: number): boolean {
  return c <= 0x1f || (c >= 0x7f && c <= 0x9f);
}

/** Format characters are dropped, control characters become spaces and runs
 *  of whitespace one space; the result is at most `max` characters. */
export function clean(text: string, max: number): string {
  let spaced = "";
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0;
    if (isFormat(code)) continue;
    spaced += isControl(code) ? " " : char;
  }
  const joined = spaced.split(/\s+/).filter(Boolean).join(" ");
  const chars = Array.from(joined);
  if (chars.length <= max) return joined;
  return `${chars.slice(0, max - 1).join("")}…`;
}
