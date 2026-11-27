/** What the diagram gallery's dev server answers on GET /api/diagrams. The
 *  page imports these types; the server builds them in sources.ts. */

export type DiagramSource = "replay" | "store" | "file";

export type Entry = {
  /** Unique and stable across reloads: `${source}:${relativePath}:${id}`. */
  key: string;
  source: DiagramSource;
  /** Where it came from: replay run and case, store with PR and version, file name. */
  origin: string;
  /** The diagram's id, e.g. dg_flow. */
  id: string;
  /** flow, columns, sequence, table, or whatever else was found. */
  type: string;
  title: string;
  /** "overview" or the id of the section the composition places it on. */
  placement?: string;
  /** The diagram's data as stored. */
  data: unknown;
};

/** A baum.review/2 document, sent raw: the page reads its diagrams with the
 *  app's own parser. */
export type DocumentEntry = {
  /** Unique and stable across reloads: `${source}:${relativePath}`. */
  key: string;
  source: DiagramSource;
  origin: string;
  document: unknown;
};

export type Response =
  | { version: string; entries: Entry[]; documents: DocumentEntry[]; problems: string[] }
  | { version: string; unchanged: true };
