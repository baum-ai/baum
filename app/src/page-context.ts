import { agentName, appURL } from "./api";
import { PAGES } from "./pages";

/** The pages a page context names, as the MCP server knows them
 *  (`agentcontext` page names): the sidebar's lists, and the pages of one
 *  pull request, group, session, job or feature. */
export type ContextPage =
  | "inbox"
  | "pulls"
  | "timeline"
  | "sessions"
  | "analytics"
  | "jobs"
  | "repositories"
  | "settings"
  | "pull_request"
  | "group"
  | "session"
  | "job"
  | "feature";

/** What the person has open in the app, as `agentcontext.PageContext`
 *  encodes it: the page, the thing it shows, its filters, the Settings or
 *  repository section, and the part of the page picked. The session panel
 *  reports it on every navigation (`PUT api/console/page`), so a
 *  conversation's agent always knows the current page. */
export type PageContext = {
  page: ContextPage;
  repository?: string;
  pull_request?: number;
  group?: string;
  session?: string;
  job?: string;
  feature?: string;
  section?: string;
  filters?: Record<string, string>;
  focus?: unknown;
};

/** The address parameters that name a page or its subject, never a filter
 *  (`agentcontext.reservedKeys`). */
const RESERVED = new Set([
  "page",
  "repository",
  "clone",
  "pr",
  "group",
  "session",
  "job",
  "feature",
  "section",
  "inbox",
  "peek",
  "walk",
]);
/** A filter's key as the MCP server's links take it. */
const FILTER_KEY = /^[a-z][a-z0-9_]{0,31}$/;
const MAX_FILTERS = 32;
const MAX_FILTER_VALUE = 256;
/** The sections the MCP server knows (`settingsSections`,
 *  `repositorySections`); another is left out. */
const SETTINGS_SECTIONS = ["general", "reviews", "fixes", "connections", "computer", "prompts"];
const REPOSITORY_SECTIONS = ["reviews", "prompts", "registration"];
const LIST_PAGES: ContextPage[] = ["inbox", "pulls", "timeline", "sessions", "analytics", "jobs"];

/** The filters of a list page's address: every parameter but those naming
 *  the page or its subject, as the page's own links write them. */
function filtersOf(search: URLSearchParams): Record<string, string> | undefined {
  const filters: Record<string, string> = {};
  let count = 0;
  for (const [key, value] of search) {
    if (RESERVED.has(key) || !FILTER_KEY.test(key) || key in filters) continue;
    if (value.length > MAX_FILTER_VALUE || count >= MAX_FILTERS) continue;
    filters[key] = value;
    count++;
  }
  return count > 0 ? filters : undefined;
}

/** The page context of the app's address, the one place the app derives it:
 *  the inverse of the MCP server's `link`. `cloneRepository` names the
 *  repository of a clone id, for a pull request's address that names its
 *  clone. */
