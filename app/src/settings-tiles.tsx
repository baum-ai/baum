import { useEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { formatDateTime, formatRelative } from "@baum/viewer/components/format";
import { ChevronLeftIcon } from "@baum/viewer/components/icons";
import { Alert, Badge, Button, CloseButton, SegmentedControl } from "@baum/viewer/components/ui";
import { appURL, request, type Repository } from "./api";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { ListSection } from "./list";
import { useModal } from "./modal";
import { LoadingStatus } from "./nine-dots";
import { Notice } from "./shell";
import { sessionPanel } from "./session-panel-state";
import {
  digestHex,
  MAX_FILE_BYTES,
  originLabel,
  saveTileTypeFile,
  tileLibrary,
  type TileTypeDetail,
  type TileTypeFileCheck,
  type TileTypePreview,
  type TileTypeSummary,
} from "./tile-library";
import { framesAllowedHere, NOT_IN_THIS_SHELL } from "./tiles/custom/environment";
import { PreviewFrame } from "./tiles/custom/tile-frame";
import type { TileSize } from "./tiles/types";
import { useToast } from "./toast";

/** Settings › Tile library (`?page=settings&section=tiles[&type=<name>]`):
 *  the person's own tile types, saved from the session panel. The list
 *  names each type, where it came from, when to use it, where it is pinned
 *  and when a review last used it; a type opens in its pane, with previews
 *  of its samples drawn in their frames, rename, Edit in chat, pins,
 *  versions, Export and Delete with Undo; Import reads a file someone
 *  shared, after a dialog that shows what it would add. The open type is the page context's
 *  focus, so the session panel's agent knows which one "this type" is. */

function errorText(error: unknown) {
  return error instanceof Error ? error.message : "The tile library could not be read.";
}

/** When a review last used a type, in words. */
export function usedLabel(t: Pick<TileTypeSummary, "used_at" | "used_in">): string {
  if (!t.used_at) return "Not used in a review yet";
  const where =
    t.used_in > 1 ? ` in ${t.used_in} repositories` : t.used_in === 1 ? " in 1 repository" : "";
  return `Used ${formatRelative(t.used_at)}${where}`;
}

/** Where a type is pinned, in words. */
export function pinnedLabel(pinned: string[]): string {
  if (pinned.length === 0) return "Not pinned";
  if (pinned.length === 1) return `Pinned to ${pinned[0]}`;
  return `Pinned to ${pinned.length} repositories`;
}

/** The library's types, read once and again after each change. */
function useTileTypes(onChanged: () => void) {
  const [types, setTypes] = useState<TileTypeSummary[] | null>(null);
  const [error, setError] = useState("");
  const read = useRef(0);
  const load = () =>
    tileLibrary
      .list()
      .then((list) => {
        setTypes(list);
        setError("");
        // The first read is Settings' own; later ones follow a change.
        if (read.current++ > 0) onChanged();
      })
      .catch((e: unknown) => setError(errorText(e)));
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once; changes read again
  useEffect(() => {
    void load();
  }, []);
  return { types, error, load };
}

/** Opens the session panel about a type: its pane is open, so the page
 *  context's focus names it (`focus.type`) for the agent, which reads it
 *  with tile_type and proposes a change with propose_tile_type. */
function editInChat(name: string, open: (type: string | null) => void) {
  open(name);
  sessionPanel.show();
}

export function TileLibrarySection({
  type,
  open,
  head,
  onChanged = () => {},
}: {
  /** The type open in its pane, from the address; null shows the list. */
  type: string | null;
  open: (type: string | null) => void;
  /** The section's heading, drawn by Settings. */
  head: (status?: ReactNode) => ReactNode;
  /** Called after the library changed, for the section list's count. */
  onChanged?: () => void;
}) {
  const library = useTileTypes(onChanged);
  const toast = useToast();
  const [error, setError] = useState("");
  const remove = async (t: Pick<TileTypeSummary, "name" | "title">) => {
    setError("");
    try {
      await tileLibrary.remove(t.name);
    } catch (e) {
      setError(errorText(e));
      return;
    }
    if (type === t.name) open(null);
    await library.load();
    toast({
      title: `Deleted ${t.title}`,
      detail: "Undo brings it back, pinned where it was.",
      action: {
        label: "Undo",
        run: () =>
          void tileLibrary
            .undelete(t.name)
            .then(() => library.load())
            .catch((e: unknown) =>
              toast({ title: "Not brought back", detail: errorText(e), error: true }),
            ),
      },
    });
  };
  /** Saves the type's file where the person chooses, or downloads it. */
  const exportType = async (t: Pick<TileTypeSummary, "name" | "title">) => {
    setError("");
    try {
      const saved = await saveTileTypeFile(t.name, await tileLibrary.exportFile(t.name));
      if (saved) toast({ title: `Exported ${t.title}`, detail: saved });
    } catch (e) {
      setError(errorText(e));
    }
  };
  const [importing, setImporting] = useState<{ file: string; check: TileTypeFileCheck } | null>(
    null,
  );
  const picker = useRef<HTMLInputElement>(null);
  const pick = async (file: File | undefined) => {
    if (!file) return;
    setError("");
    if (file.size > MAX_FILE_BYTES) {
      setError(`The file is larger than ${MAX_FILE_BYTES >> 20} MiB.`);
      return;
    }
    try {
      const text = await file.text();
      setImporting({ file: text, check: await tileLibrary.checkFile(text) });
    } catch (e) {
      setError(`${file.name} can't be imported. ${errorText(e)}`);
    }
  };
  const summary = type ? library.types?.find((t) => t.name === type) : undefined;
  const importButton = (
    <>
      <input
        ref={picker}
        type="file"
        accept=".json,application/json"
        hidden
        aria-label="Tile type file"
        data-tile-type-file=""
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          void pick(file);
        }}
      />
      <Button variant="secondary" size="compact" onClick={() => picker.current?.click()}>
        Import…
      </Button>
    </>
  );
  return (
    <section aria-labelledby="settings-tiles-heading" className="settings-section">
      {head(!type && library.types && library.types.length > 0 ? importButton : undefined)}
      {importing ? (
        <ImportDialog
          check={importing.check}
          close={() => setImporting(null)}
          save={async () => {
            const imported = await tileLibrary.importFile(importing.file);
            setImporting(null);
            await library.load();
            open(imported.name);
            toast({
              title: `Imported ${importing.check.title}`,
              detail: imported.renamed_from
                ? `Saved as ${imported.name}: ${imported.renamed_from} was taken.`
                : imported.checked
                  ? undefined
                  : "Not checked yet: its render check runs when baum can.",
            });
          }}
        />
      ) : null}
      {error ? <Alert>{error}</Alert> : null}
      {library.error ? (
        <Alert>
          {library.error}{" "}
          <Button variant="secondary" size="compact" onClick={() => void library.load()}>
            Try again
          </Button>
        </Alert>
      ) : null}
      {type ? (
        <TileTypePane
          key={type}
          name={type}
          known={summary}
          back={() => open(null)}
          changed={library.load}
          remove={remove}
          exportType={exportType}
          editInChat={() => editInChat(type, open)}
        />
      ) : library.types === null ? (
        library.error ? null : (
          <LoadingStatus block>Reading your tile library…</LoadingStatus>
        )
      ) : library.types.length === 0 ? (
        <div className="local-empty tile-library-empty">
          <h2>No tile types yet</h2>
          <p>
            Open a pull request's review, ask the session panel to make a tile for it, then ask it
            to save the tile to your library. Saved types are offered to later reviews where they
            apply. Or import a type someone shared.
          </p>
          {importButton}
        </div>
      ) : (
        <TileTypeList types={library.types} open={open} remove={remove} exportType={exportType} />
      )}
    </section>
  );
}

