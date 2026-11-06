import type { HunkPatchSource } from "../src/components/hunk-evidence";

/** Reads the code excerpts a review document left out from `GET /api/patch`,
 *  which `the reference viewer host` answers from the clone on this machine that holds the pull
 *  request's commits. */

/** A file's patch from the response body: its text, or null when the body says
 *  there is none or is not an answer. */
export function parsePatchAnswer(body: unknown): string | null {
  if (typeof body !== "object" || body === null || !("patch" in body)) return null;
  return typeof body.patch === "string" && body.patch !== "" ? body.patch : null;
}

/** Reads one file's patch, of the served document or, given `version`, of
 *  that saved version of its pull request. Everything that means "there is no
 *  patch here" — a null patch, a path or version the server does not know, a
 *  body that is no answer — resolves to null, which keeps the excerpt's link
 *  to the source. A request that fails or is answered with a server error
 *  rejects, so opening the excerpt again tries again. */
export async function fetchPatch(
  path: string,
  fetcher: typeof fetch = (...args) => fetch(...args),
  version?: number,
): Promise<string | null> {
  const versionQuery = version === undefined ? "" : `&version=${version}`;
  const response = await fetcher(`/api/patch?path=${encodeURIComponent(path)}${versionQuery}`, {
    cache: "no-store",
  });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`The patch request failed (${response.status}).`);
  try {
    return parsePatchAnswer(await response.json());
  } catch {
    return null;
  }
}

/** The patches of one document, which is at `version`, and of the other saved
 *  versions of its pull request, each read between that version's own
 *  commits. The document's own are asked for without a version, so a server
 *  that has no saved versions, such as one serving a file, answers them. Each
 *  version's path is requested once; a failed request is forgotten and asked
 *  again the next time. */
export function createPatchSource(version: number, fetcher?: typeof fetch): HunkPatchSource {
  const patches = new Map<string, Promise<string | null>>();
  return (path, requested) => {
    const key = `${requested}:${path}`;
    let request = patches.get(key);
    if (!request) {
      request = fetchPatch(path, fetcher, requested === version ? undefined : requested);
      const started = request;
      started.catch(() => {
        if (patches.get(key) === started) patches.delete(key);
      });
      patches.set(key, request);
    }
    return request;
  };
}
