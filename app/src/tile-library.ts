import { request } from "./api";
import type { TileSize } from "./tiles/tile-document";

/** A saved tile type as Settings › Tile library lists it (`GET
 *  api/tile-types`): never its HTML, which only its frame draws. */
export type TileTypeSummary = {
  name: string;
  title: string;
  use_when: string;
  glance: string;
  /** "chat" (made in chat and saved) or "import" (from a file). */
  origin: string;
  created: string;
  updated: string;
  sizes: TileSize[];
  kit: number;
  digest: string;
  /** When a review last used it; absent when none has. */
  used_at?: string;
  /** In how many repositories reviews used it. */
  used_in: number;
  samples: string[];
  versions: number;
  /** The repositories that pin it. */
  pinned: string[];
};

export type TileTypeVersion = { digest: string; at: string; current?: boolean };

/** A saved type as its pane shows it (`GET api/tile-types/{name}`). */
export type TileTypeDetail = TileTypeSummary & {
  fill: string;
  review_checks: string;
  paths: string[];
  applies_with: string[];
  version_list: TileTypeVersion[];
};

/** One sample of a type drawn without a review (`GET
 *  api/tile-types/{name}/preview?sample=`): the served definition, the
 *  sample's data and what the frame gets for its citations, per face. */
export type TileTypePreview = {
  name: string;
  title: string;
  sizes: TileSize[];
  kit: number;
  digest: string;
  samples: string[];
  sample: string;
  data: unknown;
  refs: { glance: Record<string, unknown> | null; detail: Record<string, unknown> | null };
};

const typePath = (name: string) => `api/tile-types/${encodeURIComponent(name)}`;

export const tileLibrary = {
  list: () =>
    request<{ types: TileTypeSummary[] }>("api/tile-types").then((answer) => answer.types ?? []),
  read: (name: string) => request<TileTypeDetail>(typePath(name)),
  preview: (name: string, sample?: string) =>
    request<TileTypePreview>(
      `${typePath(name)}/preview${sample ? `?sample=${encodeURIComponent(sample)}` : ""}`,
    ),
  rename: (name: string, title: string) =>
    request<TileTypeDetail>(`${typePath(name)}/title`, { title }, "PUT"),
  restore: (name: string, digest: string) =>
    request<TileTypeDetail>(`${typePath(name)}/restore`, { digest }),
  // A write carries a JSON body, as the daemon requires of every one.
  remove: (name: string) => request<void>(typePath(name), {}, "DELETE"),
  undelete: (name: string) => request<TileTypeDetail>(`${typePath(name)}/undelete`, {}),
  pin: (name: string, repositories: string[]) =>
    request<TileTypeDetail>(`${typePath(name)}/pins`, { repositories }, "PUT"),
};

/** Where a type came from, in words. */
export function originLabel(origin: string): string {
  return origin === "import" ? "Imported" : "Made in chat";
}

/** The digest's 64 hex digits, as the frame route names a document. */
export function digestHex(digest: string): string {
  return digest.startsWith("sha256:") ? digest.slice("sha256:".length) : digest;
}
