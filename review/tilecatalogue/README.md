# tilecatalogue

The tile kinds generation may offer and each kind's selection guidance. It has
no dependencies, so a host can check its tile allow-list against the catalogue
when it starts. The document model is described in
[`ARCHITECTURE.md`](../../ARCHITECTURE.md).

## Catalogue

- [`catalogue.json`](catalogue.json) order is offer order. Payload fields live
  only in [`review-1.json`](../../schema/review-1.json), which needs a
  `$defs/tile` variant per kind.
- Tiles explain existing sections and risks; they add no navigation, severity
  or approval state. JSON picks the kind, the viewer owns layout. No UI-preview
  kind.
- To retire a kind, remove it here and from the schema, and move its
  variant and payload definitions to
  [`legacy-review-tiles.json`](../../schema/legacy-review-tiles.json), where
  stored-document validation accepts tiles already stored and the viewer
  still renders them. To remove a field from a kind the viewer keeps
  rendering stored tiles of, list it in `retiredTileFields`
  ([`stored.go`](../stored.go)) and `withoutRetiredTileFields`
  (`oss/viewer/src/review-tiles.ts`). A stored tile neither file accepts
  after that is dropped on read.

## Allow-list

- A host's tile allow-list is empty by default: the model sees no guidance,
  catalogue or tile schema, and returned tiles are removed. Otherwise other
  kinds are removed before validation, so they never cause a repair
  ([`tile_kinds.go`](../tile_kinds.go)).
- An unknown or repeated kind is an error.
- `Instructions(kinds)` is the text a run adds to the prompt body for the
  offered kinds (the tile guidance and their catalogue entries), empty when
  none are offered. The generate-and-repair loop appends it, so a prompt never
  carries it ([`instructions.go`](instructions.go)).

## Sources

- The model names `head` or `merge_base` and hunk headers; the review package
  resolves the SHA, sets `review_version` and fills lines from the bundle
  ([`tiles.go`](../tiles.go)). An older-version source is accepted only
  unchanged from the previous review.
