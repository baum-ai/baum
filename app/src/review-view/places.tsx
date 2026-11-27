import { useMemo, useRef } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { CheckIcon, FilesIcon, SectionIcon, SummaryIcon } from "@baum/viewer/components/icons";
import type {
  ReviewPlaceContext,
  ReviewPlaces,
  ReviewPlaceSteps,
  ReviewRailRow,
} from "@baum/viewer/components/review-viewer";
import { SideNavButton, SideNavNest } from "@baum/viewer/components/side-nav";
import {
  RISK_SCORE_MEANING,
  type ReviewDocument,
  type RiskScoreLevel,
} from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import { useReadMarks } from "./read-marks";
import {
  CODE_PLACE,
  CONTEXT_ID,
  defaultSection,
  groupFiles,
  listedSections,
  placeOf,
  reviewOf,
  selectionOf,
  shownFile,
  viewFiles,
  type FileGrouping,
  type ListedSection,
  type ViewFile,
  type ViewPlace,
} from "./model";
import { readFileGrouping } from "./file-grouping";
import { ViewPane } from "./view-pane";

const TAB_LABEL = {
  overview: "Overview",
  sections: "How it works",
  code: "Files changed",
} as const;

/** The review page's places for the review viewer: the Overview on the
 *  Summary, How it works on a section (the Changes and a risk open it too),
 *  and Files changed on the host's own `code` place. The viewer's rail leads
 *  through them: Overview, How it works with its sections nested, Files
 *  changed, then Decisions, Sessions and Commits as plain rows (`flatRail`)
 *  that open a pane listing their items. The keys that step through the rail
 *  step through the same rows, and through the sections and files. */
export function useViewPlaces(): ReviewPlaces {
  // The latest document the viewer named, for the chips of its prose, which
  // are resolved without a context.
  const latest = useRef<ReviewDocument | null>(null);
  return useMemo<ReviewPlaces>(
    () => ({
      flatRail: true,
      ownFiles: true,
      renderRail: (context) => <ViewRail context={context} />,
      railRows: (context) => railRows(context),
      steps: (selection, context) => {
        latest.current = context.document;
        return context.document
          ? stepsOf(selection, context.document, context)
          : emptySteps(selection, context);
      },
      renderPane: (snapshot, historical, context) => (
        <ViewPane snapshot={snapshot} historical={historical} place={context} />
      ),
      jump: (section, item) => {
        const document = latest.current;
        if (!document) return null;
        // A chip naming a section, or an item of one, opens it in How it
        // works; a risk there, at the risk.
        const owner = reviewOf(document).sections.find((each) => each.id === section);
        if (owner && item && owner.risks.some((risk) => risk.id === item))
          return { kind: "risk", section, id: item };
        return { kind: "section", id: section };
      },
      // The side pane starts folded on the review's own places, which want
      // the width; on a session, the commits or the decisions it is as the
      // reader left it.
      sidePaneFolded: onPage,
      quietHistory: true,
    }),
    [],
  );
}

const onPage = (selection: Selection) =>
  ["summary", "changes", "risks", "section", "risk", "tile"].includes(selection.kind);

/** The rail row a place sits on. */
function railKey(place: ViewPlace): string {
  if (place.tab === "overview") return "overview";
  if (place.tab === "code") return "files";
  return `section:${place.section}`;
}

function railRows(context: ReviewPlaceContext): ReviewRailRow[] {
  const document = context.document;
  const go = (place: ViewPlace) => () => context.select(selectionOf(place));
  const overview: ReviewRailRow = {
    key: "overview",
    label: TAB_LABEL.overview,
    go: go({ tab: "overview" }),
  };
  if (!document)
    return context.files
      ? [overview, { key: "files", label: TAB_LABEL.code, go: () => context.select(FILES) }]
      : [overview];
  const list = listedSections(reviewOf(document));
  return [
    overview,
    ...list.map(
      (section): ReviewRailRow => ({
        key: `section:${section.id}`,
        label: section.title,
        go: go({ tab: "sections", section: section.id }),
      }),
    ),
    { key: "files", label: TAB_LABEL.code, go: go({ tab: "code", file: null }) },
  ];
}

/** The rail: the Overview, How it works with the sections under it, and Files
 *  changed. The viewer follows with Decisions, Sessions and Commits. */
