import { createContext, type ReactNode, useContext } from "react";

import type { Anchor, ResolvedAnchor, ReviewContextRef } from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { ExternalLinkIcon } from "./icons";

/** Opens one of the pull request's files in the review's own file view, at a
 *  line when one is given. Returns false for a path the pull request does not
 *  change, which has no file view to open. */
export type OpenReviewFile = (path: string, line?: number) => boolean;

type ReviewFiles = {
  has: (path: string) => boolean;
  open: OpenReviewFile;
};

const ReviewFilesContext = createContext<ReviewFiles | null>(null);

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

/** The review workspace's file view, or null outside it. */
export function useReviewFiles(): ReviewFiles | null {
  return useContext(ReviewFilesContext);
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
      <ExternalLinkIcon size={10} className="flex-none text-(--text-tertiary)" />
    </a>
  );
}

/** Opens the place a source chip names without leaving the app. `opens` says,
 *  before any click, whether the chip stays in the app, so it can drop the
 *  leaving mark; `open` is called on a click and returns false when it did
 *  not handle the anchor, which then follows its link. `openRepositoryFile`,
 *  present only when the host can show a file of the repository itself,
 *  opens a context reference to a file the pull request does not change;
 *  it returns false when it did not, and the chip then opens GitHub. */
export type ReviewSources = {
  opens: (anchor: Anchor) => boolean;
  open: (anchor: Anchor, resolved: ResolvedAnchor) => boolean;
  openRepositoryFile?: (reference: ReviewContextRef) => boolean;
};

const ReviewSourcesContext = createContext<ReviewSources | null>(null);

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

export function useReviewSources(): ReviewSources | null {
  return useContext(ReviewSourcesContext);
}
