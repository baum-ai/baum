import { vi } from "vitest";
import { INBOX_COLORS } from "./inboxes";
import { forgetUIState } from "./ui-state";

type Saved = { revision: number; value: unknown; writes: string[] };
const documents = new Map<string, Saved>();
const key = (path: string) => path.slice(path.indexOf("api/ui-state/"));
export function seedUIState(path: string, value: unknown) {
  documents.set(path, { revision: 1, value, writes: [] });
}
/** The inbox a fresh daemon starts with. */
export const DEFAULT_INBOX = {
  id: "0123456789abcdef0123456789abcdef",
  name: "Default",
  color: INBOX_COLORS[0],
  what: "any",
  owners: [],
  risk: "any",
  repositories: [],
  query: "",
  no_bots: false,
  badge: true,
};
/** A handler that does not answer the inboxes gets a daemon's fresh start:
 *  one Default inbox. */
async function withInboxes(path: string, init: RequestInit | undefined, response: Response) {
  if (!path.endsWith("api/inboxes") || init?.method === "PUT" || !response.ok) return response;
  const body = (await response.clone().json()) as { inboxes?: unknown } | null;
  return Array.isArray(body?.inboxes)
    ? response
    : Response.json({ available: true, revision: 1, inboxes: [DEFAULT_INBOX] });
}
/** Shared mock daemon boundary used by existing app tests. */
export function mockStateFetch(handler: (path: string, init?: RequestInit) => Promise<Response>) {
  forgetUIState();
  documents.clear();
  return vi.fn(async (path: string, init?: RequestInit) => {
    if (!path.includes("api/ui-state/")) return withInboxes(path, init, await handler(path, init));
    const id = key(path);
    const current = documents.get(id) ?? { revision: 0, value: null, writes: [] };
    if (init?.method !== "PUT") return Response.json(current);
    const input = JSON.parse(String(init.body));
    if (current.writes.includes(input.mutation_id)) return Response.json(current);
    if (input.revision !== current.revision) return Response.json(current, { status: 409 });
    const saved = {
      revision: current.revision + 1,
      value: input.value,
      writes: [...current.writes, input.mutation_id],
    };
    documents.set(id, saved);
    return Response.json(saved);
  });
}
