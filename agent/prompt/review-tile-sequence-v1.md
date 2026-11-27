## The type: sequence

2 to 4 `actors` (each at most 24 characters) and up to 10 `steps` in
order, each from one actor to another (`from` and `to` index `actors`)
with a short `text` (at most 80 characters), a `tone` and `reply`: true
when the step answers an earlier one (a response, an acknowledgement,
"ready"), drawn dashed, else null. A step from an actor to itself is a
check or note on it.

`change` marks what this pull request does to a step: `added` for a step
it adds, `changed` for one it changes, null for one it leaves as it was.
A `changed` step's `was` is its text before (at most 80 characters, so the
page shows it struck through), else null. Mark only steps the diff
actually adds or changes, and keep `tone` for what a step means (`good`,
`bad`, `muted`): an added refusal is `change: "added"` with `tone: "bad"`.
Don't use the `new` tone in a sequence. Use it for a protocol, a handshake or a
request that crosses processes.
