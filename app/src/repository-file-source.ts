import { createContext, useContext } from "react";
import type { ReviewDocument } from "@baum/viewer/review-document";

/** Where the page reads a repository file at the review's head: `source` is
 *  the file route with its selector, which `&path=` follows (a pull
 *  request's `api/file?<selector>` or a group's
 *  `api/groups/<id>/file?version=<n>`), and `href` the file on GitHub. Kept
 *  apart from the components that read it, as fast refresh needs. */
export type RepositoryFileSource = { source: string; href: (path: string) => string };

/** The host's route for the repository files of the version a pane shows:
 *  two compared versions each read their own. Null where it has none. */
export type RepositoryFileSources = (document: ReviewDocument) => RepositoryFileSource | null;

export const RepositoryFileSourceContext = createContext<RepositoryFileSources | null>(null);

/** The host's route for the repository files of `document`, the version
 *  rendered, or null where it has none (a draft, which is no saved
 *  version). */
export function useRepositoryFileSource(document: ReviewDocument): RepositoryFileSource | null {
  return useContext(RepositoryFileSourceContext)?.(document) ?? null;
}
