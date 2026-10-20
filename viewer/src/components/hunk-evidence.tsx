import { createContext, type ReactNode, useContext, useEffect, useRef, useState } from "react";

import { indexedHunkFromFilePatch } from "../patch";
import type {
  Anchor,
  IndexedHunk,
  IndexedHunkLine,
  ReviewDocument,
  LoadReviewVersion,
  ReviewVersionState,
} from "../review-document";
import { hunkLine, hunkUrl, pullRequestUrl } from "../review-document";
import { CodeFileLink } from "./code-file-link";
import { CodeDiff, type CodeDiffHunk, type CodeDiffLine } from "./review-tiles/code-diff";
import { cn, FOCUS_RING } from "./classnames";
import { ChevronDownIcon } from "./icons";

const CONTEXT_LINES = 2;

type HunkAnchor = Extract<Anchor, { type: "hunk" }>;

function displayLine(line: IndexedHunkLine) {
  return {
    kind: line.kind,
    text: line.text,
    oldLine: line.kind !== "add" ? line.old_line : undefined,
    newLine: line.kind !== "remove" ? line.new_line : undefined,
  } satisfies CodeDiffLine;
}

function excerptHeader(
  original: string,
  lines: readonly CodeDiffLine[],
  omittedPrefix: readonly CodeDiffLine[],
): string {
  const match = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(original);
  const oldLines = lines.flatMap((line) => (line.oldLine === undefined ? [] : [line.oldLine]));
  const newLines = lines.flatMap((line) => (line.newLine === undefined ? [] : [line.newLine]));
  const missingStart = (side: "oldLine" | "newLine", startGroup: number, countGroup: number) => {
    const originalStart = Number(match?.[startGroup] ?? 0);
    const originalCount = Number(match?.[countGroup] ?? 1);
    let cursor = originalStart + (originalCount === 0 ? 1 : 0);
    for (const line of omittedPrefix) {
      const coordinate = line[side];
      if (coordinate !== undefined) cursor = coordinate + 1;
    }
    return Math.max(0, cursor - 1);
  };
  const oldStart = oldLines[0] ?? missingStart("oldLine", 1, 2);
  const newStart = newLines[0] ?? missingStart("newLine", 3, 4);
  return `@@ -${oldStart},${oldLines.length} +${newStart},${newLines.length} @@`;
}

function selectedHunk(hunk: IndexedHunk, lines?: [number, number]): CodeDiffHunk | null {
  if (!hunk.diff) return null;
  const all = hunk.diff.lines.map(displayLine);
  if (!lines) return { header: hunk.diff.header, lines: all };
  const [start, end] = lines;
  const selected = all.flatMap((line, index) => {
    const newMatches = line.newLine !== undefined && line.newLine >= start && line.newLine <= end;
    return newMatches ? [index] : [];
  });
  if (selected.length === 0) return null;
  const first = Math.max(0, selected[0]! - CONTEXT_LINES);
  const last = Math.min(all.length, selected[selected.length - 1]! + CONTEXT_LINES + 1);
  const excerpt = all.slice(first, last);
  return { header: excerptHeader(hunk.diff.header, excerpt, all.slice(0, first)), lines: excerpt };
}

/** Loads one changed path's patch as a stored review version compared it,
 *  or null when that version changed no such text file. `hunk` is the cited
 *  hunk's id, for a source that reads it from the review holding it, as a
 *  stack review's member does. */
export type HunkPatchSource = (
  path: string,
  version: number,
  hunk: string,
) => Promise<string | null>;

const HunkPatchContext = createContext<HunkPatchSource | null>(null);

/** Set by the review workspace so a cited hunk whose stored excerpt was
 *  omitted — a patch trimmed for the generation budget, say — is read from
 *  the reviewed comparison instead. Outside it, such a hunk keeps its link. */
export function HunkPatchProvider({
  source,
  children,
}: {
  source: HunkPatchSource | null;
  children: ReactNode;
}) {
  return <HunkPatchContext.Provider value={source}>{children}</HunkPatchContext.Provider>;
}

type LoadedPatch =
  | { status: "loading" }
  | { status: "ready"; patch: string | null }
  | { status: "failed" };

