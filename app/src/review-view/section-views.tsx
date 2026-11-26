import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import pluralize from "pluralize";
import { harnessName } from "@baum/viewer/agent-names";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { formatShortDateTime } from "@baum/viewer/components/format";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { SegmentedControl } from "@baum/viewer/components/ui";
import { useUrlState, ViewerLink } from "@baum/viewer/navigation";
import type { HunkPatchSource } from "@baum/viewer/components/hunk-evidence";
import { indexedHunkFromFilePatch } from "@baum/viewer/patch";
import type { IndexedHunk, ReviewDocument } from "@baum/viewer/review-document";
import { withSelection } from "@baum/viewer/review-selection";
import { FileDiff, GeneratedToggle } from "./code-tab";
import {
  SECTION_VIEWS,
  type SectionView,
  type SectionViews,
  type SessionQuote,
  type TestChange,
  type TestOutlineFile,
  type TestOutlineItem,
  testOutline,
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
  overview = true,
}: {
  tablist: string;
  value: SectionView;
  counts: SectionViews["counts"];
  onChange: (view: SectionView) => void;
  /** Off when the Overview stands in its own column beside the others. */
  overview?: boolean;
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
        items={SECTION_VIEWS.filter(({ value: view }) => overview || view !== "overview").map(
          ({ value: view, label }) =>
            view === "overview"
              ? { value: view, label }
              : { value: view, label, count: counts[view], disabled: counts[view] === 0 },
        )}
      />
    </div>
  );
}

/** The section's views h and l step through, in the row's order: those with
 *  something to show, with the Overview only when it is in the row. */
export function steppableViews(counts: SectionViews["counts"], overview: boolean): SectionView[] {
  return SECTION_VIEWS.flatMap(({ value }) =>
    value === "overview" ? (overview ? [value] : []) : counts[value] > 0 ? [value] : [],
  );
}

/** The view h (previous) or l (next) goes to from `value`, round from either
 *  end as the row's arrows go; null for another key. */
export function viewKeyStep(
  key: string,
  value: SectionView,
  views: SectionView[],
): SectionView | null {
  if ((key !== "h" && key !== "l") || views.length === 0) return null;
  const index = views.indexOf(value);
  if (index < 0) return views[key === "l" ? 0 : views.length - 1]!;
  return views[(index + (key === "l" ? 1 : views.length - 1)) % views.length]!;
}

/** h and l, anywhere but a text field or a dialog, move through the
 *  section's views (Code, Tests, Docs…). The right-hand sidebar keeps them
 *  while it holds the keys: it takes them first and marks them used. */
export function useSectionViewKeys(go: (key: "h" | "l") => void, enabled = true) {
  const latest = useRef(go);
  latest.current = go;
  useEffect(() => {
    if (!enabled) return;
    const onKey = (event: KeyboardEvent) => {
      if (
        (event.key !== "h" && event.key !== "l") ||
        event.defaultPrevented ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey ||
        event.isComposing ||
        document.querySelector("dialog[open]")
      )
        return;
      if (
        event.target instanceof HTMLElement &&
        event.target.closest(
          'input, textarea, select, [contenteditable], [role="textbox"], [role="menu"]',
        )
      )
        return;
      event.preventDefault();
      latest.current(event.key);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [enabled]);
}

/** A section's view other than its Overview. */
export function SectionViewBody({
  document,
  shown,
  views,
  context,
  reveal,
}: {
  document: ReviewDocument;
  shown: Exclude<SectionView, "overview">;
  views: SectionViews;
  context: ReviewPlaceContext;
  /** A file an excerpt asked to see: its fold opens if it is generated,
   *  again on each new request. */
  reveal?: { path: string } | null;
}) {
  const files = (list: ViewFile[]) => (
    <SectionFiles document={document} files={list} context={context} />
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
              context={context}
              open={views.code.files.length === 0}
              reveal={reveal}
            />
          ) : null}
        </div>
      );
    case "tests":
      return <TestFiles document={document} files={views.tests.files} context={context} />;
    case "docs":
      return files(views.docs.files);
    case "session":
      return <SessionQuotes document={document} quotes={views.session} />;
  }
}

const CHANGES: { change: TestChange; mark: string; label: string; tone: string }[] = [
  { change: "added", mark: "+", label: "added", tone: "text-status-positive" },
  { change: "renamed", mark: "→", label: "renamed", tone: "text-status-info" },
  { change: "changed", mark: "~", label: "changed", tone: "text-status-caution" },
  { change: "removed", mark: "−", label: "removed", tone: "text-status-critical" },
];

/** Brings the line a test or helper starts at into view in its file's diff,
 *  unfolding the code if it is folded, and flashes it. */
