import { useEffect, useState } from "react";
import { LocalRequestError, request } from "./api";
import { LoadingStatus } from "./nine-dots";

/** What `api/prs/conflicts` answers: the files a merge of the base branch's
 *  tip into the head would leave conflicted, found in the local clone. */
export type Conflicts = {
  head_sha: string;
  base_ref: string;
  base_sha: string;
  files: string[];
};

type Read = { conflicts?: Conflicts; error?: string };

/** The files that conflict with the base branch, worked out in the local
 *  clone when shown. Without a clone, or from an older daemon, it says so and
 *  links to GitHub, which names the files on the pull request's page. */
export function ConflictingFiles({
  repository,
  number,
  head,
  url,
}: {
  repository: string;
  number: number;
  /** The head commit the panel shows; the files are worked out again when it moves. */
  head: string;
  url: string;
}) {
  const [read, setRead] = useState<Read>({});
  useEffect(() => {
    const abort = new AbortController();
    request<Conflicts>(
      `api/prs/conflicts?repository=${encodeURIComponent(repository)}&pr=${number}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((conflicts) => setRead({ conflicts: { ...conflicts, files: conflicts.files ?? [] } }))
      .catch((error: unknown) => {
        if (abort.signal.aborted) return;
        setRead({
          error:
            error instanceof LocalRequestError && error.status === 404
              ? "This version of baum cannot list the conflicting files."
              : error instanceof Error
                ? error.message
                : String(error),
        });
      });
    return () => abort.abort();
  }, [repository, number, head]);

  if (!read.conflicts) {
    return read.error ? (
      <p className="upnext-threads upnext-block-sub" role="alert">
        {read.error}{" "}
        <a href={url} target="_blank" rel="noreferrer">
          Open on GitHub
        </a>
      </p>
    ) : (
      <div className="upnext-threads">
        <LoadingStatus>Merging the branches in the local clone…</LoadingStatus>
      </div>
    );
  }
  const { files, base_ref: base } = read.conflicts;
  if (files.length === 0) {
    return (
      <p className="upnext-threads upnext-block-sub">
        The branch now merges into {base ? <code>{base}</code> : "its base"} without conflicts;
        GitHub has not caught up yet.
      </p>
    );
  }
  return (
    <div className="upnext-threads">
      <ul aria-label="Conflicting files">
        {files.map((file) => (
          <li key={file}>
            <code className="upnext-conflict-file">{file}</code>
          </li>
        ))}
      </ul>
    </div>
  );
}
