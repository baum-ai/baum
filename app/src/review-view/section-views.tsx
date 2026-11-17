import { useEffect, useMemo, useRef, useState } from "react";
import { harnessName } from "@baum/viewer/agent-names";
import { chipClass } from "@baum/viewer/components/chip-classes";
import { cn } from "@baum/viewer/components/classnames";
import { formatShortDateTime } from "@baum/viewer/components/format";
import { Part } from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { SegmentedControl } from "@baum/viewer/components/ui";
import { useUrlState, ViewerLink } from "@baum/viewer/navigation";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import { indexedHunkFromFilePatch } from "@baum/viewer/patch";
import type { IndexedHunk, ReviewDocument } from "@baum/viewer/review-document";
import { withSelection } from "@baum/viewer/review-selection";
import { FileDiff, GeneratedToggle } from "./code-tab";
import {
  notesByHunk,
  SECTION_VIEWS,
  sectionNotes,
  type HunkNote,
  type SectionView,
  type SectionViews,
  type SessionQuote,
  testNames,
  type ViewFile,
} from "./model";

/** The row of a section's views, under its title: each with its count, and
 *  disabled (never hidden) when the section has nothing for it, so the row
 *  has the same shape in every section. */
export function SectionViewTabs({
  tablist,
  value,
  counts,
  onChange,
}: {
  tablist: string;
  value: SectionView;
  counts: SectionViews["counts"];
  onChange: (view: SectionView) => void;
}) {
  // In a narrow pane the row scrolls: the view shown stays in sight.
  const row = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const track = row.current?.querySelector<HTMLElement>('[role="tablist"]');
    const tab = track?.querySelector<HTMLElement>('[aria-selected="true"]');
    if (!track || !tab) return;
    const left = tab.offsetLeft - track.offsetLeft;
    if (left < track.scrollLeft) track.scrollLeft = left;
    else if (left + tab.offsetWidth > track.scrollLeft + track.clientWidth)
      track.scrollLeft = left + tab.offsetWidth - track.clientWidth;
  }, [value]);
  return (
    <div ref={row} className="min-w-0">
      <SegmentedControl
        label="Section views"
        tablist={tablist}
        value={value}
        onChange={onChange}
        // Scrolls sideways in a narrow pane rather than widening the page.
        className="max-w-full overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        items={SECTION_VIEWS.map(({ value: view, label }) =>
          view === "overview"
            ? { value: view, label }
            : { value: view, label, count: counts[view], disabled: counts[view] === 0 },
        )}
      />
    </div>
  );
}

/** A section's view other than its Overview. */
export function SectionViewBody({
  document,
  section,
  shown,
  views,
  context,
}: {
  document: ReviewDocument;
  section: string;
  shown: Exclude<SectionView, "overview">;
  views: SectionViews;
  context: ReviewPlaceContext;
}) {
  const notes = useMemo(() => sectionNotes(notesByHunk(document), section), [document, section]);
  const files = (list: ViewFile[]) => (
    <SectionFiles document={document} files={list} notes={notes} context={context} />
  );
  switch (shown) {
    case "code":
      return (
        <div className="space-y-4">
          {files(views.code.files)}
          {views.code.generated.length > 0 ? (
            <GeneratedFiles
              document={document}
              files={views.code.generated}
              notes={notes}
              context={context}
              open={views.code.files.length === 0}
            />
          ) : null}
        </div>
      );
    case "tests":
      return (
        <div className="space-y-5">
          <TestNames document={document} files={views.tests.files} context={context} />
          {files(views.tests.files)}
        </div>
      );
    case "docs":
      return files(views.docs.files);
    case "session":
      return <SessionQuotes document={document} quotes={views.session} />;
  }
}

/** The names of the tests the section's test hunks add or change, read from
 *  their lines once the hunks the review left out are loaded; nothing while
 *  they load, or when the names can't be read with confidence. */
function TestNames({
  document,
  files,
  context,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  context: ReviewPlaceContext;
}) {
  const names = useTestNames(document, files, context.hunkPatches);
  if (!names || names.length === 0) return null;
  return (
    <Part label="Tests added or changed">
      <ul className="flex flex-wrap gap-1.5">
        {names.map((name) => (
          <li key={name} className="max-w-full min-w-0">
            <code className={cn(chipClass, "block font-mono text-xs [overflow-wrap:anywhere]")}>
              {name}
            </code>
          </li>
        ))}
      </ul>
    </Part>
  );
}

