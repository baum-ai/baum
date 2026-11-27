import { useCallback, useMemo } from "react";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { useStoredSetting } from "@baum/viewer/use-stored-setting";

/** Which sections of which reviews the reader has been through, kept on this
 *  computer with the page's other choices: a record from a review version's
 *  key to its sections' IDs. */
export const READ_MARKS_KEY = "baum.review.read";

/** The most review versions whose marks are kept; the oldest go first. */
export const READ_MARKS_KEPT = 200;

/** How long a section must have been shown for leaving it to count as having
 *  read it, so stepping past a section with the keys does not tick it. */
export const READ_AFTER_MS = 1500;

export type ReadMarks = Record<string, string[]>;

/** A review version's key: a new version starts unread, since its sections
 *  may say something else. */
export function readMarksKey(document: ReviewDocument): string {
  const { repository, pull_request, version } = document.review;
  return `${repository}#${pull_request}@${version}`;
}

/** The stored marks, or none when nothing valid is stored. */
export function parseReadMarks(raw: string | null): ReadMarks {
  if (!raw) return {};
  try {
    const value: unknown = JSON.parse(raw);
    if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
    const marks: ReadMarks = {};
    for (const [key, sections] of Object.entries(value))
      if (Array.isArray(sections))
        marks[key] = sections.filter((each): each is string => typeof each === "string");
    return marks;
  } catch {
    return {};
  }
}

/** The marks with `section` of the review `key` read. The review moves to the
 *  end of the record, so the ones dropped past READ_MARKS_KEPT are those
 *  touched longest ago. The same record comes back when nothing changes. */
export function withReadMark(marks: ReadMarks, key: string, section: string): ReadMarks {
  const sections = marks[key] ?? [];
  if (sections.includes(section)) return marks;
  const { [key]: _moved, ...rest } = marks;
  const kept = Object.entries(rest).slice(-(READ_MARKS_KEPT - 1));
  return { ...Object.fromEntries(kept), [key]: [...sections, section] };
}

/** The sections of this review version the reader has been through, and the
 *  way to mark one. With no document nothing is read and nothing is marked. */
export function useReadMarks(document: ReviewDocument | null) {
  const [raw, store] = useStoredSetting(READ_MARKS_KEY);
  const key = document ? readMarksKey(document) : null;
  const read = useMemo(() => new Set(key ? (parseReadMarks(raw)[key] ?? []) : []), [raw, key]);
  const mark = useCallback(
    (section: string) => {
      if (!key) return;
      // Read again at the moment of marking: another pane may have marked
      // since this one last rendered.
      let current: string | null = raw;
      try {
        current = localStorage.getItem(READ_MARKS_KEY) ?? raw;
      } catch {
        // Storage is unavailable; this session's value is all there is.
      }
      const marks = parseReadMarks(current);
      const next = withReadMark(marks, key, section);
      if (next !== marks) store(JSON.stringify(next));
    },
    [key, raw, store],
  );
  return { read, mark };
}
