import { Alert, Badge, Button } from "@baum/viewer/components/ui";
import { Fragment, useLayoutEffect, useMemo, useRef, useState } from "react";
import { request, type PromptEntry, type RepositoryPromptEntry } from "./api";

const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "Local request failed.";
const lf = (text: string) => text.replace(/\r\n?/g, "\n");

type Rules = PromptEntry["safety_rules"];

/** Whether a prompt is the person's own and in use at its level. A stored text
 *  that no longer passes validation is skipped by jobs, so it is not custom. */
export function isCustom(entry: PromptEntry | RepositoryPromptEntry) {
  if (entry.invalid) return false;
  return "source" in entry ? entry.source === "repository" : entry.custom_text !== null;
}

/** The rules whose text the prompt no longer contains. */
export function missingRules(text: string, rules: Rules) {
  const body = lf(text);
  return rules.filter((rule) => !body.includes(lf(rule.text)));
}

/** Whether any prompt needs attention: written for an older default, a rule
 *  removed, or a stored text that jobs skip. */
export function promptsNeedAttention(entries: PromptEntry[]) {
  return entries.some(
    (e) => e.outdated || e.invalid || (e.custom_text !== null && e.missing_safety_rules.length > 0),
  );
}

export type DiffLine = { kind: "same" | "add" | "del"; text: string };

/** A line diff from `before` to `after`, by the longest common subsequence of
 *  lines. Null when the texts are too large to compare cheaply. */
export function lineDiff(before: string, after: string): DiffLine[] | null {
  const all = lf(before).split("\n");
  const bll = lf(after).split("\n");
  // Lines both texts share at either end need no comparing.
  let head = 0;
  while (head < all.length && head < bll.length && all[head] === bll[head]) head++;
  let tail = 0;
  while (
    tail < all.length - head &&
    tail < bll.length - head &&
    all[all.length - 1 - tail] === bll[bll.length - 1 - tail]
  )
    tail++;
  const a = all.slice(head, all.length - tail);
  const b = bll.slice(head, bll.length - tail);
  if (a.length * b.length > 4_000_000) return null;
  const width = b.length + 1;
  const table = new Uint32Array((a.length + 1) * width);
  for (let i = a.length - 1; i >= 0; i--)
    for (let j = b.length - 1; j >= 0; j--)
      table[i * width + j] =
        a[i] === b[j]
          ? table[(i + 1) * width + j + 1]! + 1
          : Math.max(table[(i + 1) * width + j]!, table[i * width + j + 1]!);
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i++]! });
      j++;
    } else if (table[(i + 1) * width + j]! >= table[i * width + j + 1]!)
      out.push({ kind: "del", text: a[i++]! });
    else out.push({ kind: "add", text: b[j++]! });
  }
  while (i < a.length) out.push({ kind: "del", text: a[i++]! });
  while (j < b.length) out.push({ kind: "add", text: b[j++]! });
  return [
    ...all.slice(0, head).map((text) => ({ kind: "same" as const, text })),
    ...out,
    ...all.slice(all.length - tail).map((text) => ({ kind: "same" as const, text })),
  ];
}

/** The text cut into plain runs and runs that are a safety rule's text. */
export function markRules(text: string, rules: Rules) {
  const body = lf(text);
  const ranges: [number, number][] = [];
  for (const rule of rules) {
    const needle = lf(rule.text);
    if (!needle) continue;
    for (let at = body.indexOf(needle); at >= 0; at = body.indexOf(needle, at + needle.length))
      ranges.push([at, at + needle.length]);
  }
  ranges.sort((x, y) => x[0] - y[0]);
  const merged: [number, number][] = [];
  for (const range of ranges) {
    const last = merged.at(-1);
    if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
    else merged.push([range[0], range[1]]);
  }
  const parts: { text: string; mark: boolean }[] = [];
  let at = 0;
  for (const [from, to] of merged) {
    if (from > at) parts.push({ text: body.slice(at, from), mark: false });
    parts.push({ text: body.slice(from, to), mark: true });
    at = to;
  }
  if (at < body.length) parts.push({ text: body.slice(at), mark: false });
  return parts;
}

