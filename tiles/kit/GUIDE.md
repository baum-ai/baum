# Writing a tile type

A tile type is a small, sandboxed component that shows one kind of fact about
a pull request, such as its schema changes. It is written once. For each
review, a model fills the type's **data**; the component draws that data. This
guide is everything you need to write one that passes every check. Start from
the starter in [`starter/`](starter/) and change it; do not start from an
empty file.

**Before you write a tile**, look for one that already does the job, and
write one only when nothing else makes sense. The review page draws flows,
columns, sequences, tables, code excerpts and images (screenshots) itself:
in a chat, add_tile adds them with data you write, an image by its file's
path. Check those, the saved tile library and baum's library types (their
source can be read: [`tiles/library`](../library) holds them), and the
kit's elements and classes in section 6. Reuse a type, or start from the
closest one and change it, rather than write the same thing again.

## 1. The files

A type is one directory:

```
my_type/
  type.json          name, title, use_when, glance, sizes, kit
  fill.md            instructions for the model that fills the data
  review_checks.md   optional: what the review should check when the type applies
  schema.json        the data schema
  tile.html          the component: <style> and <script> only
  samples/*.json     example data with the code it cites (at least one)
```

`type.json`:

```json
{
  "name": "dependency_upgrades",
  "title": "Dependency upgrades",
  "use_when": "When the pull request changes dependency versions in a manifest or lock file. Not for lock-file-only reorderings.",
  "glance": "How many packages change, major upgrades first, each with its versions.",
  "sizes": ["half", "third", "full"],
  "kit": 1
}
```

| Field | Rule |
|---|---|
| `name` | `^[a-z][a-z0-9_]{0,63}$`. Not a reserved name (listed below the table). |
| `title` | 1–160 characters. The card's heading. |
| `use_when` | One line, at most 200 characters: `When …` names the trigger, `Not …` an exclusion. A model reads only this, the pull request's description and its file list to decide whether to fill the type, so be specific about what must be in the diff. |
| `glance` | One line, at most 200 characters: what the glance shows. |
| `sizes` | The widths the glance is made for, preferred first: `full`, `half`, `third`. List only sizes you checked. |
| `kit` | `1`. The kit version the component uses. |
| `applies_with` | Optional: the top-level arrays of the data that show what the type is for, such as `["items"]`. Data with all of them empty is refused as a fill: the filling model answers that the type does not apply instead of writing a tile that says "nothing". |

Reserved names, which no type may have (the check refuses them): `custom`, `overview`, `change`, `risks`, `evidence`, `intent`, `decisions`, `context`, `media`, `everything_else`, `prose`, `table`, `diagram`, `code`, `flow`, `columns`, `sequence`, `images`, `schema_change`, `api_change`. A name also cannot start with `sec_`, `table_`, `code_`, `prose_`, `diagram_` or `library_`. Most of these are only reserved: `overview`, `change`, `risks`, `evidence`, `intent`, `decisions`, `context`, `media`, `everything_else`, `prose` and `diagram` belong to an older layout, and no tile of them can be added.