function ViewRail({ context }: { context: ReviewPlaceContext }) {
  const document = context.document;
  const { read } = useReadMarks(document);
  if (!document) return <EmptyRail context={context} />;
  const review = reviewOf(document);
  // A session, a commit or the decisions are none of the page's places.
  const place = onPage(context.selection) ? placeOf(context.selection, review) : null;
  const list = listedSections(review);
  const first = defaultSection(list);
  const count = viewFiles(review).length;
  const go = (next: ViewPlace) => () => context.select(selectionOf(next));
  return (
    <>
      <SideNavButton
        label={TAB_LABEL.overview}
        icon={SummaryIcon}
        tint="accent"
        active={place?.tab === "overview"}
        controls="review-section"
        onSelect={go({ tab: "overview" })}
      />
      {first ? (
        <>
          {/* The group's own row opens its first section, and is never the
              row the reader is on: that is the section. */}
          <SideNavButton
            label={TAB_LABEL.sections}
            icon={SectionIcon}
            tint="accent"
            active={false}
            controls="review-section"
            onSelect={go({ tab: "sections", section: first })}
          />
          <SideNavNest id="review-sections">
            {list.map((section, at) => (
              <div
                key={section.id}
                title={
                  section.obsolete ? "No longer in the pull request's latest commits" : undefined
                }
                className={cn(section.obsolete && "opacity-60 grayscale")}
              >
                {/* The routine sections follow the ones that want a look,
                    under their own quiet label. */}
                {section.routine && !list[at - 1]?.routine ? (
                  <p
                    data-rail-group=""
                    className="px-2 pt-2 pb-0.5 text-xs font-medium tracking-[0.08em] text-tertiary uppercase"
                  >
                    Routine
                  </p>
                ) : null}
                <SideNavButton
                  label={section.title}
                  wrap
                  depth={1}
                  active={place?.tab === "sections" && place.section === section.id}
                  controls="review-section"
                  aside={
                    section.level || read.has(section.id) ? (
                      <SectionMark level={section.level} read={read.has(section.id)} />
                    ) : undefined
                  }
                  description={
                    [
                      section.level
                        ? `Level ${section.level} of 5: ${RISK_SCORE_MEANING[section.level]}`
                        : "",
                      read.has(section.id) ? "Read" : "",
                    ]
                      .filter(Boolean)
                      .join(". ") || undefined
                  }
                  onSelect={go({ tab: "sections", section: section.id })}
                />
              </div>
            ))}
          </SideNavNest>
        </>
      ) : null}
      <SideNavButton
        label={TAB_LABEL.code}
        icon={FilesIcon}
        tint="accent"
        active={place?.tab === "code"}
        count={count}
        controls="review-section"
        onSelect={go({ tab: "code", file: null })}
      />
    </>
  );
}

/** A section's marks at the end of its rail row: a tick once the reader has
 *  been through it, and how closely to read it as five steps, the filled
 *  ones in the level's colour (4 and 5 stand out; the rest stay quiet). */
function SectionMark({ level, read }: { level?: RiskScoreLevel; read: boolean }) {
  return (
    <span className="flex items-center gap-1.5">
      {read ? (
        <span data-rail-read="" className="flex text-tertiary">
          <CheckIcon size={12} />
        </span>
      ) : null}
      {level ? (
        <span
          data-rail-level={level}
          className={cn(
            "flex gap-0.5",
            level === 5
              ? "text-status-critical"
              : level === 4
                ? "text-status-caution"
                : "text-secondary",
          )}
        >
          {[1, 2, 3, 4, 5].map((step) => (
            <span
              key={step}
              className={cn("h-2.5 w-1 rounded-[1px] bg-current", step > level && "opacity-20")}
            />
          ))}
        </span>
      ) : null}
    </span>
  );
}

/** The viewer's own Files changed, which lists the changed files the pull
 *  request's facts name while there is no review to place them in. */
const FILES: Selection = { kind: "files" };

const onFiles = (selection: Selection) =>
  selection.kind === "files" ||
  selection.kind === "file" ||
  (selection.kind === "tile" && selection.id === CODE_PLACE);

/** The rail before there is a review (one queued, being written with no
 *  draft yet, failed, or never asked for): the same rows, the Overview
 *  saying where the review is, How it works with nothing under it yet, and
 *  Files changed listing the pull request's files, where they can be read. */
function EmptyRail({ context }: { context: ReviewPlaceContext }) {
  const files = onFiles(context.selection);
  return (
    <>
      <SideNavButton
        label={TAB_LABEL.overview}
        icon={SummaryIcon}
        tint="accent"
        active={!files && onPage(context.selection)}
        controls="review-section"
        onSelect={() => context.select({ kind: "summary" })}
      />
      <SideNavButton
        label={TAB_LABEL.sections}
        icon={SectionIcon}
        tint="accent"
        active={false}
        disabled
        onSelect={() => undefined}
      />
      <SideNavNest id="review-sections">
        <p className="px-2.5 py-1.5 text-base text-tertiary">No sections yet.</p>
      </SideNavNest>
      {context.files ? (
        <SideNavButton
          label={TAB_LABEL.code}
          icon={FilesIcon}
          tint="accent"
          active={files}
          count={context.files.count ?? undefined}
          controls="review-section"
          onSelect={() => context.select(FILES)}
        />
      ) : null}
    </>
  );
}