export function pageContextOf(
  search: URLSearchParams,
  cloneRepository: (clone: string) => string | undefined = () => undefined,
  hash = "",
): PageContext {
  const requested = search.get("page") ?? "inbox";
  const number = Number(search.get("pr"));
  const clone = search.get("clone");
  const repository = search.get("repository") ?? (clone ? cloneRepository(clone) : undefined);
  if ((requested === "inbox" || requested === "pulls") && number > 0 && repository) {
    const focus: Record<string, string> = {};
    const walk = search.get("walk");
    if (walk) focus.walk = walk;
    const anchor = hash.replace(/^#/, "");
    if (anchor) focus.anchor = anchor.slice(0, 512);
    return {
      page: "pull_request",
      repository,
      pull_request: number,
      ...(Object.keys(focus).length > 0 ? { focus } : {}),
    };
  }
  const group = search.get("group");
  if ((requested === "inbox" || requested === "pulls") && group) return { page: "group", group };
  if (requested === "sessions" && search.get("session"))
    return { page: "session", session: search.get("session")! };
  if (requested === "jobs" && search.get("job")) return { page: "job", job: search.get("job")! };
  if (requested === "timeline" && search.get("feature"))
    return { page: "feature", feature: search.get("feature")! };
  if (requested === "settings" || requested === "diagnostics") {
    const section = requested === "diagnostics" ? "computer" : (search.get("section") ?? "general");
    return SETTINGS_SECTIONS.includes(section)
      ? { page: "settings", section }
      : { page: "settings" };
  }
  if (requested === "repositories") {
    const name = search.get("repository");
    if (!name) return withFilters({ page: "repositories" }, search);
    const section = search.get("section") ?? "reviews";
    return {
      page: "repositories",
      repository: name,
      ...(REPOSITORY_SECTIONS.includes(section) ? { section } : {}),
    };
  }
  // The inbox's own pages (a new or edited inbox, the list of them) are the
  // Inbox's; an address naming no known page is the Inbox, as the app opens it.
  const page = LIST_PAGES.includes(requested as ContextPage) ? (requested as ContextPage) : "inbox";
  return withFilters({ page }, search);
}

function withFilters(context: PageContext, search: URLSearchParams): PageContext {
  const filters = filtersOf(search);
  return filters ? { ...context, filters } : context;
}

const SECTION_TITLES: Record<string, string> = {
  general: "General",
  reviews: "Reviews",
  fixes: "Fixes",
  connections: "Connections",
  computer: "This computer",
  prompts: "Prompts",
  registration: "Registration",
};

/** The page as the session panel's header names it: what the agent is
 *  looking at. `title` is the subject's own name, where the app knows it
 *  (a pull request's title). */
export function pageLabel(context: PageContext, title?: string): string {
  const named = (what: string) => (title ? `${title} · ${what}` : what);
  switch (context.page) {
    case "pull_request":
      return named(`${context.repository} #${context.pull_request}`);
    case "group":
      return named("Review group");
    case "session": {
      const agent = context.session?.split(":")[0] ?? "";
      return named(`${agentName(agent)} session`);
    }
    case "job":
      return named("Job");
    case "feature":
      return named("Feature");
    case "settings":
      return `Settings · ${SECTION_TITLES[context.section ?? "general"] ?? context.section}`;
    case "repositories":
      return context.repository
        ? `${context.repository} · ${SECTION_TITLES[context.section ?? "reviews"] ?? context.section}`
        : "Repositories";
  }
  return PAGES.find((item) => item.page === context.page)?.label ?? context.page;
}

/** The app's address of a page context, as the MCP server's `link` builds it:
 *  where a proposal made on another page is saved. */
export function hrefOf(context: PageContext): string {
  const query = new URLSearchParams();
  switch (context.page) {
    case "pull_request":
      return appURL(
        `?repository=${encodeURIComponent(context.repository ?? "")}&pr=${context.pull_request ?? 0}`,
      );
    case "group":
      return appURL(`?group=${encodeURIComponent(context.group ?? "")}`);
    case "session":
      query.set("page", "sessions");
      query.set("session", context.session ?? "");
      break;
    case "job":
      query.set("page", "jobs");
      query.set("job", context.job ?? "");
      break;
    case "feature":
      query.set("page", "timeline");
      query.set("feature", context.feature ?? "");
      break;
    case "settings":
      query.set("page", "settings");
      if (context.section) query.set("section", context.section);
      break;
    case "repositories":
      query.set("page", "repositories");
      if (context.repository) query.set("repository", context.repository);
      if (context.section) query.set("section", context.section);
      break;
    default:
      query.set("page", context.page);
      if (context.page === "timeline" && !context.filters) query.set("who", "human");
  }
  for (const [key, value] of Object.entries(context.filters ?? {})) query.set(key, value);
  return appURL(`?${query}`);
}

/** Whether two page contexts name the same page, its subject and section. */
export function samePage(a: PageContext, b: PageContext): boolean {
  return (
    a.page === b.page &&
    (a.repository ?? "").toLowerCase() === (b.repository ?? "").toLowerCase() &&
    (a.pull_request ?? 0) === (b.pull_request ?? 0) &&
    (a.group ?? "") === (b.group ?? "") &&
    (a.session ?? "") === (b.session ?? "") &&
    (a.job ?? "") === (b.job ?? "") &&
    (a.feature ?? "") === (b.feature ?? "") &&
    (a.section ?? "") === (b.section ?? "")
  );
}
