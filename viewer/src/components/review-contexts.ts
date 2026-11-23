import { createContext, useContext } from "react";

import type { Anchor, ResolvedAnchor, ReviewContextRef, ReviewRef } from "../review-document";
import type { OpenReviewFile } from "./code-file-link";

// The review workspace's openers, shared by the panes under it: kept apart
// from the components that provide them, so those files export components
// only, as fast refresh needs.

export type ReviewFiles = {
  has: (path: string) => boolean;
  open: OpenReviewFile;
};

export const ReviewFilesContext = createContext<ReviewFiles | null>(null);

/** The review workspace's file view, or null outside it. */
export function useReviewFiles(): ReviewFiles | null {
  return useContext(ReviewFilesContext);
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

export const ReviewSourcesContext = createContext<ReviewSources | null>(null);

/** The workspace's openers for source chips, or null outside it. */
export function useReviewSources(): ReviewSources | null {
  return useContext(ReviewSourcesContext);
}

/** A diff hunk a review's prose can name by its ID, resolved for a link: the
 *  file and line it opens in the file view, and the GitHub diff otherwise. */
export type ReviewHunk = { path: string; line?: number; href: string; title: string };

export const ReviewHunksContext = createContext<ReadonlyMap<string, ReviewHunk> | null>(null);

/** The shown review's hunks by ID, or null outside a review. */
export function useReviewHunks(): ReadonlyMap<string, ReviewHunk> | null {
  return useContext(ReviewHunksContext);
}

/** The IDs a review's prose may name besides hunks (risks, evidence,
 *  decisions, sections, checks, threads, comments, sessions), by the key
 *  `reviewRefs` gives each. */
export const ReviewRefsContext = createContext<ReadonlyMap<string, ReviewRef> | null>(null);

/** The shown review's nameable IDs, or null outside a review. */
export function useReviewRefs(): ReadonlyMap<string, ReviewRef> | null {
  return useContext(ReviewRefsContext);
}

/** Where a chip for a review's own item or session can take the reader. Only
 *  the review workspace provides it; elsewhere such a chip is not a link. */
export type ReviewJump = {
  /** Opens a section, and scrolls to one of its items when one is named. */
  item: (section: string, item?: string) => void;
  /** Absent when the host lists no sessions. */
  session?: (id: string) => void;
};

export const ReviewJumpContext = createContext<ReviewJump | null>(null);

export function useReviewJump(): ReviewJump | null {
  return useContext(ReviewJumpContext);
}
