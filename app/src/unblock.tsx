import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import { request, type Entry } from "./api";
import { LoadingStatus, NineDots } from "./nine-dots";

export type UpdateOptions = {
  state: string;
  draft: boolean;
  head_sha: string;
  head_ref: string;
  base_ref: string;
  behind: boolean;
  conflict: boolean;
  fork: boolean;
};
export type RerunOptions = {
  state: string;
  head_sha: string;
  runs: { id: number; name: string }[];
  not_rerunnable: string[];
};
type RerunResult = {
  rerun: { id: number; name: string }[];
  refused: { name: string; message: string }[];
};

function isOpen(entry: Entry) {
  return !entry.historical && entry.pull_request.state.toUpperCase() === "OPEN";
}

/** Whether to offer Update branch: an open pull request GitHub reports as
 *  behind its base without conflicts, in a repository the signed-in account
 *  may push to. A fork's branch is left alone. */
export function canUpdateBranch(entry: Entry) {
  const pr = entry.pull_request;
  return (
    isOpen(entry) && !pr.draft && !pr.fork && pr.behind === true && pr.viewer_can_push === true
  );
}

/** Whether to offer Re-run checks: an open pull request with failing checks, in
 *  a repository the signed-in account may push to. The dialog says which of
 *  the failing checks it can re-run. */
export function canRerunChecks(entry: Entry) {
  return isOpen(entry) && entry.checks === "failing" && entry.pull_request.viewer_can_push === true;
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "Local request failed.";
}

/** The confirmation both writes share: read what the write would do, show it,
 *  and send it for the head commit the page showed, so GitHub's own answer to
 *  a moved branch reaches the person in words. */
