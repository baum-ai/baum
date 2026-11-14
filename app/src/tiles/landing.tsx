import pluralize from "pluralize";
import { useEffect, useLayoutEffect, useMemo, useRef, type RefObject } from "react";
import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { StatusBlock } from "@baum/viewer/components/editorial";
import { ChevronDownIcon } from "@baum/viewer/components/icons";
import { JumpLink } from "@baum/viewer/components/review-parts";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { scrollerOf } from "@baum/viewer/components/scroller";
import { centerJump } from "@baum/viewer/components/jump-focus";
import { useUrlState } from "@baum/viewer/navigation";
import { humanize, TIER_COLOR } from "@baum/viewer/review-document";
import { navigate } from "../api";
import { type MenuItem, useContextMenu } from "../context-menu";
import { GlanceCard } from "./card";
import { useTileHost, type LandingMemory } from "./host";
import { breakFills, landingOf, onLanding, type Landing } from "./layout";
import { Clamp, plainText } from "./parts";
import { readExpansions, withExpansions, type Expansions } from "./place";
import { FixedStrip } from "./strip";
import { noteLanding } from "./keys";
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
  // The entry Escape in a tile goes back to, rather than adding another.
  useEffect(() => {
    if (!ctx.historical) noteLanding(url.search);
  }, [ctx.historical, url.search]);
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
          memory.current = null;
          const card = element.querySelector<HTMLElement>(
            `[data-tile="${CSS.escape(saved.read)}"]`,
          );
          // Where the reader left the page, if the card they read last is the
          // one they opened; else that card, brought into view; else, for a
          // tile the landing page does not show, the first card.
          if (saved.left && saved.left.tile === saved.read)
            scrollerOf(element).scrollTo({ top: saved.left.scroll, behavior: "instant" });
          else card?.scrollIntoView({ block: "center", behavior: "instant" });
          const target = card ?? element.querySelector<HTMLElement>("article[data-tile]");
          if (target) {
            for (const block of document.querySelectorAll("[data-review-block-active]"))
              block.removeAttribute("data-review-block-active");
            target.setAttribute("data-review-block-active", "");
            target.querySelector<HTMLElement>("[data-block-open]")?.focus({ preventScroll: true });
          }
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
          <li
            key={risk.id}
            data-risk={risk.id}
            className="flex min-w-0 items-start gap-2 text-base"
          >
            <span
              aria-hidden
              className="mt-2 h-2 w-2 flex-none rounded-full"
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
  const fills = breakFills(landing, landing.rest.length > 0 || expansions.more);
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
              navigate(host.fullReview!(tile));
            },
          },
        ]
      : []),
  ];
  return (
    <>
      {/* The cards' level: Overview and Risks stand under it, a group's under
          the group's own heading. */}
      <h3 className="sr-only">Tiles</h3>
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
                  fill={fills.get(item.tile.id)}
                  ctx={ctx}
                  menu={menu}
                  menuItems={menuItems(item.tile)}
                  comments={host.commentCounts.get(item.tile.id) ?? 0}
                />
              );
            case "more":
              return (
                <ControlRow
                  key={`more-${item.group.id}`}
                  expanded={false}
                  onClick={() => toggleGroup(item.group.id, true)}
                  label={`Show ${item.hidden.length} more ${pluralize(item.group.title.toLowerCase(), item.hidden.length)}`}
                  names={item.hidden.map((tile) => tile.title)}
                />
              );
            case "less":
              return (
                <ControlRow
                  key={`less-${item.group.id}`}
                  expanded
                  onClick={() => toggleGroup(item.group.id, false)}
                  label={`Show fewer ${item.group.title.toLowerCase()}`}
                />
              );
          }
        })}
      </div>
      {landing.rest.length > 0 || expansions.more ? (
        <ControlRow
          expanded={expansions.more}
          onClick={() => expand({ ...expansions, more: !expansions.more })}
          label={
            expansions.more
              ? "Show fewer tiles"
              : `Show ${pluralize("more tile", landing.rest.length, true)}`
          }
          names={expansions.more ? undefined : landing.rest.map((tile) => tile.title)}
        />
      ) : null}
      {menu.menu}
    </>
  );
}

/** A slim row across the grid that shows or hides tiles: a group's members
 *  past its first ones, or the tiles past the landing area, named after it. */
function ControlRow({
  label,
  names,
  expanded,
  onClick,
}: {
  label: string;
  names?: string[];
  expanded: boolean;
  onClick: () => void;
}) {
  return (
    <div data-review-block="" className="col-span-full rounded-md outline-none">
      <button
        type="button"
        aria-expanded={expanded}
        data-block-open=""
        onClick={onClick}
        className={cn(
          "flex w-full cursor-pointer items-center gap-2.5 rounded-md border border-dashed border-default px-3 py-2 text-left hover:bg-surface-hover",
          FOCUS_RING,
        )}
      >
        <ChevronDownIcon
          size={14}
          className={cn(
            "flex-none text-tertiary transition-transform motion-reduce:transition-none",
            expanded && "rotate-180",
          )}
        />
        <span className="min-w-0 flex-1 truncate text-base">
          <span className="font-medium text-primary">{label}</span>
          {names?.length ? (
            <span className="text-secondary">
              {" "}
              · {names.slice(0, 3).join(", ")}
              {names.length > 3 ? ` and ${names.length - 3} more` : ""}
            </span>
          ) : null}
        </span>
      </button>
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
  memory.current = { left: { scroll: scrollerOf(pane).scrollTop, tile }, read: tile };
}

/** Remembers the tile being read, for the landing page to come back to. */
export function rememberRead(memory: RefObject<LandingMemory>, tile: string) {
  memory.current = { left: memory.current?.left ?? null, read: tile };
}
