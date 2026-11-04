import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Markdown } from "@baum/viewer/components/markdown";
import { ExternalLinkIcon } from "@baum/viewer/components/icons";
import { ActionLink, Alert, Button, CloseButton } from "@baum/viewer/components/ui";
import { request } from "./api";
import { LoadingStatus } from "./nine-dots";

type Description = { body: string; url: string };

const normalize = (text: string) => text.replace(/\s+/g, " ").trim().toLowerCase();

/** The heading of the rendered description a review cited by name: an exact
 *  match first, else one that contains it. Best effort; null when none does. */
export function citedHeading(root: HTMLElement, section: string): HTMLElement | null {
  const wanted = normalize(section);
  if (!wanted) return null;
  const headings = [...root.querySelectorAll<HTMLElement>("h1, h2, h3, h4, h5, h6")];
  return (
    headings.find((heading) => normalize(heading.textContent ?? "") === wanted) ??
    headings.find((heading) => normalize(heading.textContent ?? "").includes(wanted)) ??
    null
  );
}

/** The pull request's description, read from GitHub and shown in the app, for
 *  a review's source chip to open instead of leaving for github.com. `section`
 *  is the heading the review cited: it is scrolled to and marked. */
export function DescriptionDialog({
  repository,
  number,
  section,
  close,
}: {
  repository: string;
  number: number;
  section?: string;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const [description, setDescription] = useState<Description | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useLayoutEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    const abort = new AbortController();
    setError("");
    setDescription(null);
    request<Description>(
      `api/prs/description?repository=${encodeURIComponent(repository)}&pr=${number}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then(setDescription)
      .catch((e) => {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : "Local request failed.");
      });
    return () => abort.abort();
  }, [repository, number, attempt]);
  useEffect(() => {
    if (!description || !section || !body.current) return;
    const heading = citedHeading(body.current, section);
    if (!heading) return;
    heading.classList.add("local-description-cited");
    heading.scrollIntoView?.({ block: "start" });
  }, [description, section]);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="description-title"
      className="local-dialog-wide"
    >
      <div className="local-dialog-head">
        <div>
          <h2 id="description-title">Description of #{number}</h2>
          <p>{repository}</p>
        </div>
        <CloseButton onClick={close} />
      </div>
      {!description && !error ? (
        <LoadingStatus>Reading the description from GitHub…</LoadingStatus>
      ) : null}
      {error ? (
        <div className="flex flex-col items-start gap-2">
          <Alert>{error}</Alert>
          <Button size="compact" onClick={() => setAttempt((n) => n + 1)}>
            Retry
          </Button>
        </div>
      ) : null}
      {description ? (
        <>
          <div ref={body}>
            {description.body ? (
              <Markdown reading>{description.body}</Markdown>
            ) : (
              <p className="text-(--text-tertiary)">This pull request has no description.</p>
            )}
          </div>
          {description.url ? (
            <div className="local-dialog-actions mt-4">
              <ActionLink
                variant="ghost"
                size="compact"
                href={description.url}
                target="_blank"
                rel="noreferrer"
              >
                View on GitHub
                <ExternalLinkIcon size={12} />
              </ActionLink>
            </div>
          ) : null}
        </>
      ) : null}
    </dialog>
  );
}
