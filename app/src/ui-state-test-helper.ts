import { vi } from "vitest";
import { forgetUIState } from "./ui-state";

type Saved = { revision: number; value: unknown; writes: string[] };
const documents = new Map<string, Saved>();
const key = (path: string) => path.slice(path.indexOf("api/ui-state/"));
export function seedUIState(path: string, value: unknown) {
  documents.set(path, { revision: 1, value, writes: [] });
}
/** Shared mock daemon boundary used by existing app tests. */
export function mockStateFetch(handler: (path: string, init?: RequestInit) => Promise<Response>) {
  forgetUIState();
  documents.clear();
  return vi.fn(async (path: string, init?: RequestInit) => {
    if (!path.includes("api/ui-state/")) return handler(path, init);
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
