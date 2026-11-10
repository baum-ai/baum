# prfilter

The pull-request filter language in Go: `repo=acme/app is!=draft
(review=approved OR risk>=4) flaky`. It parses a query (`Parse`, `Problem`),
tests index entries against it (`Compile`, `Matches`) and folds an inbox's
parts into one query (`FoldedQuery`), so the daemon can match inboxes as the
app does.

The TypeScript parser in `app/src/pr-filter.ts` (with `foldedQuery` in
`app/src/inboxes.ts`) is the twin of this package. Error messages, matching
and folding must be identical. `testdata/cases.json` holds the shared cases:
this package's tests and `app/src/pr-filter-fixtures.test.ts` both run every
one. A change to the language changes both implementations and the fixtures;
regenerate the expected values from the TypeScript side and check the Go side
agrees.
