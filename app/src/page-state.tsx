import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "@baum/viewer/components/ui";
import { LoadingStatus } from "./nine-dots";

/** What a page that reads something shows: waiting for the first read, saying
 *  the first read failed, saying there is nothing, or its content. A failed
 *  later read keeps the content it has and is reported beside it. */
export type ReadState = "loading" | "failed" | "empty" | "ready";

/** The state of a page's read. `loaded` is whether a read has succeeded,
 *  `failed` whether the latest one failed and `empty` whether what was read is
 *  nothing. Content already read wins over a later failure, so a failed
 *  refresh never blanks a page. */
export function readState({
  loaded,
  failed = false,
  empty = false,
}: {
  loaded: boolean;
  failed?: boolean;
  empty?: boolean;
}): ReadState {
  if (!loaded) return failed ? "failed" : "loading";
  return empty ? "empty" : "ready";
}

/** A page whose first read failed: what failed, why and one way to try again,
 *  in the empty-page pattern. */
export function ReadFailed({
  what,
  message,
  retry,
}: {
  /** What could not be read, as "jobs" in "Couldn't load jobs". */
  what: string;
  message?: string;
  /** Tries again; the button waits for the promise it may return. */
  retry: () => unknown;
}) {
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return (
    <div role="alert" className="local-empty">
      <h2>Couldn't load {what}</h2>
      {message ? <p>{message}</p> : null}
      <Button
        variant="secondary"
        size="compact"
        disabled={busy}
        onClick={() => {
          setBusy(true);
          void Promise.resolve(retry()).finally(() => {
            if (mounted.current) setBusy(false);
          });
        }}
      >
        {busy ? "Trying…" : "Retry"}
      </Button>
    </div>
  );
}

/** One page read in its four states: `empty` is the page's own empty-page
 *  content, `children` what it shows once read. */
export function PageRead({
  state,
  what,
  message,
  retry,
  loading,
  empty,
  children,
}: {
  state: ReadState;
  what: string;
  message?: string;
  retry: () => unknown;
  /** The waiting label; "Loading {what}…" when not given. */
  loading?: string;
  empty?: ReactNode;
  children?: ReactNode;
}) {
  switch (state) {
    case "loading":
      return <LoadingStatus block>{loading ?? `Loading ${what}…`}</LoadingStatus>;
    case "failed":
      return <ReadFailed what={what} message={message} retry={retry} />;
    case "empty":
      return <>{empty}</>;
    case "ready":
      return <>{children}</>;
  }
}