function TileTypeList({
  types,
  open,
  remove,
  exportType,
}: {
  types: TileTypeSummary[];
  open: (type: string | null) => void;
  remove: (t: TileTypeSummary) => void;
  exportType: (t: TileTypeSummary) => void;
}) {
  const menu = useContextMenu();
  const actions = (t: TileTypeSummary): MenuItem[] => [
    { label: "Open", hint: "↵", onSelect: () => open(t.name) },
    { label: "Edit in chat", onSelect: () => editInChat(t.name, open) },
    { label: "Export…", onSelect: () => exportType(t) },
    { label: "Delete", danger: true, onSelect: () => remove(t) },
  ];
  return (
    <div className="tile-library-list">
      {menu.menu}
      <ListSection title="Your tile types" count={types.length}>
        {types.map((t) => (
          <li
            key={t.name}
            data-tile-type={t.name}
            className="list-row tile-type-row"
            onContextMenu={(event) => menu.open(event, actions(t), t.title)}
          >
            <div className="tile-type-main">
              <h3>
                <a
                  href={appURL(`?page=settings&section=tiles&type=${encodeURIComponent(t.name)}`)}
                  className="tile-type-name"
                  data-row-link=""
                  onClick={(event) => {
                    if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0)
                      return;
                    event.preventDefault();
                    open(t.name);
                  }}
                >
                  {t.title}
                </a>{" "}
                <Badge size="compact">Custom</Badge>
                {t.checked ? null : <NotChecked />}
              </h3>
              <p className="tile-type-use">{t.use_when}</p>
              <p className="tile-type-meta">
                {originLabel(t.origin)} · {pinnedLabel(t.pinned)}
              </p>
            </div>
            <span
              className="tile-type-figure"
              title={t.used_at ? formatDateTime(t.used_at) : undefined}
            >
              {usedLabel(t)}
            </span>
            <div className="tile-type-actions">
              <MenuButton label={t.title} items={() => actions(t)} menu={menu} />
            </div>
          </li>
        ))}
      </ListSection>
    </div>
  );
}

