import { createContext, type ReactNode, useContext } from "react";

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