function openInDiff(from: HTMLElement, at?: TestOutlineItem["at"]) {
  const file = from.closest<HTMLElement>('article[aria-label$=" diff"]');
  if (!file || !at) return;
  const fold = file.querySelector<HTMLElement>('header button[aria-expanded="false"]');
  const go = () => {
    const row = file.querySelector<HTMLElement>(`tr[data-${at.side}-line="${at.line}"]`);
    if (!row) return;
    row.scrollIntoView({ block: "center" });
    row.animate?.(
      [{ backgroundColor: "var(--surface-selected)" }, { backgroundColor: "transparent" }],
      { duration: 1200, easing: "ease-out" },
    );
  };
  if (!fold) return go();
  fold.click();
  requestAnimationFrame(go);
}

const lineCounts = (additions: number, deletions: number) => (
  <span className="font-mono text-xs whitespace-nowrap tabular-nums">
    {additions > 0 ? <span className="text-status-positive">+{additions}</span> : null}
    {additions > 0 && deletions > 0 ? " " : null}
    {deletions > 0 ? <span className="text-status-critical">−{deletions}</span> : null}
  </span>
);

/** The section's test files, each diff headed by the tests it adds,
 *  renames, changes or removes, by name, then the helpers and other lines it
 *  changes; a line above counts the tests by change. Read once the hunks the
 *  review left out are loaded. */
function TestFiles({
  document,
  files,
  context,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  context: ReviewPlaceContext;
}) {
  const read = useTestHunks(document, files, context.hunkPatches);
  const outline = useMemo(
    () => new Map((read ? testOutline(read) : []).map((file) => [file.path, file])),
    [read],
  );
  const tests = [...outline.values()]
    .flatMap((file) => file.items ?? [])
    .filter((item) => item.kind === "test");
  const counts = CHANGES.map((each) => ({
    ...each,
    count: tests.filter((test) => test.change === each.change).length,
  })).filter((each) => each.count > 0);
  return (
    <div className="space-y-3">
      {counts.length > 0 ? (
        <p aria-label="Test changes" className="text-xs text-secondary tabular-nums">
          Tests:{" "}
          {counts.map((each, index) => (
            <span key={each.change}>
              {index > 0 ? " · " : null}
              <span className={each.tone}>
                {each.count} {each.label}
              </span>
            </span>
          ))}
        </p>
      ) : null}
      <SectionFiles
        document={document}
        files={files}
        context={context}
        intro={(file) => {
          const read = outline.get(file.path);
          return read ? <FileTests file={read} /> : null;
        }}
      />
    </div>
  );
}

const FILE_CAPTIONS: Partial<Record<TestOutlineFile["change"], string>> = {
  added: "New file",
  removed: "Deleted file",
};

/** A test file's tests, then its helpers and other lines, under its diff's
 *  header; nothing for a file that changes none. */
function FileTests({ file }: { file: TestOutlineFile }) {
  if (file.items?.length === 0) return null;
  if (file.items === null)
    return (
      <p className="px-3 py-1.5 text-xs text-tertiary">
        The tests in this file can't be read from its lines.
      </p>
    );
  const tests = file.items.filter((item) => item.kind === "test");
  const helpers = file.items.filter((item) => item.kind !== "test");
  const caption = FILE_CAPTIONS[file.change];
  return (
    <div className="py-1.5">
      {caption ? <p className="px-3 pb-0.5 text-xs text-tertiary">{caption}</p> : null}
      {tests.length > 0 ? (
        <OutlineList label={`Tests in ${file.path}`} noun="test" file={file.change} items={tests} />
      ) : null}
      {helpers.length > 0 ? (
        <div className={cn(tests.length > 0 && "mt-1 border-t border-subtle pt-1.5")}>
          <p className="px-3 text-xs text-tertiary">Helpers and setup</p>
          <OutlineList
            label={`Helpers in ${file.path}`}
            noun="helper"
            file={file.change}
            items={helpers}
          />
        </div>
      ) : null}
    </div>
  );
}

/** A file's list longer than this shows its first few, and the rest on
 *  request. */
const FOLD_AT = 8;
const FOLDED = 5;

function OutlineList({
  label,
  noun,
  file,
  items,
}: {
  label: string;
  noun: string;
  file: TestOutlineFile["change"];
  items: TestOutlineItem[];
}) {
  const [open, setOpen] = useState(false);
  const folded = items.length > FOLD_AT && !open;
  const shown = folded ? items.slice(0, FOLDED) : items;
  return (
    <div>
      <ul aria-label={label}>
        {shown.map((item) => (
          <OutlineItem key={`${item.kind}:${item.name}`} file={file} item={item} />
        ))}
      </ul>
      {items.length > FOLD_AT ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((was) => !was)}
          className={cn("ml-9 text-xs text-link hover:underline", FOCUS_RING)}
        >
          {open
            ? "Show fewer"
            : `Show ${items.length - FOLDED} more ${pluralize(noun, items.length - FOLDED)}`}
        </button>
      ) : null}
    </div>
  );
}

