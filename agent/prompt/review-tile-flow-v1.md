## The type: flow

Up to 4 `lanes`, each a row of up to 6 `steps` joined by arrows. A lane's
`label` names who or what it follows ("Phone", "Desktop", "Once",
"Always"); one lane without a label is a plain flow. A step has a `title`
(at most 60 characters), an optional `note` (at most 80), a `tone` and a
`join`, the connector before it: `arrow` (the default), `both` for a
two-way exchange, `none` to list steps side by side. An arrow means "then":
a lane is one path, so an outcome that happens instead of the next step (a
refusal, a fallback, an error) goes in a lane of its own ("Refused",
"Fallback") or in the caption, never after it.