/** One type's pane. */
function TileTypePane({
  name,
  known,
  back,
  changed,
  remove,
  exportType,
  editInChat: ask,
}: {
  name: string;
  known?: TileTypeSummary;
  back: () => void;
  changed: () => Promise<void>;
  remove: (t: Pick<TileTypeSummary, "name" | "title">) => void;
  exportType: (t: Pick<TileTypeSummary, "name" | "title">) => void;
  editInChat: () => void;
}) {
  const [detail, setDetail] = useState<TileTypeDetail | null>(null);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const load = () =>
    tileLibrary
      .read(name)
      .then((d) => {
        setDetail(d);
        setError("");
      })
      .catch((e: unknown) => {
        if ((e as { status?: number }).status === 404) setMissing(true);
        else setError(errorText(e));
      });
  // biome-ignore lint/correctness/useExhaustiveDependencies: read once per type
  useEffect(() => {
    void load();
  }, [name]);
  /** Runs a change, then shows what the daemon answered and reads the list again. */
  const change = async (what: string, run: () => Promise<TileTypeDetail>) => {
    setBusy(what);
    setError("");
    try {
      setDetail(await run());
      await changed();
      return true;
    } catch (e) {
      setError(errorText(e));
      // A type changed meanwhile is read again.
      if ((e as { status?: number }).status === 409) await load();
      return false;
    } finally {
      setBusy("");
    }
  };
  const backLink = (
    <Button variant="ghost" size="compact" className="justify-self-start" onClick={back}>
      <ChevronLeftIcon size={14} />
      All tile types
    </Button>
  );
  if (missing)
    return (
      <>
        {backLink}
        <div className="local-empty">
          <h2>This tile type isn't in your library</h2>
          <p>It may have been deleted. Your other types are in the list.</p>
        </div>
      </>
    );
  if (!detail)
    return (
      <>
        {backLink}
        {error ? (
          <Alert>{error}</Alert>
        ) : (
          <LoadingStatus block>Reading {known?.title ?? "the tile type"}…</LoadingStatus>
        )}
      </>
    );
  return (
    <div className="tile-type-pane" data-tile-type-pane={detail.name}>
      {backLink}
      <header className="tile-type-pane-head">
        <TitleField
          title={detail.title}
          saving={busy === "rename"}
          save={(title) => change("rename", () => tileLibrary.rename(detail.name, title))}
        />
        <p className="tile-type-meta">
          <Badge size="compact">Custom</Badge>
          {detail.checked ? null : <NotChecked />} {originLabel(detail.origin)} ·{" "}
          {usedLabel(detail)}
        </p>
        <div className="tile-type-pane-actions">
          <Button size="compact" onClick={ask}>
            Edit in chat
          </Button>
          <Button variant="secondary" size="compact" onClick={() => exportType(detail)}>
            Export…
          </Button>
          <Button
            variant="danger-outline"
            size="compact"
            disabled={busy !== ""}
            onClick={() => remove(detail)}
          >
            Delete
          </Button>
        </div>
      </header>
      {error ? <Alert>{error}</Alert> : null}
      <div className="local-panel settings-list">
        <Row label="Use when">{detail.use_when || "No use-when line"}</Row>
        <Row label="Glance">{detail.glance}</Row>
        <Row label="Paths">
          {detail.paths.length === 0 ? (
            "Every pull request"
          ) : (
            <span className="tile-type-paths">
              {detail.paths.map((path) => (
                <code key={path}>{path}</code>
              ))}
            </span>
          )}
        </Row>
        <details className="tile-type-fill settings-advanced">
          <summary>Fill instructions</summary>
          <p className="settings-row-detail">Given to the review agent when this type is filled.</p>
          <pre>{detail.fill}</pre>
          {detail.review_checks ? (
            <>
              <p className="settings-row-detail">What the review checks when the type applies.</p>
              <pre>{detail.review_checks}</pre>
            </>
          ) : null}
        </details>
      </div>
      <Previews detail={detail} />
      <Pins
        detail={detail}
        saving={busy === "pins"}
        save={(repositories) => change("pins", () => tileLibrary.pin(detail.name, repositories))}
      />
      <Versions
        detail={detail}
        busy={busy}
        restore={(digest) =>
          change(`restore:${digest}`, () => tileLibrary.restore(detail.name, digest))
        }
      />
    </div>
  );
}