/** The navigator bar's way through the page before there is a review: the
 *  Overview, then Files changed. */
function emptySteps(selection: Selection, context: ReviewPlaceContext): ReviewPlaceSteps {
  const overview = { label: TAB_LABEL.overview, go: () => context.select({ kind: "summary" }) };
  const files = context.files ? { label: TAB_LABEL.code, go: () => context.select(FILES) } : null;
  if (onFiles(selection))
    return {
      label: TAB_LABEL.code,
      icon: FilesIcon,
      previous: overview,
      next: null,
      railKey: "files",
    };
  return {
    label: selection.kind === "summary" ? TAB_LABEL.overview : TAB_LABEL.sections,
    icon: selection.kind === "summary" ? SummaryIcon : SectionIcon,
    previous: null,
    next: files,
    railKey: "overview",
  };
}

/** The navigator bar's and the pane foot's way through the page: the
 *  Overview, the sections in the list's order, then each changed file. */
function stepsOf(
  selection: Selection,
  document: ReviewDocument,
  context: ReviewPlaceContext,
): ReviewPlaceSteps {
  const review = reviewOf(document);
  const place = placeOf(selection, review);
  const list = listedSections(review);
  const files = viewFiles(review);
  const stops = [
    { key: "overview", label: TAB_LABEL.overview, place: { tab: "overview" } as ViewPlace },
    ...list.map((section) => ({
      key: `section:${section.id}`,
      label: section.title,
      place: { tab: "sections", section: section.id } as ViewPlace,
    })),
    ...files.map((file) => ({
      key: `file:${file.path}`,
      label: file.path.slice(file.path.lastIndexOf("/") + 1),
      place: { tab: "code", file: file.path } as ViewPlace,
    })),
  ];
  const key =
    place.tab === "code"
      ? `file:${shownFile(files, place.file)?.path ?? ""}`
      : place.tab === "sections"
        ? `section:${place.section}`
        : "overview";
  const at = stops.findIndex((stop) => stop.key === key);
  const step = (stop: (typeof stops)[number] | undefined) =>
    stop ? { label: stop.label, go: () => context.select(selectionOf(stop.place)) } : null;
  const sections = stops.filter((stop) => stop.key.startsWith("section:")).length;
  // On Files changed, j and k open the next and previous file of the list in
  // the order it shows (the grouping on the page; a file listed under several
  // sections, by its first), and stop at its ends.
  const item = place.tab === "code" ? fileItemSteps(files, place.file, list, context) : undefined;
  return {
    ...(item ? { item } : {}),
    label:
      place.tab === "sections"
        ? (list.find((section) => section.id === place.section)?.title ?? TAB_LABEL.sections)
        : place.tab === "code"
          ? TAB_LABEL.code
          : TAB_LABEL[place.tab],
    icon: place.tab === "code" ? FilesIcon : place.tab === "sections" ? SectionIcon : SummaryIcon,
    position:
      place.tab === "sections" && at > 0
        ? `${at} of ${sections}`
        : place.tab === "code" && at > sections
          ? `${at - sections} of ${files.length}`
          : undefined,
    previous: at > 0 ? step(stops[at - 1]) : null,
    next: at >= 0 ? step(stops[at + 1]) : null,
    railKey: railKey(place),
    sectionId: place.tab === "sections" && place.section !== CONTEXT_ID ? place.section : undefined,
  };
}

/** The changed files in the order the file list shows them, each once. */
function listedFiles(files: ViewFile[], grouping: FileGrouping, list: ListedSection[]): ViewFile[] {
  const seen = new Set<string>();
  return groupFiles(files, grouping, list)
    .flatMap((group) => group.files)
    .filter((file) => !seen.has(file.path) && seen.add(file.path));
}

/** The steps are read when a key is pressed, so a grouping switched on the
 *  page orders them without the viewer drawing again. */
function fileItemSteps(
  files: ViewFile[],
  open: string | null,
  list: ListedSection[],
  context: ReviewPlaceContext,
): NonNullable<ReviewPlaceSteps["item"]> {
  const around = (offset: -1 | 1, label: string) => {
    const ordered = listedFiles(files, readFileGrouping(), list);
    const shown = shownFile(files, open);
    const at = shown ? ordered.findIndex((file) => file.path === shown.path) : -1;
    const file = at < 0 ? undefined : ordered[at + offset];
    return file
      ? { label, go: () => context.select(selectionOf({ tab: "code", file: file.path })) }
      : null;
  };
  return {
    get previous() {
      return around(-1, "Previous file");
    },
    get next() {
      return around(1, "Next file");
    },
  };
}
