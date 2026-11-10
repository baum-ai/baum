import { useCallback, useMemo, useRef } from "react";
import type { LoadReviewVersion, ReviewDocument } from "@baum/viewer/review-document";

/** The viewer's version loader for one review (`key`: its pull request or
 *  group), answering from memory after the first read of a version. The
 *  viewer asks for the same earlier version from the version menu, the section
 *  panes and the hunk evidence, and a refetch of the page must not read them
 *  again.
 *
 *  Only versions before `currentVersion` are kept: the daemon refreshes the
 *  latest version's live facts in place, but an earlier version is never
 *  rewritten. A failed read is forgotten, so the next call retries it. The
 *  cache is dropped when `key` changes. */
export function useVersionLoader(
  key: string,
  fetchVersion: (version: number) => Promise<ReviewDocument>,
  currentVersion: number | undefined,
): LoadReviewVersion {
  const latest = useRef({ fetchVersion, currentVersion });
  latest.current = { fetchVersion, currentVersion };
  // biome-ignore lint/correctness/useExhaustiveDependencies: key is the cache's identity
  const cache = useMemo(() => new Map<number, Promise<ReviewDocument>>(), [key]);
  return useCallback(
    async (version) => {
      let pending = cache.get(version);
      if (!pending) {
        const { fetchVersion: fetchOne, currentVersion: current } = latest.current;
        const request = fetchOne(version);
        pending = request;
        if (current !== undefined && version < current) {
          cache.set(version, request);
          request.catch(() => {
            if (cache.get(version) === request) cache.delete(version);
          });
        }
      }
      try {
        return { status: "ready", document: await pending };
      } catch {
        return { status: "failed" };
      }
    },
    [cache],
  );
}
