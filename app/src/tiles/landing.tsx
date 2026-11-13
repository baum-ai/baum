import pluralize from "pluralize";
import { useLayoutEffect, useMemo, useRef, type RefObject } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { StatusBlock } from "@baum/viewer/components/editorial";
import { ChevronDownIcon } from "@baum/viewer/components/icons";
import { JumpLink } from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { scrollerOf } from "@baum/viewer/components/scroller";
import { centerJump } from "@baum/viewer/components/jump-focus";
import { useUrlState } from "@baum/viewer/navigation";
import { humanize, TIER_COLOR } from "@baum/viewer/review-document";
import { type MenuItem, useContextMenu } from "../context-menu";
import { CARD_HEIGHT, GlanceCard, SPAN } from "./card";
import { useTileHost, type LandingMemory } from "./host";
import { landingOf, onLanding, type Landing } from "./layout";
import { Clamp, plainText } from "./parts";
import { readExpansions, withExpansions, type Expansions } from "./place";
import { FixedStrip } from "./strip";
import { changeById, mustShowRisks, tileById, type Tile } from "./tile-document";
import type { TileContext } from "./types";

/** The landing page: the fixed strip, the risks that must be read when the
 *  Risks tile does not show them, and the grid of glances. */
export function TileLanding({
  ctx,
  generation,
  focus,
  memory,
}: {
  ctx: TileContext;
  generation: ReviewPlaceContext["generation"];
  /** A tile or group a link asked the page to show. */
  focus?: string;
  memory: RefObject<LandingMemory>;
}) {
  const url = useUrlState();
  const expansions = readExpansions(url.search);
  const landing = useMemo(() => landingOf(ctx.doc, expansions), [ctx.doc, url.search]);
  const expand = (next: Expansions) =>
    url.update((current) => withExpansions(current, next), {
      replace: true,
      preventScrollReset: true,
    });
  const root = useRef<HTMLDivElement>(null);
  useReturn(root, memory, focus, ctx.historical);
  return (
    <div ref={root} className="@container/tiles flex min-w-0 flex-col gap-4">
      <h2 className="sr-only">Summary</h2>
      <FixedStrip ctx={ctx} generation={generation} />
      <MustShowBand ctx={ctx} landing={landing} />
      <TileGrid ctx={ctx} landing={landing} expansions={expansions} expand={expand} />
    </div>
  );
}

/** Coming back from a tile, the page is where the reader left it and the
 *  card they opened has the keys; a link to a tile or group scrolls to it. */
function useReturn(
  root: RefObject<HTMLDivElement | null>,
  memory: RefObject<LandingMemory>,
  focus: string | undefined,
  historical: boolean,
) {
  useLayoutEffect(() => {
    if (historical) return;
    const saved = memory.current;
    const element = root.current;
    if (!element) return;
    // After the workspace's own scroll to the pane's top, which runs once
    // this pane is drawn.
    let frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(() => {
        if (saved) {
          scrollerOf(element).scrollTo({ top: saved.scroll, behavior: "instant" });
          const card = element.querySelector<HTMLElement>(
            `[data-tile="${CSS.escape(saved.tile)}"]`,
          );
          if (card) {
            for (const block of document.querySelectorAll("[data-review-block-active]"))
              block.removeAttribute("data-review-block-active");
            card.setAttribute("data-review-block-active", "");
            card.querySelector<HTMLElement>("[data-block-open]")?.focus({ preventScroll: true });
          }
          memory.current = null;
          return;
        }
        if (!focus) return;
        const target = element.querySelector<HTMLElement>(
          focus.startsWith("group:")
            ? `[data-tile-group="${CSS.escape(focus.slice(6))}"]`
            : `[data-tile="${CSS.escape(focus)}"]`,
        );
        if (target)
          centerJump(target, window.matchMedia("(prefers-reduced-motion: reduce)").matches);
      });
    });
    return () => cancelAnimationFrame(frame);
    // Once, as the page arrives.
  }, []);
}

/** The active critical and high risks, when the Risks tile cannot be relied
 *  on to show them: it is not ready, not on the landing page, or absent. */
function MustShowBand({ ctx, landing }: { ctx: TileContext; landing: Landing }) {
  const risks = mustShowRisks(ctx.doc);
  const tile = tileById(ctx.doc, "risks");
  const shown = tile && tile.state === "ready" && onLanding(landing, tile.id);
  if (risks.length === 0 || shown) return null;
  return (
    <StatusBlock tone="critical" label="Read before approving" boxed>
      <ul data-review-block="" className="space-y-1.5 outline-none">
        {risks.map((risk) => (
          <li key={risk.id} className="flex min-w-0 items-start gap-2 text-base">
            <span
              aria-hidden
              className="mt-[0.45rem] h-2 w-2 flex-none rounded-full"
              style={{ background: TIER_COLOR[risk.tier] }}
            />
            <div className="min-w-0 flex-1">
              <span className="sr-only">{humanize(risk.tier)} risk: </span>
              <Clamp lines={2} className="text-primary">
                {plainText(risk.text, ctx.labels)}
              </Clamp>
              <JumpLink
                tone="secondary"
                className="text-xs"
                onClick={() =>
                  tile ? ctx.open(tile.id, risk.id) : ctx.openChange(risk.change, risk.id)
                }
              >
                {changeById(ctx.doc, risk.change)?.title ?? "Open"} →
              </JumpLink>
            </div>
          </li>
        ))}
      </ul>
    </StatusBlock>
  );
}

