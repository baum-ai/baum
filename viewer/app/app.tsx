import {
  Component,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { ReviewViewer } from "../src/components/review-viewer";
import { Failed, Skeleton } from "../src/components/states";
import { Alert, Button } from "../src/components/ui";
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

const SCHEMA = "baum.review/1";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

/** The least the viewer needs to render without throwing: the schema marker
 *  and the parts it reads at the top level. */
function isReviewDocument(value: unknown): value is ReviewDocument {
  return (
    isRecord(value) &&
    value.schema === SCHEMA &&
    isRecord(value.review) &&
    typeof value.review.pull_request === "number" &&
    isRecord(value.summary) &&
    Array.isArray(value.sections)
  );
}

async function fetchDocument(signal: AbortSignal): Promise<ReviewDocument> {
  const response = await fetch("/api/document", { cache: "no-store", signal });
  if (!response.ok) throw new Error(`The document request failed (${response.status}).`);
  const body: unknown = await response.json();
  if (!isReviewDocument(body)) throw new Error(`The response is not a ${SCHEMA} document.`);
  return body;
}

type Load = { state: "loading" } | { state: "failed"; error: string } | { state: "ready" };

const RECONNECT_MIN_MS = 1000;
const RECONNECT_MAX_MS = 30_000;

/** The review document, kept current. A refetch after the first load leaves the
 *  document on screen until the new one arrives, and keeps it, marked stale, if
 *  that fails. */
function useDocument() {
  const [document, setDocument] = useState<ReviewDocument | null>(null);
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const [stale, setStale] = useState(false);
  const loaded = useRef(false);
  const controller = useRef<AbortController | null>(null);

  const refetch = useCallback(async () => {
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    try {
      const next = await fetchDocument(abort.signal);
      if (abort.signal.aborted) return;
      loaded.current = true;
      setDocument(next);
      setLoad({ state: "ready" });
      setStale(false);
    } catch (error) {
      if (abort.signal.aborted) return;
      if (loaded.current) {
        setStale(true);
      } else {
        setLoad({ state: "failed", error: error instanceof Error ? error.message : String(error) });
      }
    }
  }, []);

  const retry = useCallback(() => {
    if (!loaded.current) setLoad({ state: "loading" });
    void refetch();
  }, [refetch]);

  useEffect(() => {
    let source: EventSource | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let delay = RECONNECT_MIN_MS;

    const connect = () => {
      const events = new EventSource("/api/events");
      source = events;
      // Fires on the first connection and on every reconnection, so a change
      // made while the stream was down is picked up.
      events.addEventListener("open", () => {
        delay = RECONNECT_MIN_MS;
        void refetch();
      });
      events.addEventListener("document", () => void refetch());
      // The browser reconnects by itself unless the stream was refused (a bad
      // status or content type), which closes it for good; reopen it then.
      events.addEventListener("error", () => {
        if (events.readyState !== EventSource.CLOSED) return;
        events.close();
        timer = setTimeout(connect, delay);
        delay = Math.min(delay * 2, RECONNECT_MAX_MS);
      });
    };

    void refetch();
    connect();
    return () => {
      clearTimeout(timer);
      source?.close();
      controller.current?.abort();
    };
  }, [refetch]);

  return { document, load, stale, retry };
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
export function AppLink({ href, onClick, ...props }: ViewerLinkProps) {
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

function FailedView({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <Failed className="min-h-dvh justify-center">
      <p>Could not load the review: {message}</p>
      <Button variant="secondary" onClick={onRetry}>
        Retry
      </Button>
    </Failed>
  );
}

/** Keeps a render-time exception in the viewer from blanking the page. A new
 *  document gets another try. */
class ViewerBoundary extends Component<
  { document: ReviewDocument; onRetry: () => void; children: ReactNode },
  { error: Error | null; document: ReviewDocument }
> {
  override state = { error: null as Error | null, document: this.props.document };

  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  static getDerivedStateFromProps(
    props: { document: ReviewDocument },
    state: { error: Error | null; document: ReviewDocument },
  ) {
    return props.document === state.document ? null : { error: null, document: props.document };
  }

  override render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <FailedView
        message={error.message}
        onRetry={() => {
          this.setState({ error: null });
          this.props.onRetry();
        }}
      />
    );
  }
}

export function App() {
  const { document, load, stale, retry } = useDocument();
  const urlState = useLocationSearch();

  useEffect(() => {
    window.document.title = document ? documentTitle(document) : "baum";
  }, [document]);

  let body;
  if (document) {
    const notices = stale ? (
      <Alert className="flex items-center justify-between gap-3">
        <span>Couldn't reload the document; showing the last version loaded.</span>
        <Button variant="secondary" onClick={retry}>
          Retry
        </Button>
      </Alert>
    ) : undefined;
    body = (
      <ViewerBoundary document={document} onRetry={retry}>
        <ReviewViewer document={document} notices={notices} />
      </ViewerBoundary>
    );
  } else if (load.state === "failed") {
    body = <FailedView message={load.error} onRetry={retry} />;
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
