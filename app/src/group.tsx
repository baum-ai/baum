import { useCallback, useEffect, useMemo, useId, useRef, useState, type FormEvent } from "react";
import { ReviewViewer, type ReviewSteps } from "@baum/viewer/components/review-viewer";
import { ChevronRightIcon, MergeIcon, StackIcon } from "@baum/viewer/components/icons";
import { documentFilesSlot } from "./facts";
import { Alert, Badge, Button, LiveDot } from "@baum/viewer/components/ui";
import { useHistoryView } from "@baum/viewer/use-review-version";
import { ViewerLink, useUrlState } from "@baum/viewer/navigation";
import {
  contextRefUrl,
  type ReviewContextRef,
  type ReviewDocument,
  type ReviewVersionState,
} from "@baum/viewer/review-document";
import { RepositoryFileDialog } from "./repository-file";
import {
  MAX_GROUP_MEMBERS,
  appURL,
  backTo,
  automaticLabel,
  generateGroup,
  groupDocumentRequest,
  groupJob,
  isActiveJob,
  request,
  sameRepository,
  type Clone,
  type Entry,
  type Job,
  type LiveSession,
  type Provenance,
  type ReviewGroup,
} from "./api";
import { PullRequestState } from "./inbox";
import { CloneActions } from "./repositories";
import { CancelJob, GenerationOutcome, ReviewJob } from "./review";
import { useModal } from "./modal";
import { Notice, PageBar, RefreshButton, type RefreshRun, usePageRefreshAction } from "./shell";
import { openActiveBlock, toggleActiveBlock, useShortcuts } from "./shortcuts";
import { LoadingStatus, NineDots } from "./nine-dots";
import { liveForGroup, useGroupRail, type RailMember } from "./group-rail";

const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "Local request failed.";
const reviewStatus: Record<string, string> = {
  no_review: "No review",
  current: "Review current",
  outdated: "Review outdated",
  generating: "Generating review",
  failed: "Review failed",
};
/** "#8", "#8 and #9", "#7, #8 and #9". */
function numberList(numbers: number[]) {
  const named = numbers.map((n) => `#${n}`);
  return named.length > 1
    ? `${named.slice(0, -1).join(", ")} and ${named[named.length - 1]}`
    : (named[0] ?? "");
}

/** What a stack group says of a member that is no longer open. */
const landedLabel: Record<string, string> = {
  merged: "Merged",
  closed: "Closed",
  unknown: "No longer open",
};

/** What a group review waits for while its members' reviews run. */
function waitingText(numbers: number[]) {
  return `A group review builds on each pull request's own review. ${numberList(numbers)} ${
    numbers.length === 1 ? "is" : "are"
  } being reviewed first; the group review starts when ${
    numbers.length === 1 ? "it finishes" : "they finish"
  }.`;
}

/** Asks for a group's name: when creating one and when renaming it. */
export function NameDialog({
  title,
  detail,
  initial,
  action,
  submit,
  close,
}: {
  title: string;
  /** What the group is for, under the title. */
  detail?: string;
  initial: string;
  action: string;
  submit: (name: string) => Promise<void>;
  close: () => void;
}) {
  const dialog = useModal();
  const [name, setName] = useState(initial);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const send = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await submit(name.trim());
    } catch (e) {
      setError(errorText(e));
      setPending(false);
    }
  };
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="group-name-title">
      <form onSubmit={(event) => void send(event)}>
        <div className="grid gap-1">
          <h2 id="group-name-title" className="text-lg font-semibold">
            {title}
          </h2>
          {detail ? <p className="text-xs text-(--text-secondary)">{detail}</p> : null}
        </div>
        <label className="local-field-label">
          Group name
          <input
            type="text"
            className="local-field"
            autoFocus
            value={name}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        {error ? <Alert>{error}</Alert> : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="compact" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" size="compact" disabled={pending || !name.trim()}>
            {pending ? "Saving…" : action}
          </Button>
        </div>
      </form>
    </dialog>
  );
}
function ConfirmDialog({
  title,
  children,
  action,
  pending,
  confirm,
  close,
}: {
  title: string;
  children: string;
  action: string;
  pending: boolean;
  confirm: () => void;
  close: () => void;
}) {
  const dialog = useModal();
  return (
    <dialog ref={dialog} onCancel={close} aria-label={title}>
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="my-3">{children}</p>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="compact" disabled={pending} onClick={close}>
          Cancel
        </Button>
        <Button size="compact" disabled={pending} onClick={confirm}>
          {action}
        </Button>
      </div>
    </dialog>
  );
}