/** A textarea with the safety rules' text marked behind it: a transparent
 *  mirror of the text whose marks show through the transparent textarea. The
 *  mirror is hidden from assistive technology; the rules' state is also listed
 *  in words beside the field.
 *
 *  The textarea has neither `value` nor `defaultValue`: React writes either one
 *  into the field's text node after every keystroke, and that write ends the
 *  browser's undo step, so undo went back one letter at a time. The browser
 *  keeps the text; it is written here only when it changes from outside. */
function HighlightedTextarea({
  id,
  label,
  value,
  rules,
  describedBy,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  rules: Rules;
  describedBy: string;
  onChange: (text: string) => void;
}) {
  const field = useRef<HTMLTextAreaElement>(null);
  const mirror = useRef<HTMLDivElement>(null);
  const [gutter, setGutter] = useState(0);
  useLayoutEffect(() => {
    const ta = field.current;
    if (!ta) return;
    // Only the first text, or one changed from outside, differs from what
    // was typed; writing it replaces the field's undo history.
    if (ta.value !== value) ta.value = value;
    // A scrollbar narrows the textarea's lines; the mirror must wrap the same.
    setGutter(ta.offsetWidth - ta.clientWidth);
  }, [value]);
  return (
    <div className="prompt-field">
      <div
        ref={mirror}
        className="prompt-text prompt-mirror"
        aria-hidden="true"
        style={{ paddingRight: `calc(0.65rem + ${gutter}px)` }}
      >
        {markRules(value, rules).map((part, index) =>
          part.mark ? (
            <mark key={index}>{part.text}</mark>
          ) : (
            <Fragment key={index}>{part.text}</Fragment>
          ),
        )}
        {"\n"}
      </div>
      <textarea
        ref={field}
        id={id}
        aria-label={label}
        aria-describedby={describedBy}
        className="prompt-text prompt-input"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
        onScroll={(event) => {
          if (mirror.current) mirror.current.scrollTop = event.currentTarget.scrollTop;
        }}
        onKeyDown={(event) => {
          // Esc gives the field up first; it must not also close a dialog
          // around it and drop the text.
          if (event.key === "Escape") {
            event.preventDefault();
            event.currentTarget.blur();
          }
        }}
      />
    </div>
  );
}

function Diff({ before, after }: { before: string; after: string }) {
  const lines = useMemo(() => lineDiff(before, after), [before, after]);
  if (!lines) return <p className="settings-footnote">The texts are too large to compare.</p>;
  if (lines.every((line) => line.kind === "same"))
    return <p className="settings-footnote">No changes.</p>;
  return (
    <pre className="prompt-view prompt-diff" role="region" aria-label="Changes">
      {lines.map((line, index) => (
        <span key={index} data-kind={line.kind}>
          {line.kind === "add" ? "+ " : line.kind === "del" ? "- " : "  "}
          {line.text}
          {"\n"}
        </span>
      ))}
    </pre>
  );
}

/** Edits one prompt. Nothing is saved until Save; one save or reset runs at a
 *  time, so an older text never lands after a newer one. A failed save keeps
 *  the text. */
