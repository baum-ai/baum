import { useSyncExternalStore } from "react";
import { FILE_GROUPINGS, type FileGrouping } from "./model";

const GROUPING_KEY = "baum.review.fileGrouping";

let held: FileGrouping | null = null;
const listeners = new Set<() => void>();

/** The grouping of the file list, kept in this browser so it is the same on
 *  the next pull request. Storage can be missing or refuse a write; the
 *  choice then holds for the page alone. */
export function readFileGrouping(): FileGrouping {
  try {
    const saved = localStorage.getItem(GROUPING_KEY);
    const found = FILE_GROUPINGS.find((each) => each.value === saved)?.value;
    if (found) return found;
  } catch {
    // Storage is missing: the choice held for the page, if any.
  }
  return held ?? "type";
}

export function setFileGrouping(next: FileGrouping) {
  try {
    localStorage.setItem(GROUPING_KEY, next);
    held = null;
  } catch {
    // Not remembered; the choice stands for this page.
    held = next;
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The file list's grouping and how to change it. Every reader of it (the
 *  list, the keys that step through its files) shares one choice. */
export function useFileGrouping(): [FileGrouping, (next: FileGrouping) => void] {
  return [useSyncExternalStore(subscribe, readFileGrouping), setFileGrouping];
}
