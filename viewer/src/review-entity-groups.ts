import { seriesColor } from "./series";
import { type Block, type Entity, type Section } from "./review-document";

/** The one surface this module groups so far. A changed endpoint is the surface
 *  callers meet, and the contract tile is the only tile that zooms into it. */
const API_ENTITY_KIND = "endpoint";
const API_TILE_KIND = "api-contract";

/** What the group is called on the page: the endpoint is the API. */
const API_GROUP_LABEL = "API";

export type EntityContentGroup = {
  id: string;
  label: string;
  kind: string;
  entity: Entity;
  color: string;
  tiles: unknown[];
};

export type SectionContentGroups = {
  groups: EntityContentGroup[];
  /** Tiles no endpoint claimed, which keep rendering where they do today. */
  remainderTiles: unknown[];
};

export function entityGroupKey(entity: Entity): string {
  return `${entity.kind}:${entity.name}`;
}

export function entityGroupColor(index: number): string {
  return seriesColor(index);
}

export function entityGroupWash(color: string): string {
  return `color-mix(in srgb, ${color} 12%, var(--surface-card))`;
}

export function entityGroupStroke(color: string): string {
  return `color-mix(in srgb, ${color} 38%, var(--border-default))`;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function tileKind(tile: unknown): string | undefined {
  const kind = asRecord(tile)?.kind;
  return typeof kind === "string" ? kind : undefined;
}

function tileHunks(tile: unknown): string[] {
  const anchors = asRecord(tile)?.anchors;
  if (!Array.isArray(anchors)) return [];
  return anchors.flatMap((anchor) => {
    const record = asRecord(anchor);
    return record?.type === "hunk" && typeof record.id === "string" ? [record.id] : [];
  });
}

/** The operation an API tile says it is about: `GET /activity`. Absent when
 *  the payload does not name both halves, so a caller can fall back to the
 *  hunks and files the tile cites. */
function declaredOperation(tile: unknown): string | null {
  const data = asRecord(asRecord(tile)?.data);
  const method = data?.method;
  const path = data?.path;
  if (typeof method !== "string" || typeof path !== "string") return null;
  const operation = `${method} ${path}`.trim().replace(/\s+/g, " ");
  return operation.length > 1 ? operation : null;
}

function sameOperation(entityName: string, operation: string): boolean {
  return entityName.trim().replace(/\s+/g, " ").toLowerCase() === operation.toLowerCase();
}

function tilePaths(tile: unknown): string[] {
  const code = asRecord(tile)?.code;
  if (!Array.isArray(code)) return [];
  return code.flatMap((file) => {
    const path = asRecord(file)?.path;
    return typeof path === "string" ? [path.toLowerCase()] : [];
  });
}

/** Whether this section is the one that states what the change does.
 *
 *  The block is the only stable way to ask. A section's id is a model-chosen
 *  slug the schema constrains to `sec_*` and nothing more, and its title is
 *  free text, so neither can be matched against the word "behavior" — but a
 *  `behavior` block is a closed schema kind carrying the numbered steps. */
function describesBehavior(section: Section): boolean {
  return section.blocks.some((block) => block.kind === "behavior");
}

function apiEntities(section: Section): Entity[] {
  return section.blocks.flatMap((block) =>
    block.kind === "entities"
      ? (block as Extract<Block, { kind: "entities" }>).items.filter(
          (item) => item.kind === API_ENTITY_KIND,
        )
      : [],
  );
}

/** Which endpoint a contract tile is about. The operation the tile declares
 *  (`GET /activity`) wins, because two endpoints often share a handler and a
 *  section-wide hunk. A declared operation that names none of them stays flat
 *  rather than landing on a different one. With no declaration, a tile that
 *  cites one endpoint's hunks or names its file belongs to it; an ambiguous
 *  tile belongs to none, because a contract shown under the wrong path is
 *  worse than one shown flat. */
function assignTile(tile: unknown, groups: EntityContentGroup[]): EntityContentGroup | null {
  const operation = declaredOperation(tile);
  if (operation) {
    const declared = groups.filter((group) => sameOperation(group.entity.name, operation));
    return declared.length === 1 ? declared[0]! : null;
  }
  if (groups.length === 1) return groups[0]!;
  const hunks = tileHunks(tile);
  const paths = tilePaths(tile);
  const scores = groups.map((group) => {
    let score = hunks.some((id) => group.entity.hunks.includes(id)) ? 3 : 0;
    const name = group.entity.name.toLowerCase();
    const path = group.entity.path?.toLowerCase();
    if (paths.some((file) => file === name || file === path || file.endsWith(name))) score += 4;
    return score;
  });
  const best = Math.max(0, ...scores);
  if (best === 0) return null;
  const winners = groups.filter((_, index) => scores[index] === best);
  return winners.length === 1 ? winners[0]! : null;
}

/** Gather a section's API contract tiles under the endpoints they describe.
 *
 *  Only the section that describes the change's behaviour gets the panel: the
 *  contract belongs beside the steps it explains, not wherever a section
 *  happens to have named an endpoint. Returns null there, and whenever there is
 *  nothing to frame — no changed endpoint, or no contract tile that belongs to
 *  one. A coloured box around nothing is chrome. Everything the endpoints do
 *  not claim comes back in `remainderTiles` so the rest of the section renders
 *  exactly as it did before. */
export function groupApiContent(
  section: Section,
  tiles: unknown[] | undefined = section.tiles,
): SectionContentGroups | null {
  if (!describesBehavior(section)) return null;
  const entities = apiEntities(section);
  if (entities.length === 0) return null;

  const groups: EntityContentGroup[] = entities.map((entity, index) => ({
    id: entityGroupKey(entity),
    label: API_GROUP_LABEL,
    kind: entity.kind,
    entity,
    color: entityGroupColor(index),
    tiles: [],
  }));

  const remainderTiles: unknown[] = [];
  for (const tile of Array.isArray(tiles) ? tiles : []) {
    const group = tileKind(tile) === API_TILE_KIND ? assignTile(tile, groups) : null;
    if (group) group.tiles.push(tile);
    else remainderTiles.push(tile);
  }

  const claimed = groups.filter((group) => group.tiles.length > 0);
  return claimed.length > 0 ? { groups: claimed, remainderTiles } : null;
}