Types you can add instead of writing one: add_tile adds `flow`, `columns`, `sequence`, `table`, `code` and `images` (screenshots and other image files, by path) with data you write (read tile_type for each one's data), and the library types `schema_change` and `api_change`.

`fill.md` is at most 8000 characters, `review_checks.md` at most 1500. The
host adds `digest` when it loads the type; never write it.

## 2. The rules

1. One fragment: `<style>` and `<script>` (and `<template>` if you want). No
   markup outside them: the script draws everything. Your own class names
   (not starting with `kit-`) are fine for layout.
2. No network and no external resources: no `<link>`, `<iframe>`, `<object>`,
   `<embed>`, `<form>`, `<base>`, `<meta http-equiv>`, `<script src>`,
   `@import`, `url(https:…)`, `fetch`, `XMLHttpRequest`, `WebSocket`. The
   frame's content security policy blocks them and the check fails. The tile
   has only its data.
3. Data enters the page only through `baum.html` and the `baum.*` factories.
   Never `innerHTML`, `outerHTML`, `insertAdjacentHTML` or `document.write`:
   the data comes from a model reading untrusted code.
4. No storage, cookies, `parent`, `top`, `window.open`, `location` or `eval`.
5. Colour, type, radii and spacing come from the kit's classes and `var(--…)`
   tokens only, so the tile follows the app's light and dark themes. Never
   hard-code a colour. Never use `prefers-color-scheme` (it follows the
   system, not the app) or `vh`/`vw` units (the detail frame's height follows
   its content, so they grow without end).
6. Status tones (`data-tone`: `positive`, `caution`, `critical`, `info`,
   `neutral`) mean status only, never a category. A kind of change is a
   `kit-tag`; what a change is (New, Changed, Removed, Breaking) is a
   `kit-pill`, and "breaking" is one with `data-tone="critical"`.
7. Every fact the tile states cites the code behind it.
8. The glance answers "do I need to open this?" and fits its box. The detail
   shows everything in the data.
9. Use `baum.empty(text)` when the data has nothing to show.
10. Render synchronously, or return a promise that settles within 3 seconds.
11. Call `baum.tile` exactly once.

## 3. The two faces

`baum.tile({ glance, detail })` registers two render functions. Each gets a
`view`: `data` (already valid against your schema), `face` (`"glance"` or
`"detail"`), `size` (`"full"`, `"half"` or `"third"`), `theme`, `lang` and
`root`. Return a node (or an array, or text); the kit puts it in `root`.

**Glance**: the tile in brief, inside a card under the card's title, where
the tile library shows a type (Settings › Tile library); the render check
draws it at every size `sizes` declares. It is not interactive and gets no
code excerpts. Its box, the frame's whole viewport:

| size | width × height (px) |
|---|---|
| `third` | 310 × 164 |
| `half` | 488 × 180 |
| `full` | 1022 × 116 |
| phone | 311 × the size's height |

That is a lead line, three rows (third) or four (half) of about 26px, and a
foot; a `full` box holds a lead line and two rows, so lay its rows out in two
columns: a `<div class="kit-columns">` of `kit-row`s (`kit-columns` on a
`kit-list` does nothing, since the list's own layout wins). Use
`kit-glance` (a full-height column), `kit-lede` for the lead, `kit-list` for
rows, `kit-truncate` on anything from data that could be long, and `kit-foot`
for small facts at the bottom. Show at most what fits; say "N more" in the
foot. Branch on `size` for how many rows; lay out by width otherwise.

**Detail**: the tile as the review page shows it, on the section it stands
on and as wide as that section, with `size` `"full"`; the render check draws
it 720px and 360px wide (a phone). Its height follows its content. Use `kit-section` with a
`kit-heading`, `kit-card` per thing, `kit-table` inside `kit-scroll` for
tables, `baum.cite` chips, and `baum.diff` for the code. Put the most
important things first (breaking, destructive, high severity). Long lists go
in `<details class="kit-details">` (one may hold others). A list of cards goes
in a `kit-stack`, which spaces them. Rows of a before and an after (a
column's old and new type) mark the words that differ with `baum.compare`,
put a `kit-pill` on what changed, draw the rows that stayed the same in
`kit-muted` and, when some did, offer a `kit-switch` "Show only changes"
that hides them. On a 360px phone a table of more than
three columns wraps badly: keep tables to three or four short columns.

## 4. The data schema

`schema.json` is JSON Schema 2020-12 in a restricted dialect, so a model can
fill it as structured output and validating it is cheap.

- The top level is `"type": "object"`.
- Every object has `"additionalProperties": false` and lists all its
  properties. Put every property in `required` and make an optional value
  nullable: `"type": ["string", "null"]`. That is what structured output
  needs.
- Every array has `maxItems` (at most 500). Every string has `maxLength` (at
  most 20000) unless it has `enum` or `const`.
- Allowed: `type`, `properties`, `required`, `additionalProperties`,
  `items`, `minItems`, `maxItems`, `enum` (at most 64 values), `const`,
  `minLength`, `maxLength`, `pattern`, `minimum`, `maximum`,
  `exclusiveMinimum`, `exclusiveMaximum`, `anyOf`/`oneOf` (at most 8
  options, nested at most 2 deep), `allOf` (at most 4), `$ref` to
  `#/$defs/<name>`, `$defs` at the top level, and `title`, `description`,
  `$comment`, `examples`, `default`.
- Refused: `format`, `uniqueItems`, `if`/`then`/`else`, `not`,
  `patternProperties`, `propertyNames`, `prefixItems`, `contains`,
  `dependentRequired`, `unevaluated*`, `$id`, `$anchor`, recursion (a
  `$ref` that leads back to itself).
- At most 32 KiB, 512 schema objects, 12 levels deep.
- Provided definitions you may `$ref` but not define: `citation` (any
  citation), `hunkId`, `itemRef`, `anchor`, `markdown` (a string of at most
  4000 characters in the Markdown subset), `tier`.
- `description` (at most 1000 characters) is read by the filling model: say
  what goes in each field.
- The host writes a top-level `related` array to the library's own types;
  leave that name unused.

Data is at most 64 KiB with at most 256 citations.

**Images.** A tile draws an image from a string in its data that holds a
`data:image/…` URL: `baum.html` takes one in `src`, and no other URL. Never
write an image's bytes, or base64, into data yourself. Give the image a
plain string in the schema (`{"type": ["string", "null"], "maxLength":
20000}`: no `pattern`, and a `maxLength` of at least 100), and in the data
for a pull request put `{"image_file": "/absolute/path/to/shot.png"}` in
that string's place. The host reads the file (PNG, JPEG, GIF or WebP, at
most 40 MiB), downscales it as it does a screenshot, stores it with the
pull request's added tiles and keeps a short reference in the string; the
frame gets the string as a `data:image/…` URL. A tile's data names at most
6 images, 16 MiB together after downscaling (an image named twice counts
twice), and they do not count against its 64 KiB. Samples cannot name files (section 8): leave their image
strings `null`, or use a tiny `data:` URL. The edge samples fill an image
string with text, so draw it only when it is an image:

```js
const image = (src, alt) =>
  typeof src === "string" && src.startsWith("data:image/")
    ? baum.html`<img class="shot" src=${src} alt=${alt}>`
    : null;
```

## 5. Citations

A citation is an object in the data that names a part of the review. The page
resolves each one and the kit draws it. Cite code with a hunk:

```json
{ "type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [12, 20] }
```

`id` is a hunk ID from the diff the filling model reads
(`^h_[0-9a-f]{12}(-[0-9]+)?$`); `lines`, optional, is
`[first, last]` in the new file, inside the hunk. Declare a slot as hunks only
with a definition of your own (as the starter does: `$defs/hunk`), or any
citation with `{ "$ref": "#/$defs/citation" }`. Any object in the data with
this shape is a citation, wherever it is. Give each fact an array of 1–8
citations, the most telling first.

## 6. The kit API (`window.baum`, kit 1)

| API | What it does |
|---|---|
| `baum.tile({ glance, detail })` | Registers the two render functions. Once. |
| `` baum.html`<p>${value}</p>` `` | Escaping template: values become text, nodes or arrays of them; `null`, `undefined` and booleans draw nothing. In an attribute, a value is set as text, and `null` or `false` removes the attribute (`open=${i < 3}`). It throws on `on*` attributes, URLs, and values in `style` other than a custom property (`style="--value:${share}"`). |
| `baum.cite(citation, label?)` | A citation chip; a button that jumps to the code in a detail. |
| `baum.diff(citation, { maxLines })` | The cited hunk: one line (file and range) in a glance, numbered lines in a detail (default 40, then "Show N more lines"). |
| `baum.md(text, { inline })` | Markdown subset: paragraphs, `**strong**`, `*em*`, `` `code` ``, lists, `###` headings, quotes. Raw HTML stays text. |
| `baum.compare(was, now)` | A before and an after word by word: `{ was, now, same }`, two spans with the words the other lacks marked (`kit-del`, `kit-ins`), and whether they read the same. Put each span in its own `baum.html` slot. |
| `baum.empty(text)` | The empty state. |
| `baum.fail(message)` | The error state, for data the tile cannot draw. |
| `baum.refs.get(citation)` | What the page resolved for a citation (`label`, `path`, `range`, `tier`…), or null. |
| `baum.jump(citation)`, `baum.ask()`, `baum.openDetail()` | Ask the page to go to a citation, open the chat about the tile, or open its detail. Honoured only while the reader is in the tile. |

The kit's elements are `baum-cite`, `baum-diff` and `baum-md`; make them only
with the factories. The view's fields are `data`, `face`, `size`, `theme`,
`lang` and `root`.

**Classes.** Layout: `kit-stack` (column, `data-gap="0"`–`"6"`), `kit-row`
(wrapping row; `data-wrap="no"`, `data-align`, `data-justify="between"`),
`kit-grow` (takes the rest of a row), `kit-natural`, `kit-grid`,
`kit-columns` (`style="--columns:${n}"`), `kit-scroll` (scrolls sideways),
`kit-glance`, `kit-foot`, `kit-section`, `kit-card` (`data-tone`),
`kit-list` (`data-divided`, `data-gap`), `kit-bullets`. Text: `kit-title`,
`kit-lede`, `kit-count` (a headline number), `kit-heading`, `kit-kicker`,
`kit-muted`, `kit-subtle`, `kit-small`, `kit-strong`, `kit-mono`, `kit-num`,
`kit-break`, `kit-truncate`, `kit-clamp-1`, `kit-clamp-2`, `kit-clamp-3`,
`kit-added`, `kit-removed`, `kit-ins`, `kit-del`, `kit-sep`. Marks:
`kit-badge` (status pill, `data-tone`), `kit-pill` (a solid pill for what a
change is: `info` New, `caution` Changed, `critical` Removed or Breaking),
`kit-tag` (a kind or a method; add `kit-mono` for code),
`kit-dot` (`data-tone` or `data-tier`), `kit-callout` (`data-tone`),
`kit-tone`, `kit-bar` (`style="--value:${share}"`, 0 to 1). Structure:
`kit-facts` (a `dl`), `kit-table` (`data-tone` on a `tr` tints an added,
changed or removed row), `kit-details`. Controls: `kit-switch` (a `<label>` around an
`<input type="checkbox" role="switch">` and its words; listen with
`addEventListener`). States: `kit-empty`, `kit-fail`.

**Tokens** (in your `<style>`, as `var(--…)`): text `--text-primary`,
`--text-secondary`, `--text-tertiary`, `--text-disabled`, `--text-link`;
surfaces `--surface-page`, `--surface-card`, `--surface-sunken`,
`--surface-wash`, `--surface-selected`; borders `--border-default`,
`--border-subtle`, `--border-strong`, `--border-focus`, `--focus-ring`;
status `--status-positive`, `--status-caution`, `--status-critical`,
`--status-info`, `--status-neutral` and their backgrounds
`--status-positive-bg`, `--status-caution-bg`, `--status-critical-bg`,
`--status-info-bg`, `--status-neutral-bg`, and
`--status-positive-border`, `--status-caution-border`; risk tiers
`--tier-critical`, `--tier-high`, `--tier-medium`, `--tier-low`; code
`--code-fill`, `--syntax-foreground`, `--syntax-token-keyword`,
`--syntax-token-string`, `--syntax-token-comment`, `--syntax-token-constant`,
`--syntax-token-function`, `--syntax-token-parameter`,
`--syntax-token-punctuation`, `--syntax-token-inserted`,
`--syntax-token-deleted`, `--syntax-token-changed`; charts `--data-1` …
`--data-8` (`--data-2`, `--data-3`, `--data-4`, `--data-5`, `--data-6`,
`--data-7`), `--data-muted`; type `--font-text`, `--font-heading`,
`--font-mono`, `--text-xs`, `--text-base`, `--text-lg`, `--text-2xl`,
`--text-3xl` with `--text-xs--line-height`, `--text-base--line-height`,
`--text-lg--line-height`, `--text-2xl--line-height`,
`--text-3xl--line-height`; radii `--radius-xs`, `--radius-sm`,
`--radius-md`, `--radius-lg`, `--radius-full`; space `--space-1` (4px) …
`--space-8` (40px): `--space-2`, `--space-3`, `--space-4`, `--space-5`,
`--space-6`, `--space-7`; and `--value` and `--columns`, which data sets.

## 7. Fill instructions (`fill.md`)

The filling model sees the pull request (title, description, the diff with
each hunk's ID, path and new line range) and your `fill.md`, and must answer
with data matching the schema. Write `fill.md` as:

1. One sentence: what the tile shows.
2. **Find them**: exactly what in a diff counts, with the code shapes to look
   for, and what to leave out (tests, generated files).
3. **Fill each field**: one bullet per field that is not obvious, with the
   rule for enums and booleans.
4. **Cite**: the citation shape and which hunk goes first.
5. **When absent**: empty arrays, `null`, never guess.
6. **Example**: one complete, valid data object.

## 8. Samples

`samples/<name>.json` is example data plus the code it cites, so the tile can
be checked and drawn without a review:

```json
{
  "name": "example",
  "data": { "...": "data valid against schema.json" },
  "hunks": {
    "h_0a1b2c3d4e5f": {
      "path": "internal/worker/retry.go", "change": "modified",
      "range": [10, 14], "additions": 2, "deletions": 2,
      "header": "@@ -10,5 +10,5 @@",
      "lines": [
        { "kind": "context", "old": 10, "new": 10, "text": "var defaults = Policy{" },
        { "kind": "remove", "old": 12, "text": "\tLimit: 5," },
        { "kind": "add", "new": 12, "text": "\tLimit: 10," }
      ]
    }
  }
}
```

Every hunk the data cites must be in `hunks`, and cited `lines` inside its
`range` (the new file's first and last line). `items` likewise holds cited
risks: `{ "rsk_x": { "label": "…", "tier": "high", "text": "…" } }`. Use a
fake repository (`acme/widgets`), never real code. Write at least: the common
case, a large case (many items, long names), and the empty case.

## 9. Checking

From the repository root:

```sh
just local_app::tile-type-screens path/to/my_type
```

(`path` relative to `oss/`, several separated by commas.) It runs every check
and then draws every sample, plus a `minimal` and a `maximal` sample made
from your schema, as glances at each size and on a phone and as details at
720 and 360px, in light and dark. It fails on:

| Problem | Fix |
|---|---|
| `definition: …` | `type.json` or `schema.json` breaks a rule of sections 1 or 4; the message names the place. |
| `tile.html:L:C: error …` or `warning …` | A rule of section 2. Both fail the check. `unknown_api`, `unknown_class`, `unknown_token`: use only the names in section 6. |
| `sample …: does not match its schema` | Fix the sample or the schema. |
| `the citation at … does not resolve` | Add the hunk to the sample's `hunks`, or put `lines` inside its `range`. |
| `script_error`, `render_failed` | Your script threw. The line is in `tile.html`. A frequent cause: a value placed where `baum.html` refuses it. |
| `overflows sideways` | Something from data does not wrap: add `kit-truncate` (one line) or `kit-break`, or put tables in `kit-scroll`. |
| `overflows the glance box` | Show fewer rows for that size. The `maximal` sample only warns. |
| `citations do not resolve` | A citation in data that the sample's hunks do not hold. |

Pictures land in `oss/app/test-results/tile-types/`, with a contact sheet
of every glance per theme (`<name>-sheet-glance-light.png`). Look at them.

## 10. The render check

When a tile is made in the app, baum checks it with the **render check**
before it is shown: the same checks as `tile-type-screens`, run by the
daemon and drawn by the app in sandboxed frames, in the engine the person's
tiles draw in. It takes the definition, up to 3 samples of your own and,
for a tile made for a pull request, the data it will show there.

1. Without drawing: the definition and its schema (sections 1 and 4), the
   lint of section 2 (errors stop the check; warnings are reported), your
   samples against the schema and their citations against their own hunks,
   and the data against the schema.
2. Then it draws the data, each of your samples and two samples made from
   your schema, `minimal` (only what is required, empty arrays, the
   shortest strings) and `maximal` (every field, the longest arrays and
   strings, long words, a 120-character unbroken token, right-to-left text
   and CJK): glances at each declared size, details at 720 and 360px, and
   the data in the dark theme too.

The result is `pass`, `fail`, `hang` or `unavailable` (no window of the app
could draw it; open the app). `pass` means no errors. Each problem names its
`code`, a `message`, the `sample`, `face`, `size` and `theme` it was found
with, the `line` and `column` in your HTML when they are known, and a fixed
`hint`:

| Code | Hint |
|---|---|
| `definition_invalid` | Fix the definition: the message names the field and the rule it breaks. |
| `data_schema_invalid` | Keep the schema in the dialect of section 4: every object closed, every property required, every array and string bounded. |
| `sample_invalid` | Make the sample's data match the schema, or change the schema; the message names the place. |
| `data_invalid` | Make the data match the schema; the message names the place. |
| `lint` | Follow the rules of section 2: no network, no external resources, no innerHTML, only the names the kit has. |
| `no_hello` | The frame never started: the HTML did not parse, or a script looped before the kit loaded. Check the markup and the first script. |
| `timeout` | Draw synchronously, or return a promise that settles within 3 seconds. |
| `hang` | The component never finished drawing and was stopped: look for an endless loop, and change the code before checking again. |
| `script_error` | Your script threw at the line given; a frequent cause is a value placed where baum.html refuses it. |
| `unhandled_rejection` | A promise was rejected and nothing caught it: await it inside the render, or catch it. |
| `console_error` | Remove the console.error call, or fix what it reports. |
| `render_failed` | The tile did not draw this data: call baum.tile once, and draw what the schema allows instead of calling baum.fail. |
| `csp_violation` | The tile has only its data: remove the request or external resource the frame's policy blocked. |
| `empty` | The face drew nothing visible: draw the data, or call baum.empty(text) when there is nothing to show. |
| `overflow_glance` | Show fewer rows for that size and say "N more" in the foot; put long text in kit-truncate. |
| `overflow_x` | Something from the data does not wrap: add kit-truncate or kit-break, or put the table in kit-scroll. |
| `layout_loop` | The detail's height follows the frame's own height: remove vh, vw and percentage heights. |
| `unresolved_citation` | Cite only what the review holds: a sample's cited hunks must be in its hunks, with lines inside the hunk's range. |
| `uncited` | Cite the pull request: a tile made in chat cites at least one of its hunks, files, prompts or session events, so each claim leads to what the reader can open. |
| `slow` | Drawing took over 500 ms: draw less up front, and put long lists in kit-details. |

When each counts:

| Code | Counts as |
|---|---|
| `definition_invalid` | error, before drawing |
| `data_schema_invalid` | error, before drawing |
| `sample_invalid` | error, before drawing; a warning when an edge sample could not be made |
| `data_invalid` | error, before drawing |
| `lint` | error before drawing, or a warning |
| `no_hello` | error: no word from the frame within 5 seconds |
| `timeout` | error: not drawn 3 seconds after it was given its data |
| `hang` | error: the app stopped answering while it drew; the type is blocked until baum restarts |
| `script_error` | error |
| `unhandled_rejection` | error |
| `console_error` | error |
| `render_failed` | error: `baum.fail`, or `baum.tile` never called |
| `csp_violation` | error |
| `empty` | error: no visible text, image or drawing, and no `baum.empty` |
| `overflow_glance` | error; a warning for the `maximal` sample |
| `overflow_x` | error |
| `layout_loop` | error: the detail's height grew with its frame ten times |
| `unresolved_citation` | error for the data and your samples; not counted for edge samples |
| `uncited` | error, for the data of a tile made in chat |
| `slow` | warning: drawing took over 500 ms |

The kit catches a script's errors inside the frame and reports their
message and line, so read the message, not just the code. Lint and the
render check are quality gates for honest mistakes, not security controls:
the frame's sandbox and content security policy are what keep a tile to its
data.

## 11. Making a type in chat

In the app's session panel, an agent can make a tile for the pull request
page the person has open and add it to that page's review at once, on one of
its sections. It works with four tools:

1. `start_tile {title, section?}` puts a pending card on the section where
   the tile will stand ("Being made in the session panel"): `section` is a
   section's ID, or `context` for Where this fits; without it, the section
   the person is reading. The card counts the check rounds; if the agent
   stops before adding the tile it says "Not finished" for a few minutes,
   with Remove.
2. `tile_guide` gives this guide (`part: "guide"`), the starter's files
   (`"starter"`) and the kit's names (`"manifest"`).
3. `check_tile` runs the render check (section 10) on what you send:
   - `type`: `type.json`'s fields (`name`, `title`, `use_when`, `glance`,
     `sizes`, `kit`) and `fill`, the text of `fill.md`;
   - `data_schema`: `schema.json`;
   - `html`: `tile.html`, at most 256 KiB;
   - `samples`: up to 3 of `samples/*.json`;
   - `data`: the tile's data for this pull request, citing its hunks by the
     IDs the diff tool gives, with each image as `{"image_file": path}`
     (section 4).

   With `data`, every citation must resolve to the review the page shows,
   and the data must cite at least one hunk, file, prompt or session event
   of it (`uncited` otherwise). The data is masked for secrets before
   anything is kept. A problem with what you sent comes back as a `fail`
   result with its hints, never as an error: fix it and check again.
4. `add_custom_tile {check, draft?, section?}` adds the tile whose check
   passed with `data`, within the hour, on the pending card's section unless
   `section` names another. The page shows it at once, under a "Custom tile"
   caption, with Remove on its menu and Undo after a removal.

Two rules matter here:

- **Samples are synthetic.** Write them for a made-up repository
  (`acme/widgets`), with hunks and items of their own, never the pull
  request's code: they are what the tile library keeps and shows if the
  type is saved, and saving refuses a sample that copies the pull request's
  diff.
- **Work in your own scratch space.** Write the type's files in a temporary
  directory of your own, never in the repository's tree: the checkout is
  the person's.

A tile made this way belongs to the pull request's page. On later versions
it is not filled again; it keeps the
data it was made with, marked as from that version. Only a library type is filled for later versions, after the person saves it. To reuse it on other
pull requests, save it to the tile library: "Save to library…" on its menu
asks the agent, who checks it with a synthetic sample and proposes the type
(`propose_tile_type`); nothing is saved until the person saves it.

## 12. Definition of done

- [ ] `just local_app::tile-type-screens <dir>` passes, with no warnings.
- [ ] `baum.tile` is called once and both faces are written.
- [ ] The glance fits every declared size and the phone for every sample but
      `maximal`, and says whether to open the tile.
- [ ] The detail shows everything in the data, the most important first, and
      every fact has a `baum.cite`.
- [ ] Data reaches the page only through `baum.html` and the factories.
- [ ] Only kit classes and tokens; it reads well in light and dark, and at
      360px.
- [ ] The schema is in the dialect, every property required, optional values
      nullable, every fact with citations.
- [ ] `fill.md` says what counts, how to fill each field, how to cite and what
      to do when something is absent, with a complete example.
- [ ] `use_when` is one line: `When …` and `Not …`.
- [ ] Samples: the common case, a large case and the empty case, in a fake
      repository.

## 13. Worked examples

The library's types are complete examples of everything above:
[`tiles/library/schema_change`](../library/schema_change) and
[`tiles/library/api_change`](../library/api_change).
