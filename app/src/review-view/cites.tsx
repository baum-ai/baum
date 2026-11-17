import { useState } from "react";
import pluralize from "pluralize";
import { AnchorChips } from "@baum/viewer/components/review-parts";
import type { Anchor, ReviewDocument } from "@baum/viewer/review-document";

/** What a step, a risk or a tile rests on, folded into one quiet line ("3
 *  files") that opens its chips. The Code tab already pins each step and
 *  risk to the hunks it cites, so the citations stay out of the reading. */
export function Cites({
  document,
  anchors = [],
}: {
  document: ReviewDocument;
  anchors: Anchor[] | undefined;
}) {
  const [open, setOpen] = useState(false);
  if (anchors.length === 0) return null;
  return (
    <div className="text-xs">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((shown) => !shown)}
        className="text-tertiary hover:text-secondary hover:underline"
      >
        {citesLabel(document, anchors)}
      </button>
      {open ? <AnchorChips document={document} anchors={anchors} className="mt-1.5" /> : null}
    </div>
  );
}

/** "2 files", "2 files and a prompt": the files the anchors name, each
 *  counted once, then whatever else they cite. */
export function citesLabel(document: ReviewDocument, anchors: Anchor[]): string {
  const paths = new Set<string>();
  let others = 0;
  for (const anchor of anchors) {
    if (anchor.type === "hunk") {
      const hunk = document.index.hunks.find((each) => each.id === anchor.id);
      paths.add(hunk?.path ?? anchor.id);
    } else if (anchor.type === "file") paths.add(anchor.path);
    else others += 1;
  }
  const files = paths.size > 0 ? pluralize("file", paths.size, true) : "";
  const rest = others > 0 ? pluralize("source", others, true) : "";
  return files && rest ? `${files} and ${rest}` : files || rest;
}