export function PromptEditor({
  entry,
  path,
  baseline,
  baselineName,
  resetLabel,
  saveLabel,
  draft,
  setDraft,
  saved,
  close,
}: {
  entry: PromptEntry;
  /** The PUT route for this prompt. */
  path: string;
  /** What a new custom prompt starts from: the effective text. */
  baseline: string;
  /** What the diff compares with, as the person calls it. */
  baselineName: string;
  resetLabel: string;
  saveLabel: string;
  /** The text typed and not yet saved, kept by the list so closing the editor
   *  or opening another does not drop it; undefined when there is none. */
  draft: string | undefined;
  setDraft: (update: (current: string | undefined) => string | undefined) => void;
  /** Receives the entry the daemon returns. */
  saved: (entry: PromptEntry) => void;
  close: () => void;
}) {
  const stored = entry.custom_text;
  const text = draft ?? stored ?? baseline;
  const working = useRef(false);
  // While a save runs, the stored text is about to change, so a text equal to
  // the old one is still an edit and stays a draft.
  const setText = (next: string) =>
    setDraft(() => (!working.current && next === (stored ?? baseline) ? undefined : next));
  const [view, setView] = useState<"" | "default" | "changes">("");
  const [busy, setBusy] = useState<"" | "save" | "reset">("");
  const [error, setError] = useState("");
  // What the last save stored; "Saved" shows only while the text is still it.
  const [savedText, setSavedText] = useState<string | null>(null);
  const rulesId = `prompt-rules-${entry.id}`;
  const missing = missingRules(text, entry.safety_rules);
  const send = async (body: string | null, kind: "save" | "reset") => {
    if (working.current) return;
    working.current = true;
    setBusy(kind);
    setError("");
    setSavedText(null);
    try {
      const next = await request<PromptEntry>(path, { text: body }, "PUT");
      saved(next);
      if (kind === "reset") {
        setDraft(() => undefined);
        close();
      } else {
        setSavedText(body);
        // The text may have changed while the save ran.
        setDraft((current) => (current === body ? undefined : current));
      }
    } catch (e) {
      setError(errorText(e));
      // Nothing was stored, so a draft equal to the stored text is no edit.
      setDraft((current) => (current === (stored ?? baseline) ? undefined : current));
    } finally {
      working.current = false;
      setBusy("");
    }
  };
  const empty = text.trim() === "";
  return (
    <div className="prompt-editor">
      {entry.invalid ? (
        <p role="status" className="settings-caution">
          This custom prompt is not in use: {entry.invalid_reason ?? "it is no longer valid"}. Edit
          and save it, or reset it.
        </p>
      ) : null}
      <HighlightedTextarea
        id={`prompt-${entry.id}`}
        label={`${entry.title} prompt`}
        value={text}
        rules={entry.safety_rules}
        describedBy={rulesId}
        onChange={setText}
      />
      <div id={rulesId}>
        {entry.safety_rules.length > 0 ? (
          <>
            <h4 className="prompt-subhead">Safety rules</h4>
            <ul className="prompt-rules">
              {entry.safety_rules.map((rule) => {
                const gone = missing.some((m) => m.id === rule.id);
                return (
                  <li key={rule.id}>
                    <Badge tone={gone ? "caution" : "positive"} size="compact">
                      {gone ? "Missing" : "Present"}
                    </Badge>{" "}
                    {rule.label}
                  </li>
                );
              })}
            </ul>
          </>
        ) : null}
        {missing.length > 0 ? (
          <p role="status" className="settings-caution">
            This prompt no longer contains: {missing.map((m) => m.label).join(", ")}.
          </p>
        ) : null}
        <p className="settings-footnote">
          baum adds the output format and the delimiting of pull request data itself. Safety rules
          in this text are not added back if you remove them.
        </p>
      </div>
      {error ? <Alert>{error}</Alert> : null}
      <div className="prompt-actions">
        <Button
          size="compact"
          disabled={busy !== "" || empty || (stored !== null && text === stored && !entry.invalid)}
          onClick={() => void send(text, "save")}
        >
          {busy === "save" ? "Saving…" : saveLabel}
        </Button>
        {stored !== null ? (
          <Button
            size="compact"
            variant="secondary"
            disabled={busy !== ""}
            onClick={() => void send(null, "reset")}
          >
            {busy === "reset" ? "Resetting…" : resetLabel}
          </Button>
        ) : null}
        <Button
          size="compact"
          variant="ghost"
          aria-expanded={view === "default"}
          onClick={() => setView(view === "default" ? "" : "default")}
        >
          {view === "default" ? "Hide default" : "Show default"}
        </Button>
        <Button
          size="compact"
          variant="ghost"
          aria-expanded={view === "changes"}
          onClick={() => setView(view === "changes" ? "" : "changes")}
        >
          {view === "changes" ? "Hide changes" : "Show changes"}
        </Button>
        <Button size="compact" variant="ghost" disabled={busy !== ""} onClick={close}>
          Close
        </Button>
        <p role="status" className="settings-saved">
          {savedText === text ? "Saved" : ""}
        </p>
      </div>
      {empty ? (
        <p className="settings-footnote">An empty prompt cannot be saved. Reset it instead.</p>
      ) : null}
      {view === "default" ? (
        <pre className="prompt-view" role="region" aria-label="The default prompt">
          {entry.default_text}
        </pre>
      ) : null}
      {view === "changes" ? (
        <>
          <p className="settings-footnote">Changes from {baselineName}.</p>
          <Diff before={baseline} after={text} />
        </>
      ) : null}
    </div>
  );
}

