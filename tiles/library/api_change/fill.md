Fill the interface changes tile: every entry point other code calls that this pull request adds, removes, changes or deprecates, each cited to the hunk that shows it.

## Find the entry points

Read the diff by its content. An entry point is any of:

- An HTTP route: a registration such as Go `mux.HandleFunc("GET /api/widgets/{id}", …)` or `r.Get(…)`, Express or Hono `app.get(…)`, Rails `routes.rb`, a Next or Remix route file, a FastAPI or Flask decorator.
- An RPC, GraphQL or agent tool: a gRPC or JSON-RPC method, a `.proto` or GraphQL schema edit, an MCP or agent tool definition (its name, input schema and result).
- A CLI command or flag: a subcommand, a flag added, renamed or removed, a changed exit code or output format that scripts read.
- A published document: a JSON Schema, OpenAPI file or protocol whose consumers are outside this change.
- An event: a webhook payload, a queue message or an event other services consume.
- A library export: an exported symbol of a package published for other repositories to import (an SDK or a module with its own releases). An exported function or type of an application's own packages is internal, even when exported.

Leave out:

- internal code: functions, types and packages only this repository uses (Go `internal/` packages, the app's own modules), test servers and mocks;
- the user interface: pages and their URLs, components, keyboard shortcuts, CSS classes, data attributes and layout;
- behaviour behind an unchanged interface: a handler that returns different values in the same shape, with the same fields and status codes, is not an interface change.

A configuration file or environment variable counts (surface `other`) only when people outside the code write it: a documented config file, a CLI's settings.

## Fill each entry point

- `surface`, `method` (HTTP only, else null), `name` as callers write it: `/api/widgets/{id}`, `review_get`, `baum review --watch`, `schema/review-1.json`.
- `change`: `added`, `removed`, `changed` or `deprecated` (still served, marked for removal).
- `request` and `response`: for an added entry point, every field the handler reads or returns, from the types the registration leads to; for a changed one, only the fields that change. `before` and `after` are the type or meaning (`string`, `int, 1–100`, `array of Widget`); `required` is whether a caller must send the field or can rely on it.
- `errors`: status, error or exit codes added, removed or changed, with what they mean.
- `auth`: who may call it, when the code shows it (middleware, a token check). null when unknown.
- `breaking`: true when an existing caller can break: the entry point is removed or renamed, a request field becomes required, a field is removed or changes type or meaning, auth is tightened, a status or error code changes, a default changes what callers get. An added entry point is not breaking. `breaking_reason` says who breaks and how, in one sentence.
- `consumers`: callers in this repository of a changed entry point, such as the app's API module or another service; never tests or mocks. `updated: true` and cite the hunks when this pull request changes them. When you see a caller that still uses the old shape, list it with `updated: false` and name its file in `label`.
- `docs`: the interface's reference or README section; `updated` and its hunks, or null when it has none.
- `summary`: one or two sentences of fact, in the Markdown subset (`code` for names). Lead with what breaks.

Give each entry point its own entry. When there are more than 30, group the added ones of one kind under a shared name, such as `settings_* (6 tools)`, and keep every breaking, removed and changed one on its own. Order `endpoints` with breaking ones first, then removed, changed, added.

## Cite

Every entry point cites at least one hunk: the registration or definition first, then the handler and the request and response types. A citation is `{"type": "hunk", "id": "<hunk id from the diff>", "lines": [first, last]}`; `lines` are lines of the new file inside the hunk's new range, covering the registration or type. In a long new file, cite each route's lines, not the whole hunk. For a removed entry point, cite the hunk that removes it without `lines`.

## When something is unknown or absent

Use `null` for what the diff does not show and empty arrays for parts with nothing in them; never guess a field or a status code. When a patch is cut off, list what is shown.

## Example

```json
{
  "summary": "Adds `GET /api/widgets/{id}/tags`; `POST /api/widgets` now **requires** `owner`, which breaks callers that omit it.",
  "endpoints": [
    {"surface": "http", "method": "POST", "name": "/api/widgets", "change": "changed", "auth": "session cookie",
     "request": [{"field": "owner", "change": "added", "before": null, "after": "string, a user login", "required": true}],
     "response": [], "errors": [{"code": "400", "change": "added", "meaning": "owner is missing"}],
     "breaking": true, "breaking_reason": "Clients that create widgets without owner now get 400.",
     "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [88, 104]}]},
    {"surface": "http", "method": "GET", "name": "/api/widgets/{id}/tags", "change": "added", "auth": "session cookie",
     "request": [{"field": "id", "change": "added", "before": null, "after": "path, widget id", "required": true}],
     "response": [{"field": "tags", "change": "added", "before": null, "after": "array of string", "required": true}],
     "errors": [{"code": "404", "change": "added", "meaning": "no such widget"}],
     "breaking": false, "breaking_reason": null,
     "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [40, 41]}, {"type": "hunk", "id": "h_1b2c3d4e5f60", "lines": [10, 36]}]}],
  "consumers": [{"label": "app/src/api.ts createWidget", "updated": true, "cites": [{"type": "hunk", "id": "h_2c3d4e5f6071", "lines": [55, 61]}]}],
  "docs": {"updated": false, "cites": []}
}
```
