import { useEffect, useId, useRef, useState, type ComponentType } from "react";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL, type Entry } from "./api";
import {
  sentence,
  templateInbox,
  countOf,
  fromTemplate,
  inboxesOf,
  matches,
  MAX_INBOXES,
  nameProblem,
  ownersOf,
  templates,
  type Inbox,
} from "./inboxes";
import { MenuButton, useContextMenu, type MenuItem } from "./context-menu";
import { FilterBar, FilterButton } from "./filter-menu";
import { KEEP_ONE, PageBar } from "./shell";
import { queryProblem, tidyQuery } from "./pr-filter";
import { SettingRow } from "./settings";
import { ColourSwatches, GROUPS, InboxTag, OTHERS_TITLE, type QueueItem } from "./up-next";
import { FilterChips } from "./filter-chips";

/** The preview's groups: the queue's, then others' pull requests. */
const PREVIEW_GROUPS = [
  ...GROUPS,
  { id: "others", title: OTHERS_TITLE, kinds: ["other"] as QueueItem["kind"][] },
];
import { DANGER, PRIMARY, SECONDARY } from "./action-classes";

/** What the three inbox pages share: the person's inboxes and the unsnoozed
 *  pull requests they are matched against (the queue and others' open
 *  ones), the same items the sidebar counts. */
export type InboxPageProps = {
  inboxes: Inbox[];
  items: QueueItem[];
  /** The signed-in GitHub account. */
  login?: string;
  /** The person's repositories, as owner/name. */
  repositories: string[];
  /** Every pull request read, whose values the query's filter offers. */
  entries?: Entry[];
  /** How many saves the daemon has accepted; each shows "Saved" for a moment. */
  saves: number;
  save: (inboxes: Inbox[]) => void;
  go: (href: string) => void;
  Link: ComponentType<ViewerLinkProps>;
};

export const newInboxHref = () => appURL("?page=inbox-new");
export const manageInboxesHref = () => appURL("?page=inboxes");
export const editInboxHref = (id: string) =>
  appURL(`?page=inbox-edit&inbox=${encodeURIComponent(id)}`);

/** "Saved" for two seconds after each save the daemon accepts. */
function useSavedMark(saves: number) {
  const [shown, setShown] = useState(false);
  const last = useRef(saves);
  useEffect(() => {
    if (saves === last.current) return;
    last.current = saves;
    setShown(true);
    const timer = setTimeout(() => setShown(false), 2000);
    return () => clearTimeout(timer);
  }, [saves]);
  return (
    <p role="status" className="settings-saved">
      {shown ? "Saved" : ""}
    </p>
  );
}

/** A pull request a bit of the page names: its title and where it is. */
function prLine(item: QueueItem) {
  const pr = item.entry.pull_request;
  return { title: pr.title, where: `${pr.repository} #${pr.number}` };
}

/** `?page=inbox-new`: six starting points, each a button that saves an inbox
 *  and opens its page. */
