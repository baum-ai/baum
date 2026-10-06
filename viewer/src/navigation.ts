import {
  type AnchorHTMLAttributes,
  type ComponentType,
  createContext,
  createElement,
  type ReactNode,
  useContext,
} from "react";

/** How the viewer navigates, supplied by its host so the viewer needs no
 *  router of its own: a link component for in-app links, and the URL's search
 *  parameters, where the reader's place in the review (the open pane, the
 *  version being read) is kept so a reload, a shared link and Back restore
 *  it. */

export type ViewerLinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & {
  href: string;
};

export type ViewerLinkComponent = ComponentType<ViewerLinkProps>;

function PlainLink(props: ViewerLinkProps) {
  return createElement("a", props);
}

const LinkContext = createContext<ViewerLinkComponent>(PlainLink);

/** Sets the component every {@link ViewerLink} below renders. Without one, a
 *  link is a plain anchor and navigating it loads the page. */
export function ViewerLinkProvider({
  link,
  children,
}: {
  link: ViewerLinkComponent;
  children: ReactNode;
}) {
  return createElement(LinkContext.Provider, { value: link }, children);
}

/** An in-app link, rendered by the host's link component. */
export function ViewerLink(props: ViewerLinkProps) {
  return createElement(useContext(LinkContext), props);
}

export type SearchUpdate = (current: URLSearchParams) => URLSearchParams;

export type UrlState = {
  /** The search parameters of the URL the page shows now. */
  search: URLSearchParams;
  /** Writes new search parameters as a navigation, built from the latest
   *  ones written rather than the ones this render saw, so two writes in
   *  quick succession do not drop each other's parameters. */
  update: (update: SearchUpdate, options?: { preventScrollReset?: boolean }) => void;
};

const UrlStateContext = createContext<UrlState | null>(null);

/** Connects the viewer's URL state to the host's router or history. */
export function UrlStateProvider({ value, children }: { value: UrlState; children: ReactNode }) {
  return createElement(UrlStateContext.Provider, { value }, children);
}

export function useUrlState(): UrlState {
  const state = useContext(UrlStateContext);
  if (!state) throw new Error("The review viewer's URL state needs a UrlStateProvider above it.");
  return state;
}
