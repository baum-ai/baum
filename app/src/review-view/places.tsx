import { useMemo, useRef } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { FilesIcon, SectionIcon, SummaryIcon } from "@baum/viewer/components/icons";
import type {
  ReviewPlaceContext,
  ReviewPlaces,
  ReviewPlaceSteps,
  ReviewRailRow,
} from "@baum/viewer/components/review-viewer";
import { SideNavButton, SideNavNest } from "@baum/viewer/components/side-nav";
import type { ReviewDocument } from "@baum/viewer/review-document";
import type { Selection } from "@baum/viewer/review-selection";
import {
  CONTEXT_ID,
  defaultSection,
  listedSections,
  placeOf,
  reviewOf,
  selectionOf,
  shownFile,
  viewFiles,
  type ViewPlace,
} from "./model";
import { ViewPane } from "./view-pane";

export const TAB_LABEL = {
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
        return context.document ? stepsOf(selection, context.document, context) : null;
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
  if (!document) return [overview];
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
  if (!document) return null;
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
            {list.map((section) => (
              <div key={section.id} className={cn(section.obsolete && "opacity-60 grayscale")}>
                <SideNavButton
                  label={section.title}
                  wrap
                  depth={1}
                  active={place?.tab === "sections" && place.section === section.id}
                  controls="review-section"
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
  return {
    label:
      place.tab === "sections"
        ? (list.find((section) => section.id === place.section)?.title ?? TAB_LABEL.sections)
        : place.tab === "code"
          ? "Files"
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
