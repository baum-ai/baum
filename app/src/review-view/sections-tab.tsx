import { type RefObject, useEffect, useMemo, useRef, useState } from "react";
import pluralize from "pluralize";
import { chipLinkClass } from "@baum/viewer/components/chip-classes";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import {
  ChevronDownIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  ExternalLinkIcon,
} from "@baum/viewer/components/icons";
import { centerJump, flashJump } from "@baum/viewer/components/jump-focus";
import { InlineMarkdown, Markdown } from "@baum/viewer/components/markdown";
import { Cites } from "./cites";
import { useReviewFiles, useReviewSources } from "@baum/viewer/components/review-contexts";
import {
  Item,
  Part,
  PROSE,
  ReviewScoreBadge,
  TierBadge,
} from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { tabPanelProps } from "@baum/viewer/components/tabs";
import { Badge, Button, CardHeader, LiveDot } from "@baum/viewer/components/ui";
import {
  contextRefUrl,
  isContextDirectory,
  type ReviewContextRef,
  type ReviewDocument,
} from "@baum/viewer/review-document";
import { draftsOn, type DraftCard } from "../tiles/drafts";
import { useTileHost } from "../tiles/host";
import { PendingCard } from "../tiles/pending-card";
import type { TileContext } from "../tiles/types";
import type { V2Context, V2Risk } from "./document";
import {
  CONTEXT_ID,
  detailsOf,
  SECTION_VIEWS,
  sectionViews,
  selectionOf,
  shownSectionView,
  tilesOn,
  type ListedSection,
  type ReviewV2,
  type SectionView,
  type V2Section,
  type ViewTile,
} from "./model";
import { NavRow } from "./nav-row";
import { SectionViewBody, SectionViewTabs } from "./section-views";
import { ViewTileCard } from "./view-tile";

/** How it works: the list of sections ("Where this fits" first) beside the
 *  section read, which shows its views (its Overview: its summary, the tiles
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
    <div className="grid min-w-0 gap-6 @3xl:grid-cols-[minmax(13rem,17rem)_minmax(0,1fr)]">
      <SectionList list={list} section={section} idPrefix={idPrefix} context={context} />
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
    </div>
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
  const shown = shownSectionView(asked, views.counts);
  const tablist = `${idPrefix}section-views`;
  return (
    <div className="space-y-5">
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
      <SectionViewTabs
        tablist={tablist}
        value={shown}
        counts={views.counts}
        onChange={(next) =>
          context.select(selectionOf({ tab: "sections", section: section.id, view: next }))
        }
      />
      {SECTION_VIEWS.map(({ value }) => (
        <div key={value} {...tabPanelProps(tablist, value, value === shown)} className="min-w-0">
          {value !== shown ? null : value === "overview" ? (
            <SectionOverview
              document={document}
              review={review}
              section={section}
              ctx={ctx}
              idPrefix={idPrefix}
              drafts={drafts}
              onRetryDetails={onRetryDetails}
            />
          ) : (
            <SectionViewBody
              document={document}
              review={review}
              section={section.id}
              shown={value}
              views={views}
              context={context}
            />
          )}
        </div>
      ))}
    </div>
  );
}

/** A section's Overview: what it does, in its summary, its lead diagram, its
 *  steps, its risks and the rest of its tiles. */
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
  const details = detailsOf(section);
  const steps = section.steps;
  // The section's first diagram leads, right under its summary; tables,
  // excerpts and library tiles follow what the section does and its risks.
  const tiles = tilesOn(review, section.id);
  const lead = tiles.find((tile) => DIAGRAM_TYPES.has(tile.type));
  const rest = tiles.filter((tile) => tile !== lead);
  const obsolete = section.derived.state === "stale";
  const open = section.risks.filter((risk) => risk.state !== "mitigated" || obsolete);
  const mitigated = obsolete ? [] : section.risks.filter((risk) => risk.state === "mitigated");
  const itemId = (ref: string) => `${idPrefix}${section.id}-${ref}`;
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
      {section.summary.trim() ? <Markdown className={PROSE}>{section.summary}</Markdown> : null}
      {lead ? (
        <SectionTiles
          tiles={[lead]}
          drafts={[]}
          document={document}
          ctx={ctx}
          idPrefix={idPrefix}
        />
      ) : null}
      {steps.length > 0 ? (
        <Part label="What it does">
          <ol className="space-y-2">
            {steps.map((step, position) => (
              <Item key={step.id} id={itemId(step.id)} className="flex gap-3">
                <span className="w-5 flex-none pt-px text-right text-base tabular-nums text-tertiary">
                  {position + 1}
                </span>
                <div className="min-w-0 flex-1 space-y-1.5">
                  {step.title ? (
                    <p className="font-medium text-primary">
                      <InlineMarkdown>{step.title}</InlineMarkdown>
                    </p>
                  ) : null}
                  <Markdown className="text-primary">{step.text}</Markdown>
                  <Cites document={document} anchors={step.cites} />
                </div>
              </Item>
            ))}
          </ol>
        </Part>
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
      <SectionTiles
        tiles={rest}
        drafts={drafts}
        document={document}
        ctx={ctx}
        idPrefix={idPrefix}
      />
    </div>
  );
}

