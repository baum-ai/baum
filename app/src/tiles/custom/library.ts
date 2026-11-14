import type { Tile } from "../tile-document";

/** The built-in library tile types the daemon ships (GET api/tile-library),
 *  by definition digest. A custom tile whose definition has one of these
 *  digests is one of baum's own types: the page names it by its title, as
 *  it names its other built-in tiles, and still mounts it in its sandboxed
 *  frame. Membership is decided by the digest the daemon lists, never by
 *  anything the tile says about itself. */
let builtin: ReadonlySet<string> = new Set();
let loading: Promise<void> | null = null;

type Listing = { types?: { digest?: unknown }[] };

/** Loads the listing once per page. It never fails: on an error the set
 *  stays as it was and the next call asks again. */
export function loadTileLibrary(read: () => Promise<Listing>): Promise<void> {
  loading ??= read()
    .then((listing) => {
      const digests = new Set<string>();
      for (const type of listing.types ?? [])
        if (typeof type.digest === "string") digests.add(type.digest);
      builtin = digests;
    })
    .catch(() => {
      loading = null;
    });
  return loading;
}

/** Whether a custom tile is of a built-in library type. */
export function isBuiltinType(tile: Tile): boolean {
  const digest = (tile.definition as { digest?: unknown } | null)?.digest;
  return tile.type === "custom" && typeof digest === "string" && builtin.has(digest);
}

/** For tests: the listing as if loaded. */
export function setBuiltinDigests(digests: Iterable<string>) {
  builtin = new Set(digests);
  loading = Promise.resolve();
}