/** An imported version whose render check has not run yet. */
function NotChecked() {
  return (
    <Badge
      size="compact"
      tone="caution"
      title="Imported without its render check; it runs when baum can check tiles."
    >
      Not checked yet
    </Badge>
  );
}

/** What an imported file would add, before anything is saved: its name (and
 *  that it was renamed, when its own was taken), title, use-when line, the
 *  fill instructions the review agent would be given, its paths and its
 *  first sample drawn as a glance and a detail. */
function ImportDialog({
  check,
  close,
  save,
}: {
  check: TileTypeFileCheck;
  close: () => void;
  save: () => Promise<void>;
}) {
  const dialog = useModal();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const size = check.sizes.includes("half") ? "half" : (check.sizes[0] ?? "full");
  const frames = framesAllowedHere();
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="tile-import-title"
      className="local-dialog-wide tile-import-dialog"
    >
      <div className="local-dialog-head">
        <div>
          <h2 id="tile-import-title">Import {check.title}</h2>
          <p>Check what this tile type would add to your library before you import it.</p>
        </div>
        <CloseButton onClick={close} />
      </div>
      {check.renamed_from ? (
        <Notice>
          Your library already has a type named <code>{check.renamed_from}</code>, so this one is
          saved as <code>{check.name}</code>.
        </Notice>
      ) : null}
      <div className="local-panel settings-list">
        <Row label="Title">{check.title}</Row>
        <Row label="Use when">{check.use_when || "No use-when line"}</Row>
        <Row label="Glance">{check.glance || "No glance line"}</Row>
        <Row label="Paths">
          {check.paths.length === 0 ? (
            "Every pull request"
          ) : (
            <span className="tile-type-paths">
              {check.paths.map((path) => (
                <code key={path}>{path}</code>
              ))}
            </span>
          )}
        </Row>
        <div className="tile-type-fill">
          <span className="settings-row-label">Fill instructions</span>
          <p className="settings-row-detail">Given to the review agent when this type is filled.</p>
          <pre data-import-fill="">{check.fill}</pre>
        </div>
        {check.review_checks ? (
          <div className="tile-type-fill">
            <span className="settings-row-label">Review checks</span>
            <p className="settings-row-detail">
              Instructions given to the review agent: what the review checks when this type applies.
            </p>
            <pre data-import-review-checks="">{check.review_checks}</pre>
          </div>
        ) : null}
      </div>
      <section aria-labelledby="tile-import-preview" className="tile-type-block">
        <h3 id="tile-import-preview" className="settings-subhead">
          Preview · {check.sample}
        </h3>
        {frames ? (
          <div className="tile-type-glances">
            <figure className={cn("tile-type-glance", GLANCE_CARDS[size].card)}>
              <div className="tile-type-card">
                <span className="tile-type-kicker">Custom · {check.title}</span>
                <div className={cn("relative", GLANCE_CARDS[size].box)}>
                  <PreviewFrame
                    digest={digestHex(check.digest)}
                    kit={check.kit}
                    title={`${check.title}, glance`}
                    face="glance"
                    size={size}
                    data={check.data}
                    refs={check.refs.glance ?? {}}
                  />
                </div>
              </div>
              <figcaption>{GLANCE_CARDS[size].label}</figcaption>
            </figure>
            <figure className="tile-type-detail w-full">
              <div className="tile-type-card tile-import-detail">
                <span className="tile-type-kicker">Custom · {check.title}</span>
                <PreviewFrame
                  digest={digestHex(check.digest)}
                  kit={check.kit}
                  title={`${check.title}, detail`}
                  face="detail"
                  size="full"
                  data={check.data}
                  refs={check.refs.detail ?? {}}
                />
              </div>
              <figcaption>Detail</figcaption>
            </figure>
          </div>
        ) : (
          <p className="tile-type-unavailable">{NOT_IN_THIS_SHELL}</p>
        )}
      </section>
      <p className="settings-row-detail">
        It will be marked Imported and Not checked yet until baum runs its render check.
      </p>
      {error ? <Alert>{error}</Alert> : null}
      <div className="local-dialog-actions">
        <Button variant="secondary" size="compact" onClick={close} disabled={busy}>
          Cancel
        </Button>
        <Button
          size="compact"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            setError("");
            save().catch((e: unknown) => {
              setError(errorText(e));
              setBusy(false);
            });
          }}
        >
          {busy ? "Importing…" : "Import"}
        </Button>
      </div>
    </dialog>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="settings-row">
      <div className="settings-row-text">
        <span className="settings-row-label">{label}</span>
        <div className="settings-row-detail">{children}</div>
      </div>
    </div>
  );
}