/** The sections to choose from: the whole list beside the section where the
 *  page is wide, and where it is not (a narrow window, or the side pane
 *  open) one row naming the section read, which opens the list, with the
 *  previous and next section beside it, so the section starts on the first
 *  screen. */
function SectionList({
  list,
  section,
  idPrefix,
  context,
}: {
  list: ListedSection[];
  section: string;
  idPrefix: string;
  context: ReviewPlaceContext;
}) {
  const [open, setOpen] = useState(false);
  const at = list.findIndex((entry) => entry.id === section);
  const current = list[at];
  const pick = (id: string) => {
    setOpen(false);
    context.select({ kind: "section", id });
  };
  const rows = (
    <ol className="flex flex-col gap-0.5">
      {list.map((entry) => (
        <li key={entry.id} className="min-w-0">
          <NavRow
            title={entry.title}
            line={entry.obsolete ? "No longer in the code" : entry.line}
            wideLine
            muted={entry.obsolete}
            active={entry.id === section}
            controls={`${idPrefix}review-view-section`}
            onSelect={() => pick(entry.id)}
          />
        </li>
      ))}
    </ol>
  );
  return (
    <nav aria-label="Sections" className="min-w-0">
      <div className="hidden @3xl:block">{rows}</div>
      <div className="space-y-2 @3xl:hidden">
        <div className="flex items-stretch gap-2">
          <button
            type="button"
            aria-expanded={open}
            aria-controls={`${idPrefix}review-view-section-list`}
            onClick={() => setOpen((shown) => !shown)}
            className={cn(
              "flex min-w-0 flex-1 items-center gap-2 rounded-md border border-default bg-surface-card px-3 py-2 text-left shadow-xs hover:bg-surface-hover",
              FOCUS_RING,
            )}
          >
            <span className="flex-none text-xs text-tertiary tabular-nums">
              {at >= 0 ? `${at + 1} of ${list.length}` : `${list.length} sections`}
            </span>
            <span className="min-w-0 flex-1 truncate text-base text-primary">
              {current ? <InlineMarkdown>{current.title}</InlineMarkdown> : "Choose a section"}
            </span>
            <ChevronDownIcon
              aria-hidden
              className={cn("size-4 flex-none text-tertiary", open && "rotate-180")}
            />
          </button>
          <Button
            variant="secondary"
            size="compact"
            aria-label="Previous section"
            disabled={at <= 0}
            onClick={() => {
              const previous = list[at - 1];
              if (previous) pick(previous.id);
            }}
          >
            <ChevronLeftIcon aria-hidden className="size-4" />
          </Button>
          <Button
            variant="secondary"
            size="compact"
            aria-label="Next section"
            disabled={at < 0 || at >= list.length - 1}
            onClick={() => {
              const next = list[at + 1];
              if (next) pick(next.id);
            }}
          >
            <ChevronRightIcon aria-hidden className="size-4" />
          </Button>
        </div>
        {open ? <div id={`${idPrefix}review-view-section-list`}>{rows}</div> : null}
      </div>
    </nav>
  );
}

/** The native diagram types: one of them leads a section. */
const DIAGRAM_TYPES = new Set(["flow", "columns", "sequence"]);

/** The tiles placed on a section, and the pending cards of those being made
 *  for it in the session panel. */
function SectionTiles({
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

function RiskItem({ risk, document, id }: { risk: V2Risk; document: ReviewDocument; id: string }) {
  return (
    <Item id={id} className="space-y-1.5">
      <TierBadge tier={risk.tier} />
      <Markdown className="text-primary">{risk.text}</Markdown>
      <Cites document={document} anchors={risk.cites} />
    </Item>
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
 *  on, then the tiles placed on it. */
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
  return (
    <div className="space-y-7">
      <CardHeader size="pane" title="Where this fits" />
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
                          <ContextChip document={document} reference={{ path, symbol: null }} />
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
}

/** A file the context names: a changed one opens in the Code tab, another
 *  in the app's file dialog where it has one, anything else on GitHub. */
function ContextChip({
  document,
  reference,
}: {
  document: ReviewDocument;
  reference: ReviewContextRef;
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
          if (!openFile?.({ path, symbol: reference.symbol }))
            window.open(href, "_blank", "noreferrer");
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
