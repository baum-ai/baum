/** Where the reader is in the review pane: Summary, the Changes or Risks
 *  overview, one section or risk, the changed files (all folded, or one
 *  opened), the linked sessions
 *  (all of them, or one opened), or the pull request's commits (all, or one
 *  named by its sha). Kept in the URL rather than component
 *  state, the same way the pull request itself already is, so a section or
 *  risk can be reloaded, linked to someone else, and reached with Back. */
export type Selection =
  | { kind: "summary" }
  | { kind: "changes" }
  | { kind: "section"; id: string; decision?: string }
  | { kind: "risks" }
  | { kind: "risk"; section: string; id: string }
  | { kind: "files" }
  | { kind: "file"; id: string }
  | { kind: "sessions" }
  | { kind: "session"; id: string }
  | { kind: "commits" }
  | { kind: "commit"; id: string };

const PARAM = {
  pane: "pane",
  section: "section",
  decision: "decision",
  riskSection: "risk_section",
  riskId: "risk_id",
  file: "file",
  session: "session",
  commit: "commit",
  // A host's own place within the selection, such as the prompt of a session
  // a link opens at. Readers of it take it from the URL themselves; it is
  // cleared with the selection so it never applies to the next one.
  turn: "turn",
} as const;

/** Summary is the default: an absent or unrecognized `pane` reads as it,
 *  rather than as an error, so a hand-edited or stale URL degrades quietly. */
export function readSelection(search: URLSearchParams): Selection {
  const pane = search.get(PARAM.pane);
  if (pane === "changes") return { kind: "changes" };
  if (pane === "risks") return { kind: "risks" };
  if (pane === "section") {
    const id = search.get(PARAM.section);
    if (id) {
      const decision = search.get(PARAM.decision);
      return { kind: "section", id, ...(decision ? { decision } : {}) };
    }
  }
  if (pane === "risk") {
    const section = search.get(PARAM.riskSection);
    const id = search.get(PARAM.riskId);
    if (section && id) return { kind: "risk", section, id };
  }
  if (pane === "files") return { kind: "files" };
  if (pane === "file") {
    const id = search.get(PARAM.file);
    if (id) return { kind: "file", id };
  }
  if (pane === "sessions") return { kind: "sessions" };
  if (pane === "session") {
    const id = search.get(PARAM.session);
    if (id) return { kind: "session", id };
  }
  if (pane === "commits") return { kind: "commits" };
  if (pane === "commit") {
    const id = search.get(PARAM.commit);
    if (id) return { kind: "commit", id };
  }
  return { kind: "summary" };
}

/** Whether two selections name the same place: their canonical encodings
 *  agree. Canonical, because a shared link may have written the same params
 *  in another order, and that is still the same place. */
export function sameSelection(left: Selection, right: Selection): boolean {
  const encode = (selection: Selection) =>
    withSelection(new URLSearchParams(), selection).toString();
  return encode(left) === encode(right);
}

/** Replaces whatever selection `search` carried with `selection`, keeping
 *  every other param (the analytics filters, and so on). */
export function withSelection(search: URLSearchParams, selection: Selection): URLSearchParams {
  const next = withoutSelection(search);
  if (
    selection.kind === "changes" ||
    selection.kind === "risks" ||
    selection.kind === "files" ||
    selection.kind === "sessions" ||
    selection.kind === "commits"
  ) {
    next.set(PARAM.pane, selection.kind);
  } else if (selection.kind === "section") {
    next.set(PARAM.pane, "section");
    next.set(PARAM.section, selection.id);
    if (selection.decision) next.set(PARAM.decision, selection.decision);
  } else if (selection.kind === "risk") {
    next.set(PARAM.pane, "risk");
    next.set(PARAM.riskSection, selection.section);
    next.set(PARAM.riskId, selection.id);
  } else if (selection.kind === "file") {
    next.set(PARAM.pane, "file");
    next.set(PARAM.file, selection.id);
  } else if (selection.kind === "session") {
    next.set(PARAM.pane, "session");
    next.set(PARAM.session, selection.id);
  } else if (selection.kind === "commit") {
    next.set(PARAM.pane, "commit");
    next.set(PARAM.commit, selection.id);
  }
  return next;
}

/** Strips a selection, so switching pull requests lands back on Summary
 *  instead of carrying over a section or risk id the new pull request has no
 *  reason to share. */
export function withoutSelection(search: URLSearchParams): URLSearchParams {
  const next = new URLSearchParams(search);
  for (const key of Object.values(PARAM)) next.delete(key);
  return next;
}
