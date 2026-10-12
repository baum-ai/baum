import pluralize from "pluralize";
import {
  type CSSProperties,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { useUrlState } from "../navigation";
import {
  checkStatus,
  type LoadReviewVersion,
  type ReviewDocument,
  RISK_SCORE_MEANING,
  riskCategory,
  riskLabel,
  type Section,
  sectionBadgeState,
  sectionsInReviewOrder,
  type TriageRisk,
  triageRisks,
} from "../review-document";
import type { Selection } from "../review-selection";
import { reviewVersionChanges } from "../review-versions";
import { riskKey } from "../row-keys";
import { useConversationCollapsed } from "../use-conversation-collapsed";
import { type ConversationTab, useConversationTab } from "../use-conversation-tab";
import { RAIL_LIMITS, useConversationWidth, useRailWidth } from "../use-conversation-width";
import { useReviewSelection } from "../use-review-selection";
import { useHistoryView } from "../use-review-version";
import { useSectionHistory } from "../use-section-history";
import { cn } from "./classnames";
import { ReviewFilesProvider } from "./code-file-link";
import { HunkPatchProvider, type HunkPatchSource } from "./hunk-evidence";
import {
  ChecksIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CloseIcon,
  CommitIcon,
  FilesIcon,
  RiskIcon,
  SectionIcon,
  SessionsIcon,
  SummaryIcon,
  TeamChatIcon,
} from "./icons";
import { PaneDivider } from "./pane-divider";
import { ReviewActivity, ReviewDetails } from "./review-details";
import { ChangesOverview, RisksOverview } from "./review-groups";
import {
  REVIEW_BAR_PX,
  REVIEW_GUTTER,
  ReviewFreshness,
  ReviewHeader,
  type ReviewHeaderSlots,
} from "./review-header";
import { HistoryToolbar, ReviewHistory } from "./review-history";
import { ReviewNavigator, ReviewPaneFoot, type ReviewStep } from "./review-navigator";
import { ReviewScoreBadge, TierDot } from "./review-parts";
import { ReviewRisk } from "./review-risk";
import { ReviewSection } from "./review-section";
import { ReviewSummary, type SummaryDestination } from "./review-summary";
import { SideNavButton, SideNavDisclosure, SideNavGroup, SideNavNest } from "./side-nav";
import { Empty, Failed, Skeleton } from "./states";
import { tabPanelProps } from "./tabs";
import { Alert, Badge, Button, CardHeader, IconButton, SegmentedControl, Spinner } from "./ui";

/** The history view a host drives itself, when it needs the version being
 *  read for its own requests: the return of `useHistoryView`. */
export type HistoryView = ReturnType<typeof useHistoryView>;

/** A rail row's read mark or approval, and whether the row counts as read.
 *  The viewer draws the controls only on the current version; whether a row
 *  is read tints it on every version, as the reader's progress through the
 *  review does not depend on which version they are looking at. */
export type ReviewChecks = {
  renderSection: (section: { id: string; label: string }) => ReactNode;
  renderRisk: (risk: TriageRisk) => ReactNode;
  sectionRead: (id: string) => boolean;
  riskRead: (risk: TriageRisk) => boolean;
  /** Leaves the "3 of 6" out of the Changes row's name while the host cannot
   *  vouch for it; the bar under the row stays. */
  hideSectionProgress?: boolean;
  /** Shown under the Summary row on the current version, such as a failed
   *  save. */
  notice?: ReactNode;
  /** Shown under the rail's rows on every version. */
  footer?: ReactNode;
};

/** A rail group a host fills: the pull request's linked sessions or its
 *  commits. The rail lists the items; the pane is the host's. */
export type ReviewListState =
  | { status: "loading" }
  | { status: "failed" }
  | { status: "ready"; items: { id: string; label: string }[] };

export type ReviewListSlot = {
  state: ReviewListState;
  /** The pane for the whole list (`selected` null) or for one item. */
  renderPane: (selected: string | null, actions: ReviewViewerActions) => ReactNode;
};

/** What a host's pane may ask the viewer to do. */
export type ReviewViewerActions = {
  select: (selection: Selection) => void;
  /** Opens a changed file in the Files pane, by its id. */
  openFile: (id: string) => void;
};

/** A changed file of the pull request, as far as the rail needs to know. */
export type ReviewFile = { id: string; path: string; old_path?: string; viewed: boolean };

/** The line a file pane should scroll to and mark. */
export type FileLineFocus = { side: "LEFT" | "RIGHT"; line: number } | null;

/** The file the Files pane should unfold and bring into view. `at` tells a
 *  repeated open of the same file (a rail click on it after it was folded)
 *  from the last; a file reached through the URL alone opens with `at` 0. */
export type FileOpenRequest = { id: string; at: number } | null;

/** The pull request's changed files, which the host loads and draws: the
 *  rail's tree and the pane listing every file's diff, all folded except the
 *  one `open` names. Null `files` until the first load answers. A host that loads files for one review version at a time should
 *  load none while two versions are compared. */
export type ReviewFilesSlot<F extends ReviewFile = ReviewFile> = {
  files: F[] | null;
  loading: boolean;
  failed: boolean;
  retry: () => void;
  /** False when the host keeps no viewed marks, so the rail counts no
   *  progress through the files. */
  tracksViewed?: boolean;
  renderTree: (props: {
    files: F[];
    selected: string | null;
    onSelect: (file: F) => void;
  }) => ReactNode;
  renderFiles: (props: {
    files: F[];
    /** The file to unfold and scroll to, or null for all folded. */
    open: FileOpenRequest;
    /** The line to scroll to in the open file. */
    focus: FileLineFocus;
  }) => ReactNode;
};

/** The Details tab's Checks part as a host draws it, and whether it shows a
 *  failure, which flags the tab as the document's own failing checks do. */
export type ReviewDetailsChecks = { content: ReactNode; failing: boolean };

/** What the side pane's first tab is told about where the reader is. */
export type ReviewConversationContext = {
  /** The document being read: the earlier version when one is read alone,
   *  else the current one. */
  document: ReviewDocument | null;
  /** The section the selection names, directly or through one of its
   *  risks, whether or not the rail lists it. */
  sectionId: string | null;
  /** The same section when the rail lists it. */
  viewedSectionId: string | null;
  riskId?: string;
  /** An earlier version is on screen, alone or compared. */
  historical: boolean;
  /** The earlier version read alone, if one is. */
  version?: number;
  /** Opens a changed file at a line. Does nothing without files. */
  revealLine: (path: string, side: "LEFT" | "RIGHT", line: number) => void;
};

export type ReviewViewerProps<F extends ReviewFile = ReviewFile> = {
  /** The current review. Null while a host has none to show; `placeholder`
   *  then fills the pane. */
  document: ReviewDocument | null;
  /** The earlier versions that can be read or compared, newest first. */
  versions?: number[];
  /** Loads one earlier version. Required to read any of `versions`. */
  loadVersion?: LoadReviewVersion;
  /** The host's own history view, from `useHistoryView` with the same
   *  loader, when it needs the version being read. Otherwise the viewer
   *  keeps its own. */
  history?: HistoryView;
  /** The version list could not be loaded; the pane offers a retry. */
  versionsFailed?: { retry: () => void };
  header?: ReviewHeaderSlots;
  /** The pane's content while there is no document. */
  placeholder?: ReactNode;
  /** The Details and Activity tabs' content while there is no document. */
  asidePlaceholder?: ReactNode;
  /** Rail rows to list while there is no document. */
  fallbackSections?: { id: string; label: string }[];
  checks?: ReviewChecks;
  /** Drawn at the top of the pane while a section, or one of its risks, is
   *  read on the current version. */
  renderSectionAside?: (sectionId: string) => ReactNode;
  /** Drawn on a section's line of badges, in its pane and in the Changes
   *  overview, on whichever version is read: what the host knows of the
   *  section, such as the pull requests a stack review's section covers.
   *  Called with the document the section belongs to. */
  renderSectionLabels?: (section: Section, document: ReviewDocument) => ReactNode;
  /** Drawn at the top of the pane on every selection, such as the state of
   *  the review's generation. */
  notices?: ReactNode;
  files?: ReviewFilesSlot<F>;
  sessions?: ReviewListSlot;
  commits?: ReviewListSlot;
  /** The side pane's first tab, such as a discussion. Absent, the side pane
   *  holds only the document's details and activity. */
  conversation?: (context: ReviewConversationContext) => ReactNode;
  /** Replaces the Details tab's Checks part for the document it describes,
   *  such as checks gathered from elsewhere; null content leaves the part
   *  out. */
  detailsChecks?: (document: ReviewDocument) => ReviewDetailsChecks;
  /** Leaves out the side pane's Activity tab. */
  hideActivity?: boolean;
  /** Puts the cursor in the conversation's text box once the side pane has
   *  unfolded on its tab, when the reader asks to discuss the review. */
  focusConversation?: (pane: HTMLElement) => void;
  /** Loads the patch for cited code whose excerpt the document omitted. */
  hunkPatches?: HunkPatchSource | null;
  /** Called with the versions whose documents are on screen, each time that
   *  set changes. */
  onVersionsShown?: (versions: number[]) => void;
};

const NO_VERSIONS: number[] = [];
const noVersion: LoadReviewVersion = async () => ({ status: "missing" });

/** A whole review page: the head, the version menu, the rail of Summary,
 *  Changes, Risks and whatever groups the host fills, the pane, and the side
 *  pane. Given a document alone it renders that document and nothing a host
 *  would have to supply. Needs a `UrlStateProvider` above it. */
export function ReviewViewer<F extends ReviewFile = ReviewFile>(props: ReviewViewerProps<F>) {
  return (
    <div data-review-page className="flex flex-col">
      <ReviewHeader document={props.document} slots={props.header} />
      {/* No foot from xl: the rail and the side pane fill the window to its
        bottom edge, and a margin under the workspace would push them up by
        its height at the end of the page. */}
      <div className={cn(REVIEW_GUTTER, "flex flex-1 flex-col pt-3 pb-4 xl:pb-0")}>
        {props.history ? (
          <ReviewWorkspace {...props} view={props.history} />
        ) : (
          <OwnHistory {...props} />
        )}
      </div>
    </div>
  );
}

function OwnHistory<F extends ReviewFile>(props: ReviewViewerProps<F>) {
  const view = useHistoryView(props.document?.review.version, props.loadVersion ?? noVersion);
  return <ReviewWorkspace {...props} view={view} />;
}

/** How wide a pane's content may run, centred in the pane. The Summary set
 *  this measure, and every pane keeps to it so that switching between them
 *  does not move the text. */
const PANE_WIDTH = "mx-auto w-full max-w-[1480px]";

/** Keeps `--review-visible` on the element at the height of the window below
 *  its top, or below the bar once it has scrolled under it. A sticky column
 *  that height ends at the window's bottom however much of the head is still
 *  showing, so the composer is never below the fold; CSS alone can only size
 *  it for the head being gone. */
function useVisibleHeight<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      // The columns start inside the workspace's border, not at its edge.
      const top = Math.max(element.getBoundingClientRect().top + element.clientTop, REVIEW_BAR_PX);
      element.style.setProperty("--review-visible", `${window.innerHeight - top}px`);
    };
    const schedule = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    measure();
    window.addEventListener("scroll", schedule, { passive: true });
    window.addEventListener("resize", schedule);
    // The head grows as its facts arrive, which moves the top without a scroll.
    const resized = new ResizeObserver(schedule);
    resized.observe(document.body);
    return () => {
      resized.disconnect();
      cancelAnimationFrame(frame);
      window.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
    };
  }, []);
  return ref;
}