/** The page of a group id the daemon does not have, or no longer has. */
export function GroupMissing() {
  const back = backTo(useUrlState().search);
  return (
    <section>
      <PageBar title="Review group not found" />
      <div className="local-page-body local-stack">
        <ViewerLink href={back.href}>{back.label}</ViewerLink>
        <p>
          This review group does not exist any more. Its pull requests and saved reviews are kept.
        </p>
      </div>
    </section>
  );
}

/** A review group: one combined review of its member pull requests, with the
 *  members and the controls that change them above it. */
export function GroupPage({
  group,
  entries,
  jobs,
  clones,
  cloneFor,
  live = [],
  revision,
  changed,
  refresh,
}: {
  group: ReviewGroup;
  /** The inbox's entries, with generating ones marked. */
  entries: Entry[];
  jobs: Job[];
  clones: Clone[];
  cloneFor: (entry: Entry) => string;
  /** Sessions in progress, for the Sessions row. */
  live?: LiveSession[];
  revision: number;
  changed: () => Promise<void>;
  /** Reads the group's repository from GitHub again, from the head's refresh. */
  refresh?: (run: RefreshRun) => Promise<unknown>;
}) {
  // The refresh key works before the head and its button are drawn.
  usePageRefreshAction(refresh);
  const url = useUrlState();
  const back = backTo(url.search);
  const job = groupJob(group, jobs);
  const generating = isActiveJob(job);
  const clone = clones.find((c) => sameRepository(c.repository, group.repository) && c.available);
  const members = group.members.map((number) => {
    const same = entries.filter((e) => sameRepository(e.pull_request.repository, group.repository));
    return {
      number,
      entry:
        same.find((e) => e.pull_request.number === number && !e.historical) ??
        same.find((e) => e.pull_request.number === number),
    };
  });
  const landed = (group.landed ?? []).map(({ number, state }) => ({
    number,
    state,
    entry: entries.find(
      (e) =>
        sameRepository(e.pull_request.repository, group.repository) &&
        e.pull_request.number === number,
    ),
  }));
  const memberHref = (entry: Entry) => {
    const cloneId = cloneFor(entry);
    return appURL(
      `?${back.prefix}${cloneId ? `clone=${encodeURIComponent(cloneId)}` : `repository=${encodeURIComponent(entry.pull_request.repository)}`}&pr=${entry.pull_request.number}`,
    );
  };
  // Member reviews still running: a group review cannot start before them.
  const reviewing = members.filter((m) => m.entry?.status === "generating").map((m) => m.number);
  const addable = [
    ...new Map(
      entries
        .filter(
          (e) =>
            sameRepository(e.pull_request.repository, group.repository) &&
            !e.historical &&
            e.pull_request.state.toUpperCase() === "OPEN" &&
            !group.members.includes(e.pull_request.number),
        )
        .map((e) => [e.pull_request.number, e]),
    ).values(),
  ];

  const [doc, setDoc] = useState<ReviewDocument | null>(null);
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [provenance, setProvenance] = useState<Provenance[]>([]);
  const [historyFailed, setHistoryFailed] = useState(false);
  const loadVersion = useCallback(
    async (version: number): Promise<ReviewVersionState> => {
      try {
        return { status: "ready", document: await groupDocumentRequest(group.id, version) };
      } catch {
        return { status: "failed" };
      }
    },
    [group.id],
  );
  const history = useHistoryView(doc?.review.version, loadVersion);
  // The document on screen: an earlier version read alone, else the latest.
  // Its files, their patches and the files its chips open belong to it.
  const viewedVersion =
    history.selection && !history.selection.compare ? history.selection.version : undefined;
  const viewedDocument = viewedVersion
    ? history.state?.status === "ready"
      ? history.state.document
      : null
    : doc;
  // Decisions, Sessions and Commits come from the members' own saved reviews:
  // the daemon has no group routes for them.
  const railMembers: RailMember[] = [...landed, ...members].flatMap(({ number, entry }) => {
    const cloneId = entry && entry.version_count > 0 ? cloneFor(entry) : "";
    return cloneId ? [{ number, clone: cloneId }] : [];
  });
  const rail = useGroupRail({
    repository: group.repository,
    members: railMembers,
    numbers: group.members,
    running: liveForGroup(live, group.repository, group.members),
    document: doc,
    revision: `${group.version_count}:${group.updated_at}:${revision}`,
  });
  useEffect(() => {
    if (group.version_count === 0) {
      setDoc(null);
      setProvenance([]);
      return;
    }
    const abort = new AbortController();
    setFailed(false);
    groupDocumentRequest(group.id, undefined, abort.signal)
      .then(setDoc)
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    request<Provenance[]>(
      `api/groups/${encodeURIComponent(group.id)}/versions`,
      undefined,
      "GET",
      abort.signal,
    )
      .then((rows) => {
        setProvenance(rows);
        setHistoryFailed(false);
      })
      .catch(() => {
        if (!abort.signal.aborted) setHistoryFailed(true);
      });
    return () => abort.abort();
  }, [group.id, group.version_count, group.updated_at, revision, retry]);
  const patches = useMemo(() => {
    const cache = new Map<string, Promise<string | null>>();
    return (path: string, version: number, hunk: string) => {
      const key = `${version}:${path}:${hunk}`;
      let pending = cache.get(key);
      if (!pending) {
        pending = request<{ patch: string | null }>(
          `api/groups/${encodeURIComponent(group.id)}/patch?version=${version}&path=${encodeURIComponent(path)}&hunk=${encodeURIComponent(hunk)}`,
        )
          .then((body) => body.patch)
          .catch(() => {
            cache.delete(key);
            return null;
          });
        cache.set(key, pending);
      }
      return pending;
    };
  }, [group.id]);

  // A file's whole diff for the Files pane, for the version on screen. Each
  // hunk names the member whose comparison changes the file, and the daemon
  // answers that member's whole file patch, so hunks of one member share a
  // patch: each distinct patch is shown once. Kept the same function while
  // the document is, so an opened file is not read again.
  const filePatches = useCallback(
    async (path: string) => {
      if (!viewedDocument) return null;
      const parts = await Promise.all(
        viewedDocument.index.hunks
          .filter((hunk) => hunk.path === path)
          .map((hunk) => patches(path, viewedDocument.review.version, hunk.id)),
      );
      const text = [...new Set(parts.filter((part): part is string => Boolean(part)))];
      return text.length > 0 ? text.join("\n") : null;
    },
    [viewedDocument, patches],
  );

  // Generation. A group review builds on each member's own review: the
  // daemon queues the reviews of members without one ahead of the group's,
  // which starts after them wherever the person goes.
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  // The group's job is queued behind its members' own reviews.
  const waiting = generating && job?.state === "queued" && reviewing.length > 0;
  const needsClone =
    "Needs a local clone. Choose a folder or clone the repository in Repositories.";
  const generate = async (confirm = false) => {
    if (!clone) return;
    setBusy(true);
    setError("");
    try {
      const answer = await generateGroup(clone.id, group.id, confirm);
      setConfirming(answer === "isolation");
      if (answer === "needs_clone") setError(needsClone);
      else if (typeof answer === "object")
        setError(
          `${numberList(answer.missing)} ${answer.missing.length === 1 ? "has" : "have"} no review yet.`,
        );
      else if (answer === "queued") await changed();
    } catch (e) {
      setConfirming(false);
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  // Managing the group.
  const [renaming, setRenaming] = useState(false);
  // A repository file a context chip opened, with its GitHub link.
  const [repositoryFile, setRepositoryFile] = useState<{
    reference: ReviewContextRef;
    version: number;
    href: string;
  } | null>(null);
  // The pull requests start folded under the title.
  const [membersOpen, setMembersOpen] = useState(false);
  const membersId = useId();
  const mergedCount = landed.filter((member) => member.state === "merged").length;
  const [deleting, setDeleting] = useState(false);
  const [manageError, setManageError] = useState("");
  const [managing, setManaging] = useState(false);
  const [removing, setRemoving] = useState<number | null>(null);
  const patch = async (body: { name?: string; members?: number[] }) => {
    await request<ReviewGroup>(`api/groups/${encodeURIComponent(group.id)}`, body, "PATCH");
    await changed();
  };
  const change = async (body: { name?: string; members?: number[] }) => {
    setManaging(true);
    setManageError("");
    try {
      await patch(body);
    } catch (e) {
      setManageError(errorText(e));
    } finally {
      setManaging(false);
    }
  };
  const remove = async () => {
    setManaging(true);
    setManageError("");
    try {
      await request<void>(`api/groups/${encodeURIComponent(group.id)}`, {}, "DELETE");
      setDeleting(false);
      url.update(() => new URLSearchParams());
      await changed();
    } catch (e) {
      setDeleting(false);
      setManageError(errorText(e));
      setManaging(false);
    }
  };

  const generationState = generating
    ? "pending"
    : job?.state === "failed" || job?.state === "interrupted"
      ? "failed"
      : group.version_count === 0
        ? undefined
        : group.outdated
          ? "outdated"
          : "current";
  const regenerate = (
    <Button
      variant="secondary"
      size="compact"
      disabled={generating || busy || !clone}
      onClick={() => void generate()}
    >
      {waiting
        ? "Reviewing pull requests…"
        : generating
          ? job?.state === "queued"
            ? "Review queued"
            : "Generating review…"
          : busy
            ? "Starting…"
            : "Regenerate"}
    </Button>
  );
  const saved = provenance.find((p) => p.version === doc?.review.version);
  // As on a pull request's page: j and k step through the review, g
  // generates it again.
  const steps = useRef<ReviewSteps>(null);
  useShortcuts((event) => {
    // Space opens or closes the fold of the block j and k are on.
    if (
      event.key === " " &&
      !(event.target instanceof HTMLElement && event.target.closest("a, button, summary")) &&
      toggleActiveBlock()
    ) {
      event.preventDefault();
      return;
    }
    // Else Space opens or closes the selected group (Changes, Sessions,
    // Commits, Files) in the rail.
    if (
      event.key === " " &&
      !(event.target instanceof HTMLElement && event.target.closest("a, button, summary")) &&
      steps.current?.toggleGroup
    ) {
      event.preventDefault();
      steps.current.toggleGroup();
      return;
    }
    // Enter opens the block j and k are on, such as a commit from the list.
    if (
      event.key === "Enter" &&
      !(
        event.target instanceof HTMLElement &&
        event.target.closest("a, button, summary, input, textarea")
      ) &&
      openActiveBlock()
    ) {
      event.preventDefault();
      return;
    }
    const step =
      // As on a pull request's page: h and l (← →) between sections, j and k
      // (↓ ↑) through the blocks of the one read.
      event.key === "j" || event.key === "ArrowDown"
        ? steps.current?.item?.next
        : event.key === "k" || event.key === "ArrowUp"
          ? steps.current?.item?.previous
          : event.key === "l" || event.key === "ArrowRight"
            ? (steps.current?.rail?.next ?? steps.current?.next)
            : event.key === "h" || event.key === "ArrowLeft"
              ? (steps.current?.rail?.previous ?? steps.current?.previous)
              : null;
    if (step) {
      event.preventDefault();
      step.go();
    } else if (event.key === "g" && !generating && !busy && clone) {
      event.preventDefault();
      void generate();
    }
  });

  return (
    <>
      <ReviewViewer
        // A file a context chip names opens here, in a dialog over the page,
        // read at the stack's head from the group's own file route.
        openRepositoryFile={(reference) => {
          if (!viewedDocument) return false;
          setRepositoryFile({
            reference,
            version: viewedDocument.review.version,
            href: contextRefUrl(viewedDocument, reference.path),
          });
          return true;
        }}
        document={doc}
        history={history}
        versions={provenance.map((p) => p.version).filter((v) => v !== doc?.review.version)}
        loadVersion={loadVersion}
        versionsFailed={historyFailed ? { retry: () => setRetry((n) => n + 1) } : undefined}
        steps={steps}
        // Every file of the stack's change, as GitHub's Files changed shows
        // them: a file's diff is its hunks' patches, read when it is opened.
        files={
          viewedDocument
            ? documentFilesSlot(viewedDocument.index.hunks, filePatches, "")
            : undefined
        }
        header={{
          heading: group.name,
          titleAside: group.stack ? (
            <Badge
              size="compact"
              className="gap-1"
              title="baum grouped these stacked pull requests"
            >
              <StackIcon size={12} />
              Stack
            </Badge>
          ) : (
            <Badge size="compact">Review group</Badge>
          ),
          // Its pull requests, folded to a line under the title until asked
          // for, rather than a panel over the review.
          statistics: (
            <div className="local-group-fold">
              <button
                type="button"
                className="local-group-toggle"
                aria-expanded={membersOpen}
                aria-controls={membersId}
                onClick={() => setMembersOpen((open) => !open)}
              >
                <ChevronRightIcon size={12} className="local-group-toggle-chevron" />
                <span>
                  {group.members.length + landed.length} pull requests
                  {mergedCount > 0 ? ` · ${mergedCount} merged` : ""}
                </span>
              </button>
              {membersOpen ? (
                <div id={membersId} className="local-group-fold-body">
                  <section aria-label="Group members" className="local-group-panel">
                    <header className="local-group-head">
                      {addable.length > 0 && group.members.length < MAX_GROUP_MEMBERS ? (
                        <select
                          aria-label="Add pull request"
                          className="local-field local-group-add"
                          value=""
                          disabled={managing}
                          onChange={(event) => {
                            const number = Number(event.target.value);
                            if (number) void change({ members: [...group.members, number] });
                          }}
                        >
                          <option value="">
                            {managing && removing === null ? "Adding…" : "Add…"}
                          </option>
                          {addable.map((e) => (
                            <option key={e.pull_request.number} value={e.pull_request.number}>
                              #{e.pull_request.number} {e.pull_request.title}
                            </option>
                          ))}
                        </select>
                      ) : null}
                    </header>
                    <ul className="local-group-members">
                      {landed.map(({ number, state, entry }) => (
                        <li
                          key={`landed-${number}`}
                          className="local-group-member"
                          data-landed={state}
                        >
                          <span className="local-group-number">#{number}</span>
                          <div className="local-group-member-title">
                            {entry ? (
                              <ViewerLink href={memberHref(entry)}>
                                {entry.pull_request.title}
                              </ViewerLink>
                            ) : (
                              <span className="text-(--text-tertiary)">Not in the inbox</span>
                            )}
                            <span className="local-group-member-state">
                              {state === "merged" ? (
                                <span className="local-merged">
                                  <MergeIcon size={13} className="flex-none" />
                                  Merged
                                </span>
                              ) : (
                                <span>{landedLabel[state] ?? "No longer open"}</span>
                              )}
                            </span>
                          </div>
                        </li>
                      ))}
                      {members.map(({ number, entry }) => (
                        <li key={number} className="local-group-member">
                          <span className="local-group-number">#{number}</span>
                          <div className="local-group-member-title">
                            {entry ? (
                              <ViewerLink href={memberHref(entry)}>
                                {entry.pull_request.title}
                              </ViewerLink>
                            ) : (
                              <span className="text-(--text-tertiary)">Not in the inbox</span>
                            )}
                            {entry ? (
                              <span className="local-group-member-state">
                                <PullRequestState entry={entry} />
                                {entry.status === "generating" ? (
                                  <span className="inline-flex items-center gap-1.5 text-(--text-secondary)">
                                    <NineDots size={14} />
                                    {reviewStatus.generating}
                                  </span>
                                ) : (
                                  <span>{reviewStatus[entry.status] ?? entry.status}</span>
                                )}
                              </span>
                            ) : null}
                          </div>
                          {group.members.length > 2 ? (
                            <button
                              type="button"
                              className="local-group-remove"
                              aria-label={`Remove #${number}`}
                              title="Remove from the group"
                              disabled={managing}
                              onClick={() => {
                                setRemoving(number);
                                void change({
                                  members: group.members.filter((n) => n !== number),
                                }).finally(() => setRemoving(null));
                              }}
                            >
                              {removing === number ? "Removing…" : "Remove"}
                            </button>
                          ) : null}
                        </li>
                      ))}
                    </ul>
                    <footer className="local-group-foot">
                      <Button
                        variant="secondary"
                        size="tiny"
                        disabled={managing}
                        onClick={() => setRenaming(true)}
                      >
                        Rename
                      </Button>
                      <Button
                        variant="danger-outline"
                        size="tiny"
                        disabled={managing}
                        onClick={() => setDeleting(true)}
                      >
                        Delete group
                      </Button>
                    </footer>
                    {manageError ? <Alert>{manageError}</Alert> : null}
                  </section>
                </div>
              ) : null}
            </div>
          ),
          back: { href: back.href, label: back.label },
          generation: { state: generationState, rewriting: generating && Boolean(doc) },
          beside: doc ? regenerate : null,
          actions: refresh ? (
            <RefreshButton refresh={refresh} label="Refresh from GitHub" />
          ) : undefined,
        }}
        placeholder={
          !clone ? (
            <div className="local-review-empty">
              <h2>Needs a local clone</h2>
              <CloneActions repository={group.repository} changed={changed} />
            </div>
          ) : waiting ? (
            <section aria-label="Review generation" className="local-review-empty">
              <LiveDot />
              <h2 role="status">Reviewing the pull requests first</h2>
              <p>{waitingText(reviewing)}</p>
              {job ? <CancelJob job={job} changed={changed} /> : null}
            </section>
          ) : generating ? (
            <section aria-label="Review generation" className="local-review-empty">
              <LiveDot />
              <h2 role="status">
                {job?.state === "queued" ? "Group review queued" : "Generating the group review"}
              </h2>
              <p>
                {job && automaticLabel(job) ? `${automaticLabel(job)}. ` : ""}This page updates when
                it is ready.
              </p>
              {job ? <CancelJob job={job} changed={changed} /> : null}
            </section>
          ) : group.version_count === 0 && reviewing.length > 0 ? (
            <section aria-label="Member review generation" className="local-review-empty">
              <LiveDot />
              <h2 role="status">Reviewing the pull requests</h2>
              <p>
                A group review builds on each pull request's own review. {numberList(reviewing)}{" "}
                {reviewing.length === 1 ? "is" : "are"} being reviewed. Generate now and the group
                review starts when {reviewing.length === 1 ? "it finishes" : "they finish"}.
              </p>
              <Button size="compact" disabled={busy} onClick={() => void generate()}>
                {busy ? "Starting…" : "Generate review"}
              </Button>
            </section>
          ) : failed && group.version_count > 0 ? (
            <div className="local-review-empty">
              <h2>The saved review couldn't be read</h2>
              <Button size="compact" onClick={() => setRetry((n) => n + 1)}>
                Retry review
              </Button>
            </div>
          ) : group.version_count > 0 ? (
            <div className="local-review-empty">
              <LoadingStatus block>Loading review…</LoadingStatus>
            </div>
          ) : job && job.state !== "completed" ? (
            <div className="local-review-empty">
              <GenerationOutcome job={job} />
              <Button size="compact" disabled={busy} onClick={() => void generate()}>
                {busy ? "Starting…" : job.state === "failed" ? "Try again" : "Generate review"}
              </Button>
            </div>
          ) : (
            <div className="local-review-empty">
              <h2>No group review yet</h2>
              <p>
                Generate one to see how these pull requests fit together: shared files, conflicting
                assumptions and merge order. It builds on each pull request's own review.
              </p>
              <Button size="compact" disabled={busy} onClick={() => void generate()}>
                {busy ? "Starting…" : "Generate review"}
              </Button>
            </div>
          )
        }
        detailsFooter={() =>
          saved ? <p className="local-generated-by">Generated with {saved.model}</p> : null
        }
        notices={
          <>
            {group.outdated && group.version_count > 0 ? (
              <Notice
                action={
                  <Button
                    variant="secondary"
                    size="compact"
                    disabled={generating || busy || !clone}
                    onClick={() => void generate()}
                  >
                    {busy ? "Starting…" : "Regenerate"}
                  </Button>
                }
              >
                Members changed since this group review was generated.
              </Notice>
            ) : null}
            {!clone && doc ? (
              <Notice>
                Needs a local clone. This saved review remains available.
                <CloneActions repository={group.repository} changed={changed} />
              </Notice>
            ) : null}
            {waiting && doc ? <Notice>{waitingText(reviewing)}</Notice> : null}
            {error ? <Alert>{error}</Alert> : null}
            {job && generating && doc ? (
              <ReviewJob key={job.id} job={job} changed={changed} />
            ) : job && doc && job.state !== "completed" && !generating ? (
              <div className="local-job-outcome">
                <GenerationOutcome job={job} compact />
              </div>
            ) : null}
            {failed && doc ? (
              <Alert>
                The latest review could not be read. This saved version remains available.{" "}
                <Button onClick={() => setRetry((n) => n + 1)}>Retry review</Button>
              </Alert>
            ) : null}
          </>
        }
        renderSectionLabels={(section) =>
          section.pull_requests?.length ? (
            <>
              {section.pull_requests.map((number) => {
                const entry = members.find((m) => m.number === number)?.entry;
                return (
                  <Badge key={number} size="compact">
                    {entry ? (
                      <ViewerLink href={memberHref(entry)}>#{number}</ViewerLink>
                    ) : (
                      `#${number}`
                    )}
                  </Badge>
                );
              })}
            </>
          ) : null
        }
        hunkPatches={patches}
        {...rail}
      />
      {repositoryFile ? (
        <RepositoryFileDialog
          reference={repositoryFile.reference}
          source={`api/groups/${encodeURIComponent(group.id)}/file?version=${repositoryFile.version}`}
          href={repositoryFile.href}
          close={() => setRepositoryFile(null)}
        />
      ) : null}
      {renaming ? (
        <NameDialog
          title="Rename review group"
          initial={group.name}
          action="Rename"
          close={() => setRenaming(false)}
          submit={async (name) => {
            await patch({ name });
            setRenaming(false);
          }}
        />
      ) : null}
      {deleting ? (
        <ConfirmDialog
          title="Delete review group?"
          action={managing ? "Deleting…" : "Delete group"}
          pending={managing}
          close={() => setDeleting(false)}
          confirm={() => void remove()}
        >
          Delete this review group? The pull requests and their saved reviews are kept.
        </ConfirmDialog>
      ) : null}
      {confirming ? (
        <ConfirmDialog
          title="Review code from outside your team?"
          action={busy ? "Starting…" : "Generate anyway"}
          pending={busy}
          close={() => setConfirming(false)}
          confirm={() => void generate(true)}
        >
          One of these pull requests comes from a fork or from an author without write access.
          Generating the review runs your agent on its code. Only continue if you trust it, or if
          your agent runs in an isolated environment.
        </ConfirmDialog>
      ) : null}
    </>
  );
}
