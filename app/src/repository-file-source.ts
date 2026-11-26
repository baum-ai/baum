import { createContext, useContext } from "react";

/** Where the page reads a repository file at the review's head: `source` is
 *  the file route with its selector, which `&path=` follows (a pull
 *  request's `api/file?<selector>` or a group's
 *  `api/groups/<id>/file?version=<n>`), and `href` the file on GitHub. Kept
 *  apart from the components that read it, as fast refresh needs. */
export type RepositoryFileSource = { source: string; href: (path: string) => string };

export const RepositoryFileSourceContext = createContext<RepositoryFileSource | null>(null);

/** The host's route for repository files, or null where it has none (a
 *  draft, which is no saved version). */
export function useRepositoryFileSource(): RepositoryFileSource | null {
  return useContext(RepositoryFileSourceContext);
}
