import {
  type CSSProperties,
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import pluralize from "pluralize";
import { chipLinkClass } from "@baum/viewer/components/chip-classes";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import {
  ChevronLeftIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  ExternalLinkIcon,
  LightbulbIcon,
  WarningIcon,
} from "@baum/viewer/components/icons";
import { centerJump, flashJump } from "@baum/viewer/components/jump-focus";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { Cites } from "./cites";
import { AnchorChips } from "@baum/viewer/components/review-parts";
import { useReviewFiles, useReviewSources } from "@baum/viewer/components/review-contexts";
import { Part, PROSE, ReviewScoreBadge, TierBadge } from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { tabPanelProps } from "@baum/viewer/components/tabs";
import { PaneDivider } from "@baum/viewer/components/pane-divider";
import { Badge, Button, CardHeader, IconButton, LiveDot } from "@baum/viewer/components/ui";
import { usePaneWidth } from "@baum/viewer/use-conversation-width";
import { useStoredSetting } from "@baum/viewer/use-stored-setting";
import {
  anchorFile,
  contextRefUrl,
  isContextDirectory,
  type ReviewContextRef,
  type ReviewDocument,
} from "@baum/viewer/review-document";
import { draftsOn, type DraftCard } from "../tiles/drafts";
import { useTileHost } from "../tiles/host";
import { PendingCard } from "../tiles/pending-card";
import type { TileContext } from "../tiles/types";
import {
  type NoteKind,
  pointId,
  type Tone,
  type V2Block,
  type V2Context,
  type V2Point,
  type V2Risk,
} from "./document";
import {
  CONTEXT_ID,
  detailsOf,
  SECTION_VIEWS,
  sectionViews,
  selectionOf,
  shownSectionView,
  sideSectionView,
  tilesOn,
  viewTile,
  type ListedSection,
  type ReviewV2,
  type SectionView,
  type V2Section,
  type ViewTile,
} from "./model";
import { type Reveal, SectionSplit } from "./section-split";
import {
  SectionViewBody,
  SectionViewTabs,
  steppableViews,
  useSectionViewKeys,
  viewKeyStep,
} from "./section-views";
import { SIDE_COLLAPSED_KEY, SIDE_LIMITS, useBracketKey } from "./side-column";
import { useSideReveal } from "./side-reveal";
import { RepositoryFileDialog, RepositoryFileView } from "../repository-file";
import { type RepositoryFileSource, useRepositoryFileSource } from "../repository-file-source";
import { BLOCK_LABEL, TONE_MEANS } from "./native-tiles";
import { ViewTileCard } from "./view-tile";
import { openExternal } from "../outside-links";

/** How it works: the section read (the rail lists the sections, "Where this
 *  fits" first), which shows its views (its Overview: its summary, the tiles
 *  it owns, its steps and its risks; its code, tests, the session turns it
 *  cites and its docs). It reads the baum.review/2 `review`; `document` is
 *  what the viewer's parts read. */
export function SectionsTab({
  document,
  review,
  list,
  section,
  item,
  sectionView,
  ctx,
  idPrefix,
  context,
  historical,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  list: ListedSection[];
  section: string;
  item?: string;
  sectionView?: SectionView;
  ctx: TileContext;
  idPrefix: string;
  context: ReviewPlaceContext;
  historical: boolean;
}) {
  const shown = review.sections.find((each) => each.id === section);
  // The tiles being made in the session panel stand on their sections, on
  // the latest version only.
  const drafts = historical
    ? []
    : draftsOn(
        review.drafts ?? [],
        section,
        list.map((entry) => entry.id),
      );
  const root = useRef<HTMLElement>(null);
  useItemJump(
    root,
    item ? [`${idPrefix}${section}-${item}`, `${idPrefix}${item}`] : [],
    historical,
  );
  return (
    <>
      <article ref={root} id={`${idPrefix}review-view-section`} className="min-w-0 space-y-7">
        {section === CONTEXT_ID ? (
          <WhereThisFits
            document={document}
            review={review}
            ctx={ctx}
            idPrefix={idPrefix}
            drafts={drafts}
            pending={context.contextPending && !historical}
          />
        ) : shown ? (
          <SectionBody
            key={shown.id}
            document={document}
            review={review}
            section={shown}
            asked={sectionView}
            ctx={ctx}
            idPrefix={idPrefix}
            drafts={drafts}
            context={context}
            onRetryDetails={historical ? undefined : context.onRetryDetails}
          />
        ) : (
          <p className="text-base text-secondary">
            This section isn't in review v{document.review.version}.
          </p>
        )}
      </article>
    </>
  );
}

function SectionBody({
  document,
  review,
  section,
  asked,
  ctx,
  idPrefix,
  drafts,
  context,
  onRetryDetails,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  section: V2Section;
  asked?: SectionView;
  ctx: TileContext;
  idPrefix: string;
  drafts: DraftCard[];
  context: ReviewPlaceContext;
  onRetryDetails?: (id: string) => void | Promise<unknown>;
}) {
  const views = useMemo(() => sectionViews(review, section), [review, section]);
  const root = useRef<HTMLDivElement>(null);
  const wide = useWideSection(root);
  const side = wide ? sideSectionView(asked, views.counts) : null;
  const shown = side ?? shownSectionView(asked, views.counts);
  const tablist = `${idPrefix}section-views`;
  const select = (next: SectionView) =>
    context.select(selectionOf({ tab: "sections", section: section.id, view: next }));
  // Once wide the side column takes h and l itself, since it can fold.
  useSectionViewKeys(
    (key) => {
      const next = viewKeyStep(key, shown, steppableViews(views.counts, true));
      if (!next || next === shown) return false;
      select(next);
      return true;
    },
    root,
    !side,
  );
  const overview = (
    <SectionOverview
      document={document}
      review={review}
      section={section}
      ctx={ctx}
      idPrefix={idPrefix}
      drafts={drafts}
      onRetryDetails={onRetryDetails}
    />
  );
  const viewBody = (value: Exclude<SectionView, "overview">, reveal?: Reveal | null) => (
    <SectionViewBody
      document={document}
      shown={value}
      views={views}
      context={context}
      reveal={reveal}
    />
  );
  return (
    <div ref={root} className="space-y-5">
      <div>
        <div className="mb-2 flex flex-wrap items-center gap-2">
          {section.derived.review_score !== undefined ? (
            <ReviewScoreBadge score={section.derived.review_score} withMeaning />
          ) : null}
          {/* The risks show their own tiers below: beside the review score
              a second level reads as a contradiction, so the tier stands
              only where the section has no score. */}
          {section.derived.state === "stale" ? (
            <Badge tone="neutral">No longer in the code</Badge>
          ) : section.derived.review_score === undefined ? (
            <TierBadge tier={section.derived.risk_tier} risks={section.risks.length} />
          ) : null}
        </div>
        <CardHeader size="pane" title={<InlineMarkdown>{section.title}</InlineMarkdown>} />
      </div>
      {side ? (
        <SectionSplit
          overview={overview}
          views={views}
          side={side}
          tablist={tablist}
          select={select}
          body={viewBody}
        />
      ) : (
        <>
          <SectionViewTabs
            tablist={tablist}
            value={shown}
            counts={views.counts}
            onChange={select}
          />
          {SECTION_VIEWS.map(({ value }) => (
            <div
              key={value}
              {...tabPanelProps(tablist, value, value === shown)}
              className="min-w-0"
            >
              {value !== shown ? null : value === "overview" ? overview : viewBody(value)}
            </div>
          ))}
        </>
      )}
    </div>
  );
}

/** The section's width (in rem) from which its Overview and its other views
 *  stand side by side, each column about as wide as a diff reads well. */
const SPLIT_REM = 64;

/** Whether the section is wide enough for its two columns, measured on the
 *  section itself so the rail, the session panel and the window all count. */
function useWideSection(root: RefObject<HTMLElement | null>) {
  const [wide, setWide] = useState(false);
  // Before the first paint, so a wide section never shows stacked first.
  useLayoutEffect(() => {
    const element = root.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const rem =
        Number.parseFloat(getComputedStyle(element.ownerDocument.documentElement).fontSize) || 16;
      setWide(element.clientWidth >= SPLIT_REM * rem);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [root]);
  return wide;
}

/** A section's Overview: its frame (the gist, then its risks last) around the
 *  body its agent composed, drawn block by block in order: prose, points,
 *  notes and the section's tiles. */
function SectionOverview({
  document,
  review,
  section,
  ctx,
  idPrefix,
  drafts,
  onRetryDetails,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  section: V2Section;
  ctx: TileContext;
  idPrefix: string;
  drafts: DraftCard[];
  onRetryDetails?: (id: string) => void | Promise<unknown>;
}) {
  const host = useTileHost();
  const details = detailsOf(section);
  const obsolete = section.derived.state === "stale";
  const open = section.risks.filter((risk) => risk.state !== "mitigated" || obsolete);
  const mitigated = obsolete ? [] : section.risks.filter((risk) => risk.state === "mitigated");
  const itemId = (ref: string) => `${idPrefix}${section.id}-${ref}`;
  // The list's line, shown as the section's own once, unless its first
  // prose already opens with it (a section read from an older document).
  const lead = section.body.find((block) => block.type === "prose");
  const gist =
    section.gist && !(lead?.type === "prose" && lead.text.trim().startsWith(section.gist))
      ? section.gist
      : "";
  return (
    <div className="space-y-7">
      {details === "pending" ? (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          Writing details…
        </p>
      ) : details === "failed" ? (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <p className="text-base text-secondary">Details unavailable.</p>
          {onRetryDetails && !obsolete ? (
            <RetryDetails onRetry={() => onRetryDetails(section.id)} />
          ) : null}
        </div>
      ) : null}
      {gist ? (
        <p data-section-gist="" className="max-w-[72ch] text-base leading-[1.6] text-secondary">
          <InlineMarkdown>{gist}</InlineMarkdown>
        </p>
      ) : null}
      {section.body.length > 0 || drafts.length > 0 ? (
        <div data-section-body="" className="min-w-0 space-y-6">
          {section.body.map((block, at) => (
            <BodyBlock
              key={block.type === "tile" ? `tile-${block.tile}` : `${block.type}-${at}`}
              block={block}
              at={at}
              section={section.id}
              document={document}
              review={review}
              ctx={ctx}
              idPrefix={idPrefix}
              itemId={itemId}
            />
          ))}
          {drafts.map((draft) => (
            <PendingCard key={draft.tile} draft={draft} remove={host.removeDraft} />
          ))}
        </div>
      ) : null}
      {open.length + mitigated.length > 0 ? (
        <Part label="Risks">
          {open.length > 0 ? (
            <ol className="space-y-2">
              {open.map((risk) => (
                <RiskItem key={risk.id} risk={risk} document={document} id={itemId(risk.id)} />
              ))}
            </ol>
          ) : null}
          {mitigated.length > 0 ? (
            <details className={open.length > 0 ? "mt-2.5" : undefined}>
              <summary className="cursor-pointer text-base text-link hover:underline">
                {pluralize("mitigated risk", mitigated.length, true)}
              </summary>
              <ol className="mt-2 space-y-2">
                {mitigated.map((risk) => (
                  <RiskItem key={risk.id} risk={risk} document={document} id={itemId(risk.id)} />
                ))}
              </ol>
            </details>
          ) : null}
        </Part>
      ) : null}
    </div>
  );
}

/** What the keys (j and k) move between and fill while it is the one. */
const BLOCK =
  "min-w-0 scroll-mt-4 rounded-md outline-none data-[review-block-active]:bg-surface-wash";

/** One block of a section's body, by its type. A tile block is the tile
 *  bare, under its title as the block's label; a text block's optional title
 *  is the same label. */
function BodyBlock({
  block,
  at,
  section,
  document,
  review,
  ctx,
  idPrefix,
  itemId,
}: {
  block: V2Block;
  at: number;
  section: string;
  document: ReviewDocument;
  review: ReviewV2;
  ctx: TileContext;
  idPrefix: string;
  itemId: (ref: string) => string;
}) {
  if (block.type === "tile") {
    const tile = viewTile(review, block.tile);
    return tile ? (
      <ViewTileCard tile={tile} review={document} ctx={ctx} idPrefix={idPrefix} variant="bare" />
    ) : null;
  }
  return (
    <div data-review-block="" data-block={block.type} className={cn(BLOCK, "space-y-2")}>
      {block.title ? <h3 className={BLOCK_LABEL}>{block.title}</h3> : null}
      {block.type === "prose" ? (
        <p className="max-w-[72ch] text-base leading-[1.7] text-primary">
          <InlineMarkdown>{block.text}</InlineMarkdown>
        </p>
      ) : block.type === "points" ? (
        <PointList block={block} at={at} section={section} document={document} itemId={itemId} />
      ) : block.kind ? (
        <KindNote kind={block.kind} text={block.text} />
      ) : (
        <p className="max-w-[72ch] border-l-2 border-default py-0.5 pl-3 text-base text-secondary">
          <InlineMarkdown>{block.text}</InlineMarkdown>
        </p>
      )}
    </div>
  );
}

/** A points block's lines; when some point has a tone, each line leads with
 *  its marker. */
function PointList({
  block,
  at,
  section,
  document,
  itemId,
}: {
  block: Extract<V2Block, { type: "points" }>;
  at: number;
  section: string;
  document: ReviewDocument;
  itemId: (ref: string) => string;
}) {
  const marked = block.items.some((item) => item.tone);
  return (
    <ul className="border-t border-subtle">
      {block.items.map((point, position) => (
        <PointRow
          key={position}
          id={itemId(pointId(section, at, position, point))}
          point={point}
          marked={marked}
          document={document}
        />
      ))}
    </ul>
  );
}

/** A note's kind: its icon, its word and their colour, and its rule. Only a
 *  caveat takes a status colour; the others are told apart by icon and word
 *  in the text's own greys. */
const NOTE_KINDS: Record<
  NoteKind,
  { icon: typeof WarningIcon; label: string; ink: string; rule: string }
> = {
  caveat: {
    icon: WarningIcon,
    label: "Caveat",
    ink: "text-status-caution",
    rule: "border-status-caution",
  },
  decision: {
    icon: LightbulbIcon,
    label: "Decision",
    ink: "text-primary",
    rule: "border-strong",
  },
  unchanged: {
    icon: CircleCheckIcon,
    label: "Unchanged",
    ink: "text-tertiary",
    rule: "border-subtle",
  },
  follow_up: {
    icon: CircleDashedIcon,
    label: "Follow-up",
    ink: "text-secondary",
    rule: "border-default",
  },
};

/** A note of a kind: its icon beside the text, which its word leads, on a
 *  rule in the kind's colour. A note without a kind is the plain aside. */
function KindNote({ kind, text }: { kind: NoteKind; text: string }) {
  const { icon: Icon, label, ink, rule } = NOTE_KINDS[kind];
  return (
    <div data-kind={kind} className={cn("flex max-w-[72ch] gap-2 border-l-2 py-0.5 pl-3", rule)}>
      <span aria-hidden className={cn("flex h-[1lh] flex-none items-center text-base", ink)}>
        <Icon size={14} />
      </span>
      <p className="min-w-0 text-base text-secondary">
        <span className={cn("font-medium", ink)}>{label}.</span>{" "}
        <InlineMarkdown>{text}</InlineMarkdown>
      </p>
    </div>
  );
}

/** A point's marker by its tone: a dot filled in the tone's colour, a
 *  square for what is wrong (filled when bad, hollow when missing), so they
 *  differ from the others by shape as well as colour, and a grey ring for a
 *  plain point beside toned ones. */
const MARK: Record<Tone, string> = {
  plain: "rounded-full border border-strong",
  new: "rounded-full bg-status-info",
  good: "rounded-full bg-status-positive",
  bad: "rounded-[1px] bg-status-critical",
  muted: "rounded-full bg-(--border-strong)",
  missing: "rounded-[1px] border-[1.5px] border-status-critical",
};

/** What a point's marker says, read out before its text. */
const MARK_MEANS: Partial<Record<Tone, string>> = { ...TONE_MEANS, new: "added by this change" };

/** One line of a points block. With a detail or cited files it is a button:
 *  it opens (the caret turns) to show the detail and the files it rests on.
 *  In a block where some point has a tone (`marked`), each line leads with
 *  its tone's marker.
 *  Each line is a block of its own, so the keys (j and k) go through them one
 *  by one rather than over the whole list, and Enter or Space opens the one
 *  they are on. Opening it beside a wide section's side column also shows
 *  there the first file it cites that the section draws. */
function PointRow({
  id,
  point,
  marked,
  document,
}: {
  id: string;
  point: V2Point;
  marked: boolean;
  document: ReviewDocument;
}) {
  const [open, setOpen] = useState(false);
  const openable = Boolean(point.more) || point.cites.length > 0;
  const panel = `${id}-more`;
  const tone = point.tone ?? "plain";
  const means = MARK_MEANS[tone];
  const text = marked ? (
    <span className="flex min-w-0 items-baseline gap-2.5">
      <span
        aria-hidden
        title={means}
        data-mark=""
        className={cn("size-2 flex-none -translate-y-px", MARK[tone])}
      />
      <span className={cn("min-w-0", tone === "muted" && "text-secondary")}>
        {means ? <span className="sr-only">{means}: </span> : null}
        <InlineMarkdown>{point.text}</InlineMarkdown>
      </span>
    </span>
  ) : (
    <InlineMarkdown>{point.text}</InlineMarkdown>
  );
  const reveal = useSideReveal();
  const toggle = () => {
    setOpen(!open);
    if (open || !reveal) return;
    for (const anchor of point.cites) {
      const file = anchorFile(document, anchor);
      if (file && reveal(file.path, file.line)) return;
    }
  };
  return (
    <li
      id={id}
      data-point=""
      data-tone={point.tone}
      data-review-block=""
      className="scroll-mt-4 border-b border-subtle outline-none target:bg-surface-wash data-[review-block-active]:bg-surface-wash"
      onKeyDown={(event) => {
        // Only on the row itself, where the keys leave the focus: the
        // button and the chips inside handle their own.
        if (!openable || event.target !== event.currentTarget) return;
        if (event.key !== "Enter" && event.key !== " ") return;
        event.preventDefault();
        toggle();
      }}
    >
      {openable ? (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panel}
          onClick={toggle}
          className={cn(
            "grid w-full cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-baseline gap-3 rounded-sm px-1 py-2.5 text-left text-base text-primary hover:bg-surface-hover",
            FOCUS_RING,
          )}
        >
          {text}
          <ChevronRightIcon
            aria-hidden
            className={cn("size-3.5 flex-none self-center text-tertiary", open && "rotate-90")}
          />
        </button>
      ) : (
        <p className="px-1 py-2.5 text-base text-primary">{text}</p>
      )}
      {openable && open ? (
        <div
          id={panel}
          className={cn(
            "max-w-[72ch] space-y-2 px-1 pb-3 text-base text-secondary",
            marked && "pl-[1.375rem]",
          )}
        >
          {point.more ? (
            <p>
              <InlineMarkdown>{point.more}</InlineMarkdown>
            </p>
          ) : null}
          <AnchorChips document={document} anchors={point.cites} />
        </div>
      ) : null}
    </li>
  );
}

/** The tiles placed on a section (or the Overview), and the pending cards of
 *  those being made for it in the session panel. */
export function SectionTiles({
  tiles,
  drafts,
  document,
  ctx,
  idPrefix,
}: {
  tiles: ViewTile[];
  drafts: DraftCard[];
  document: ReviewDocument;
  ctx: TileContext;
  idPrefix: string;
}) {
  const host = useTileHost();
  if (tiles.length === 0 && drafts.length === 0) return null;
  return (
    <div className="space-y-4">
      {tiles.map((tile) => (
        <ViewTileCard key={tile.id} tile={tile} review={document} ctx={ctx} idPrefix={idPrefix} />
      ))}
      {drafts.map((draft) => (
        <PendingCard key={draft.tile} draft={draft} remove={host.removeDraft} />
      ))}
    </div>
  );
}

/** A risk's tier in its own colour, the label in front of its row. */
const TIER_INK: Record<string, string> = {
  critical: "text-status-critical",
  high: "text-status-critical",
  medium: "text-status-caution",
  low: "text-tertiary",
};

/** One risk as a row: its tier in front, then its text and what it cites,
 *  under the section's Risks label. */
function RiskItem({ risk, document, id }: { risk: V2Risk; document: ReviewDocument; id: string }) {
  const tier = risk.tier ?? "low";
  return (
    <li
      id={id}
      data-review-block=""
      className="grid scroll-mt-4 grid-cols-[4.5rem_minmax(0,1fr)] gap-x-3 rounded-sm py-1 outline-none target:bg-surface-wash data-[review-block-active]:bg-surface-wash"
    >
      <span className={cn("pt-px text-xs font-medium", TIER_INK[tier] ?? "text-tertiary")}>
        {tier[0]!.toUpperCase()}
        {tier.slice(1)}
      </span>
      <div className="min-w-0 space-y-1">
        <Markdown className="text-secondary">{risk.text}</Markdown>
        <Cites document={document} anchors={risk.cites} />
      </div>
    </li>
  );
}

/** Retry for a section whose details failed: busy while the host's request
 *  runs, then the section reads as being written or offers it again. */
function RetryDetails({ onRetry }: { onRetry: () => void | Promise<unknown> }) {
  const [busy, setBusy] = useState(false);
  return (
    <Button
      variant="secondary"
      size="compact"
      disabled={busy}
      onClick={() => {
        setBusy(true);
        void Promise.resolve()
          .then(onRetry)
          .catch(() => undefined)
          .finally(() => setBusy(false));
      }}
    >
      {busy ? "Retrying…" : "Retry"}
    </Button>
  );
}

const CONTEXT_PARTS: { key: keyof V2Context; label: string }[] = [
  { key: "area", label: "Area" },
  { key: "reached_by", label: "How it's reached" },
  { key: "contracts", label: "What it relies on" },
  { key: "related", label: "Nearby code" },
];

/** "Where this fits": the review's context, each part with the files it rests
 *  on, then the tiles placed on it. A file it names opens whole, at the
 *  review's head (never its diff: the context is about the code around the
 *  change): in a column beside the context when the section is wide enough,
 *  as the other sections show their views, else in a dialog over the page. */
function WhereThisFits({
  document,
  review,
  ctx,
  idPrefix,
  drafts,
  pending,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  ctx: TileContext;
  idPrefix: string;
  drafts: DraftCard[];
  pending: boolean;
}) {
  const context = review.context;
  const tiles = tilesOn(review, CONTEXT_ID);
  const source = useRepositoryFileSource();
  const root = useRef<HTMLDivElement>(null);
  const wide = useWideSection(root);
  const named = useMemo(() => contextFiles(document, context), [document, context]);
  const split = wide && source !== null && named.length > 0;
  const [stored, store] = useStoredSetting(SIDE_COLLAPSED_KEY);
  const collapsed = stored !== "false";
  const [picked, setPicked] = useState<string | null>(null);
  const [dialog, setDialog] = useState<string | null>(null);
  const shown = picked !== null && named.includes(picked) ? picked : (named[0] ?? null);
  useBracketKey(() => store(String(!collapsed)), split);
  const open = source
    ? (path: string) => {
        if (!split) return setDialog(path);
        setPicked(path);
        if (collapsed) store("false");
      }
    : undefined;
  const body = (
    <div className="min-w-0 space-y-7">
      {!context && pending ? (
        <p role="status" className="flex items-center gap-2 text-base text-secondary">
          <LiveDot />
          Writing the context…
        </p>
      ) : null}
      {context
        ? CONTEXT_PARTS.flatMap(({ key, label }) => {
            const part = context[key];
            if (!part) return [];
            return [
              <Part key={key} label={label}>
                <div data-review-block="" className="space-y-2 rounded-sm outline-none">
                  <Markdown className={PROSE}>{part.text}</Markdown>
                  {part.files.length > 0 ? (
                    <ul className="flex flex-wrap gap-1.5" aria-label={`${label} files`}>
                      {part.files.map((path, position) => (
                        <li key={`${path}-${position}`} className="max-w-full min-w-0">
                          <ContextChip
                            document={document}
                            reference={{ path, symbol: null }}
                            onOpen={open}
                            shown={split && !collapsed && trimPath(path) === shown}
                          />
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </div>
              </Part>,
            ];
          })
        : null}
      <SectionTiles
        tiles={tiles}
        drafts={drafts}
        document={document}
        ctx={ctx}
        idPrefix={idPrefix}
      />
      {!context && !pending && tiles.length === 0 && drafts.length === 0 ? (
        <p className="text-base text-secondary">This review doesn't say where the change sits.</p>
      ) : null}
    </div>
  );
  return (
    <div ref={root} className="space-y-7">
      <CardHeader size="pane" title="Where this fits" />
      {split && shown ? (
        <ContextFileSplit
          collapsed={collapsed}
          collapse={(next) => store(String(next))}
          path={shown}
          source={source}
        >
          {body}
        </ContextFileSplit>
      ) : (
        body
      )}
      {dialog && source ? (
        <RepositoryFileDialog
          reference={{ path: dialog, symbol: null }}
          source={source.source}
          href={source.href(dialog)}
          close={() => setDialog(null)}
        />
      ) : null}
    </div>
  );
}

const trimPath = (path: string) => path.replace(/\/+$/, "");

/** The files the context names that open whole, in the order it names them:
 *  directories have no text to show. */
function contextFiles(document: ReviewDocument, context: V2Context | null): string[] {
  if (!context) return [];
  const paths = CONTEXT_PARTS.flatMap(({ key }) => context[key]?.files ?? [])
    .filter((path) => !isContextDirectory(document, path))
    .map(trimPath);
  return [...new Set(paths)];
}

/** "Where this fits" beside the file it shows, as a wide section stands
 *  beside its views: the column shares their width and fold (`]`), and
 *  folds to a strip that a file's chip opens again. */
function ContextFileSplit({
  collapsed,
  collapse,
  path,
  source,
  children,
}: {
  collapsed: boolean;
  collapse: (next: boolean) => void;
  path: string;
  source: RepositoryFileSource;
  children: ReactNode;
}) {
  const grid = useRef<HTMLDivElement>(null);
  const left = useRef<HTMLDivElement>(null);
  const column = useRef<HTMLDivElement>(null);
  const pane = usePaneWidth(SIDE_LIMITS);
  return (
    <div
      ref={grid}
      data-context-split=""
      data-collapsed={collapsed ? "" : undefined}
      data-pane-full={collapsed ? undefined : ""}
      style={{ "--side-width": `${pane.width}px` } as CSSProperties}
      className={cn(
        "grid items-start",
        collapsed
          ? "grid-cols-[minmax(0,1fr)_2rem] gap-4"
          : "grid-cols-[minmax(0,1fr)_2rem_minmax(0,var(--side-width))]",
      )}
    >
      <div ref={left} className="min-w-0">
        {children}
      </div>
      {collapsed ? (
        <div className="sticky top-12 flex justify-center pt-1">
          <IconButton
            label="Show file"
            title="Show file (])"
            aria-keyshortcuts="]"
            aria-expanded={false}
            onClick={() => collapse(false)}
          >
            <ChevronLeftIcon size={14} />
          </IconButton>
        </div>
      ) : (
        <>
          <PaneDivider
            width={pane.width}
            onWidth={pane.choose}
            onPreview={(width) => grid.current?.style.setProperty("--side-width", `${width}px`)}
            onFit={pane.fit}
            reviewRef={left}
            paneRef={column}
            limits={SIDE_LIMITS}
            label="Resize file"
            className="self-stretch"
          />
          <div
            ref={column}
            data-context-file=""
            className="sticky top-12 flex max-h-[calc(var(--review-visible,100dvh)-1rem)] min-w-0 flex-col"
          >
            <div className="flex flex-none items-center justify-between gap-2 border-b border-subtle pt-1 pb-2">
              <div className="min-w-0">
                <p className="truncate font-mono text-xs text-primary" title={path}>
                  {path}
                </p>
                <p className="text-xs text-tertiary">As of the review's head</p>
              </div>
              <div className="flex flex-none items-center gap-1">
                <a
                  href={source.href(path)}
                  target="_blank"
                  rel="noreferrer"
                  title="View on GitHub"
                  aria-label="View on GitHub"
                  className={cn(
                    "grid size-7 place-items-center rounded-sm text-tertiary hover:bg-surface-wash hover:text-primary",
                    FOCUS_RING,
                  )}
                >
                  <ExternalLinkIcon size={12} />
                </a>
                <IconButton
                  label="Hide file"
                  title="Hide file (])"
                  aria-keyshortcuts="]"
                  aria-expanded
                  onClick={() => collapse(true)}
                >
                  <ChevronRightIcon size={14} />
                </IconButton>
              </div>
            </div>
            <div className="flex min-h-0 flex-1 flex-col pt-3">
              <RepositoryFileView
                key={path}
                reference={{ path, symbol: null }}
                source={source.source}
                fill
              />
            </div>
          </div>
        </>
      )}
    </div>
  );
}

/** A file the context names. With `onOpen` (a host that reads repository
 *  files) a file opens whole there; without it a changed one opens in the
 *  Code tab, another in the app's file dialog where it has one. A directory,
 *  and anything else, opens on GitHub. `shown` marks the file the column
 *  beside the context shows. */
function ContextChip({
  document,
  reference,
  onOpen,
  shown = false,
}: {
  document: ReviewDocument;
  reference: ReviewContextRef;
  onOpen?: (path: string) => void;
  shown?: boolean;
}) {
  const files = useReviewFiles();
  const sources = useReviewSources();
  const directory = isContextDirectory(document, reference.path);
  const path = reference.path.replace(/\/+$/, "");
  const href = contextRefUrl(document, path);
  const name = `${path.slice(path.lastIndexOf("/") + 1)}${directory ? "/" : ""}`;
  const title = reference.symbol ? `${reference.symbol} in ${path}` : path;
  const label = (
    <span className="flex min-w-0 items-baseline gap-1.5 whitespace-nowrap">
      {reference.symbol ? (
        <>
          <code className="min-w-[4ch] shrink truncate font-mono text-xs text-primary">
            {reference.symbol}
          </code>
          <span aria-hidden className="flex-none text-tertiary">
            ·
          </span>
        </>
      ) : null}
      <span className="min-w-[4ch] shrink-[2] truncate text-secondary">{name}</span>
    </span>
  );
  if (onOpen && !directory)
    return (
      <button
        type="button"
        title={title}
        aria-pressed={shown}
        data-shown={shown ? "" : undefined}
        className={cn(
          chipLinkClass,
          "cursor-pointer text-left data-[shown]:border-default data-[shown]:bg-surface-selected data-[shown]:text-primary",
          FOCUS_RING,
        )}
        onClick={() => onOpen(path)}
      >
        {label}
      </button>
    );
  const changed = !directory && files?.has(path);
  const openFile = sources?.openRepositoryFile;
  if (changed || (!directory && openFile))
    return (
      <button
        type="button"
        title={title}
        data-changed={changed ? "" : undefined}
        className={cn(chipLinkClass, "cursor-pointer text-left", FOCUS_RING)}
        onClick={() => {
          if (changed && files?.open(path)) return;
          if (!openFile?.({ path, symbol: reference.symbol })) openExternal(href);
        }}
      >
        {label}
      </button>
    );
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      title={title}
      className={cn(chipLinkClass, FOCUS_RING)}
    >
      {label}
      <ExternalLinkIcon size={10} className="flex-none text-tertiary" />
    </a>
  );
}

/** Brings the item a link names (a risk, a step, a tile) into view and marks
 *  it, once the section is drawn. */
function useItemJump(root: RefObject<HTMLElement | null>, ids: string[], historical: boolean) {
  const key = ids.join(" ");
  useEffect(() => {
    if (!key || historical) return;
    let cancel: (() => void) | undefined;
    const frame = requestAnimationFrame(() => {
      const element = root.current;
      const target = key
        .split(" ")
        .map((id) => window.document.getElementById(id))
        .find((found) => found && element?.contains(found));
      if (!target) return;
      for (
        let own = target.closest("details");
        own;
        own = own.parentElement?.closest("details") ?? null
      )
        own.open = true;
      const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? true;
      cancel = centerJump(target, reduced);
      flashJump(target, reduced);
      if (!target.hasAttribute("tabindex")) target.tabIndex = -1;
      target.focus({ preventScroll: true });
    });
    return () => {
      cancelAnimationFrame(frame);
      cancel?.();
    };
  }, [key, historical, root]);
}