function OutlineItem({ file, item }: { file: TestOutlineFile["change"]; item: TestOutlineItem }) {
  const change = CHANGES.find((each) => each.change === item.change)!;
  const gone = item.change === "removed";
  // A new file's tests are all added and a deleted one's all removed: its
  // caption says so once, so the rows don't repeat it.
  const marked = !(
    (file === "added" && item.change === "added") ||
    (file === "removed" && item.change === "removed")
  );
  // A name that is a sentence (JavaScript's `it("…")`) reads as prose; an
  // identifier (Go's `TestX`, a helper) as code.
  const prose = item.kind === "other" || /\s/.test(item.name);
  return (
    <li data-change={item.change}>
      <button
        type="button"
        onClick={(event) => openInDiff(event.currentTarget, item.at)}
        className={cn(
          "grid w-full grid-cols-[1rem_minmax(0,1fr)_auto] items-baseline gap-x-2 px-3 py-0.5 text-left hover:bg-surface-hover",
          FOCUS_RING,
        )}
      >
        <span aria-hidden="true" className={cn("text-center font-mono text-xs", change.tone)}>
          {marked ? change.mark : null}
        </span>
        <span className="min-w-0">
          <span className="sr-only">{change.label}: </span>
          <span
            className={cn(
              "[overflow-wrap:anywhere]",
              prose ? "text-base" : "font-mono text-xs",
              item.kind === "test" ? "text-primary" : "text-secondary",
              gone && "text-tertiary line-through",
            )}
          >
            {item.name}
          </span>
          {item.was ? (
            <span className="block text-xs text-tertiary [overflow-wrap:anywhere]">
              renamed from <span className="line-through">{item.was}</span>
            </span>
          ) : null}
        </span>
        <span className="text-tertiary">
          {item.change === "changed" || item.change === "renamed"
            ? lineCounts(item.additions, item.deletions)
            : null}
        </span>
      </button>
    </li>
  );
}

/** The files with the hunks the review left out read from their patches;
 *  undefined while they load. Hunks that can't be loaded stay without code,
 *  and their files' tests unread. */
function useTestHunks(
  document: ReviewDocument,
  files: ViewFile[],
  source: HunkPatchSource | null,
): { path: string; hunks: IndexedHunk[] }[] | undefined {
  const version = document.review.version;
  const complete = files.every((file) => file.hunks.every((hunk) => hunk.diff));
  const [loaded, setLoaded] = useState<{ path: string; hunks: IndexedHunk[] }[] | undefined>(
    undefined,
  );
  const key = files.map((file) => file.path).join("\n");
  // biome-ignore lint/correctness/useExhaustiveDependencies: the files are read again only when their paths or the version change
  useEffect(() => {
    if (complete || !source) return;
    let current = true;
    setLoaded(undefined);
    Promise.all(
      files.map(async (file) => {
        const missing = file.hunks.find((hunk) => !hunk.diff);
        if (!missing) return file;
        const patch = await source(file.path, version, missing.id).catch(() => null);
        const hunks = file.hunks.map((hunk) => {
          if (hunk.diff || !patch) return hunk;
          const diff = indexedHunkFromFilePatch(patch, hunk);
          return diff ? { ...hunk, diff } : hunk;
        });
        return { path: file.path, hunks };
      }),
    ).then((all) => current && setLoaded(all));
    return () => {
      current = false;
    };
  }, [key, version, complete, source]);
  if (complete || !source) return files;
  return loaded;
}

function SectionFiles({
  document,
  files,
  context,
  intro,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  context: ReviewPlaceContext;
  /** Drawn under each file's header, above its code. */
  intro?: (file: ViewFile) => ReactNode;
}) {
  if (files.length === 0) return null;
  return (
    <div className="space-y-4">
      {files.map((file) => (
        <FileDiff
          key={file.path}
          document={document}
          file={file}
          context={context}
          intro={intro?.(file)}
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
  context,
  open: openFirst,
  reveal,
}: {
  document: ReviewDocument;
  files: ViewFile[];
  context: ReviewPlaceContext;
  open: boolean;
  reveal?: { path: string } | null;
}) {
  const [open, setOpen] = useState(openFirst);
  useEffect(() => {
    if (reveal && files.some((file) => file.path === reveal.path)) setOpen(true);
  }, [reveal, files]);
  return (
    <div className="space-y-3">
      <GeneratedToggle open={open} files={files.length} onToggle={() => setOpen((was) => !was)} />
      {open ? <SectionFiles document={document} files={files} context={context} /> : null}
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
