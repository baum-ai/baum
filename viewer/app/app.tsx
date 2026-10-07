import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { ReviewViewer } from "../src/components/review-viewer";
import { Failed, Skeleton } from "../src/components/states";
import { Button } from "../src/components/ui";
import {
  type SearchUpdate,
  type UrlState,
  UrlStateProvider,
  type ViewerLinkProps,
  ViewerLinkProvider,
} from "../src/navigation";
import type { ReviewDocument } from "../src/review-document";

/** The standalone viewer: one review document, served by `baum view` from the
 *  same origin. It reads `/api/document` and refetches it whenever
 *  `/api/events` says the document changed. */

async function fetchDocument(signal: AbortSignal): Promise<ReviewDocument> {
  const response = await fetch("/api/document", { cache: "no-store", signal });
  if (!response.ok) throw new Error(`The document request failed (${response.status}).`);
  return (await response.json()) as ReviewDocument;
}

type Load = { state: "loading" } | { state: "failed"; error: string } | { state: "ready" };

/** The review document, kept current. A refetch after the first load leaves the
 *  document on screen until the new one arrives, and keeps it if that fails. */
function useDocument() {
  const [document, setDocument] = useState<ReviewDocument | null>(null);
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const controller = useRef<AbortController | null>(null);

  const refetch = useCallback(async () => {
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    try {
      const next = await fetchDocument(abort.signal);
      setDocument(next);
      setLoad({ state: "ready" });
    } catch (error) {
      if (abort.signal.aborted) return;
      const message = error instanceof Error ? error.message : String(error);
      setLoad((current) =>
        current.state === "ready" ? current : { state: "failed", error: message },
      );
    }
  }, []);

  const retry = useCallback(() => {
    setLoad({ state: "loading" });
    void refetch();
  }, [refetch]);

  useEffect(() => {
    void refetch();
    const events = new EventSource("/api/events");
    events.addEventListener("document", () => void refetch());
    return () => {
      events.close();
      controller.current?.abort();
    };
  }, [refetch]);

  return { document, load, retry };
}

/** The page's search parameters, read from and written to the address bar. */
function useLocationSearch(): UrlState {
  const [search, setSearch] = useState(() => new URLSearchParams(window.location.search));
  // The latest search written, so two writes in a row build on each other.
  const latest = useRef(search);

  useEffect(() => {
    const onPopState = () => {
      latest.current = new URLSearchParams(window.location.search);
      setSearch(latest.current);
    };
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const update = useCallback((change: SearchUpdate) => {
    const next = change(new URLSearchParams(latest.current));
    const query = next.toString();
    if (query === latest.current.toString()) return;
    latest.current = next;
    window.history.pushState(null, "", `${window.location.pathname}${query ? `?${query}` : ""}`);
    setSearch(next);
  }, []);

  return useMemo(() => ({ search, update }), [search, update]);
}

/** An in-app link: a plain left click moves within the page through the
 *  history instead of loading it. */
function AppLink({ href, onClick, ...props }: ViewerLinkProps) {
  return (
    <a
      href={href}
      onClick={(event) => {
        onClick?.(event);
        const target = new URL(href, window.location.href);
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey ||
          props.target === "_blank" ||
          target.origin !== window.location.origin
        ) {
          return;
        }
        event.preventDefault();
        window.history.pushState(null, "", `${target.pathname}${target.search}${target.hash}`);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }}
      {...props}
    />
  );
}

function documentTitle(document: ReviewDocument): string {
  const { pull_request } = document.review;
  return document.summary.title
    ? `${document.summary.title} · #${pull_request}`
    : `Pull request #${pull_request}`;
}

export function App() {
  const { document, load, retry } = useDocument();
  const urlState = useLocationSearch();

  useEffect(() => {
    window.document.title = document ? documentTitle(document) : "baum";
  }, [document]);

  let body;
  if (document) {
    body = <ReviewViewer document={document} />;
  } else if (load.state === "failed") {
    body = (
      <Failed className="min-h-dvh justify-center">
        <p>Could not load the review: {load.error}</p>
        <Button variant="secondary" onClick={retry}>
          Retry
        </Button>
      </Failed>
    );
  } else {
    body = (
      <div className="p-6" role="status" aria-label="Loading the review">
        <Skeleton height={320} />
      </div>
    );
  }

  return (
    <UrlStateProvider value={urlState}>
      <ViewerLinkProvider link={AppLink}>{body}</ViewerLinkProvider>
    </UrlStateProvider>
  );
}
