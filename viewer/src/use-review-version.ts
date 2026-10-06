import { useEffect, useRef, useState } from "react";

import { useUrlState } from "./navigation";
import type { LoadReviewVersion, ReviewVersionState } from "./review-document";

export type HistorySelection = { version: number; compare: boolean };

const PARAM = { version: "review", compare: "compare" } as const;

/** The review version the URL asks for, `?review=3`, compared against the
 *  current one with `&compare=1`. Without it, or with anything that is not a
 *  version number, the page reads the latest review. */
export function readHistorySelection(search: URLSearchParams): HistorySelection | null {
  return parseSelection(search.get(PARAM.version), search.get(PARAM.compare));
}

function parseSelection(raw: string | null, compare: string | null): HistorySelection | null {
  if (!raw || !/^\d+$/.test(raw)) return null;
  const version = Number(raw);
  if (!Number.isSafeInteger(version) || version < 1) return null;
  return { version, compare: compare === "1" };
}

export function withHistorySelection(
  search: URLSearchParams,
  selection: HistorySelection | null,
): URLSearchParams {
  const next = new URLSearchParams(search);
  next.delete(PARAM.version);
  next.delete(PARAM.compare);
  if (selection) {
    next.set(PARAM.version, String(selection.version));
    if (selection.compare) next.set(PARAM.compare, "1");
  }
  return next;
}

/** Which review version the page shows, kept in the URL so a link can open
 *  the pull request on an earlier review. Naming the current version reads as
 *  the current review, and nothing is chosen until that version is known.
 *
 *  The page paints from a local copy, as `useReviewSelection` does: the router
 *  lands the URL in a transition, and a controlled menu row reading the URL
 *  alone would snap back before it did. A URL change of its own (Back, a
 *  pasted link) replaces the copy. */
export function useHistoryView(currentVersion: number | undefined, loadVersion: LoadReviewVersion) {
  const { search, update: writeSearch } = useUrlState();
  const versionParam = search.get(PARAM.version);
  const compareParam = search.get(PARAM.compare);
  const [requested, setRequested] = useState(() => parseSelection(versionParam, compareParam));
  useEffect(
    () => setRequested(parseSelection(versionParam, compareParam)),
    [versionParam, compareParam],
  );
  const selection =
    currentVersion !== undefined && requested && requested.version !== currentVersion
      ? requested
      : null;
  const setSelection = (value: HistorySelection | null) => {
    setRequested(value);
    writeSearch((current) => withHistorySelection(current, value), { preventScrollReset: true });
  };
  // The latest loader, read when a version is asked for: a host that passes
  // a new function on each render must not reload the version on screen.
  const load = useRef(loadVersion);
  load.current = loadVersion;
  const [loaded, setLoaded] = useState<{ version: number; state: ReviewVersionState } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const version = selection?.version;
  useEffect(() => {
    if (version === undefined) return;
    let active = true;
    setLoaded({ version, state: { status: "loading" } });
    void load.current(version).then(
      (state) => {
        if (active) setLoaded({ version, state });
      },
      () => {
        if (active) setLoaded({ version, state: { status: "failed" } });
      },
    );
    return () => {
      active = false;
    };
  }, [version, attempt]);
  const state = loaded?.version === version ? loaded?.state : null;
  return { selection, setSelection, state, retry: () => setAttempt((value) => value + 1) };
}
