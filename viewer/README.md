# viewer

`@baum/viewer` is the React package that renders a `baum.review/1` document
and its earlier versions: the summary, the sections and their blocks, risks,
tiles, hunk evidence, description images, checks, the details and activity
panes, and the version menu with comparison. It also holds the pure
derivations behind them (review order, risk triage, anchor resolution, version
changes) and the design tokens and primitives the components are built from.

The package is private for now. Hosts consume it as TypeScript source through
a `link:` dependency and compile it with their own bundler; there is no build
step. It has no dependency on any server: it reads only the document and the
versions a host hands it. The page composition around the components (the
workspace, the rail and the panes) is not in the package yet; each host
builds its own from them.

## Using it

React, React DOM and the Markdown, sanitising, validation and pluralisation
libraries are peer dependencies (see `package.json`), so the host and the
viewer share one copy of each. A host that links the package should resolve
those imports from its own `node_modules` (in Vite, `resolve.dedupe`).

Modules are imported by path, for example
`@baum/viewer/components/review-section` or `@baum/viewer/review-document`;
`package.json` lists every entry point.

Styling uses Tailwind CSS v4. Import the viewer's tokens after Tailwind and
add its sources so the utilities its components use are generated:

```css
@import "tailwindcss";
@import "@baum/viewer/viewer.css";
@source "<path to this package>/src";
```

`viewer.css` defines the type scale, the colour ramps and semantic tokens
with their dark variants (applied under `:root[data-theme="dark"]`), and the
categorical series. The sans-serif family comes from `--viewer-font-sans`,
which falls back to the system's sans-serif face; set it on `:root` to
restyle the viewer.

## Host seams

The viewer never fetches anything and never decides who may see a document.
A host connects it through props and context:

| Seam | Required | How |
| --- | --- | --- |
| document source | yes | the `document` prop, and `loadVersion` (`LoadReviewVersion`), which answers a `ReviewVersionState` for an earlier version |
| navigation | yes, for the selection and version hooks | `UrlStateProvider` with the URL's search params and a writer; `ViewerLinkProvider` with an in-app link component, which `ViewerLink` renders (a plain anchor without one). No component of the package links in-app yet; `ViewerLink` is there for hosts and for the page composition to come |
| attachment URL | no | `AttachmentUrlProvider` resolves an attachment id to an address; without it `images` blocks draw nothing |
| hunk patch source | no | `HunkPatchProvider` loads a file's patch for cited code whose stored excerpt was omitted |
| file opener | no | `ReviewFilesProvider` opens a cited file in the host's own diff view instead of following the link |
| row actions | no | `renderCheck` on `ChangesOverview` and `RisksOverview` puts a host's read mark or approval beside each row |

Side panes and header controls (discussion, statistics, generation) are the
host's own components, placed around the viewer's.

## Schema and tile types

The document types in `src/review-document.ts` mirror
[`../schema/review-1.json`](../schema/review-1.json) by hand; the schema is
canonical. Tiles are validated against it with Ajv at runtime, and
`src/legacy-review-tiles.json` keeps the definitions of kinds the schema has
retired, so stored tiles of those kinds still render. `pnpm generate` writes
the tile types to `generated/review-tile-types.d.ts` from both files;
`pnpm typecheck` runs it first, and the output is not committed.

## Development

Requires Node.js 24 and pnpm.

```sh
pnpm install
pnpm check            # lint, format check, typecheck and unit tests
just check            # the same, through this directory's justfile
pnpm format           # format with Biome
pnpm test:unit:watch  # watch the unit tests while editing
```

Unit tests use Vitest and sit beside the code they cover; fixtures live in
`test/`. The tests run with the locale forced to en-US, since the formatters
follow the runtime locale. `src/typography.test.ts` fails on a font size
outside the type scale in `viewer.css`, and `src/dependencies.test.ts` on a
shipped module importing a package that is not a peer dependency.
