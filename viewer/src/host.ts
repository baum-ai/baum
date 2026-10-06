import { createContext, createElement, type ReactNode, useContext } from "react";

/** Resolves an attachment of the document being read, by the id its
 *  `index.attachments` lists, to an address the browser can load it from. The
 *  host knows where its copies are kept and whose pull request this is; the
 *  viewer never does. */
export type AttachmentUrl = (attachmentId: string) => string;

const AttachmentUrlContext = createContext<AttachmentUrl | null>(null);

/** Without a resolver, as in a preview of a document with no stored copies,
 *  `images` blocks draw nothing. */
export function AttachmentUrlProvider({
  resolve,
  children,
}: {
  resolve: AttachmentUrl | null;
  children: ReactNode;
}) {
  return createElement(AttachmentUrlContext.Provider, { value: resolve }, children);
}

export function useAttachmentUrl(): AttachmentUrl | null {
  return useContext(AttachmentUrlContext);
}
