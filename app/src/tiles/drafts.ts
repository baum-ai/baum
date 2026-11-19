/** A tile being made in the session panel, as the tile route lists it
 *  (tiledrafts.Draft): the section it will stand on (`context` for Where this
 *  fits), its title and how far it got. */
export type DraftCard = {
  tile: string;
  title: string;
  /** The section's ID; absent when the route named none. */
  section?: string;
  phase: "writing" | "checking" | "failed" | "unfinished";
  round?: number;
  reason?: string;
};

const PHASES = new Set(["writing", "checking", "failed", "unfinished"]);

/** The tile route's pending cards; a card that is not well formed is left
 *  out. */
export function parseDrafts(value: unknown): DraftCard[] {
  if (!Array.isArray(value)) return [];
  const out: DraftCard[] = [];
  for (const item of value) {
    if (typeof item !== "object" || item === null) continue;
    const d = item as Record<string, unknown>;
    if (typeof d.tile !== "string" || typeof d.title !== "string" || !PHASES.has(d.phase as string))
      continue;
    const card: DraftCard = { tile: d.tile, title: d.title, phase: d.phase as DraftCard["phase"] };
    if (typeof d.section === "string" && d.section) card.section = d.section;
    if (typeof d.round === "number" && d.round > 0) card.round = d.round;
    if (typeof d.reason === "string" && d.reason) card.reason = d.reason;
    out.push(card);
  }
  return out;
}

/** What a pending card says of its tile: being made, and the step it is
 *  at, or that it was left unfinished. */
export function draftLine(draft: DraftCard): string {
  switch (draft.phase) {
    case "writing":
      return "Being made in the session panel · Writing the component";
    case "checking":
      return `Being made in the session panel · Checking${draft.round && draft.round > 1 ? ` (round ${draft.round})` : ""}`;
    case "failed":
      return `Being made in the session panel · ${draft.reason ?? "The check could not run"}`;
    case "unfinished":
      return "Not finished";
  }
}

/** The pending cards on a section, in the route's order: a card stands on the
 *  section it names, and one naming a section the page does not list on the
 *  first section listed. */
export function draftsOn(
  drafts: readonly DraftCard[],
  section: string,
  listed: readonly string[],
): DraftCard[] {
  return drafts.filter((draft) =>
    draft.section && listed.includes(draft.section)
      ? draft.section === section
      : section === listed[0],
  );
}