export function HunkEvidence({
  document,
  anchors,
  historicalVersion,
  loadVersion,
  diffClassName,
}: {
  document: ReviewDocument;
  anchors: Anchor[];
  historicalVersion?: number;
  loadVersion?: LoadReviewVersion;
  /** Placed on the opened excerpts only, so a caller can let the code take
   *  width its own indentation would cost while the summary line stays put. */
  diffClassName?: string;
}) {
  const documentKey = `${document.review.repository}#${document.review.pull_request}`;
  const latestRequest = useRef(0);
  const [historical, setHistorical] = useState<{
    documentKey: string;
    requestId: number;
    version: number;
    state: ReviewVersionState;
  } | null>(null);
  const historicalDocument =
    historical !== null &&
    historical.documentKey === documentKey &&
    historical.version === historicalVersion &&
    historical.state.status === "ready"
      ? historical.state.document
      : null;
  const historicalReady = historicalDocument !== null;
  const indexed = new Map(document.index.hunks.map((hunk) => [hunk.id, hunk]));
  const historicalIndexed = new Map(
    historicalDocument?.index.hunks.map((hunk) => [hunk.id, hunk]) ?? [],
  );
  const patchSource = useContext(HunkPatchContext);
  const [patches, setPatches] = useState<Record<string, LoadedPatch>>({});
  const [openKey, setOpenKey] = useState<string | null>(null);
  const seen = new Set<string>();
  const selected: Array<{
    anchor: HunkAnchor;
    hunk?: IndexedHunk;
    diff?: CodeDiffHunk;
    historical: boolean;
    patch?: { key: string; path: string; version: number; hunk: string; state?: LoadedPatch };
  }> = [];
  for (const anchor of anchors) {
    if (anchor.type !== "hunk") continue;
    const key = `${anchor.id}:${anchor.lines?.join("-") ?? "all"}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const currentHunk = indexed.get(anchor.id);
    const historicalHunk = currentHunk ? undefined : historicalIndexed.get(anchor.id);
    const hunk = currentHunk ?? historicalHunk;
    // Without a stored excerpt, the hunk is read from the file's patch at the
    // version the review compared, once the reader opens the evidence.
    const version = historicalHunk ? historicalVersion : document.review.version;
    const patch =
      hunk && !hunk.diff && patchSource && version !== undefined
        ? {
            key: `${documentKey}:${version}:${hunk.path}:${hunk.id}`,
            path: hunk.path,
            version,
            hunk: hunk.id,
          }
        : undefined;
    const state = patch ? patches[patch.key] : undefined;
    const loaded =
      hunk && state?.status === "ready" && state.patch !== null
        ? indexedHunkFromFilePatch(state.patch, hunk)
        : null;
    const diff = hunk
      ? selectedHunk(loaded ? { ...hunk, diff: loaded } : hunk, anchor.lines)
      : null;
    selected.push({
      anchor,
      hunk,
      diff: diff ?? undefined,
      historical: Boolean(historicalHunk),
      patch: patch && { ...patch, state },
    });
  }
  const disclosureKey = `${documentKey}:${historicalVersion ?? "current"}:${[...seen].join(",")}`;
  const wanted = new Map(
    openKey === disclosureKey
      ? selected.flatMap((item) =>
          item.patch && !item.patch.state ? [[item.patch.key, item.patch] as const] : [],
        )
      : [],
  );
  // The effect runs when the set of patches wanted changes, named by its keys,
  // and reads the patches themselves from the ref.
  const wantedRef = useRef(wanted);
  wantedRef.current = wanted;
  const wantedKey = [...wanted.keys()].join("\n");
  useEffect(() => {
    const pending = wantedKey ? [...wantedRef.current.values()] : [];
    if (!patchSource || pending.length === 0) return;
    setPatches((current) => {
      const next = { ...current };
      for (const item of pending) next[item.key] ??= { status: "loading" };
      return next;
    });
    for (const item of pending) {
      void patchSource(item.path, item.version, item.hunk).then(
        (patch) =>
          setPatches((current) => ({ ...current, [item.key]: { status: "ready", patch } })),
        () => setPatches((current) => ({ ...current, [item.key]: { status: "failed" } })),
      );
    }
  }, [patchSource, wantedKey]);
  if (selected.length === 0) return null;

  const missingHistorical = selected.some((item) => !item.hunk);
  const canLoadHistorical =
    missingHistorical && historicalVersion !== undefined && loadVersion !== undefined;
  const historicalIsLoading =
    historical !== null &&
    historical.documentKey === documentKey &&
    historical.version === historicalVersion &&
    historical.state.status === "loading";
  const historicalLoadFailed =
    historical !== null &&
    historical.documentKey === documentKey &&
    historical.version === historicalVersion &&
    historical.state.status !== "loading" &&
    historical.state.status !== "ready";
  const loadHistorical = () => {
    if (!canLoadHistorical || historicalVersion === undefined || !loadVersion) return;
    if (historicalIsLoading || historicalReady) return;
    const request = {
      documentKey,
      requestId: latestRequest.current + 1,
      version: historicalVersion,
    };
    latestRequest.current = request.requestId;
    setHistorical({ ...request, state: { status: "loading" } });
    void loadVersion(historicalVersion).then(
      (state) =>
        setHistorical((current) =>
          current?.requestId === request.requestId ? { ...request, state } : current,
        ),
      () =>
        setHistorical((current) =>
          current?.requestId === request.requestId
            ? { ...request, state: { status: "failed" } }
            : current,
        ),
    );
  };

  const byPath = new Map<string, (typeof selected)[number][]>();
  for (const item of selected.filter((candidate) => candidate.hunk && candidate.diff)) {
    const path = item.hunk!.path;
    byPath.set(path, [...(byPath.get(path) ?? []), item]);
  }
  const files = [...byPath.entries()].map(([path, items], sourceIndex) => {
    const hunks = items.map((item) => item.diff!);
    const lines = hunks.flatMap((hunk) => hunk.lines);
    return {
      sourceIndex,
      path,
      href: hunkUrl(document, items[0]!.hunk!),
      additions: lines.filter((line) => line.kind === "add").length,
      deletions: lines.filter((line) => line.kind === "remove").length,
      hunks,
    };
  });
  const unavailable = selected.filter((item) => !item.diff);
  const hasLineTotals = unavailable.length === 0;
  const usesHistorical = selected.some((item) => item.historical);
  const additions = files.reduce((total, file) => total + file.additions, 0);
  const deletions = files.reduce((total, file) => total + file.deletions, 0);
  const fileCount = Math.max(
    files.length,
    new Set(selected.map((item) => item.hunk?.path).filter(Boolean)).size,
  );
  const summary =
    fileCount === 0
      ? `${selected.length} ${selected.length === 1 ? "excerpt" : "excerpts"} · ${canLoadHistorical && !historicalReady && !historicalLoadFailed ? "historical evidence" : "source unavailable"}`
      : `${selected.length} ${selected.length === 1 ? "excerpt" : "excerpts"} in ${fileCount} ${fileCount === 1 ? "file" : "files"}${hasLineTotals ? ` · +${additions} −${deletions}` : ""}${usesHistorical && historicalVersion ? ` · review v${historicalVersion}` : ""}`;

  return (
    <details
      key={disclosureKey}
      className="group mt-1.5"
      onToggle={(event) => {
        if (!event.currentTarget.open) return;
        setOpenKey(disclosureKey);
        // Reopening retries a patch that failed to load; the source keeps
        // no failed request, so asking again fetches it again.
        const failed = selected.flatMap((item) =>
          item.patch?.state?.status === "failed" ? [item.patch.key] : [],
        );
        if (failed.length > 0)
          setPatches((current) => {
            const next = { ...current };
            for (const key of failed) delete next[key];
            return next;
          });
        loadHistorical();
      }}
    >
      {/* A quiet line under the item it backs, not a panel of its own: how
       *  much code stands behind it, opened for a reader who wants to see it. */}
      <summary
        aria-label={`Code evidence: ${summary}`}
        className={cn(
          "inline-flex cursor-pointer list-none items-center gap-1 rounded-(--radius-sm) text-xs text-(--text-tertiary) hover:text-(--text-secondary) marker:hidden [&::-webkit-details-marker]:hidden",
          FOCUS_RING,
        )}
      >
        {fileCount === 0 ? (
          summary
        ) : (
          <span>
            {selected.length} {selected.length === 1 ? "excerpt" : "excerpts"} in {fileCount}{" "}
            {fileCount === 1 ? "file" : "files"}
            {hasLineTotals && (
              <>
                {" · "}
                <span className="text-(--status-positive)">+{additions}</span>{" "}
                <span className="text-(--status-critical)">−{deletions}</span>
              </>
            )}
            {usesHistorical && historicalVersion ? ` · review v${historicalVersion}` : ""}
          </span>
        )}
        <ChevronDownIcon
          size={12}
          className="transition-transform group-open:rotate-180 motion-reduce:transition-none"
        />
      </summary>
      {usesHistorical && historicalVersion ? (
        <p className="mt-3 text-base text-(--text-tertiary)">
          Historical evidence from review v{historicalVersion}
        </p>
      ) : null}
      <CodeDiff
        files={files}
        bare
        highlight={openKey === disclosureKey}
        className={diffClassName}
      />
      {unavailable.length > 0 ? (
        <ul className="mt-3 space-y-1.5 text-base text-(--text-tertiary)">
          {unavailable.map(({ anchor, hunk, patch }) => (
            <li key={`${anchor.id}:${anchor.lines?.join("-") ?? "all"}`}>
              {hunk && patch && (!patch.state || patch.state.status === "loading") ? (
                <>Loading excerpt from {hunk.path}…</>
              ) : !hunk && canLoadHistorical && !historicalReady ? (
                historicalLoadFailed ? (
                  <>Historical evidence from review v{historicalVersion} could not be loaded.</>
                ) : (
                  <>Loading historical evidence from review v{historicalVersion}…</>
                )
              ) : (
                <>
                  Excerpt unavailable ·{" "}
                  {hunk ? (
                    <CodeFileLink
                      path={hunk.path}
                      line={hunkLine(hunk)}
                      href={hunkUrl(document, hunk)}
                      className="text-(--text-link) hover:underline"
                    >
                      View {hunk.path}
                    </CodeFileLink>
                  ) : (
                    <a href={pullRequestUrl(document)} target="_blank" rel="noreferrer">
                      View {anchor.id} on GitHub
                    </a>
                  )}
                </>
              )}
            </li>
          ))}
        </ul>
      ) : null}
    </details>
  );
}