function useTestNames(
  document: ReviewDocument,
  files: ViewFile[],
  source: HunkPatchSource | null,
): string[] | null | undefined {
  const version = document.review.version;
  const complete = files.every((file) => file.hunks.every((hunk) => hunk.diff));
  const [loaded, setLoaded] = useState<IndexedHunk[] | null | undefined>(undefined);
  const key = files.map((file) => file.path).join("\n");
  // biome-ignore lint/correctness/useExhaustiveDependencies: the files are read again only when their paths or the version change
  useEffect(() => {
    if (complete || !source) return;
    let current = true;
    setLoaded(undefined);
    Promise.all(
      files.map(async (file) => {
        const missing = file.hunks.find((hunk) => !hunk.diff);
        if (!missing) return file.hunks;
        const patch = await source(file.path, version, missing.id);
        return file.hunks.map((hunk) => {
          if (hunk.diff || !patch) return hunk;
          const diff = indexedHunkFromFilePatch(patch, hunk);
          return diff ? { ...hunk, diff } : hunk;
        });
      }),
    ).then(
      (all) => current && setLoaded(all.flat()),
      () => current && setLoaded(null),
    );
    return () => {
      current = false;
    };
  }, [key, version, complete, source]);
  if (complete) return testNames(files.flatMap((file) => file.hunks));
  if (!source) return null;
  return loaded ? testNames(loaded) : loaded;
}

/** A note's link from a section's own code goes back to its Overview, at
 *  the risk it names. */
const toOverview = (_note: HunkNote) => "In Overview →";

function SectionFiles({
  document,
  files,
  notes,
  context,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  notes: Map<string, HunkNote[]>;
  context: ReviewPlaceContext;
}) {
  if (files.length === 0) return null;
  return (
    <div className="space-y-4">
      {files.map((file) => (
        <FileDiff
          key={file.path}
          document={document}
          file={file}
          notes={notes}
          context={context}
          noteLink={toOverview}
        />
      ))}
    </div>
  );
}

/** The section's generated and vendored files, folded at the end of its
 *  code unless they are all it has. */
function GeneratedFiles({
  document,
  files,
  notes,
  context,
  open: openFirst,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  notes: Map<string, HunkNote[]>;
  context: ReviewPlaceContext;
  open: boolean;
}) {
  const [open, setOpen] = useState(openFirst);
  return (
    <div className="space-y-3">
      <GeneratedToggle open={open} files={files.length} onToggle={() => setOpen((was) => !was)} />
      {open ? (
        <SectionFiles document={document} files={files} notes={notes} context={context} />
      ) : null}
    </div>
  );
}

/** The prompts and session events the section cites, in the order they
 *  happened, each with when and who, linking to its turn of the transcript. */
function SessionQuotes({ document, quotes }: { document: ReviewDocument; quotes: SessionQuote[] }) {
  const { search } = useUrlState();
  const sessions = new Set(quotes.map((quote) => quote.session));
  return (
    <ol aria-label="Session" className="divide-y divide-subtle">
      {quotes.map((quote) => {
        const next = withSelection(search, { kind: "session", id: quote.session });
        if (quote.turn !== undefined) next.set("turn", String(quote.turn));
        const agent = quote.vendor ? harnessName(quote.vendor) : "Agent";
        const title =
          sessions.size > 1
            ? document.index.sessions.find((each) => each.id === quote.session)?.title
            : null;
        return (
          <li
            key={quote.key}
            data-review-block=""
            data-quote={quote.kind}
            className="space-y-1.5 py-3 outline-none first:pt-0"
          >
            <p className="flex flex-wrap items-baseline gap-x-2 text-xs">
              <span
                className={cn(
                  "font-medium tracking-[0.08em] uppercase",
                  quote.who === "person" ? "text-primary" : "text-tertiary",
                )}
              >
                {quote.who === "person" ? "Person" : agent}
              </span>
              {quote.at ? (
                <time dateTime={quote.at} className="text-tertiary tabular-nums">
                  {formatShortDateTime(quote.at)}
                </time>
              ) : null}
              {title ? <span className="min-w-0 truncate text-tertiary">{title}</span> : null}
            </p>
            <blockquote
              className={cn(
                "border-l-2 pl-3 text-base whitespace-pre-line [overflow-wrap:anywhere]",
                quote.who === "person"
                  ? "border-strong text-primary"
                  : "border-subtle text-secondary",
              )}
            >
              {quote.text}
            </blockquote>
            <ViewerLink href={`?${next}`} className="text-xs text-link hover:underline">
              {quote.kind === "prompt"
                ? `${agent} · prompt ${quote.turn} →`
                : quote.turn !== undefined
                  ? `${agent} · after prompt ${quote.turn} →`
                  : `${agent} session →`}
            </ViewerLink>
          </li>
        );
      })}
    </ol>
  );
}
