Fill the findings tile: each fact of this kind the pull request shows, with the hunk that shows it.

## Find them

Read the diff. An item is a <the kind of fact, said exactly: what counts and where it is found>. Leave out tests, fixtures and generated files.

## Fill each item

- `title`: the fact in a few words, naming the thing as the code does.
- `severity`: `high` when <condition>, `medium` when <condition>, otherwise `low`.
- `text`: one or two sentences of fact in the Markdown subset, or null.
- `cites`: the hunks that show it, most telling first, as `{"type": "hunk", "id": "<hunk id>", "lines": [first, last]}` with lines of the new file inside the hunk's new range. For a fact only in removed lines, leave out `lines`.

`summary`: one or two sentences, the most severe item first.

## When something is absent

When the pull request has none, answer that the tile does not apply and write no data. Use null for a value the diff does not show. Never guess.

## Example

```json
{"summary": "Raises the retry limit to 10.", "items": [{"title": "Retry limit raised to 10", "severity": "medium", "text": null,
  "cites": [{"type": "hunk", "id": "h_0a1b2c3d4e5f", "lines": [12, 12]}]}]}
```
