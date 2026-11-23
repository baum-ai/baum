import { useEffect, useId, useState } from "react";
import { Alert, Button, SegmentedControl } from "@baum/viewer/components/ui";
import { request, type Diagnostics, type IssueParts } from "./api";
import { useModal } from "./modal";
import { LoadingStatus } from "./nine-dots";
import { COMMAND } from "./shortcuts";

/** Where a problem is reported: the public repository's bug report form,
 *  whose field ids are those of `.github/ISSUE_TEMPLATE/bug_report.yml`. */
export const ISSUE_REPOSITORY = "baum-ai/baum";
const ISSUES_NEW = `https://github.com/${ISSUE_REPOSITORY}/issues/new`;
const ISSUE_FORM = "bug_report.yml";
/** Without the daemon's parts, the same bound it uses. */
const DEFAULT_MAX_URL = 6500;

/** How much of this computer's Diagnostics the issue carries. */
export type Attachment = "none" | "versions" | "all";

const ATTACHMENTS: { value: Attachment; label: string }[] = [
  { value: "none", label: "Nothing" },
  { value: "versions", label: "Versions only" },
  { value: "all", label: "All diagnostics" },
];

/** The new issue's link and what it attaches, or `null` when even with no
 *  diagnostics what was written is too long for a link. The diagnostics field
 *  loses whole lines from its end, with the daemon's note, until the link
 *  fits; `diagnostics` is what is left, so a preview shows what is sent. */
export function issueLink(
  description: string,
  attachment: Attachment,
  parts: IssueParts | null,
): { url: string; version: string; os: string; diagnostics: string } | null {
  const attached = attachment !== "none" && parts;
  const version = attached ? parts.version : "";
  const os = attached ? parts.os : "";
  const full = attached ? (attachment === "versions" ? parts.versions : parts.diagnostics) : "";
  const max = parts?.max_url || DEFAULT_MAX_URL;
  const build = (diagnostics: string) => {
    const query = new URLSearchParams({ template: ISSUE_FORM, description });
    if (version) query.set("version", version);
    if (os) query.set("os", os);
    if (diagnostics) query.set("diagnostics", diagnostics);
    return `${ISSUES_NEW}?${query}`;
  };
  const fits = (diagnostics: string) => {
    const url = build(diagnostics);
    return url.length <= max ? { url, version, os, diagnostics } : null;
  };
  const whole = fits(full);
  if (whole || !full) return whole;
  const lines = full.split("\n");
  const note = parts?.truncation_note ?? "";
  for (let n = lines.length - 1; n > 0; n--) {
    const shorter = fits(`${lines.slice(0, n).join("\n")}\n${note}`);
    if (shorter) return shorter;
  }
  return fits(note) ?? fits("");
}

/** Reports a problem: what the person writes, with as much of this
 *  computer's redacted Diagnostics as they choose, opened as a new issue in
 *  the public repository in their browser. A preview shows what each choice
 *  attaches; nothing is sent until they submit the form on GitHub. `parts`
 *  are the daemon's, when the caller already has them; otherwise the dialog
 *  asks for them. */
export function ReportProblemDialog({
  parts: given,
  close,
}: {
  parts?: IssueParts | null;
  close: () => void;
}) {
  const dialog = useModal();
  const id = useId();
  const [description, setDescription] = useState("");
  const [attachment, setAttachment] = useState<Attachment>("versions");
  const [parts, setParts] = useState<IssueParts | null>(given ?? null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (given) return;
    const abort = new AbortController();
    request<Diagnostics>("api/diagnostics", undefined, "GET", abort.signal)
      .then((body) => {
        if (body.report_issue) setParts(body.report_issue);
        else setError("Diagnostics are not available from this daemon.");
      })
      .catch((e) => {
        if (!abort.signal.aborted)
          setError(e instanceof Error ? e.message : "Local request failed.");
      });
    return () => abort.abort();
  }, [given]);
  const waiting = attachment !== "none" && !parts;
  const link = issueLink(description, attachment, parts);
  const ready = description.trim() !== "" && !waiting && link !== null;
  const open = () => {
    if (!ready || !link) return;
    // The desktop app hands a new window to the browser.
    window.open(link.url, "_blank", "noopener,noreferrer");
    close();
  };
  return (
    <dialog
      ref={dialog}
      className="report-problem"
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      aria-labelledby={`${id}-title`}
    >
      <h2 id={`${id}-title`} className="text-lg font-semibold">
        Report a problem
      </h2>
      <label className="report-problem-field">
        What happened?
        <textarea
          autoFocus
          value={description}
          placeholder="What you did, what you expected, and what happened instead."
          onChange={(event) => setDescription(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              open();
            }
          }}
          className="report-problem-text"
        />
      </label>
      <div className="report-problem-field">
        {/* The control is named the same; this is its visible label. */}
        <span aria-hidden="true">Attach diagnostics</span>
        <SegmentedControl
          label="Attach diagnostics"
          value={attachment}
          items={ATTACHMENTS}
          onChange={setAttachment}
          className="self-start"
        />
        <AttachmentPreview attachment={attachment} link={link} waiting={waiting} error={error} />
      </div>
      {link === null ? <Alert>Too long for a link to GitHub. Shorten what you wrote.</Alert> : null}
      <p className="mt-4 text-xs text-secondary">
        This will open an issue in {ISSUE_REPOSITORY} on GitHub
      </p>
      <div className="mt-3 flex justify-end gap-2">
        <Button variant="ghost" size="compact" onClick={close}>
          Cancel
          <kbd aria-hidden="true" className="key-hint">
            Esc
          </kbd>
        </Button>
        <Button size="compact" disabled={!ready} onClick={open}>
          Open issue
          <kbd aria-hidden="true" className="key-hint">
            {COMMAND}↵
          </kbd>
        </Button>
      </div>
    </dialog>
  );
}

/** What the chosen attachment adds to the issue, as the link carries it. */
function AttachmentPreview({
  attachment,
  link,
  waiting,
  error,
}: {
  attachment: Attachment;
  link: ReturnType<typeof issueLink>;
  waiting: boolean;
  error: string;
}) {
  if (attachment === "none")
    return <p className="report-problem-note">Only what you write is sent.</p>;
  if (waiting)
    return error ? (
      <Alert>{error} You can still send what you write with Nothing attached.</Alert>
    ) : (
      <LoadingStatus>Checking this computer…</LoadingStatus>
    );
  if (!link) return null;
  const text = [
    `baum version: ${link.version}`,
    `Operating system: ${link.os}`,
    "",
    link.diagnostics || "(no versions found)",
  ].join("\n");
  return (
    <pre aria-label="What is attached" className="report-problem-preview">
      {text}
    </pre>
  );
}