/** The type's title, renamed in place: Enter or Save keeps it, Escape or
 *  Cancel puts it back. */
function TitleField({
  title,
  saving,
  save,
}: {
  title: string;
  saving: boolean;
  save: (title: string) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(title);
  const field = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (editing) field.current?.select();
  }, [editing]);
  const commit = async () => {
    const next = text.trim();
    if (!next || next === title) {
      setEditing(false);
      return;
    }
    if (await save(next)) setEditing(false);
  };
  if (!editing)
    return (
      <div className="tile-type-title">
        <h3>{title}</h3>
        <Button
          variant="ghost"
          size="compact"
          onClick={() => {
            setText(title);
            setEditing(true);
          }}
        >
          Rename
        </Button>
      </div>
    );
  return (
    <form
      className="tile-type-title"
      onSubmit={(event) => {
        event.preventDefault();
        void commit();
      }}
    >
      <input
        ref={field}
        aria-label="Title"
        className="local-field tile-type-title-field"
        value={text}
        maxLength={160}
        disabled={saving}
        onChange={(event) => setText(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            setEditing(false);
          }
        }}
      />
      <Button type="submit" size="compact" disabled={saving}>
        {saving ? "Saving…" : "Save"}
      </Button>
      <Button variant="ghost" size="compact" disabled={saving} onClick={() => setEditing(false)}>
        Cancel
      </Button>
    </form>
  );
}

