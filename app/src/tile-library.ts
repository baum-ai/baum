import { appURL, LocalRequestError, request } from "./api";
import { invoke } from "./desktop/native";
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
  /** False while an imported version waits for its render check. */
  checked: boolean;
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

/** A tile type file checked for import (`POST api/tile-type-files/check`):
 *  what the import dialog shows before anything is saved, and its first
 *  sample drawn as a saved type's preview is. */
export type TileTypeFileCheck = {
  /** The name the type would get: the file's own, or with a numeric
   *  suffix when that one is taken (`renamed_from`). */
  name: string;
  renamed_from?: string;
  title: string;
  use_when: string;
  fill: string;
  glance: string;
  review_checks: string;
  paths: string[];
  sizes: TileSize[];
  kit: number;
  digest: string;
  samples: string[];
  sample: string;
  data: unknown;
  refs: { glance: Record<string, unknown> | null; detail: Record<string, unknown> | null };
};

/** What an import saved (`POST api/tile-type-files`). */
export type TileTypeImported = { name: string; renamed_from?: string; checked: boolean };

/** The largest tile type file the daemon reads. */
export const MAX_FILE_BYTES = 3 << 20;

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
  /** The type's file, as text. */
  exportFile: async (name: string) => {
    const response = await fetch(appURL(`${typePath(name)}/export`), { cache: "no-store" });
    if (!response.ok) {
      const text = await response.text();
      let message = `The tile type could not be exported (${response.status})`;
      try {
        message = (JSON.parse(text) as { message?: string }).message ?? message;
      } catch {
        // A plain-text answer.
      }
      throw new LocalRequestError(message, undefined, response.status);
    }
    return response.text();
  },
  checkFile: (file: string) => request<TileTypeFileCheck>("api/tile-type-files/check", { file }),
  importFile: (file: string) => request<TileTypeImported>("api/tile-type-files", { file }),
};

/** Saves an exported file: through the desktop app's save dialog (null when
 *  the person cancelled it), or as a download in a browser. */
export async function saveTileTypeFile(name: string, contents: string): Promise<string | null> {
  if ((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__)
    return invoke<string | null>("save_tile_type", { name, contents });
  const url = URL.createObjectURL(new Blob([contents], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = `${name}.baumtile.json`;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
  return link.download;
}

/** Where a type came from, in words. */
export function originLabel(origin: string): string {
  return origin === "import" ? "Imported" : "Made in chat";
}

/** The digest's 64 hex digits, as the frame route names a document. */
export function digestHex(digest: string): string {
  return digest.startsWith("sha256:") ? digest.slice("sha256:".length) : digest;
}
