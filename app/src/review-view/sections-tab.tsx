import { type RefObject, useEffect, useRef, useState } from "react";
import pluralize from "pluralize";
import { chipLinkClass } from "@baum/viewer/components/chip-classes";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { HunkEvidence } from "@baum/viewer/components/hunk-evidence";
import { ExternalLinkIcon } from "@baum/viewer/components/icons";
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
import { Badge, Button, CardHeader, LiveDot } from "@baum/viewer/components/ui";
import {
  contextRefUrl,
  isContextDirectory,
  sectionDetails,
  type Anchor,
  type ReviewContext,
  type ReviewContextRef,
  type ReviewDocument,
  type Risk,
  type Section,
} from "@baum/viewer/review-document";
import { draftsOn, type DraftCard } from "../tiles/drafts";
import { useTileHost } from "../tiles/host";
import { PendingCard } from "../tiles/pending-card";
import type { TileContext } from "../tiles/types";
import {
  behaviourSteps,
  CONTEXT_ID,
  tilesOn,
  type ListedSection,
  type ReviewView,
  type ViewTile,
} from "./model";
import { NavRow } from "./nav-row";
import { ViewTileCard } from "./view-tile";

/** How it works: the list of sections ("Where this fits" first) beside the
 *  section read, which shows its summary, the tiles placed on it, its
 *  behaviour, its risks and its code, folded. */
export function SectionsTab({
  document,
  view,
  list,
  section,
  item,
  ctx,
  idPrefix,
  context,
  historical,
}: {
  document: ReviewDocument;
  view: ReviewView;
  list: ListedSection[];
  section: string;
  item?: string;
  ctx: TileContext;
  idPrefix: string;
  context: ReviewPlaceContext;
  historical: boolean;
}) {
  const shown = document.sections.find((each) => each.id === section);
  // The tiles being made in the session panel stand on their sections, on
  // the latest version only.
  const drafts = historical
    ? []
    : draftsOn(
        view.drafts ?? [],
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
      <nav aria-label="Sections" className="min-w-0">
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
                onSelect={() => context.select({ kind: "section", id: entry.id })}
              />
            </li>
          ))}
        </ol>
      </nav>
      <article ref={root} id={`${idPrefix}review-view-section`} className="min-w-0 space-y-7">
        {section === CONTEXT_ID ? (
          <WhereThisFits
            document={document}
            view={view}
            ctx={ctx}
            idPrefix={idPrefix}
            drafts={drafts}
            pending={context.contextPending && !historical}
          />
        ) : shown ? (
          <SectionBody
            key={shown.id}
            document={document}
            view={view}
            section={shown}
            ctx={ctx}
            idPrefix={idPrefix}
            drafts={drafts}
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
  view,
  section,
  ctx,
  idPrefix,
  drafts,
  onRetryDetails,
}: {
  document: ReviewDocument;
  view: ReviewView;
  section: Section;
  ctx: TileContext;
  idPrefix: string;
  drafts: DraftCard[];
  onRetryDetails?: (id: string) => void | Promise<unknown>;
}) {
  const details = sectionDetails(section);
  const steps = behaviourSteps(section);
  // The section's first diagram leads, right under its summary; tables,
  // excerpts and library tiles follow what the section does and its risks.
  const tiles = tilesOn(view, section.id);
  const lead = tiles.find((tile) => DIAGRAM_TYPES.has(tile.type));
  const rest = tiles.filter((tile) => tile !== lead);
  const obsolete = section.derived.state === "stale";
  const open = section.risks.filter((risk) => risk.state !== "mitigated" || obsolete);
  const mitigated = obsolete ? [] : section.risks.filter((risk) => risk.state === "mitigated");
  const itemId = (ref: string) => `${idPrefix}${section.id}-${ref}`;
  return (
    <div className="space-y-7">
      <div>
        <div className="mb-2 flex flex-wrap items-center gap-2">
          {section.derived.review_score !== undefined ? (
            <ReviewScoreBadge score={section.derived.review_score} withMeaning />
          ) : null}
          {/* The risks show their own tiers below: beside the review score
              a second level reads as a contradiction, so the tier stands
              only where the section has no score. */}
          {obsolete ? (
            <Badge tone="neutral">No longer in the code</Badge>
          ) : section.derived.review_score === undefined ? (
            <TierBadge tier={section.derived.risk_tier} risks={section.risks.length} />
          ) : null}
        </div>
        <CardHeader size="pane" title={<InlineMarkdown>{section.title}</InlineMarkdown>} />
      </div>
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
      {section.summary ? (
        <div className="space-y-2">
          <Markdown className={PROSE}>{section.summary.text}</Markdown>
          <Cites document={document} anchors={withoutHunks(section.summary.anchors)} />
        </div>
      ) : null}
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
                  <Markdown className="text-primary">{step.after?.trim() || step.text}</Markdown>
                  {step.before ? (
                    <p className="text-base text-secondary">Before: {step.before}</p>
                  ) : null}
                  <Cites document={document} anchors={step.anchors} />
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
      {section.hunks.length > 0 ? (
        <Part label="Code">
          <HunkEvidence
            document={document}
            anchors={section.hunks.map((id) => ({ type: "hunk" as const, id }))}
          />
        </Part>
      ) : null}
    </div>
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

function RiskItem({ risk, document, id }: { risk: Risk; document: ReviewDocument; id: string }) {
  return (
    <Item id={id} className="space-y-1.5">
      <TierBadge tier={risk.tier} />
      <Markdown className="text-primary">{risk.text}</Markdown>
      <Cites document={document} anchors={risk.anchors} />
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

const CONTEXT_PARTS: { key: keyof ReviewContext; label: string }[] = [
  { key: "area", label: "Area" },
  { key: "reached_by", label: "How it's reached" },
  { key: "contracts", label: "What it relies on" },
  { key: "related", label: "Nearby code" },
];

/** "Where this fits": the review's context, each part with the files it rests
 *  on, then the tiles placed on it. */
function WhereThisFits({
  document,
  view,
  ctx,
  idPrefix,
  drafts,
  pending,
}: {
  document: ReviewDocument;
  view: ReviewView;
  ctx: TileContext;
  idPrefix: string;
  drafts: DraftCard[];
  pending: boolean;
}) {
  const context = document.summary.context;
  const tiles = tilesOn(view, CONTEXT_ID);
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
                  {part.refs.length > 0 ? (
                    <ul className="flex flex-wrap gap-1.5" aria-label={`${label} files`}>
                      {part.refs.map((reference, position) => (
                        <li
                          key={`${reference.path}-${reference.symbol}-${position}`}
                          className="max-w-full min-w-0"
                        >
                          <ContextChip document={document} reference={reference} />
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

/** Prose names its code in chips of its own; the code is under the section. */
function withoutHunks(anchors: Anchor[] | undefined): Anchor[] | undefined {
  return anchors?.filter((anchor) => anchor.type !== "hunk");
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