/** Which repositories the type is pinned to: a pinned type is always filled
 *  in that repository's generated reviews. Each switch saves at once. */
function Pins({
  detail,
  saving,
  save,
}: {
  detail: TileTypeDetail;
  saving: boolean;
  save: (repositories: string[]) => Promise<boolean>;
}) {
  const [repositories, setRepositories] = useState<string[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    request<Repository[]>("api/repositories")
      .then((list) => setRepositories(list.map((r) => r.repository).sort()))
      .catch((e: unknown) => setError(errorText(e)));
  }, []);
  // Shown at once as switched; the daemon's answer, or a failure, puts the
  // saved pins back.
  const [shown, setShown] = useState(detail.pinned);
  useEffect(() => setShown(detail.pinned), [detail.pinned]);
  const pinned = new Set(shown.map((r) => r.toLowerCase()));
  const toggle = async (repository: string, on: boolean) => {
    const next = on
      ? shown.filter((r) => r.toLowerCase() !== repository.toLowerCase())
      : [...shown, repository];
    setShown(next);
    if (!(await save(next))) setShown(detail.pinned);
  };
  return (
    <section aria-labelledby="tile-type-pins-heading" className="tile-type-block">
      <h3 id="tile-type-pins-heading" className="settings-subhead">
        Pin to repositories
      </h3>
      <p className="settings-row-detail">
        A pinned type is filled in every generated review of that repository. Unpinned, it is
        offered where its paths match.
      </p>
      {error ? <Alert>{error}</Alert> : null}
      {repositories === null ? (
        error ? null : (
          <LoadingStatus block>Reading your repositories…</LoadingStatus>
        )
      ) : repositories.length === 0 ? (
        <p className="settings-row-detail">
          Add repositories on the Repositories page to pin types to them.
        </p>
      ) : (
        <div className="local-panel settings-list">
          {repositories.map((repository) => {
            const id = `tile-pin-${repository.replace(/[^a-z0-9]/gi, "-")}`;
            const on = pinned.has(repository.toLowerCase());
            return (
              <div key={repository} className="settings-row">
                <div className="settings-row-text">
                  <label htmlFor={id} className="settings-row-label">
                    {repository}
                  </label>
                </div>
                <div className="settings-row-control">
                  <input
                    id={id}
                    type="checkbox"
                    role="switch"
                    className="local-switch"
                    checked={on}
                    disabled={saving}
                    onChange={() => void toggle(repository, on)}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

/** The widths a glance is drawn at, one per size a type declares (a kit
 *  type still draws a glance at each), and the box it draws in inside each. */
const GLANCE_CARDS: Record<TileSize, { card: string; box: string; label: string }> = {
  third: { card: "w-[344px]", box: "h-48", label: "Third width" },
  half: { card: "w-[522px]", box: "h-52", label: "Half width" },
  full: { card: "w-full", box: "h-36", label: "Full width" },
};

/** Each sample of the type, as a glance at each size it declares and as its
 *  detail, drawn through the preview route in the type's frame. */
function Previews({ detail }: { detail: TileTypeDetail }) {
  const allowed = framesAllowedHere();
  const [sample, setSample] = useState(detail.samples[0] ?? "");
  const [preview, setPreview] = useState<TileTypePreview | null>(null);
  const [error, setError] = useState("");
  const read = () => tileLibrary.preview(detail.name, sample);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new sample or version reads again
  useEffect(() => {
    if (!allowed || !sample) return;
    let live = true;
    setPreview(null);
    read()
      .then((answer) => {
        if (live) {
          setPreview(answer);
          setError("");
        }
      })
      .catch((e: unknown) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [allowed, sample, detail.digest]);
  const kicker = `Custom · ${detail.title}`;
  return (
    <section aria-labelledby="tile-type-preview-heading" className="tile-type-block">
      <h3 id="tile-type-preview-heading" className="settings-subhead">
        Preview
      </h3>
      {!allowed ? (
        <p className="tile-type-unavailable">{NOT_IN_THIS_SHELL}</p>
      ) : (
        <>
          {detail.samples.length > 1 ? (
            <SegmentedControl
              label="Sample"
              value={sample}
              items={detail.samples.map((s) => ({ value: s, label: s }))}
              onChange={setSample}
            />
          ) : null}
          {error ? <Alert>{error}</Alert> : null}
          {preview ? (
            <>
              <div className="tile-type-glances">
                {preview.sizes.map((size) => (
                  <figure key={size} className={cn("tile-type-glance", GLANCE_CARDS[size].card)}>
                    <div className="tile-type-card">
                      <span className="tile-type-kicker">{kicker}</span>
                      <div className={cn("relative", GLANCE_CARDS[size].box)}>
                        <PreviewFrame
                          key={`${preview.digest}:${preview.sample}`}
                          digest={digestHex(preview.digest)}
                          kit={preview.kit}
                          title={`${detail.title}, ${GLANCE_CARDS[size].label.toLowerCase()} glance`}
                          face="glance"
                          size={size}
                          data={preview.data}
                          refs={preview.refs.glance ?? {}}
                          reread={read}
                        />
                      </div>
                    </div>
                    <figcaption>{GLANCE_CARDS[size].label}</figcaption>
                  </figure>
                ))}
              </div>
              <figure className="tile-type-detail">
                <div className="tile-type-card">
                  <span className="tile-type-kicker">{kicker}</span>
                  <PreviewFrame
                    key={`${preview.digest}:${preview.sample}`}
                    digest={digestHex(preview.digest)}
                    kit={preview.kit}
                    title={`${detail.title}, detail`}
                    face="detail"
                    size="full"
                    data={preview.data}
                    refs={preview.refs.detail ?? {}}
                    reread={read}
                  />
                </div>
                <figcaption>Detail</figcaption>
              </figure>
            </>
          ) : error ? null : (
            <LoadingStatus block>Drawing the sample…</LoadingStatus>
          )}
        </>
      )}
    </section>
  );
}

/** The type's versions, newest first: any earlier one can be made current
 *  again. */
function Versions({
  detail,
  busy,
  restore,
}: {
  detail: TileTypeDetail;
  busy: string;
  restore: (digest: string) => Promise<boolean>;
}) {
  return (
    <section aria-labelledby="tile-type-versions-heading" className="tile-type-block">
      <h3 id="tile-type-versions-heading" className="settings-subhead">
        Versions
      </h3>
      <ol className="local-panel settings-list tile-type-versions">
        {detail.version_list.map((version, index) => (
          <li key={version.digest} className="settings-row" data-version={version.digest}>
            <div className="settings-row-text">
              <span className="settings-row-label">
                {index === detail.version_list.length - 1 ? "First saved" : "Saved"}{" "}
                {formatDateTime(version.at)}
              </span>
            </div>
            <div className="settings-row-control">
              {version.current ? (
                <Badge size="compact" tone="positive">
                  Current
                </Badge>
              ) : (
                <Button
                  variant="secondary"
                  size="compact"
                  disabled={busy !== ""}
                  aria-label={`Restore the version saved ${formatDateTime(version.at)}`}
                  onClick={() => void restore(version.digest)}
                >
                  {busy === `restore:${version.digest}` ? "Restoring…" : "Restore"}
                </Button>
              )}
            </div>
          </li>
        ))}
      </ol>
    </section>
  );
}
