import { type ReactNode, useContext, useMemo } from "react";

import {
  formatLines,
  humanize,
  hunkLine,
  hunkUrl,
  type ReviewDocument,
  reviewRefs,
} from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { ExternalLinkIcon } from "./icons";
import {
  ReviewFilesContext,
  ReviewHunksContext,
  ReviewRefsContext,
  ReviewSourcesContext,
  type ReviewFiles,
  type ReviewHunk,
  type ReviewSources,
} from "./review-contexts";

export type { ReviewSources } from "./review-contexts";

/** Opens one of the pull request's files in the review's own file view, at a
 *  line when one is given. Returns false for a path the pull request does not
 *  change, which has no file view to open. */
export type OpenReviewFile = (path: string, line?: number) => boolean;

/** Set by the review workspace so every code link in the panes under it opens
 *  the file here rather than on GitHub. Outside it, links keep going to GitHub. */
export function ReviewFilesProvider({
  files,
  children,
}: {
  files: ReviewFiles | null;
  children: ReactNode;
}) {
  return <ReviewFilesContext.Provider value={files}>{children}</ReviewFilesContext.Provider>;
}

/** A link to a file of the change. It opens the file view when the pull
 *  request changes that file, and only otherwise leaves for `href` on GitHub,
 *  marked as leaving. */
export function CodeFileLink({
  path,
  line,
  href,
  className,
  title,
  children,
}: {
  path: string;
  line?: number;
  href?: string;
  className?: string;
  title?: string;
  children: ReactNode;
}) {
  const files = useContext(ReviewFilesContext);
  if (files?.has(path)) {
    return (
      <button
        type="button"
        title={title}
        className={cn("cursor-pointer text-left", FOCUS_RING, className)}
        onClick={() => files.open(path, line)}
      >
        {children}
      </button>
    );
  }
  if (!href) {
    return (
      <span title={title} className={className}>
        {children}
      </span>
    );
  }
  return (
    <a href={href} target="_blank" rel="noreferrer" title={title} className={className}>
      {children}
      <ExternalLinkIcon size={10} className="flex-none text-tertiary" />
    </a>
  );
}

/** Set by the review workspace so source chips for the host's own places (the
 *  pull request's description, a listed commit, another pull request) open
 *  there. Outside it, chips keep going to their URL. */
export function ReviewSourcesProvider({
  sources,
  children,
}: {
  sources: ReviewSources | null;
  children: ReactNode;
}) {
  return <ReviewSourcesContext.Provider value={sources}>{children}</ReviewSourcesContext.Provider>;
}

/** Set by the review workspace so a hunk ID written into the review's prose
 *  reads as a link to its file and line, and so do the other IDs it names
 *  (items, checks, threads, comments, sessions). A hunk ID names the hunk's content,
 *  so the shown documents' hunks are pooled. Other IDs resolve only against
 *  document, the version whose prose is being rendered. */
export function ReviewHunksProvider({
  document,
  historical,
  children,
}: {
  document: ReviewDocument | null;
  historical: ReviewDocument | null;
  children: ReactNode;
}) {
  const hunks = useMemo(() => {
    const byId = new Map<string, ReviewHunk>();
    for (const shown of [document, historical]) {
      if (!shown) continue;
      for (const hunk of shown.index.hunks) {
        if (byId.has(hunk.id)) continue;
        byId.set(hunk.id, {
          path: hunk.path,
          line: hunkLine(hunk),
          href: hunkUrl(shown, hunk),
          title: `${humanize(hunk.change)} ${hunk.role} hunk in ${hunk.path}, ${formatLines(hunk.additions, hunk.deletions)}`,
        });
      }
    }
    return byId;
  }, [document, historical]);
  return (
    <ReviewHunksContext.Provider value={hunks}>
      <ReviewRefsProvider document={document}>{children}</ReviewRefsProvider>
    </ReviewHunksContext.Provider>
  );
}

/** Unlike content-addressed hunks, item and index IDs can mean different
 *  things in each version. Comparison panes each provide their own snapshot. */
export function ReviewRefsProvider({
  document,
  children,
}: {
  document: ReviewDocument | null;
  children: ReactNode;
}) {
  const refs = useMemo(() => (document ? reviewRefs(document) : null), [document]);
  return <ReviewRefsContext.Provider value={refs}>{children}</ReviewRefsContext.Provider>;
}
