import { useMemo, useRef } from "react";
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
  reviewViewOf,
  selectionOf,
  shownFile,
  viewFiles,
  type ViewPlace,
} from "./model";
import { ViewPane, type ViewMemory } from "./view-pane";

export const TAB_LABEL = { overview: "Overview", sections: "How it works", code: "Code" } as const;

/** The review page's tabs as the review viewer's places: the Overview on the
 *  Summary, How it works on a section (the Changes and a risk open it too),
 *  and Code on the host's own `code` place. The rail lists the tabs, the
 *  sections under How it works, so the keys that step through the rail step
 *  through them; it starts folded, as the tabs lead. */
export function useViewPlaces(): ReviewPlaces {
  // The latest document the viewer named, for the chips of its prose, which
  // are resolved without a context.
  const latest = useRef<ReviewDocument | null>(null);
  const memory = useRef<ViewMemory>({ section: null, file: null });
  return useMemo<ReviewPlaces>(
    () => ({
      renderRail: (context) => <ViewRail context={context} />,
      railRows: (context) => railRows(context),
      steps: (selection, context) => {
        latest.current = context.document;
        return context.document ? stepsOf(selection, context.document, context) : null;
      },
      renderPane: (snapshot, historical, context) => (
        <ViewPane snapshot={snapshot} historical={historical} place={context} memory={memory} />
      ),
      jump: (section, item) => {
        const document = latest.current;
        if (!document) return null;
        // A chip naming a section, or an item of one, opens it in How it
        // works; a risk there, at the risk.
        const owner = document.sections.find((each) => each.id === section);
        if (owner && item && owner.risks.some((risk) => risk.id === item))
          return { kind: "risk", section, id: item };
        return { kind: "section", id: section };
      },
      // The tabs lead, so the rail starts folded on the page's own places;
      // the workspace's panes (Files, Sessions and the rest) keep it open.
      railFolded: onPage,
      sidePaneFolded: onPage,
      quietHistory: true,
    }),
    [],
  );
}

const onPage = (selection: Selection) =>
  ["summary", "changes", "risks", "section", "risk", "tile"].includes(selection.kind);

function railKey(place: ViewPlace): string {
  if (place.tab === "overview") return "overview";
  if (place.tab === "code") return "code";
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
  const list = listedSections(document, reviewViewOf(document));
  return [
    overview,
    ...list.map(
      (section): ReviewRailRow => ({
        key: `section:${section.id}`,
        label: section.title,
        go: go({ tab: "sections", section: section.id }),
      }),
    ),
    { key: "code", label: TAB_LABEL.code, go: go({ tab: "code", file: null }) },
  ];
}

/** The navigator bar's and the pane foot's way through the page: the
 *  Overview, the sections in the list's order, then each changed file. */
function stepsOf(
  selection: Selection,
  document: ReviewDocument,
  context: ReviewPlaceContext,
): ReviewPlaceSteps {
  const view = reviewViewOf(document);
  const place = placeOf(selection, document, view);
  const list = listedSections(document, view);
  const files = viewFiles(document);
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

/** The rail: the tabs, with the sections under How it works. */
function ViewRail({ context }: { context: ReviewPlaceContext }) {
  const document = context.document;
  if (!document) return null;
  const view = reviewViewOf(document);
  const place = placeOf(context.selection, document, view);
  const list = listedSections(document, view);
  const first = defaultSection(list);
  return (
    <>
      <SideNavButton
        label={TAB_LABEL.overview}
        icon={SummaryIcon}
        tint="accent"
        active={place.tab === "overview"}
        controls="review-section"
        onSelect={() => context.select({ kind: "summary" })}
      />
      <SideNavButton
        label={TAB_LABEL.sections}
        icon={SectionIcon}
        tint="accent"
        active={false}
        controls="review-section"
        onSelect={() => first && context.select({ kind: "section", id: first })}
      />
      <SideNavNest>
        {list.map((section) => (
          <SideNavButton
            key={section.id}
            label={section.title}
            depth={1}
            wrap
            active={place.tab === "sections" && place.section === section.id}
            controls="review-section"
            onSelect={() => context.select({ kind: "section", id: section.id })}
          />
        ))}
      </SideNavNest>
      <SideNavButton
        label={TAB_LABEL.code}
        icon={FilesIcon}
        tint="accent"
        active={place.tab === "code"}
        controls="review-section"
        onSelect={() => context.select(selectionOf({ tab: "code", file: null }))}
      />
    </>
  );
}
