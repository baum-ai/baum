import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Alert, Button } from "@baum/viewer/components/ui";
import { request, type Entry } from "./api";
import { LoadingStatus, NineDots } from "./nine-dots";

export type MergeOptions = {
  methods: string[];
  default: string;
  state: string;
  draft: boolean;
  head_sha: string;
  mergeable: string;
  merge_state_status: string;
};

const METHODS: Record<string, { label: string; detail: string }> = {
  MERGE: { label: "Create a merge commit", detail: "Keep every commit and add a merge commit." },
  SQUASH: { label: "Squash and merge", detail: "Combine the commits into one." },
  REBASE: { label: "Rebase and merge", detail: "Replay the commits onto the base branch." },
};

/** A merge method in words, as GitHub's button names it. */
export function mergeMethodLabel(name: string) {
  return METHODS[name]?.label ?? name;
}

/** Why GitHub's current answer rules a merge out, or "" when nothing does. */
export function mergeRefusal(options: MergeOptions, entry: Entry) {
  return options.state !== "OPEN"
    ? "This pull request is no longer open."
    : options.draft
      ? "This pull request is a draft. Mark it ready for review on GitHub first."
      : options.head_sha !== entry.pull_request.head_sha
        ? "The pull request has new commits since baum last refreshed. Refresh the inbox and review them first."
        : options.methods.length === 0
          ? "This repository allows no merge method."
          : "";
}

/** The method a merge uses unless the person picks another: GitHub's default
 *  for them when the repository allows it. */
export function defaultMergeMethod(options: MergeOptions) {
  return options.methods.includes(options.default) ? options.default : (options.methods[0] ?? "");
}

/** Whether to offer Merge: an open pull request GitHub reports as ready. */
export function canMerge(entry: Entry) {
  return (
    !entry.historical &&
    entry.merge === "ready" &&
    entry.pull_request.state.toUpperCase() === "OPEN" &&
    !entry.pull_request.draft
  );
}

/** Confirms and performs one merge on GitHub. The merge uses the head commit
 *  the page showed, so GitHub refuses it if the branch has moved since. */
export function MergeDialog({
  entry,
  close,
  merged,
}: {
  entry: Entry;
  close: () => void;
  /** Called once GitHub has merged the pull request. */
  merged: () => Promise<void>;
}) {
  const pr = entry.pull_request;
  const dialog = useRef<HTMLDialogElement>(null);
  const [options, setOptions] = useState<MergeOptions | null>(null);
  const [method, setMethod] = useState("");
  const [error, setError] = useState("");
  const [merging, setMerging] = useState(false);
  useLayoutEffect(() => {
    dialog.current?.showModal();
  }, []);
  useEffect(() => {
    const abort = new AbortController();
    request<MergeOptions>(
      `api/prs/merge-options?repository=${encodeURIComponent(pr.repository)}&pr=${pr.number}`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((answer) => {
        setOptions(answer);
        setMethod(defaultMergeMethod(answer));
      })
      .catch((e) => {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : "Local request failed.");
      });
    return () => abort.abort();
  }, [pr.repository, pr.number]);
  const refusal = options ? mergeRefusal(options, entry) : "";
  const methods = options && !refusal ? options.methods : [];
  const ready = Boolean(options) && !refusal && Boolean(method) && !merging;
  const focusMethod = (name: string) =>
    dialog.current?.querySelector<HTMLInputElement>(`input[value="${name}"]`)?.focus();
  // Once the methods arrive, the chosen one takes the focus from Cancel.
  useEffect(() => {
    if (options) focusMethod(defaultMergeMethod(options));
  }, [options]);
  const merge = async () => {
    setMerging(true);
    setError("");
    try {
      await request("api/prs/merge", {
        repository: pr.repository,
        pr: pr.number,
        method,
        head_sha: pr.head_sha,
      });
      await merged();
      close();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Local request failed.");
      setMerging(false);
    }
  };
  return (
    <dialog
      ref={dialog}
      onCancel={(event) => {
        if (merging) event.preventDefault();
        else close();
      }}
      // Escape cancels through onCancel. Enter merges unless a button has the
      // focus, and j/k or the arrows step through the methods.
      onKeyDown={(event) => {
        if (event.ctrlKey || event.metaKey || event.altKey || event.nativeEvent.isComposing) return;
        if (event.key === "Enter") {
          if (event.target instanceof HTMLElement && event.target.closest("button")) return;
          event.preventDefault();
          if (ready) void merge();
          return;
        }
        const step = ["j", "ArrowDown", "ArrowRight"].includes(event.key)
          ? 1
          : ["k", "ArrowUp", "ArrowLeft"].includes(event.key)
            ? -1
            : 0;
        if (!step || methods.length === 0 || merging) return;
        event.preventDefault();
        const index = methods.indexOf(method);
        const next = methods[Math.min(methods.length - 1, Math.max(0, index + step))]!;
        setMethod(next);
        focusMethod(next);
      }}
      aria-labelledby="merge-title"
    >
      <h2 id="merge-title" className="text-lg font-semibold">
        Merge pull request #{pr.number}?
      </h2>
      <p className="my-3 text-(--text-secondary)">
        {pr.title} · {pr.repository}
      </p>
      {!options && !error ? <LoadingStatus>Checking what GitHub allows…</LoadingStatus> : null}
      {options && !refusal ? (
        <fieldset className="merge-methods" disabled={merging}>
          <legend className="sr-only">Merge method</legend>
          {options.methods.map((name) => (
            <label key={name}>
              <input
                type="radio"
                name="merge-method"
                value={name}
                checked={method === name}
                onChange={() => setMethod(name)}
              />
              <span>
                {mergeMethodLabel(name)}
                <small>{METHODS[name]?.detail}</small>
              </span>
            </label>
          ))}
        </fieldset>
      ) : null}
      {refusal ? <Alert>{refusal}</Alert> : null}
      {error ? <Alert>{error}</Alert> : null}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="ghost" size="compact" disabled={merging} onClick={close}>
          Cancel
          <kbd aria-hidden="true" className="key-hint">
            Esc
          </kbd>
        </Button>
        <Button size="compact" disabled={!ready} onClick={() => void merge()}>
          {merging ? (
            <>
              <NineDots tone="text" />
              Merging…
            </>
          ) : (
            <>
              Merge pull request
              <kbd aria-hidden="true" className="key-hint">
                ↵
              </kbd>
            </>
          )}
        </Button>
      </div>
    </dialog>
  );
}