function UnblockDialog<Options extends { head_sha: string }, Result>({
  entry,
  close,
  done,
  title,
  optionsPath,
  writePath,
  action,
  pending,
  refusal,
  describe,
  finished,
}: {
  entry: Entry;
  close: () => void;
  /** Called once GitHub has accepted the write. */
  done: () => Promise<void>;
  title: string;
  optionsPath: string;
  writePath: string;
  /** The confirm button's label, and its label while the write is sent. */
  action: string;
  pending: string;
  /** Why the write cannot be sent now, or empty. */
  refusal: (options: Options) => string;
  describe: (options: Options) => ReactNode;
  /** What to tell the person once the write was sent, or null to close. */
  finished: (result: Result) => { message: ReactNode; problem?: string } | null;
}) {
  const pr = entry.pull_request;
  const dialog = useRef<HTMLDialogElement>(null);
  const [options, setOptions] = useState<Options | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<ReturnType<typeof finished>>(null);
  useLayoutEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    const abort = new AbortController();
    request<Options>(
      `${optionsPath}?repository=${encodeURIComponent(pr.repository)}&pr=${pr.number}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then(setOptions)
      .catch((e) => {
        if (!abort.signal.aborted) setError(errorText(e));
      });
    return () => abort.abort();
  }, [optionsPath, pr.repository, pr.number]);
  const why = !options
    ? ""
    : options.head_sha !== pr.head_sha
      ? "The pull request has new commits since baum last refreshed. Refresh the inbox and review them first."
      : refusal(options);
  const send = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await request<Result>(writePath, {
        repository: pr.repository,
        pr: pr.number,
        head_sha: pr.head_sha,
      });
      await done();
      const next = finished(result);
      if (next) {
        setOutcome(next);
        setBusy(false);
      } else close();
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  };
  const titleId = `${writePath.replace(/\W/g, "-")}-title`;
  return (
    <dialog
      ref={dialog}
      onCancel={(event) => {
        if (busy) event.preventDefault();
        else close();
      }}
      aria-labelledby={titleId}
    >
      <h2 id={titleId} className="text-lg font-semibold">
        {title} #{pr.number}?
      </h2>
      <p className="my-3 text-(--text-secondary)">
        {pr.title} · {pr.repository}
      </p>
      {!options && !error ? <LoadingStatus>Checking GitHub…</LoadingStatus> : null}
      {options && !why && !outcome ? describe(options) : null}
      {outcome ? <div role="status">{outcome.message}</div> : null}
      {why ? <Alert>{why}</Alert> : null}
      {outcome?.problem ? <Alert>{outcome.problem}</Alert> : null}
      {error ? <Alert>{error}</Alert> : null}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="compact" disabled={busy} onClick={close}>
          {outcome ? "Close" : "Cancel"}
        </Button>
        {outcome ? null : (
          <Button
            size="compact"
            disabled={!options || Boolean(why) || busy}
            onClick={() => void send()}
          >
            {busy ? (
              <>
                <NineDots tone="text" />
                {pending}
              </>
            ) : (
              action
            )}
          </Button>
        )}
      </div>
    </dialog>
  );
}

/** Confirms merging the base branch into the pull request's branch, as
 *  GitHub's Update branch button does. */
export function UpdateBranchDialog({
  entry,
  close,
  updated,
}: {
  entry: Entry;
  close: () => void;
  /** Called once GitHub has accepted the update. */
  updated: () => Promise<void>;
}) {
  return (
    <UnblockDialog<UpdateOptions, { updating: boolean }>
      entry={entry}
      close={close}
      done={updated}
      title="Update branch of pull request"
      optionsPath="api/prs/update-branch-options"
      writePath="api/prs/update-branch"
      action="Update branch"
      pending="Updating…"
      refusal={(o) =>
        o.fork
          ? "This pull request's branch is in a fork, which baum does not update."
          : o.conflict
            ? "This pull request has conflicts with its base branch. Resolve them before updating the branch."
            : ""
      }
      describe={(o) => (
        <p>
          GitHub will merge <code>{o.base_ref}</code> into <code>{o.head_ref}</code> and add a merge
          commit to the pull request. Checks run again on the new commit.
        </p>
      )}
      finished={() => ({
        message: (
          <p>
            GitHub accepted the update and is adding the merge commit now. The new state shows here
            after the next refresh.
          </p>
        ),
      })}
    />
  );
}

/** Confirms re-running the failed jobs of the head commit's failed GitHub
 *  Actions workflow runs, and names the failing checks it cannot re-run. */
export function RerunChecksDialog({
  entry,
  close,
  rerun,
}: {
  entry: Entry;
  close: () => void;
  /** Called once GitHub has accepted the re-run. */
  rerun: () => Promise<void>;
}) {
  return (
    <UnblockDialog<RerunOptions, RerunResult>
      entry={entry}
      close={close}
      done={rerun}
      title="Re-run failed checks of pull request"
      optionsPath="api/prs/rerun-checks-options"
      writePath="api/prs/rerun-checks"
      action="Re-run failed checks"
      pending="Re-running…"
      refusal={(o) =>
        o.runs.length === 0
          ? "No failed GitHub Actions workflow run was found for this commit, so there is nothing baum can re-run."
          : ""
      }
      describe={(o) => (
        <>
          <p>GitHub will re-run only the failed jobs of these workflow runs:</p>
          <ul className="my-2 list-disc pl-5">
            {o.runs.map((run) => (
              <li key={run.id}>{run.name}</li>
            ))}
          </ul>
          {o.not_rerunnable.length > 0 ? (
            <>
              <p className="text-(--text-secondary)">Not re-runnable from baum:</p>
              <ul className="my-2 list-disc pl-5 text-(--text-secondary)">
                {o.not_rerunnable.map((name) => (
                  <li key={name}>{name}</li>
                ))}
              </ul>
            </>
          ) : null}
        </>
      )}
      finished={(result) =>
        result.refused.length === 0
          ? null
          : {
              message: (
                <p>
                  Re-running {result.rerun.map((run) => run.name).join(", ")}. GitHub shows the
                  checks as running shortly.
                </p>
              ),
              problem: `GitHub would not re-run ${result.refused
                .map((r) => `${r.name} (${r.message})`)
                .join("; ")}`,
            }
      }
    />
  );
}
