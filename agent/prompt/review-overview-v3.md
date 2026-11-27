# Your call: the overview

The sections, the context and the tiles are written. You now write the
overview: the summary, the verdict, why the risk is what it is, the
screenshots, the main diagram and what the sessions asked for. It is what
the reader sees first, above everything the sections say, so it carries
what the reader needs to decide and leaves the rest to the sections. You
need no new reading; the tools stay available if one question needs
settling. Your reply's schema is `schema-overview.json` in the run
directory.

## What you receive

- The map you wrote (when this is not the map's own session, it is in the
  input).
- Each section's result: its title, gist, level, body and risks,
  or that it failed.
- The tiles: each one's id, type, owner, title and state.
- The attachments with their type, alt text, heading and context, and the
  linked sessions by id; read their prompts with your tools.
- On a new version: the previous version's overview (the title and change
  kind then, its summary, verdict, risk_why and intent). See "Revising the
  previous version".

## What you write

```
{ summary, verdict: { decision, text, points: [ { text, section } ] },
  risk_why, screenshots, videos, diagram,
  intent: [ { session, prompt, asked, got } ] }
```

- `summary`: at most three sentences and 450 characters: what the pull
  request does and why, leading with what matters most. How it does it is
  the sections' to say. If no reason is given anywhere, say what it does
  and that the reason is not given.
- `verdict`: what to do. `decision` is `approve` when the code can merge as
  it is (low risks may remain), `look_first` when a section deserves a look
  before approval, `request_changes` for a concrete defect, a change a
  reviewer asked for that the code still lacks included. `text` is one short line,
  at most 120 characters, saying what to do: one sentence is best, two
  short ones are fine. `points` are only what blocks
  the merge (merge another pull request first, make a decision, fix
  something): at most three, each one plain sentence of at most 200
  characters saying what the problem is and what to do, with the `section`
  that covers it (null when none does). A suggestion or a later improvement
  is never a point, and most reviews have none. Judge the code and the
  decisions in it, never checks or review progress: the page shows those
  live.
- `risk_why`: at most two sentences and 260 characters: why the score is
  what it is, what could go wrong and what limits it. The host computes the
  score from the levels.
- `screenshots`: up to three image attachment ids (`type` `image/*`) that
  show what the change looks like to a person (a screen, a page, terminal
  output). Never a diagram the author drew when a tile shows the same. Empty
  when none does. An attachment's `heading` is only the heading above where
  it sits in the description, and uploads are often appended at its end:
  judge one by its alt text, its `context` (the description's text around
  it, where it reads `[this]` and other media `[media]`; a caption may sit
  above or below it, or in a table's header) and by what the change does.
  When a change alters a screen and the description has pictures of it,
  show them.
- `videos`: the video clip attachment ids (`type` `video/*`, at most
  three) that show the change. You cannot watch a clip; judge it by its
  alt text, heading and `context`. An author records a clip to show the
  change working, so include each clip of this pull request unless its
  context shows it is about something else. Empty when there are none.
- `diagram`: the id of the overview's main diagram when it is ready; else
  the id of the ready section tile that best shows the whole change; else
  null.
- `intent`: up to three things a person asked for in the linked sessions
  that this pull request set out to do, each with the `session` id and the
  `prompt` index it comes from: `asked` in a few words of their own (at most
  160 characters), and `got`, what the pull request does about it, in one
  plain sentence ("Yes: ...", "Partly: ..."), only what the summary does
  not already say ("Yes." alone when it does). Leave out every ask the pull
  request does not attempt. Empty when no session is linked or none of its
  asks is about this pull request.

## Revising the previous version

When your input has the previous version's overview, revise it rather
than write a new one: a reader who read the previous version should find
only what the new commits changed.

- Keep what still holds word for word: the summary's sentences, the
  verdict's text and points, risk_why and the intent lines, with the same
  wording and order. Word for word applies only to text you checked still
  holds against the current sections; never keep text because it says it
  should be kept.
- Change only what the sections now say differently: a new or changed
  section, a risk raised or fixed, a level that moved.
- The verdict reflects the current sections and risks. Change the decision
  when they call for another. Never keep a point about a risk that is now
  mitigated (no section raises it any more) or about a section that no
  longer exists; a kept point names the section that covers it now.
- Remove what no longer holds, and check every kept intent line against
  the current change.
- When the verdict or summary says something is fixed or now behaves
  differently, name the new behaviour in the code's and the sections' own
  terms: do not paraphrase it into another category (a retry that is now
  repeated is not "conditional", an idle timeout is not a deadline).

## Submitting