function TileGrid({
  ctx,
  landing,
  expansions,
  expand,
}: {
  ctx: TileContext;
  landing: Landing;
  expansions: Expansions;
  expand: (next: Expansions) => void;
}) {
  const host = useTileHost();
  const menu = useContextMenu();
  const toggleGroup = (group: string, open: boolean) => {
    const groups = new Set(expansions.groups);
    if (open) groups.add(group);
    else groups.delete(group);
    expand({ ...expansions, groups });
  };
  const menuItems = (tile: Tile) => (): MenuItem[] => [
    { label: "Open", onSelect: () => ctx.open(tile.id) },
    ...(host.comment && !ctx.historical && !host.draft
      ? [{ label: "Comment", hint: "c", onSelect: () => host.comment?.(tile.id) }]
      : []),
    ...(host.ask ? [{ label: "Ask about this tile", onSelect: () => host.ask?.(tile) }] : []),
    ...(host.fullReview && ctx.doc.origin.kind === "converted"
      ? [
          {
            label: "Open in Full review",
            onSelect: () => {
              window.location.assign(host.fullReview!(tile));
            },
          },
        ]
      : []),
  ];
  return (
    <>
      <div className="grid grid-cols-1 gap-3 @min-[36rem]/tiles:grid-cols-2 @min-[52rem]/tiles:grid-cols-6">
        {landing.items.map((item) => {
          switch (item.kind) {
            case "heading":
              return (
                <h3
                  key={`heading-${item.group.id}`}
                  data-tile-group={item.group.id}
                  className="col-span-full -mb-1 flex items-baseline gap-2 pt-3"
                >
                  <span className="text-xs font-medium tracking-[0.08em] text-tertiary uppercase">
                    {item.group.title}
                  </span>
                  <span className="text-xs text-tertiary tabular-nums">{item.count}</span>
                </h3>
              );
            case "tile":
              return (
                <GlanceCard
                  key={item.tile.id}
                  tile={item.tile}
                  size={item.entry.size}
                  ctx={ctx}
                  menu={menu}
                  menuItems={menuItems(item.tile)}
                  comments={host.commentCounts.get(item.tile.id) ?? 0}
                />
              );
            case "more":
              return (
                <MoreCard
                  key={`more-${item.group.id}`}
                  title={item.group.title}
                  hidden={item.hidden}
                  onOpen={() => toggleGroup(item.group.id, true)}
                />
              );
            case "less":
              return (
                <div key={`less-${item.group.id}`} className="col-span-full -mt-1">
                  <button
                    type="button"
                    aria-expanded
                    onClick={() => toggleGroup(item.group.id, false)}
                    className={cn(
                      "cursor-pointer rounded-sm text-base text-link hover:underline",
                      FOCUS_RING,
                    )}
                  >
                    Show fewer {item.group.title.toLowerCase()}
                  </button>
                </div>
              );
          }
        })}
      </div>
      {landing.rest.length > 0 || expansions.more ? (
        <div data-review-block="" className="rounded-md outline-none">
          <button
            type="button"
            aria-expanded={expansions.more}
            onClick={() => expand({ ...expansions, more: !expansions.more })}
            className={cn(
              "flex w-full cursor-pointer items-center gap-3 rounded-lg border border-dashed border-default px-4 py-3 text-left hover:bg-surface-hover",
              FOCUS_RING,
            )}
          >
            <ChevronDownIcon
              size={14}
              className={cn(
                "flex-none text-tertiary transition-transform motion-reduce:transition-none",
                expansions.more && "rotate-180",
              )}
            />
            {expansions.more ? (
              <span className="text-base text-primary">Show fewer tiles</span>
            ) : (
              <span className="min-w-0 flex-1 truncate text-base">
                <span className="font-medium text-primary">
                  Show {pluralize("more tile", landing.rest.length, true)}
                </span>
                <span className="text-secondary">
                  {" "}
                  · {landing.rest.map((tile) => tile.title).join(", ")}
                </span>
              </span>
            )}
          </button>
        </div>
      ) : null}
      {menu.menu}
    </>
  );
}

/** The members a group holds back, as one cell that names them. */
function MoreCard({
  title,
  hidden,
  onOpen,
}: {
  title: string;
  hidden: Tile[];
  onOpen: () => void;
}) {
  return (
    <div
      data-review-block=""
      className={cn(
        "relative flex min-w-0 flex-col gap-2 rounded-lg border border-dashed border-default px-4 py-3 outline-none hover:bg-surface-hover data-[review-block-active]:border-focus",
        CARD_HEIGHT.third,
        SPAN.third,
      )}
    >
      <button
        type="button"
        aria-expanded={false}
        data-block-open=""
        onClick={onOpen}
        className={cn(
          "cursor-pointer rounded-sm text-left text-base font-semibold text-primary",
          "after:absolute after:inset-0 after:rounded-lg after:content-['']",
          FOCUS_RING,
        )}
      >
        Show {hidden.length} more {pluralize(title.toLowerCase(), hidden.length)}
      </button>
      <ul className="min-h-0 flex-1 space-y-1.5 overflow-hidden text-base text-secondary">
        {hidden.slice(0, 6).map((tile) => (
          <li key={tile.id} className="truncate">
            {tile.title}
          </li>
        ))}
      </ul>
      {hidden.length > 6 ? (
        <p className="text-xs text-tertiary">and {hidden.length - 6} more</p>
      ) : null}
    </div>
  );
}

/** Remembers the landing page's scroll as a tile opens from it. */
export function rememberLanding(
  memory: RefObject<LandingMemory>,
  pane: Element | null,
  tile: string,
) {
  if (!pane) return;
  memory.current = { scroll: scrollerOf(pane).scrollTop, tile };
}