/** What a rail group says when it holds nothing, or while it is still
 *  finding out. */
function RailNote({ children, busy = false }: { children: string; busy?: boolean }) {
  return (
    <p className="flex items-center gap-2 px-2.5 py-1.5 text-base text-(--text-tertiary)">
      {busy ? <Spinner size={11} /> : null}
      {children}
    </p>
  );
}

/** The pane's content for one selection. Keyed by the caller on what is
 *  selected, so a change of selection replays the arrival and a re-render of
 *  the same selection does not. A flex column, because what it wraps is a
 *  header and a scroller that expect to be laid out by one. */
function PaneIn({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-0 flex-1 flex-col animate-pane-in motion-reduce:animate-none">
      {children}
    </div>
  );
}

function ReviewWorkspace<F extends ReviewFile>({
  document,
  versions = NO_VERSIONS,
  loadVersion = noVersion,
  view: historyView,
  versionsFailed,
  header,
  placeholder,
  asidePlaceholder,
  fallbackSections,
  checks,
  renderSectionAside,
  renderSectionLabels,
  notices,
  files: filesSlot,
  sessions,
  commits,
  conversation,
  detailsChecks,
  hideActivity = false,
  focusConversation,
  hunkPatches = null,
  onVersionsShown,
}: ReviewViewerProps<F> & { view: HistoryView }) {
  const [requested, setSelection] = useReviewSelection();
  // A place whose group the host has not supplied reads as the Summary. The
  // URL keeps what it asked for, so a host that mounts the slot later (or a
  // link shared from a host that has it) still lands there, and nothing is
  // written to the history on the reader's behalf.
  const hasSessions = Boolean(sessions);
  const hasCommits = Boolean(commits);
  const hasFiles = Boolean(filesSlot);
  const selection = useMemo<Selection>(
    () =>
      ((requested.kind === "sessions" || requested.kind === "session") && !hasSessions) ||
      ((requested.kind === "commits" || requested.kind === "commit") && !hasCommits) ||
      ((requested.kind === "files" || requested.kind === "file") && !hasFiles)
        ? { kind: "summary" }
        : requested,
    [requested, hasSessions, hasCommits, hasFiles],
  );
  // Tiles are not ready for readers yet, so they stay hidden unless the URL
  // asks for them with `?tiles=1`, which keeps them reachable while built.
  const { search } = useUrlState();
  const showTiles = search.get("tiles") === "1";
  // A section or risk being read has to stay visible in the rail, however
  // the selection arrived (a click, Back, a shared link). File diffs are
  // different: the tree may fold while the selected diff stays in the pane,
  // then reopen on the same file. Files, Sessions and Commits start folded: a
  // tree of paths and a list of sessions under the sections is a wall, and a
  // reader reaches for either once the review has said where to look. Changes
  // and Risks stay open.
  const [folded, setFolded] = useState({
    changes: false,
    risks: false,
    files: true,
    sessions: true,
    commits: true,
  });
  const open = {
    changes: !folded.changes || selection.kind === "section",
    risks: !folded.risks || selection.kind === "risk",
    files: !folded.files,
    sessions: !folded.sessions || selection.kind === "session",
    commits: !folded.commits || selection.kind === "commit",
  };
  const reviewPane = useRef<HTMLElement>(null);
  const conversationPane = useRef<HTMLElement>(null);
  const railPane = useRef<HTMLElement>(null);
  const navigator = useRef<HTMLDivElement>(null);
  const scrim = useRef<HTMLButtonElement>(null);
  // Where the panes stack (below xl) the rail and the side pane are sheets
  // over the page, opened from the navigator bar: the rail rather than a
  // screen of rows above the pane, the side pane rather than a second page
  // under it, whose start a reader scrolling the review cannot see coming.
  const [sheet, setSheet] = useState<"rail" | "aside" | null>(null);
  const sheetOpen = sheet === "rail";
  const asideOpen = sheet === "aside";
  const workspace = useVisibleHeight<HTMLDivElement>();
  const split = useConversationWidth();
  const rail = useRailWidth();
  // The side pane can fold to a strip at the window's edge, so the review
  // pane has the width when the talk is not the point.
  const [chatCollapsed, setChatCollapsed] = useConversationCollapsed();
  // Which of its tabs the side pane shows: the host's conversation, the pull
  // request's details, or how it came about. Without a conversation the
  // details stand in for it.
  const [storedTab, setBarTab] = useConversationTab();
  const barTab: ConversationTab =
    (!conversation && storedTab === "chat") || (hideActivity && storedTab === "activity")
      ? "details"
      : storedTab;
  /** Opens a tab of the side pane, unfolding it if it was folded. */
  const openBarTab = (tab: ConversationTab) => {
    setBarTab(tab);
    setChatCollapsed(false);
    setSheet("aside");
  };
  // "Discuss this PR" asks for the composer; the focus waits for the render
  // that unfolds the side pane, since a folded pane hides the text box.
  const [composerFocus, setComposerFocus] = useState(0);
  const focusComposer = useRef(focusConversation);
  focusComposer.current = focusConversation;
  useEffect(() => {
    const pane = conversationPane.current;
    if (composerFocus === 0 || !pane) return;
    focusComposer.current?.(pane);
  }, [composerFocus]);
  const sectionHistory = useSectionHistory(document, versions, loadVersion);
  const historicalDocument =
    historyView.state?.status === "ready" ? historyView.state.document : null;
  const historicalOnly = Boolean(historyView.selection && !historyView.selection.compare);
  const navigationDocument = historicalOnly ? historicalDocument : document;
  const comparing = Boolean(historyView.selection?.compare);
  const fileVersion = historicalOnly ? historyView.selection?.version : undefined;
  const fileList = filesSlot?.files ?? null;
  const filesSelected = selection.kind === "files" || selection.kind === "file";
  const selectedFile =
    selection.kind === "file" ? fileList?.find((file) => file.id === selection.id) : undefined;
  const [diffFocus, setDiffFocus] = useState<FileLineFocus>(null);
  // A click on a file already selected (folded since, say) has to open it
  // again, so a click is stamped with when it was made. A file reached through
  // the URL alone, a shared link or Back, opens with `at` 0.
  const [fileClick, setFileClick] = useState<FileOpenRequest>(null);
  const selectedFileID = selection.kind === "file" ? selection.id : null;
  const fileOpen = useMemo<FileOpenRequest>(
    () =>
      selectedFileID === null
        ? null
        : fileClick?.id === selectedFileID
          ? fileClick
          : { id: selectedFileID, at: 0 },
    [selectedFileID, fileClick],
  );
  const sessionState = sessions?.state;
  const commitState = commits?.state;
  // A session id the pull request no longer lists — a stale link, a session
  // unlinked since — opens the list rather than a pane of closed cards.
  useEffect(() => {
    if (selection.kind !== "session" || sessionState?.status !== "ready") return;
    if (!sessionState.items.some((item) => item.id === selection.id))
      setSelection({ kind: "sessions" });
  }, [selection, sessionState, setSelection]);
  // The same for a commit sha the pull request no longer lists — a force
  // push since the link was made.
  const selectedCommit =
    selection.kind === "commit" && commitState?.status === "ready"
      ? commitState.items.find((item) => item.id === selection.id)
      : undefined;
  useEffect(() => {
    if (selection.kind !== "commit" || commitState?.status !== "ready") return;
    if (!selectedCommit) setSelection({ kind: "commits" });
  }, [selection, commitState, selectedCommit, setSelection]);
  useEffect(() => {
    if (selection.kind !== "file" || !fileList || selectedFile) return;
    setSelection({ kind: "files" });
  }, [selection, selectedFile, fileList, setSelection]);
  const changes =
    comparing && document && historicalDocument
      ? reviewVersionChanges(document, historicalDocument)
      : null;
  // In the order the Changes pane lists them, so the rail and the pane agree.
  const navigationSections = [...sectionsInReviewOrder(navigationDocument?.sections ?? [])];
  if (comparing && historicalDocument) {
    for (const section of historicalDocument.sections) {
      if (!navigationSections.some((item) => item.id === section.id))
        navigationSections.push(section);
    }
  }
  // The rail lists the review's own sections. Without a document it falls back
  // to the sections the host knows, so whatever hangs off them stays
  // reachable.
  const sections = document
    ? navigationSections.map((section) => ({
        id: section.id,
        label: section.title,
        tier: section.derived.risk_tier,
        stale: section.derived.state === "stale",
        // A section only the compared version has sits after the sorted ones,
        // so its old score would read as out of order; it shows none.
        score: navigationDocument?.sections.some((item) => item.id === section.id)
          ? section.derived.review_score
          : undefined,
      }))
    : (fallbackSections ?? []).map((section) => ({
        ...section,
        tier: null,
        stale: false,
        score: undefined,
      }));
  // An obsolete section counts as absent from the current version, so it
  // takes no part in the rail's progress and offers no box to tick.
  const currentSections = sections.filter((section) => !section.stale);
  // Critical and high risk: the same set, in the same order, the summary page
  // leads with. The lower tiers stay there rather than being restated here.
  const triage = navigationDocument ? triageRisks(navigationDocument) : null;
  const historicalTriage = comparing && historicalDocument ? triageRisks(historicalDocument) : null;
  const risks = [...(triage?.lead ?? [])];
  if (historicalTriage) {
    for (const risk of historicalTriage.lead) {
      if (!risks.some((item) => item.section === risk.section && item.id === risk.id))
        risks.push(risk);
    }
  }
  // Every version whose document is on screen counts as shown: the current
  // one unless an earlier version replaces it, and the earlier one once loaded.
  const shownVersions = [
    historicalOnly ? undefined : document?.review.version,
    historicalDocument?.review.version,
  ].filter((version) => version !== undefined);
  const shownKey = shownVersions.join(",");
  const versionsShown = useRef(onVersionsShown);
  versionsShown.current = onVersionsShown;
  useEffect(() => {
    versionsShown.current?.(shownKey ? shownKey.split(",").map(Number) : []);
  }, [shownKey]);
  const selectSection = (id: string) => setSelection({ kind: "section", id });
  const selectRisk = (risk: TriageRisk) =>
    setSelection({ kind: "risk", section: risk.section, id: risk.id });
  /** Opens one file in the Files pane, which otherwise lists every file
   *  folded, at a line when one is given. */
  const openFile = (id: string, line: FileLineFocus = null) => {
    setDiffFocus(line);
    setSelection({ kind: "file", id });
    setFileClick({ id, at: Date.now() });
  };
  const selectFile = (file: F) => openFile(file.id);
  const actions: ReviewViewerActions = {
    select: setSelection,
    openFile: (id) => openFile(id),
  };
  /** The Summary's "view all" links: each opens the pane the rail row of the
   *  same name would; Team unfolds the side pane and puts the cursor in its
   *  text box. */
  const navigateFromSummary = (destination: SummaryDestination) => {
    if (destination === "team") {
      if (!conversation) return;
      openBarTab("chat");
      setComposerFocus((value) => value + 1);
      return;
    }
    if (destination === "files") {
      setFolded((current) => ({ ...current, files: false }));
      setSelection({ kind: "files" });
      return;
    }
    if (destination === "sessions" && !sessions) return;
    setFolded((current) => ({ ...current, [destination]: false }));
    setSelection({ kind: destination });
  };
  const selectGroup = (kind: "changes" | "risks", expanded: boolean) => {
    setFolded((current) => ({ ...current, [kind]: !expanded }));
    setSelection({ kind });
  };
  // A risk is read inside its own section, so both the side pane and the
  // section's aside follow that section while the risk is open.
  const sectionId =
    selection.kind === "section"
      ? selection.id
      : selection.kind === "risk"
        ? selection.section
        : null;
  const viewedId = sections.some((section) => section.id === sectionId) ? sectionId : null;
  const selected = sections.find((section) => section.id === viewedId);
  // Named straight from the sections of either compared document, whatever
  // its tier or state: the rail and the triage lists filter, but a link can
  // name any risk the pane will render, and its label and the side pane's
  // context have to say so too.
  const riskNamed = (from: ReviewDocument | null): TriageRisk | undefined => {
    if (selection.kind !== "risk" || !from) return undefined;
    const section = from.sections.find((item) => item.id === selection.section);
    const risk = section?.risks.find((item) => item.id === selection.id);
    return section && risk
      ? {
          ...risk,
          section: section.id,
          sectionTitle: section.title,
          sectionState: sectionBadgeState(section.derived.state, from.review),
        }
      : undefined;
  };
  const selectedRisk = riskNamed(navigationDocument) ?? riskNamed(historicalDocument);
  const sectionAside =
    !historyView.selection && viewedId && renderSectionAside ? renderSectionAside(viewedId) : null;
  const sectionRead = (id: string) => checks?.sectionRead(id) ?? false;
  const riskRead = (risk: TriageRisk) => checks?.riskRead(risk) ?? false;
  const sectionCheck = (id: string, label: string) =>
    historyView.selection || !checks ? null : checks.renderSection({ id, label });
  const riskCheck = (risk: TriageRisk) =>
    historyView.selection || !checks ? null : checks.renderRisk(risk);
  const changesRead = currentSections.filter((section) => sectionRead(section.id)).length;
  const risksRead = risks.filter(riskRead).length;
  const tracked = Boolean(checks) && !historyView.selection;
  // A review with nothing critical or high has no group to offer, once the
  // document says so. While it loads the row stays, since most reviews have
  // risks and a row that appears late would push the rows below it down. The
  // group stays while its pane is open (a `?pane=risks` link opens it
  // regardless), so the pane never shows without its row.
  const showRisksRow = !navigationDocument || risks.length > 0 || selection.kind === "risks";
  const paneLabel =
    selection.kind === "changes"
      ? "Changes"
      : selection.kind === "risks"
        ? "Critical risks"
        : selection.kind === "sessions" || selection.kind === "session"
          ? "Sessions"
          : selectedCommit
            ? `Commit: ${selectedCommit.label}`
            : selection.kind === "commits" || selection.kind === "commit"
              ? "Commits"
              : selectedRisk
                ? `Risk: ${riskCategory(selectedRisk)}`
                : filesSelected
                  ? "Files changed"
                  : (selected?.label ?? "Summary");
  // What the pane's arrival is keyed on: the place, not the data behind it.
  const selectionKey = JSON.stringify(selection);
  // Every file shares one pane, which scrolls its own cards into view.
  const paneKey = filesSelected ? "files" : selectionKey;
  // A new place starts at its top. The pane shares the page's scroll, so
  // one read far down would otherwise open the next part way through; the
  // page only moves when the pane's top has gone under the bar. Where the
  // panes stack, that bar is the navigator's, and the pane lands under it.
  useEffect(() => {
    const pane = reviewPane.current;
    if (!paneKey || !pane) return;
    const bar = navigator.current;
    const top = pane.getBoundingClientRect().top;
    // No offset parent: the bar is not displayed, as from xl.
    if (bar?.offsetParent) {
      const below = bar.getBoundingClientRect().bottom;
      if (top < below) window.scrollBy({ top: top - below });
    } else if (top < REVIEW_BAR_PX) pane.scrollIntoView({ block: "start" });
  }, [paneKey]);
  // A sheet has done its job once the reader has gone somewhere, including
  // a file already selected and picked again, or a link out of the side pane.
  useEffect(() => setSheet(null), [selectionKey, fileClick]);
  const closeSheet = () => setSheet(null);
  // A sheet only exists below xl; one left open across a resize past it would
  // leave the page behind it inert beside the column it has become.
  useEffect(() => {
    const wide = window.matchMedia("(width >= 80rem)");
    const onChange = () => {
      if (wide.matches) setSheet(null);
    };
    wide.addEventListener("change", onChange);
    return () => wide.removeEventListener("change", onChange);
  }, []);
  useEffect(() => {
    if (!sheet) return;
    const pane = sheet === "rail" ? railPane.current : conversationPane.current;
    if (!pane) return;
    // A modal in all but name: everything but the sheet and its scrim is
    // inert while it is open, so neither a keyboard nor a screen reader can
    // reach the page it covers. The sheet stays where it is in the document
    // (from xl the same element is a column), so rather than a dialog, each
    // of its ancestors' other children is made inert, and only those this
    // sheet made inert are released.
    const keep = new Set<Element | null>([pane, scrim.current]);
    const made: Element[] = [];
    // Only while it covers the page: an element styled as the column has
    // nothing over which to be modal.
    if (getComputedStyle(pane).position === "fixed") {
      for (let node: Element | null = pane; node?.parentElement; node = node.parentElement) {
        for (const sibling of node.parentElement.children) {
          if (sibling === node || keep.has(sibling) || sibling.hasAttribute("inert")) continue;
          sibling.setAttribute("inert", "");
          made.push(sibling);
        }
        if (node.parentElement === window.document.body) break;
      }
    }
    // Start on the row being read, or the tab on show, unless something in
    // the sheet already asked for the focus (the composer, for "Discuss").
    if (!pane.contains(window.document.activeElement)) {
      const start = pane.querySelector<HTMLElement>(
        sheet === "rail"
          ? '[aria-pressed="true"][aria-controls~="review-section"]'
          : '[role="tab"][aria-selected="true"]',
      );
      start?.focus({ preventScroll: true });
      start?.scrollIntoView({ block: "nearest" });
    }
    const opener = navigator.current?.querySelector<HTMLElement>(
      `[aria-controls="review-${sheet}"]`,
    );
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setSheet(null);
    };
    window.document.addEventListener("keydown", onKeyDown);
    return () => {
      window.document.removeEventListener("keydown", onKeyDown);
      for (const element of made) element.removeAttribute("inert");
      // However it closed (Escape, the scrim, a place picked in it), focus
      // left in the hidden sheet goes back to the bar button that opened it.
      const active = window.document.activeElement;
      if (!active || active === window.document.body || pane.contains(active)) opener?.focus();
    };
  }, [sheet]);
  // Reading order, for the navigator's previous and next: the Summary, then
  // each section as the rail lists it. A risk is read inside its section, and
  // the Changes and Critical risks overviews sit between the Summary and the
  // first section; the other groups are places to look things up, not steps.
  const steps: (ReviewStep & { key: string })[] = [
    {
      key: "summary",
      label: conversation ? "Summary / General" : "Summary",
      go: () => setSelection({ kind: "summary" }),
    },
    ...sections.map((section) => ({
      key: section.id,
      label: section.label,
      go: () => selectSection(section.id),
    })),
  ];
  const stepAt =
    selection.kind === "summary"
      ? 0
      : selection.kind === "changes" || selection.kind === "risks"
        ? 0.5
        : viewedId
          ? steps.findIndex((step) => step.key === viewedId)
          : -1;
  const previousStep = stepAt < 0 ? null : (steps[Math.ceil(stepAt) - 1] ?? null);
  const nextStep = stepAt < 0 ? null : (steps[Math.floor(stepAt) + 1] ?? null);
  const navigatorIcon =
    selection.kind === "risk" || selection.kind === "risks"
      ? RiskIcon
      : selection.kind === "section" || selection.kind === "changes"
        ? SectionIcon
        : filesSelected
          ? FilesIcon
          : selection.kind === "sessions" || selection.kind === "session"
            ? SessionsIcon
            : selection.kind === "commits" || selection.kind === "commit"
              ? CommitIcon
              : SummaryIcon;
  const sectionIndex = viewedId ? sections.findIndex((section) => section.id === viewedId) : -1;
  const findFile = useCallback(
    (path: string) =>
      fileList?.find((candidate) => candidate.path === path || candidate.old_path === path),
    [fileList],
  );
  const revealLine = (path: string, side: "LEFT" | "RIGHT", line: number) => {
    const file = findFile(path);
    if (!file) return;
    openFile(file.id, { side, line });
  };
  // Every code link in the panes opens the file here, at its line, rather
  // than leaving for GitHub. A path this pull request does not change has no
  // file view, and its link keeps going to GitHub.
  const reviewFiles = fileList
    ? {
        has: (path: string) => Boolean(findFile(path)),
        open: (path: string, line?: number) => {
          const file = findFile(path);
          if (!file) return false;
          // Unfold the rail's files so the one opened shows where it sits.
          setFolded((current) => ({ ...current, files: false }));
          openFile(file.id, line ? { side: "RIGHT", line } : null);
          return true;
        },
      }
    : null;
  // The document the Details and Activity tabs describe: the version being
  // read, or the current one while two are compared.
  const barDocument = navigationDocument;
  const barHistorical = historicalOnly;
  const barChecks = barDocument && detailsChecks ? detailsChecks(barDocument) : null;
  const barTabs: { value: ConversationTab; label: string; icon: ReactNode; tone?: "critical" }[] = [
    ...(conversation ? [{ value: "chat" as const, label: "Chat", icon: <TeamChatIcon /> }] : []),
    {
      value: "details",
      label: "Details",
      icon: <ChecksIcon />,
      // Failing CI is the one detail worth flagging from another tab.
      tone: (
        barChecks
          ? barChecks.failing
          : barDocument && checkStatus(barDocument).state === "failing"
      )
        ? "critical"
        : undefined,
    },
    ...(hideActivity
      ? []
      : [{ value: "activity" as const, label: "Activity", icon: <SessionsIcon /> }]),
  ];
  const asideLabel = conversation ? "Conversation" : "Details";
  // The tabs and the fold share the pane's first line; folding only exists
  // from xl up, where the panes sit side by side. Below xl the pane is a
  // sheet, and the same place holds the way to close it.
  const barHeader = (
    <div className="mb-4 flex flex-none items-end">
      <SegmentedControl
        label={conversation ? "Conversation pane" : "Side pane"}
        variant="tabs"
        tablist="conversation"
        value={barTab}
        onChange={setBarTab}
        items={barTabs}
        className="min-w-0 flex-1"
      />
      <span className="box-content hidden h-9 flex-none items-center border-b border-(--border-subtle) xl:flex">
        <IconButton
          label={conversation ? "Collapse conversation" : "Collapse side pane"}
          onClick={() => setChatCollapsed(true)}
        >
          <ChevronRightIcon />
        </IconButton>
      </span>
      <span className="box-content flex h-9 flex-none items-center border-b border-(--border-subtle) xl:hidden">
        <IconButton
          label={`Close ${asideLabel.toLowerCase()}`}
          className="h-9 w-9"
          onClick={closeSheet}
        >
          <CloseIcon />
        </IconButton>
      </span>
    </div>
  );
  const barPlaceholder = asidePlaceholder ?? (
    <Empty title="No review document">The details and activity come from the review.</Empty>
  );
  const historicalNote =
    barHistorical && barDocument ? (
      <p className="mb-4 text-xs text-(--text-tertiary)">
        As of review v{barDocument.review.version}
      </p>
    ) : null;
  const conversationContext: ReviewConversationContext = {
    document: navigationDocument,
    sectionId,
    viewedSectionId: viewedId,
    riskId: selectedRisk?.id,
    historical: Boolean(historyView.selection),
    version: fileVersion,
    revealLine,
  };

  // The panes' columns: the rail, the review pane, and the side pane or the
  // strip it folds to.
  const columns = chatCollapsed
    ? "xl:grid-cols-[var(--rail-width)_minmax(0,1fr)_3rem]"
    : "xl:grid-cols-[var(--rail-width)_minmax(0,1fr)_10px_var(--conversation-width)]";
  const columnStyle = {
    "--rail-width": `${rail.width}px`,
    "--conversation-width": `${split.width}px`,
  } as CSSProperties;
  const fileProgress =
    !comparing && fileList && filesSlot?.tracksViewed !== false
      ? { done: fileList.filter((file) => file.viewed).length, total: fileList.length }
      : undefined;

  return (
    <ReviewFilesProvider files={reviewFiles}>
      <div className="flex min-h-0 flex-1 flex-col gap-3">
        {/* On a phone the head leaves out its version line, and the review's
          age sits beside the version menu instead; a review with no earlier
          version has no menu, so its number stands in for it. */}
        {document && (versions.length > 0 || historyView.selection || header?.beside) ? (
          <HistoryToolbar
            currentVersion={document.review.version}
            versions={versions}
            view={historyView}
            status={
              <ReviewFreshness
                document={document}
                generation={header?.generation}
                className="sm:hidden"
              />
            }
          >
            {changes && (
              <span className="text-base text-(--text-secondary)">
                {pluralize("new section", changes.sections.size, true)} ·{" "}
                {pluralize("new risk", changes.risks.size, true)} since v
                {historyView.selection?.version}
              </span>
            )}
            {header?.beside ? (
              <div className="min-w-0 basis-full sm:basis-auto">{header.beside}</div>
            ) : null}
          </HistoryToolbar>
        ) : document ? (
          <div className="flex items-center gap-3 text-base text-(--text-tertiary) sm:hidden">
            Review v{document.review.version}
            <ReviewFreshness document={document} generation={header?.generation} />
          </div>
        ) : header?.beside ? (
          <div className="min-w-0">{header.beside}</div>
        ) : null}
        {/* One ruled rectangle divided by hairlines rather than three floating
          cards: the rail, the pane and the side pane are one workspace, and
          the lines between them are where the reader's eye already draws them. */}
        {/* The pane scrolls with the page; the rail and the side pane stick
          under the head's bar and keep their own scroll. The rail is as tall
          as the window under the bar whether or not the head still shows:
          sized to what is left below the head, it would overflow only until
          the head scrolled away, take the wheel for itself, and change its
          range under the gesture as the page moved. The side pane follows
          the visible height instead, so its composer stays on screen. */}
        <div
          ref={workspace}
          style={columnStyle}
          className={cn(
            // Edge to edge on a phone: the gutter, this frame and each pane's
            // own padding would otherwise stack up to a tenth of the width.
            // Stacked, the one column is the window's width: an `auto` track
            // would grow to the bar's untruncated title and widen the page.
            "-mx-4 grid flex-1 grid-cols-[minmax(0,1fr)] items-start border-y border-(--border-default) bg-(--surface-card) sm:mx-0 sm:border-x xl:items-stretch",
            columns,
          )}
        >
          <ReviewNavigator
            ref={navigator}
            label={paneLabel}
            icon={navigatorIcon}
            position={sectionIndex >= 0 ? `${sectionIndex + 1} of ${sections.length}` : undefined}
            check={selected && !selected.stale ? sectionCheck(selected.id, selected.label) : null}
            previous={previousStep}
            next={nextStep}
            expanded={sheetOpen}
            onOpen={() => setSheet("rail")}
            aside={{
              label: asideLabel,
              alert: barTabs.some((tab) => tab.tone),
              expanded: asideOpen,
              onOpen: () => setSheet("aside"),
            }}
          />
          {/* A button rather than a div: dismissing a sheet by tapping beside
            it is a real control, as the application drawer's is. */}
          {sheet ? (
            <button
              ref={scrim}
              type="button"
              aria-label={sheetOpen ? "Close sections" : `Close ${asideLabel.toLowerCase()}`}
              onClick={closeSheet}
              className="fixed inset-0 z-(--z-sheet) bg-(--surface-scrim) xl:hidden"
            />
          ) : null}
          <nav
            ref={railPane}
            id="review-rail"
            aria-label="Review sections"
            // Below xl a sheet from the bottom of the window, `hidden` while
            // closed so its rows leave the tab order with it; from xl the
            // column beside the pane, under the head's bar.
            className={cn(
              "fixed inset-x-0 bottom-0 z-(--z-sheet) max-h-[80dvh] min-w-0 overflow-x-hidden overflow-y-auto border-t border-(--border-default) bg-(--surface-card) px-4 pt-4 pb-[max(1rem,env(safe-area-inset-bottom))] shadow-(--shadow-overlay)",
              "xl:sticky xl:inset-x-auto xl:top-12 xl:bottom-auto xl:z-auto xl:block xl:h-[calc(100dvh-3rem)] xl:max-h-none xl:self-start xl:border-t-0 xl:bg-transparent xl:p-4 xl:shadow-none",
              sheetOpen ? "block" : "hidden",
            )}
          >
            <div className="absolute top-1 right-2 xl:hidden">
              <IconButton label="Close sections" className="h-10 w-10" onClick={closeSheet}>
                <CloseIcon />
              </IconButton>
            </div>
            <SideNavGroup label="Review">
              <SideNavButton
                label={conversation ? "Summary / General" : "Summary"}
                icon={SummaryIcon}
                tint="accent"
                active={selection.kind === "summary"}
                controls="review-section"
                onSelect={() => setSelection({ kind: "summary" })}
              />

              {checks?.notice && !historyView.selection ? checks.notice : null}
              <SideNavDisclosure
                label="Changes"
                icon={SectionIcon}
                tint="accent"
                expanded={open.changes}
                active={selection.kind === "changes"}
                // The count is about the rows in front of the reader, whatever
                // supplied them: without a document those are the sections the
                // host knows, and reading one of those is progress through the
                // rail as it stands.
                progress={
                  tracked && currentSections.length > 0 && !checks?.hideSectionProgress
                    ? `${changesRead} of ${currentSections.length}`
                    : undefined
                }
                read={tracked ? { done: changesRead, total: currentSections.length } : undefined}
                list="review-changes"
                controls="review-section"
                onSelect={(expanded) => selectGroup("changes", expanded)}
              />
              {open.changes && (
                <SideNavNest id="review-changes">
                  {sections.length === 0 ? (
                    <RailNote>No sections yet.</RailNote>
                  ) : (
                    sections.map((section) => (
                      <div
                        key={section.id}
                        className={cn(
                          "flex min-w-0 items-center gap-1 rounded-(--radius-sm) transition-colors duration-300",
                          !section.stale &&
                            sectionRead(section.id) &&
                            "bg-(--status-positive-bg)/60",
                        )}
                      >
                        <div className="min-w-0 flex-1">
                          {/* Section titles are sentences: the row drops the
                              glyph its Changes parent already shows and takes
                              a second line, so the title reads rather than
                              stopping after two words. */}
                          <SideNavButton
                            label={section.label}
                            wrap
                            depth={1}
                            active={selection.kind === "section" && viewedId === section.id}
                            controls="review-section"
                            description={
                              section.score === undefined
                                ? undefined
                                : `Review score ${section.score} of 5: ${RISK_SCORE_MEANING[section.score]}`
                            }
                            aside={
                              <>
                                {section.score !== undefined && (
                                  <ReviewScoreBadge score={section.score} size="compact" />
                                )}
                                {changes?.sections.has(section.id) && (
                                  <Badge size="compact" tone="info">
                                    New
                                  </Badge>
                                )}
                                {comparing &&
                                  !document?.sections.some((item) => item.id === section.id) && (
                                    <Badge size="compact">
                                      Only v{historyView.selection?.version}
                                    </Badge>
                                  )}
                              </>
                            }
                            onSelect={() => selectSection(section.id)}
                          />
                        </div>
                        {!section.stale && sectionCheck(section.id, section.label)}
                      </div>
                    ))
                  )}
                </SideNavNest>
              )}

              {showRisksRow && (
                <>
                  <SideNavDisclosure
                    label="Critical risks"
                    icon={RiskIcon}
                    tint="critical"
                    expanded={open.risks}
                    active={selection.kind === "risks"}
                    progress={
                      tracked && risks.length > 0 ? `${risksRead} of ${risks.length}` : undefined
                    }
                    read={tracked ? { done: risksRead, total: risks.length } : undefined}
                    list="review-risks"
                    controls="review-section"
                    onSelect={(expanded) => selectGroup("risks", expanded)}
                  />
                  {open.risks && (
                    <SideNavNest id="review-risks">
                      {risks.length === 0 ? (
                        <RailNote>
                          {document ? "No critical or high risk." : "No risks yet."}
                        </RailNote>
                      ) : (
                        risks.map((risk) => (
                          <div
                            key={riskKey(risk.section, risk.id)}
                            className={cn(
                              "flex min-w-0 items-center gap-1 rounded-(--radius-sm) transition-colors duration-300",
                              riskRead(risk) && "bg-(--status-positive-bg)/60",
                            )}
                          >
                            <div className="min-w-0 flex-1">
                              <SideNavButton
                                label={riskLabel(risk)}
                                icon={RiskIcon}
                                tint="critical"
                                depth={1}
                                active={
                                  selection.kind === "risk" &&
                                  selection.section === risk.section &&
                                  selection.id === risk.id
                                }
                                controls="review-section"
                                aside={
                                  <>
                                    {changes?.risks.has(riskKey(risk.section, risk.id)) && (
                                      <Badge size="compact" tone="info">
                                        New
                                      </Badge>
                                    )}
                                    <TierDot tier={risk.tier} />
                                  </>
                                }
                                onSelect={() => selectRisk(risk)}
                              />
                            </div>
                            {riskCheck(risk)}
                          </div>
                        ))
                      )}
                    </SideNavNest>
                  )}
                </>
              )}

              {filesSlot ? (
                <>
                  <SideNavDisclosure
                    label="Files changed"
                    icon={SectionIcon}
                    busy={!comparing && filesSlot.loading && Boolean(fileList)}
                    expanded={open.files}
                    active={filesSelected}
                    progress={
                      fileProgress && fileProgress.total > 0
                        ? `${fileProgress.done} of ${fileProgress.total}`
                        : undefined
                    }
                    read={fileProgress}
                    // Counted even when the host keeps no viewed marks.
                    count={!comparing && fileList ? fileList.length : undefined}
                    list="review-files"
                    controls="review-section"
                    onSelect={(expanded) => {
                      setFolded((current) => ({ ...current, files: !expanded }));
                      if (!comparing && expanded && !filesSelected) setSelection({ kind: "files" });
                    }}
                  />
                  {open.files && (
                    <SideNavNest id="review-files">
                      {comparing ? (
                        <RailNote>Files are unavailable while comparing versions.</RailNote>
                      ) : filesSlot.loading && !fileList ? (
                        <RailNote busy>Loading changed files…</RailNote>
                      ) : filesSlot.failed && !fileList ? (
                        <RailNote>Changed files could not be loaded.</RailNote>
                      ) : fileList?.length ? (
                        filesSlot.renderTree({
                          files: fileList,
                          selected: selection.kind === "file" ? selection.id : null,
                          onSelect: selectFile,
                        })
                      ) : (
                        <RailNote>No changed files.</RailNote>
                      )}
                    </SideNavNest>
                  )}
                </>
              ) : null}

              {sessionState ? (
                <>
                  <SideNavDisclosure
                    label="Sessions"
                    icon={SessionsIcon}
                    tint="teal"
                    busy={sessionState.status === "loading"}
                    expanded={open.sessions}
                    active={selection.kind === "sessions"}
                    count={sessionState.status === "ready" ? sessionState.items.length : undefined}
                    list="review-sessions"
                    controls="review-section"
                    onSelect={(expanded) => {
                      setFolded((current) => ({ ...current, sessions: !expanded }));
                      setSelection({ kind: "sessions" });
                    }}
                  />
                  {open.sessions && (
                    <SideNavNest id="review-sessions">
                      {sessionState.status === "loading" ? (
                        <RailNote busy>Loading linked sessions…</RailNote>
                      ) : sessionState.status === "failed" ? (
                        <RailNote>Linked sessions could not be loaded.</RailNote>
                      ) : sessionState.items.length === 0 ? (
                        <RailNote>No linked sessions.</RailNote>
                      ) : (
                        sessionState.items.map((item) => (
                          <SideNavButton
                            key={item.id}
                            label={item.label}
                            icon={SessionsIcon}
                            tint="teal"
                            depth={1}
                            active={selection.kind === "session" && selection.id === item.id}
                            controls="review-section"
                            onSelect={() => setSelection({ kind: "session", id: item.id })}
                          />
                        ))
                      )}
                    </SideNavNest>
                  )}
                </>
              ) : null}

              {commitState ? (
                <>
                  <SideNavDisclosure
                    label="Commits"
                    icon={CommitIcon}
                    busy={commitState.status === "loading"}
                    expanded={open.commits}
                    active={selection.kind === "commits"}
                    count={commitState.status === "ready" ? commitState.items.length : undefined}
                    list="review-commits"
                    controls="review-section"
                    onSelect={(expanded) => {
                      setFolded((current) => ({ ...current, commits: !expanded }));
                      setSelection({ kind: "commits" });
                    }}
                  />
                  {open.commits && (
                    <SideNavNest id="review-commits">
                      {commitState.status === "loading" ? (
                        <RailNote busy>Loading commits…</RailNote>
                      ) : commitState.status === "failed" ? (
                        <RailNote>Commits could not be loaded.</RailNote>
                      ) : commitState.items.length === 0 ? (
                        <RailNote>No commits synced.</RailNote>
                      ) : (
                        commitState.items.map((item) => (
                          <SideNavButton
                            key={item.id}
                            label={item.label}
                            icon={CommitIcon}
                            depth={1}
                            active={selection.kind === "commit" && selection.id === item.id}
                            controls="review-section"
                            onSelect={() => setSelection({ kind: "commit", id: item.id })}
                          />
                        ))
                      )}
                    </SideNavNest>
                  )}
                </>
              ) : null}
            </SideNavGroup>
            {checks?.footer ?? null}
          </nav>

          <section
            ref={reviewPane}
            id="review-section"
            aria-label={paneLabel}
            className="relative flex min-h-[22rem] min-w-0 flex-col border-b border-(--border-subtle) p-4 sm:p-5 xl:min-h-0 xl:border-b-0 xl:border-l"
          >
            {/* Laid over the pane's left border rather than given a column, so
              the rail and the pane keep their places and only gain a grip. */}
            <PaneDivider
              width={rail.width}
              onWidth={rail.choose}
              reviewRef={reviewPane}
              paneRef={railPane}
              limits={RAIL_LIMITS}
              side="before"
              label="Resize the section rail"
              className="absolute inset-y-0 -left-[5px] z-10 w-[10px]"
            />
            <div className={cn("flex min-h-0 flex-1 flex-col", PANE_WIDTH)}>
              {sectionAside}
              {document && versionsFailed && (
                <Alert className="mb-3">
                  <span className="mr-2">Review history could not be loaded.</span>
                  <Button size="compact" variant="secondary" onClick={versionsFailed.retry}>
                    Retry history
                  </Button>
                </Alert>
              )}
              {notices}
              {(selection.kind === "sessions" || selection.kind === "session") && sessions ? (
                <PaneIn key={selectionKey}>
                  {sessions.renderPane(selection.kind === "session" ? selection.id : null, actions)}
                </PaneIn>
              ) : (selection.kind === "commits" || selection.kind === "commit") && commits ? (
                <PaneIn key={selectionKey}>
                  {commits.renderPane(selectedCommit ? selectedCommit.id : null, actions)}
                </PaneIn>
              ) : filesSelected && filesSlot ? (
                comparing ? (
                  <Empty title="Files unavailable during comparison">
                    Close Compare to inspect one review version's file diff.
                  </Empty>
                ) : filesSlot.loading && !fileList ? (
                  <Skeleton height={280} />
                ) : filesSlot.failed && !fileList ? (
                  <div className="space-y-3">
                    <Failed>The changed files could not be loaded.</Failed>
                    <Button variant="secondary" onClick={filesSlot.retry}>
                      Retry files
                    </Button>
                  </div>
                ) : fileList?.length === 0 ? (
                  <Empty title="No changed files">
                    This pull request revision changes no files.
                  </Empty>
                ) : fileList && (selection.kind === "files" || selectedFile) ? (
                  <PaneIn key="files">
                    {filesSlot.renderFiles({ files: fileList, open: fileOpen, focus: diffFocus })}
                  </PaneIn>
                ) : (
                  <Skeleton height={280} />
                )
              ) : document ? (
                <ReviewHistory key={document.review.version} document={document} view={historyView}>
                  {(snapshot, historical) => (
                    <PaneIn key={selectionKey}>{renderPane(snapshot, historical)}</PaneIn>
                  )}
                </ReviewHistory>
              ) : (
                <>
                  <CardHeader size="pane" title={paneLabel} />
                  <div className="min-h-0 flex-1 xl:overflow-y-auto">
                    {placeholder ?? (
                      <Empty title="No review document">There is no review to show yet.</Empty>
                    )}
                  </div>
                </>
              )}
              {document ? <ReviewPaneFoot previous={previousStep} next={nextStep} /> : null}
            </div>
          </section>

          {!chatCollapsed && (
            <PaneDivider
              width={split.width}
              onWidth={split.choose}
              onFit={split.fit}
              reviewRef={reviewPane}
              paneRef={conversationPane}
              label={conversation ? "Resize the conversation pane" : "Resize the side pane"}
            />
          )}

          <section
            ref={conversationPane}
            id="review-aside"
            aria-label={conversation ? "Conversation" : "Side pane"}
            // Below xl a sheet from the bottom of the window, nearly its full
            // height so the chat's feed and composer both have room; from xl
            // the column beside the pane, under the head's bar.
            className={cn(
              "fixed inset-x-0 top-[max(3rem,8dvh)] bottom-0 z-(--z-sheet) min-w-0 flex-col overflow-hidden border-t border-(--border-default) bg-(--surface-aside) p-4 pb-[max(1rem,env(safe-area-inset-bottom))] shadow-(--shadow-overlay) sm:p-5",
              "xl:sticky xl:inset-x-auto xl:top-12 xl:bottom-auto xl:z-auto xl:flex xl:h-(--review-visible) xl:self-start xl:border-t-0 xl:shadow-none",
              asideOpen ? "flex" : "hidden",
              chatCollapsed && "xl:items-center xl:px-0 xl:py-2",
            )}
          >
            {/* The folded strip: a way back, then one glyph per tab, each still
              carrying the state its tab reports. Only from xl up — below it
              the panes stack, and there is nothing to fold away from. */}
            <div
              className={cn(
                "hidden",
                chatCollapsed && "xl:flex xl:flex-none xl:flex-col xl:items-center xl:gap-1",
              )}
            >
              <IconButton
                label={conversation ? "Expand conversation" : "Expand side pane"}
                className="h-9 w-9 text-(--text-secondary)"
                onClick={() => setChatCollapsed(false)}
              >
                <ChevronLeftIcon />
              </IconButton>
              <span aria-hidden className="my-1 h-px w-5 bg-(--border-subtle)" />
              {barTabs.map((tab) => (
                <IconButton
                  key={tab.value}
                  label={tab.label}
                  className={cn(
                    "relative h-9 w-9",
                    tab.value === barTab ? "text-(--text-primary)" : "text-(--text-secondary)",
                  )}
                  onClick={() => openBarTab(tab.value)}
                >
                  {tab.icon}
                  {tab.tone ? (
                    <span
                      aria-hidden
                      className="absolute top-1.5 right-1.5 h-1.5 w-1.5 rounded-full bg-(--status-critical)"
                    />
                  ) : null}
                </IconButton>
              ))}
            </div>
            <div
              className={cn(chatCollapsed ? "flex min-h-0 flex-1 flex-col xl:hidden" : "contents")}
            >
              {barHeader}
              {conversation ? (
                <div
                  {...tabPanelProps("conversation", "chat", barTab === "chat")}
                  className="flex min-h-0 flex-1 flex-col"
                >
                  {conversation(conversationContext)}
                </div>
              ) : null}
              {/* The other tabs scroll on their own under the strip, as the
                chat's feed does; their parts query the pane's width. */}
              <div
                {...tabPanelProps("conversation", "details", barTab === "details")}
                className="@container min-h-0 flex-1 overflow-y-auto xl:-mr-2 xl:pr-2"
              >
                {barTab === "details" ? (
                  barDocument ? (
                    <>
                      {historicalNote}
                      <ReviewDetails
                        document={barDocument}
                        checks={barChecks ? barChecks.content : undefined}
                      />
                    </>
                  ) : (
                    barPlaceholder
                  )
                ) : null}
              </div>
              {hideActivity ? null : (
                <div
                  {...tabPanelProps("conversation", "activity", barTab === "activity")}
                  className="@container min-h-0 flex-1 overflow-y-auto xl:-mr-2 xl:pr-2"
                >
                  {barTab === "activity" ? (
                    barDocument ? (
                      <>
                        {historicalNote}
                        <ReviewActivity
                          document={barDocument}
                          onSelectSection={selectSection}
                          onSelectSession={
                            barHistorical || !sessions
                              ? undefined
                              : (id) => {
                                  setFolded((current) => ({ ...current, sessions: false }));
                                  setSelection({ kind: "session", id });
                                }
                          }
                          onViewSessions={
                            barHistorical || !sessions
                              ? undefined
                              : () => navigateFromSummary("sessions")
                          }
                        />
                      </>
                    ) : (
                      barPlaceholder
                    )
                  ) : null}
                </div>
              )}
            </div>
          </section>
        </div>
      </div>
    </ReviewFilesProvider>
  );

  /** What the pane shows for the current selection in one snapshot of the
   *  review, current or historical. */
  function renderPane(snapshot: ReviewDocument, historical: boolean) {
    if (selection.kind === "changes")
      return (
        <ChangesOverview
          document={snapshot}
          onSelectSection={selectSection}
          renderLabels={
            renderSectionLabels ? (section) => renderSectionLabels(section, snapshot) : undefined
          }
          renderCheck={(section) =>
            historical || section.derived.state === "stale"
              ? null
              : sectionCheck(section.id, section.title)
          }
        />
      );
    if (selection.kind === "risks")
      return (
        <RisksOverview
          risks={triageRisks(snapshot).lead}
          onSelectRisk={selectRisk}
          onSelectSection={selectSection}
          renderCheck={historical ? () => null : riskCheck}
        />
      );
    if (selection.kind !== "section" && selection.kind !== "risk") {
      return (
        <ReviewSummary
          document={snapshot}
          onSelectSection={selectSection}
          onNavigate={historical ? undefined : navigateFromSummary}
        />
      );
    }
    const section = snapshot.sections.find((item) => item.id === sectionId);
    if (!section) {
      return (
        <p className="text-base text-(--text-secondary)">
          This section did not exist in review v{snapshot.review.version}.
        </p>
      );
    }
    if (selection.kind === "risk") {
      const risk = section.risks.find((item) => item.id === selection.id);
      if (!risk) {
        return (
          <p className="text-base text-(--text-secondary)">
            This risk is not present in review v{snapshot.review.version}.
          </p>
        );
      }
      return (
        <HunkPatchProvider source={comparing ? null : hunkPatches}>
          <ReviewRisk
            document={snapshot}
            risk={{
              ...risk,
              section: section.id,
              sectionTitle: section.title,
              sectionState: sectionBadgeState(section.derived.state, snapshot.review),
            }}
            onSelectSection={selectSection}
            loadVersion={loadVersion}
            activeVersions={sectionHistory?.active.get(section.id)}
            showTiles={showTiles}
          />
        </HunkPatchProvider>
      );
    }
    return (
      <HunkPatchProvider source={comparing ? null : hunkPatches}>
        <ReviewSection
          document={snapshot}
          section={section}
          onSelectSection={selectSection}
          loadVersion={loadVersion}
          activeVersions={sectionHistory?.active.get(section.id)}
          idPrefix={historical ? `history-v${snapshot.review.version}-` : ""}
          showTiles={showTiles}
          labels={renderSectionLabels?.(section, snapshot)}
        />
      </HunkPatchProvider>
    );
  }
}