export function NewInboxPage({
  inboxes,
  items,
  login,
  repositories,
  start,
  Link,
}: InboxPageProps & {
  /** Opens a template as an inbox not saved yet, on its edit page. */
  start: (inbox: Inbox) => void;
}) {
  const owners = ownersOf(repositories, login);
  const full = inboxes.length >= MAX_INBOXES;
  return (
    <section>
      <PageBar title="What should this inbox catch?" />
      <div className="local-page-body inbox-page">
        <Link href={appURL()} className="inbox-back">
          ← Inbox
        </Link>
        {full ? (
          <p role="status" className="inbox-limit">
            You have {MAX_INBOXES} inboxes, the most there can be. Delete one to start another.
          </p>
        ) : null}
        <ul className="inbox-templates">
          {templates(owners, login).map((template) => {
            const made = fromTemplate(template, inboxes, "preview");
            const count = countOf(made, items);
            return (
              <li key={template.name}>
                <button
                  type="button"
                  disabled={full}
                  className={`inbox-template inbox-${made.color}`}
                  onClick={() => start(fromTemplate(template, inboxes))}
                >
                  <span className="inbox-template-head">
                    <span aria-hidden="true" className="inbox-dot" />
                    <span className="inbox-template-name">{made.name}</span>
                    <span className="inbox-template-count">{count} now</span>
                  </span>
                  <span className="inbox-template-sentence">
                    {template.description ??
                      sentence(templateInbox(template, inboxes, "preview"), login)}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
        <p className="settings-footnote">A pull request can sit in more than one inbox.</p>
      </div>
    </section>
  );
}

type Diff = { added: Set<string>; removed: QueueItem[] };

/** `?page=inbox-edit&inbox=<id>`: one inbox, saved as it is changed, beside
 *  what it would show right now. */
export function EditInboxPage({
  id,
  fresh,
  remove,
  draft,
  setDraft,
  onCreated,
  ...page
}: InboxPageProps & {
  /** The id in the address. */
  id: string;
  /** Just created. */
  fresh: boolean;
  /** Deletes the inbox once confirmed, then runs `after`. */
  remove: (inbox: Inbox, after?: () => void) => void;
  /** An inbox started from a template on New inbox, not saved until Save. */
  draft: Inbox | null;
  setDraft: (inbox: Inbox | null) => void;
  onCreated?: () => void;
}) {
  const { inboxes, Link } = page;
  const saved = inboxes.find((one) => one.id === id);
  const unsaved = !saved && draft?.id === id ? draft : null;
  // A Save of an inbox not saved yet goes to its queue once the daemon has
  // it; one refused leaves the draft here to try again.
  const [saving, setSaving] = useState<number | null>(null);
  useEffect(() => {
    if (saving === null || page.saves <= saving) return;
    setSaving(null);
    onCreated?.();
    setDraft(null);
    page.go(appURL(`?inbox=${encodeURIComponent(id)}`));
  }, [page.saves]);
  const inbox = saved ?? unsaved;
  if (!inbox)
    return (
      <section>
        <PageBar title="Inbox not found" />
        <div className="local-page-body">
          <div className="local-empty">
            <h2>This inbox no longer exists</h2>
            <p>
              <Link href={appURL()}>Go to Inbox</Link>
            </p>
          </div>
        </div>
      </section>
    );
  return (
    <EditInbox
      key={inbox.id}
      inbox={inbox}
      fresh={fresh || !!unsaved}
      remove={remove}
      keepDraft={unsaved ? setDraft : undefined}
      saveDraft={(made) => {
        setDraft(made);
        page.save([...inboxes, made]);
        setSaving(page.saves);
      }}
      {...page}
    />
  );
}

function EditInbox({
  inbox,
  fresh,
  remove,
  keepDraft,
  saveDraft,
  inboxes,
  items,
  repositories,
  entries,
  saves,
  save,
  Link,
}: InboxPageProps & {
  inbox: Inbox;
  fresh: boolean;
  remove: (inbox: Inbox, after?: () => void) => void;
  /** Takes each change while the inbox is not saved yet; Save then adds it. */
  keepDraft?: (inbox: Inbox | null) => void;
  /** Adds the inbox not saved yet. */
  saveDraft?: (inbox: Inbox) => void;
}) {
  const saved = useSavedMark(saves);
  const ids = useId();
  // The last inbox cannot go: Inbox always has one to open.
  const last = inboxes.length <= 1;
  const [draft, setDraft] = useState(inbox.name);
  const [nameError, setNameError] = useState("");
  const [diff, setDiff] = useState<Diff | null>(null);
  const nameField = useRef<HTMLInputElement>(null);
  // A new inbox opens with its name to type over.
  useEffect(() => {
    if (fresh) {
      nameField.current?.focus();
      nameField.current?.select();
    }
  }, []);
  // The name shown follows the saved one, as after a refusal elsewhere.
  useEffect(() => setDraft(inbox.name), [inbox.name]);
  useEffect(() => {
    if (!diff) return;
    const timer = setTimeout(() => setDiff(null), 4000);
    return () => clearTimeout(timer);
  }, [diff]);

  const mine = items.filter((item) => matches(inbox, item));

  /** Saves a change, and notes what it added to and took out of the preview. */
  const change = (patch: Partial<Inbox>) => {
    const next = { ...inbox, ...patch };
    if (keepDraft) keepDraft(next);
    else save(inboxes.map((one) => (one.id === inbox.id ? next : one)));
    const was = new Set(mine.map((item) => item.key));
    const after = items.filter((item) => matches(next, item));
    const now = new Set(after.map((item) => item.key));
    const added = after.filter((item) => !was.has(item.key)).map((item) => item.key);
    const removed = mine.filter((item) => !now.has(item.key));
    if (added.length + removed.length > 0) setDiff({ added: new Set(added), removed });
  };

  const commitName = () => {
    const problem = nameProblem(draft, inboxes, inbox.id);
    setNameError(problem);
    if (problem) return;
    const name = draft.trim();
    setDraft(name);
    if (name !== inbox.name) change({ name });
  };

  const kinds = new Map(items.map((item) => [item.entry, item.kind] as const));

  // The preview: what matches now, plus what the last change took out until
  // it fades, in the queue's groups.
  const removedKeys = new Set(diff?.removed.map((item) => item.key));
  const nowKeys = new Set(mine.map((item) => item.key));
  const shown = items.filter((item) => nowKeys.has(item.key) || removedKeys.has(item.key));
  const groups = PREVIEW_GROUPS.map((group) => ({
    ...group,
    rows: shown.filter((item) => group.kinds.includes(item.kind)),
  })).filter((group) => group.rows.length > 0);

  return (
    <section
      onKeyDown={(event) => {
        if (
          !keepDraft ||
          event.key !== "Enter" ||
          !(event.metaKey || event.ctrlKey) ||
          event.nativeEvent.isComposing
        )
          return;
        event.preventDefault();
        const problem = nameProblem(draft, inboxes, inbox.id);
        setNameError(problem);
        if (!problem) {
          const text =
            event.currentTarget.querySelector<HTMLTextAreaElement>(".inbox-query textarea")?.value;
          const query =
            text !== undefined && !queryProblem(text, true) ? tidyQuery(text) : inbox.query;
          saveDraft?.({ ...inbox, name: draft.trim(), query });
        }
      }}
    >
      <PageBar
        title={keepDraft ? "New inbox" : "Edit inbox"}
        detail={keepDraft ? "Save adds it to the sidebar" : "Changes save as you make them"}
        actions={keepDraft ? null : saved}
      />
      <div className="local-page-body inbox-page inbox-edit">
        {keepDraft ? (
          <Link href={newInboxHref()} className="inbox-back">
            ← New inbox
          </Link>
        ) : (
          <Link href={appURL(`?inbox=${encodeURIComponent(inbox.id)}`)} className="inbox-back">
            ← {inbox.name}
          </Link>
        )}
        <div className={`inbox-form inbox-${inbox.color} local-panel`}>
          {keepDraft ? (
            <p role="status" className="inbox-fresh">
              Not saved yet. Adjust anything below, then Save.
            </p>
          ) : fresh ? (
            <p role="status" className="inbox-fresh">
              Created and added to the sidebar. Adjust anything below.
            </p>
          ) : null}
          <div className="inbox-block">
            <label htmlFor={`${ids}-name`} className="settings-row-label">
              Name
            </label>
            <div className="inbox-name-row">
              <span aria-hidden="true" className="inbox-dot inbox-dot-large" />
              <input
                id={`${ids}-name`}
                ref={nameField}
                type="text"
                value={draft}
                spellCheck={false}
                autoComplete="off"
                aria-invalid={nameError ? true : undefined}
                aria-describedby={nameError ? `${ids}-name-error` : undefined}
                className="local-field inbox-name"
                onChange={(event) => {
                  setDraft(event.target.value);
                  setNameError("");
                }}
                onBlur={commitName}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    commitName();
                  }
                }}
              />
              <ColourSwatches
                color={inbox.color}
                pick={(color) => {
                  if (color !== inbox.color) change({ color });
                }}
              />
            </div>
            {nameError ? (
              <p id={`${ids}-name-error`} role="alert" className="inbox-name-error">
                {nameError}
              </p>
            ) : null}
          </div>
          <div className="inbox-block">
            <h2 className="settings-row-label">What it catches</h2>
            {/* Each condition, removed by its own ×; then the whole query,
                picked from the filter menu or typed. */}
            <FilterChips
              query={inbox.query}
              onChange={(query) => change({ query })}
              empty="No filter: every open pull request. Add filters to narrow it."
            />
            <div className="inbox-query">
              <FilterButton
                query={inbox.query}
                onChange={(query) => change({ query })}
                entries={items.map((item) => item.entry)}
                known={{ entries, repositories }}
                kindOf={(entry) => kinds.get(entry)}
                inbox
              />
              <FilterBar
                query={inbox.query}
                onChange={(query) => change({ query })}
                entries={items.map((item) => item.entry)}
                known={{ entries, repositories }}
                inbox
              />
            </div>
          </div>
          <div className="settings-list inbox-badge-block">
            <SettingRow
              label="Show its count in the sidebar"
              htmlFor={`${ids}-badge`}
              detail={
                inbox.badge
                  ? "The sidebar shows how many of its pull requests wait."
                  : "No count beside it, so it never nags. Its pull requests still add to the Inbox total."
              }
            >
              <input
                id={`${ids}-badge`}
                type="checkbox"
                role="switch"
                className="local-switch"
                checked={inbox.badge}
                onChange={(event) => change({ badge: event.target.checked })}
              />
            </SettingRow>
          </div>
          {keepDraft ? (
            <div className="inbox-block inbox-delete">
              <p className="settings-row-detail">Nothing is saved until you save it.</p>
              <button
                type="button"
                className={PRIMARY}
                onClick={() => {
                  // The name as typed, checked as leaving its field would.
                  const problem = nameProblem(draft, inboxes, inbox.id);
                  setNameError(problem);
                  if (!problem) saveDraft?.({ ...inbox, name: draft.trim() });
                }}
              >
                Save
              </button>
            </div>
          ) : (
            <div className="inbox-block inbox-delete">
              <p id={`${ids}-delete-note`} className="settings-row-detail">
                {last
                  ? KEEP_ONE
                  : "Deleting an inbox keeps its pull requests, reviews and snoozes."}
              </p>
              <button
                type="button"
                className={DANGER}
                disabled={last}
                aria-describedby={last ? `${ids}-delete-note` : undefined}
                onClick={() =>
                  remove(inbox, () => {
                    // The page goes to the first inbox left; focus lands on its row.
                    const next = inboxes.find((one) => one.id !== inbox.id);
                    if (!next) return;
                    document
                      .querySelector<HTMLElement>(
                        `.local-rail a.local-rail-sub[href="${appURL(`?inbox=${encodeURIComponent(next.id)}`)}"]`,
                      )
                      ?.focus();
                  })
                }
              >
                Delete inbox
              </button>
            </div>
          )}
        </div>
        <aside aria-label="Preview" className="inbox-preview local-panel">
          <div className="inbox-preview-head">
            <h2>Would show right now</h2>
            <span className="inbox-preview-count">{mine.length}</span>
          </div>
          <p role="status" className="inbox-diff">
            {diff
              ? [
                  diff.added.size > 0 ? `${diff.added.size} added` : "",
                  diff.removed.length > 0 ? `${diff.removed.length} removed` : "",
                ]
                  .filter(Boolean)
                  .join(" · ")
              : ""}
          </p>
          {groups.map((group) => (
            <section key={group.id} aria-label={group.title} className="inbox-preview-group">
              <h3>
                {group.title} · {group.rows.filter((item) => nowKeys.has(item.key)).length}
              </h3>
              <ul>
                {group.rows.map((item) => {
                  const gone = !nowKeys.has(item.key);
                  const { title, where } = prLine(item);
                  return (
                    <li
                      key={item.key}
                      className={`inbox-preview-row${gone ? " gone" : ""}${diff?.added.has(item.key) ? " added" : ""}`}
                    >
                      <span className="inbox-preview-title">{title}</span>
                      <span className="inbox-preview-meta">
                        <span>{where}</span>
                        {inboxesOf(item, inboxes)
                          .filter((other) => other.id !== inbox.id)
                          .map((other) => (
                            <InboxTag key={other.id} inbox={other} />
                          ))}
                      </span>
                    </li>
                  );
                })}
              </ul>
            </section>
          ))}
          {mine.length === 0 && !diff ? (
            <p className="settings-row-detail">
              Nothing yet. It fills up as pull requests come in.
            </p>
          ) : null}
          <p className="settings-footnote">
            Tags show the other inboxes a pull request is also in.
          </p>
        </aside>
      </div>
    </section>
  );
}

/** `?page=inboxes`: every inbox in the sidebar's order, to reorder, switch in
 *  or out of the badge, and open. */
export function ManageInboxesPage({ inboxes, items, saves, save, go, Link }: InboxPageProps) {
  const saved = useSavedMark(saves);
  const move = (index: number, by: -1 | 1) => {
    const next = inboxes.slice();
    const [one] = next.splice(index, 1);
    next.splice(index + by, 0, one!);
    save(next);
  };
  // Each inbox's actions, behind its ⋯ and a right click on it; its name
  // opens it to edit.
  const menu = useContextMenu();
  const actions = (inbox: Inbox, index: number): MenuItem[] => {
    const choices: MenuItem[] = [
      { label: "Edit…", hint: "↵", onSelect: () => go(editInboxHref(inbox.id)) },
    ];
    if (index > 0) choices.push({ label: "Move up", onSelect: () => move(index, -1) });
    if (index < inboxes.length - 1)
      choices.push({ label: "Move down", onSelect: () => move(index, 1) });
    choices.push({
      label: "Show count in the sidebar",
      checked: inbox.badge,
      onSelect: () =>
        save(inboxes.map((one) => (one.id === inbox.id ? { ...one, badge: !one.badge } : one))),
    });
    return choices;
  };
  const newLink = (
    <Link href={newInboxHref()} className={SECONDARY}>
      New inbox
    </Link>
  );
  return (
    <section>
      <PageBar
        title="Inboxes"
        actions={
          <>
            {saved}
            {newLink}
          </>
        }
      />
      <div className="local-page-body local-list-page">
        <>
          <p className="local-list-note inbox-lede">
            These are your inboxes, in their order in the sidebar. A pull request can be in several.
          </p>
          {menu.menu}
          <ul className="list inbox-manage">
            {inboxes.map((inbox, index) => (
              <li
                key={inbox.id}
                className={`list-row inbox-manage-row inbox-${inbox.color}`}
                onContextMenu={(event) => menu.open(event, actions(inbox, index), inbox.name)}
              >
                <span aria-hidden="true" className="inbox-dot inbox-dot-large" />
                <div className="inbox-manage-text">
                  <span className="inbox-manage-name">
                    <Link href={editInboxHref(inbox.id)} aria-label={`Edit ${inbox.name}`}>
                      <strong>{inbox.name}</strong>
                    </Link>
                    <span>
                      {countOf(inbox, items)} now
                      {inbox.badge ? "" : " · count hidden"}
                    </span>
                  </span>
                </div>
                <MenuButton label={inbox.name} items={() => actions(inbox, index)} menu={menu} />
              </li>
            ))}
          </ul>
        </>
      </div>
    </section>
  );
}
