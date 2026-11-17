## The type: flow

Up to 4 `lanes`, each a row of up to 6 `steps` joined by arrows. A lane's
`label` names who or what it follows ("Phone", "Desktop", "Once",
"Always"); one lane without a label is a plain flow. A step has a `title`
(at most 60 characters, ideally under 30), an optional `note` (at most 80,
ideally one short clause), a `tone` and a `join`, the connector before it:
`arrow` (the default), `both` for a two-way exchange, `none` to list steps
side by side. Steps are drawn as equal boxes in a row, so keep titles and
notes about the same length; a long note makes every box in its row tall.

An arrow means "then": a lane is one path. An outcome that happens instead
of the next step (a refusal, a fallback, an error) is a branch: a lane of
its own, labelled by the case ("No source", "Refused"), with one or two
steps (what goes wrong, then what happens), and `from` set to the number
(from 1) of the first lane's step it leaves after. The page draws a branch
under the steps that follow that one, so the reader sees where it splits
off. A lane that is not a branch has `from` null. A lane that lists
outcomes which do not follow one another (several ways a step can fail,
several refusals) joins every step after the first with `none`, so no arrow
suggests one leads to the next.

Mark at most two steps of the first lane with a tone, the ones the pull
request adds or changes; a branch's first step is usually `bad`.
