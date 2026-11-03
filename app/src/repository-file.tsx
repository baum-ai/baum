import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { HighlightedText } from "@baum/viewer/components/highlighted-text";
import { ExternalLinkIcon } from "@baum/viewer/components/icons";
import { ActionLink, Alert, Button, CloseButton } from "@baum/viewer/components/ui";
import { highlightCode, languageForPath, type HighlightedLine } from "@baum/viewer/highlight";
import type { ReviewContextRef } from "@baum/viewer/review-document";
import { request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** GET /api/file's answer: the file at the document's head, null when it has
 *  no text to show (a directory, a binary file, a clone that cannot answer). */
type RepositoryFile = { path: string; content: string | null; truncated: boolean };

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** The 0-based line that defines `symbol` in `lines`, best effort: the first
 *  line that reads as its definition (`func Name`, `func (r T) Name`,
 *  `type Name`, `class Name`, `function Name`, `const Name`, `def Name`, …),
 *  preferring, for a qualified symbol such as `Worker.Run`, one that also
 *  names its qualifier; else the first line holding the name as a whole word.
 *  Null when no line does. */
export function symbolLine(lines: readonly string[], symbol: string | null): number | null {
  const parts = (symbol ?? "").split(/\.|::|#/).filter(Boolean);
  const name = parts.at(-1);
  if (!name) return null;
  const word = escape(name);
  const definition = new RegExp(
    [
      `\\bfunc\\s+(?:\\([^)]*\\)\\s*)?${word}\\b`,
      `\\b(?:type|class|interface|struct|enum|trait|impl|module|protocol|record|object)\\s+${word}\\b`,
      `\\b(?:function\\*?|def|fn|sub|proc)\\s+${word}\\b`,
      `\\b(?:const|let|var|val)\\s+${word}\\b`,
    ].join("|"),
  );
  const qualifier = parts.length > 1 ? parts.at(-2) : undefined;
  const qualified = qualifier ? new RegExp(`(?<![\\w$])${escape(qualifier)}(?![\\w$])`) : null;
  let first: number | null = null;
  for (const [index, line] of lines.entries()) {
    if (!definition.test(line)) continue;
    if (!qualified || qualified.test(line)) return index;
    first ??= index;
  }
  if (first !== null) return first;
  const whole = new RegExp(`(?<![\\w$])${word}(?![\\w$])`);
  const found = lines.findIndex((line) => whole.test(line));
  return found >= 0 ? found : null;
}

/** A file of the repository that the pull request does not change, read from
 *  the clone at the review's head and shown in the app, for a context chip
 *  to open instead of leaving for GitHub. The symbol's definition, when it is
 *  found, is scrolled to and marked. `query` is the page's document selector;
 *  `href` is the file on GitHub, offered beside it. */
export function RepositoryFileDialog({
  reference,
  source,
  href,
  close,
}: {
  reference: ReviewContextRef;
  /** The file route with its selector, which `&path=` follows: a pull
   *  request's `api/file?<selector>` or a group's
   *  `api/groups/<id>/file?version=<n>`. */
  source: string;
  href: string;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const marked = useRef<HTMLDivElement>(null);
  const [file, setFile] = useState<RepositoryFile | null>(null);
  const [tokens, setTokens] = useState<HighlightedLine[] | null>(null);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const { path, symbol } = reference;
  useLayoutEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    const abort = new AbortController();
    setError("");
    setFile(null);
    setTokens(null);
    request<RepositoryFile>(
      `${source}&path=${encodeURIComponent(path)}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then(setFile)
      .catch((e) => {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : "Local request failed.");
      });
    return () => abort.abort();
  }, [source, path, attempt]);
  const content = file?.content ?? null;
  // Colour the file when the viewer knows its language; until then, and when
  // it cannot, the text stays plain.
  useEffect(() => {
    const lang = languageForPath(path);
    if (content === null || !lang) return;
    let current = true;
    void highlightCode(content, lang).then((lines) => {
      if (current && lines) setTokens(lines);
    });
    return () => {
      current = false;
    };
  }, [content, path]);
  const lines = content === null ? [] : content.replace(/\n$/, "").split("\n");
  const target = content === null ? null : symbolLine(lines, symbol);
  useEffect(() => {
    if (target !== null) marked.current?.scrollIntoView?.({ block: "center" });
  }, [target, content]);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="repository-file-title"
      className="local-dialog-wide local-file-dialog"
    >
      <div className="local-dialog-head">
        <div className="min-w-0">
          <h2 id="repository-file-title" className="truncate" title={path}>
            {path}
          </h2>
          <p>{symbol ? `${symbol} · as of the review's head` : "As of the review's head"}</p>
        </div>
        <CloseButton onClick={close} />
      </div>
      {!file && !error ? <LoadingStatus>Reading the file from this clone…</LoadingStatus> : null}
      {error ? (
        <div className="flex flex-col items-start gap-2">
          <Alert>{error}</Alert>
          <Button size="compact" onClick={() => setAttempt((n) => n + 1)}>
            Retry
          </Button>
        </div>
      ) : null}
      {file && content === null ? (
        <p className="text-(--text-tertiary)">This file has no text to show.</p>
      ) : null}
      {content !== null ? (
        <>
          {file?.truncated ? (
            <p className="local-file-truncated">Truncated: only the start of this file is shown.</p>
          ) : null}
          <div className="local-file-code" role="region" aria-label={`Contents of ${path}`}>
            {lines.map((line, index) => {
              const lineTokens = tokens?.[index];
              const fits = lineTokens?.map((token) => token.content).join("") === line;
              return (
                <div
                  key={index}
                  ref={index === target ? marked : undefined}
                  className="local-file-line"
                  data-marked={index === target ? "" : undefined}
                >
                  <span className="local-file-number" aria-hidden="true">
                    {index + 1}
                  </span>
                  <span className="local-file-text">
                    <HighlightedText text={line} tokens={fits ? lineTokens : null} />
                  </span>
                </div>
              );
            })}
          </div>
        </>
      ) : null}
      {file || error ? (
        <div className="local-dialog-actions mt-4">
          <ActionLink variant="ghost" size="compact" href={href} target="_blank" rel="noreferrer">
            View on GitHub
            <ExternalLinkIcon size={12} />
          </ActionLink>
        </div>
      ) : null}
    </dialog>
  );
}