const groups: { id: PromptEntry["group"]; title: string }[] = [
  { id: "reviews", title: "Reviews" },
  { id: "fixes", title: "Fixes" },
  { id: "extraction", title: "Data extraction" },
];

/** Every prompt, grouped, each with its state and an editor opened in place. A
 *  `repository` makes the entries that repository's own, with their source. */
export function PromptList({
  entries,
  repository,
  replace,
}: {
  entries: (PromptEntry | RepositoryPromptEntry)[];
  repository?: string;
  replace: (entry: PromptEntry) => void;
}) {
  const [open, setOpen] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const Heading = "h3";
  return (
    <>
      {groups.map((group) => {
        const rows = entries.filter((e) => e.group === group.id);
        if (rows.length === 0) return null;
        return (
          <section key={group.id} aria-label={group.title} className="prompt-group">
            <Heading className="settings-subhead">{group.title}</Heading>
            <div className="local-panel settings-list">
              {rows.map((entry) => {
                const custom = isCustom(entry);
                const inherited = "source" in entry ? entry : null;
                const baseline = inherited ? inherited.inherited_text : entry.default_text;
                const status = repository
                  ? custom
                    ? "Custom for this repository"
                    : inherited?.source === "global"
                      ? "Uses global"
                      : "Uses default"
                  : custom
                    ? "Custom"
                    : "Default";
                const action =
                  entry.custom_text !== null
                    ? "Edit"
                    : repository
                      ? "Customise for this repository"
                      : "Customise";
                const editing = open === entry.id;
                return (
                  <div key={entry.id} className="prompt-item" data-prompt={entry.id}>
                    <div className="settings-row">
                      <div className="settings-row-text">
                        <span className="settings-row-label">
                          {entry.title}{" "}
                          <Badge tone={custom ? "info" : "neutral"} size="compact">
                            {status}
                          </Badge>
                        </span>
                        <div className="settings-row-detail">{entry.used_by}</div>
                        {drafts[entry.id] !== undefined ? (
                          <div className="settings-row-detail settings-caution">
                            Unsaved changes
                          </div>
                        ) : null}
                        {entry.outdated ? (
                          <div className="settings-row-detail settings-caution">
                            Written for {entry.based_on}; the default is now {entry.default_version}
                            .
                          </div>
                        ) : null}
                        {entry.invalid ? (
                          <div className="settings-row-detail settings-caution">
                            This custom prompt is not in use:{" "}
                            {entry.invalid_reason ?? "it is no longer valid"}. Edit and save it, or
                            reset it.
                          </div>
                        ) : null}
                        {!entry.invalid &&
                        entry.custom_text !== null &&
                        entry.missing_safety_rules.length > 0 ? (
                          <div className="settings-row-detail settings-caution">
                            No longer contains a safety rule:{" "}
                            {entry.missing_safety_rules
                              .map((id) => entry.safety_rules.find((r) => r.id === id)?.label ?? id)
                              .join(", ")}
                            .
                          </div>
                        ) : null}
                      </div>
                      <div className="settings-row-control">
                        <Button
                          size="compact"
                          variant="secondary"
                          aria-expanded={editing}
                          aria-label={`${action}: ${entry.title}`}
                          onClick={() => setOpen(editing ? "" : entry.id)}
                        >
                          {action}
                        </Button>
                      </div>
                    </div>
                    {editing ? (
                      <PromptEditor
                        entry={entry}
                        path={
                          repository
                            ? `api/repositories/${repository}/prompts/${entry.id}`
                            : `api/settings/prompts/${entry.id}`
                        }
                        baseline={baseline}
                        baselineName={
                          inherited?.source === "global" ? "the global prompt" : "the default"
                        }
                        resetLabel={repository ? "Use inherited" : "Reset to default"}
                        saveLabel={repository ? "Save for this repository" : "Save"}
                        draft={drafts[entry.id]}
                        setDraft={(update) =>
                          setDrafts((all) => {
                            const { [entry.id]: current, ...rest } = all;
                            const next = update(current);
                            return next === undefined ? rest : { ...rest, [entry.id]: next };
                          })
                        }
                        saved={replace}
                        close={() => setOpen("")}
                      />
                    ) : null}
                  </div>
                );
              })}
            </div>
          </section>
        );
      })}
    </>
  );
}
