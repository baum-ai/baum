import { useEffect, useId, useRef, useState, type ComponentType, type ReactNode } from "react";
import type { ViewerLinkProps } from "@baum/viewer/navigation";
import { appURL } from "./api";
import {
  choices,
  countOf,
  fromTemplate,
  inboxesOf,
  INBOX_COLORS,
  matches,
  MAX_INBOXES,
  nameProblem,
  ownersOf,
  sentence,
  sentenceParts,
  templates,
  type Choice,
  type Inbox,
  type InboxColor,
} from "./inboxes";
import { PageBar } from "./shell";
import { SettingRow } from "./settings";
import { GROUPS, InboxTag, type UpNextItem } from "./up-next";

/** What the three inbox pages share: the person's inboxes and the unsnoozed
 *  queue they are matched against, the same items the sidebar counts. */
export type InboxPageProps = {
  inboxes: Inbox[];
  items: UpNextItem[];
  /** The signed-in GitHub account. */
  login?: string;
  /** The person's repositories, as owner/name. */
  repositories: string[];
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

const COLOR_NAMES: Record<InboxColor, string> = {
  blue: "Blue",
  green: "Green",
  red: "Red",
  amber: "Amber",
  purple: "Purple",
  grey: "Grey",
};

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

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);

/** A pull request a bit of the page names: its title and where it is. */
function prLine(item: UpNextItem) {
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
  save,
  go,
  created,
  Link,
}: InboxPageProps & { created: (id: string) => void }) {
  const full = inboxes.length >= MAX_INBOXES;
  const owners = ownersOf(repositories, login);
  return (
    <section>
      <PageBar
        title="What should this inbox catch?"
        detail="Start from one of these. You can change anything afterwards."
      />
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
                  className={`inbox-template inbox-${template.color}`}
                  onClick={() => {
                    const inbox = fromTemplate(template, inboxes);
                    save([...inboxes, inbox]);
                    created(inbox.id);
                    go(editInboxHref(inbox.id));
                  }}
                >
                  <span className="inbox-template-head">
                    <span aria-hidden="true" className="inbox-dot" />
                    <span className="inbox-template-name">{made.name}</span>
                    <span className="inbox-template-count">{count} now</span>
                  </span>
                  <span className="inbox-template-sentence">{sentence(made, login)}</span>
                </button>
              </li>
            );
          })}
        </ul>
        <p className="settings-footnote">
          A pull request can sit in more than one inbox. Inbox at the top always shows everything.
        </p>
      </div>
    </section>
  );
}

/** A choice of one part of an inbox's sentence, with how many pull requests
 *  picking it would show. */
function Chip({
  on,
  label,
  count,
  onClick,
}: {
  on: boolean;
  label: string;
  count?: number;
  onClick: () => void;
}) {
  return (
    <button type="button" aria-pressed={on} className="inbox-chip" onClick={onClick}>
      {label}
      {count === undefined ? null : (
        <>
          <span aria-hidden="true" className="inbox-chip-count">
            {count}
          </span>
          <span className="sr-only"> ({count} now)</span>
        </>
      )}
    </button>
  );
}

type Diff = { added: Set<string>; removed: UpNextItem[] };

/** `?page=inbox-edit&inbox=<id>`: one inbox, saved as it is changed, beside
 *  what it would show right now. */
export function EditInboxPage({
  id,
  fresh,
  remove,
  ...page
}: InboxPageProps & {
  /** The id in the address. */
  id: string;
  /** Just created from a template. */
  fresh: boolean;
  remove: (inbox: Inbox) => void;
}) {
  const { inboxes, Link } = page;
  const inbox = inboxes.find((one) => one.id === id);
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
  return <EditInbox key={inbox.id} inbox={inbox} fresh={fresh} remove={remove} {...page} />;
}

function EditInbox({
  inbox,
  fresh,
  remove,
  inboxes,
  items,
  login,
  repositories,
  saves,
  save,
  go,
  Link,
}: InboxPageProps & { inbox: Inbox; fresh: boolean; remove: (inbox: Inbox) => void }) {
  const saved = useSavedMark(saves);
  const ids = useId();
  const [draft, setDraft] = useState(inbox.name);
  const [nameError, setNameError] = useState("");
  const [open, setOpen] = useState<"what" | "where" | "risk" | null>(null);
  const [fine, setFine] = useState(inbox.repositories.length > 0 || inbox.no_bots);
  const [diff, setDiff] = useState<Diff | null>(null);
  // The name shown follows the saved one, as after a refusal elsewhere.
  useEffect(() => setDraft(inbox.name), [inbox.name]);
  useEffect(() => {
    if (!diff) return;
    const timer = setTimeout(() => setDiff(null), 4000);
    return () => clearTimeout(timer);
  }, [diff]);

  const owners = ownersOf(repositories, login);
  const mine = items.filter((item) => matches(inbox, item));

  /** Saves a change, and notes what it added to and took out of the preview. */
  const change = (patch: Partial<Inbox>) => {
    const next = { ...inbox, ...patch };
    save(inboxes.map((one) => (one.id === inbox.id ? next : one)));
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

  const options = choices(inbox, owners, login);
  const parts = sentenceParts(inbox, login);
  const part = (key: "what" | "where" | "risk", text: string) => (
    <button
      type="button"
      className="inbox-part"
      aria-expanded={open === key}
      aria-controls={`${ids}-${key}`}
      onClick={() => setOpen(open === key ? null : key)}
    >
      {key === "what" ? `${text.charAt(0).toUpperCase()}${text.slice(1)}` : text}
    </button>
  );
  const choiceList = (key: "what" | "where" | "risk", label: string) =>
    open === key ? (
      <div id={`${ids}-${key}`} role="group" aria-label={label} className="inbox-choices">
        {options[key].map((choice: Choice) => (
          <Chip
            key={choice.key}
            on={choice.on}
            label={choice.label}
            count={countOf({ ...inbox, ...choice.patch }, items)}
            onClick={() => {
              setOpen(null);
              if (!choice.on) change(choice.patch);
            }}
          />
        ))}
      </div>
    ) : null;

  const repositoryChips = Array.from(new Set([...repositories, ...inbox.repositories])).sort(
    (a, b) => a.localeCompare(b),
  );
  const toggleRepository = (repository: string) =>
    change({
      repositories: inbox.repositories.some((r) => r.toLowerCase() === repository.toLowerCase())
        ? inbox.repositories.filter((r) => r.toLowerCase() !== repository.toLowerCase())
        : [...inbox.repositories, repository],
    });

  // The preview: what matches now, plus what the last change took out until
  // it fades, in the queue's groups.
  const removedKeys = new Set(diff?.removed.map((item) => item.key));
  const nowKeys = new Set(mine.map((item) => item.key));
  const shown = items.filter((item) => nowKeys.has(item.key) || removedKeys.has(item.key));
  const groups = GROUPS.map((group) => ({
    ...group,
    rows: shown.filter((item) => group.kinds.includes(item.kind)),
  })).filter((group) => group.rows.length > 0);

  return (
    <section>
      <PageBar title="Edit inbox" detail="Changes save as you make them" actions={saved} />
      <div className="local-page-body inbox-page inbox-edit">
        <Link href={appURL(`?inbox=${encodeURIComponent(inbox.id)}`)} className="inbox-back">
          ← {inbox.name}
        </Link>
        <div className={`inbox-form inbox-${inbox.color} local-panel`}>
          {fresh ? (
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
              <div role="radiogroup" aria-label="Colour" className="inbox-swatches">
                {INBOX_COLORS.map((color) => (
                  <button
                    key={color}
                    type="button"
                    role="radio"
                    aria-checked={inbox.color === color}
                    aria-label={COLOR_NAMES[color]}
                    title={COLOR_NAMES[color]}
                    className={`inbox-swatch inbox-${color}`}
                    onClick={() => {
                      if (inbox.color !== color) change({ color });
                    }}
                  />
                ))}
              </div>
            </div>
            {nameError ? (
              <p id={`${ids}-name-error`} role="alert" className="inbox-name-error">
                {nameError}
              </p>
            ) : null}
          </div>
          <div className="inbox-block">
            <h2 className="settings-row-label">What it catches</h2>
            <p className="inbox-sentence">
              {part("what", parts.what)} {part("where", parts.where)}, {part("risk", parts.risk)}.
            </p>
            {choiceList("what", "What it holds")}
            {choiceList("where", "Where")}
            {choiceList("risk", "Risk")}
            <button
              type="button"
              className="inbox-disclosure"
              aria-expanded={fine}
              onClick={() => setFine(!fine)}
            >
              Fine-tune
            </button>
            {fine ? (
              <div className="inbox-fine">
                <div role="group" aria-labelledby={`${ids}-repositories`}>
                  <h3 id={`${ids}-repositories`} className="inbox-fine-title">
                    Only these repositories
                    <span> · none picked means all that match above</span>
                  </h3>
                  <div className="inbox-choices">
                    {repositoryChips.map((repository) => (
                      <Chip
                        key={repository}
                        label={repository}
                        on={inbox.repositories.some(
                          (r) => r.toLowerCase() === repository.toLowerCase(),
                        )}
                        onClick={() => toggleRepository(repository)}
                      />
                    ))}
                    {repositoryChips.length === 0 ? (
                      <span className="settings-row-detail">
                        Pick repositories in Repositories first.
                      </span>
                    ) : null}
                  </div>
                </div>
                <fieldset className="inbox-leave-out">
                  <legend className="inbox-fine-title">Leave out</legend>
                  <label className="inbox-check">
                    <input
                      type="checkbox"
                      checked={inbox.no_bots}
                      onChange={(event) => change({ no_bots: event.target.checked })}
                    />
                    <span>Bots</span>
                    <span className="inbox-check-note">· Renovate, Dependabot</span>
                  </label>
                </fieldset>
              </div>
            ) : null}
          </div>
          <div className="settings-list inbox-badge-block">
            <SettingRow
              label="Count it in the Inbox badge"
              htmlFor={`${ids}-badge`}
              detail={
                inbox.badge
                  ? "Its pull requests add to the number on Inbox."
                  : "Left out of the badge, so it never nags. It still shows here and in Inbox."
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
          <div className="inbox-block inbox-delete">
            <p className="settings-row-detail">
              Deleting an inbox leaves its pull requests in Inbox.
            </p>
            <button
              type="button"
              className="inbox-btn"
              onClick={() => {
                remove(inbox);
                go(appURL());
              }}
            >
              Delete inbox
            </button>
          </div>
        </div>
        <aside aria-label="Preview" className="inbox-preview local-panel">
          <div className="inbox-preview-head">
            <h2>Would show right now</h2>
            <span className="inbox-preview-count">{mine.length}</span>
          </div>
          {diff ? (
            <p role="status" className="inbox-diff">
              {diff.added.size} added · {diff.removed.length} removed
            </p>
          ) : null}
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
export function ManageInboxesPage({ inboxes, items, login, saves, save, Link }: InboxPageProps) {
  const saved = useSavedMark(saves);
  const move = (index: number, by: -1 | 1) => {
    const next = inboxes.slice();
    const [one] = next.splice(index, 1);
    next.splice(index + by, 0, one!);
    save(next);
  };
  const uncovered = items.filter((item) => inboxesOf(item, inboxes).length === 0).length;
  const newLink = (
    <Link href={newInboxHref()} className="inbox-btn">
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
            {inboxes.length > 0 ? newLink : null}
          </>
        }
      />
      <div className="local-page-body inbox-page">
        {inboxes.length === 0 ? (
          <div className="local-empty">
            <h2>No inboxes yet</h2>
            <p>
              An inbox is a saved view of Inbox, such as everything urgent or only your work. Start
              with one.
            </p>
            {newLink}
          </div>
        ) : (
          <>
            <p className="inbox-lede">
              Inbox shows everything. These sit under it in the sidebar, in this order. A pull
              request can be in several.
            </p>
            <ul className="inbox-manage local-panel">
              {inboxes.map((inbox, index) => (
                <li key={inbox.id} className={`inbox-manage-row inbox-${inbox.color}`}>
                  <div className="inbox-move">
                    <button
                      type="button"
                      className="inbox-btn inbox-btn-small"
                      aria-label={`Move ${inbox.name} up`}
                      disabled={index === 0}
                      onClick={() => move(index, -1)}
                    >
                      <span aria-hidden="true">▲</span>
                    </button>
                    <button
                      type="button"
                      className="inbox-btn inbox-btn-small"
                      aria-label={`Move ${inbox.name} down`}
                      disabled={index === inboxes.length - 1}
                      onClick={() => move(index, 1)}
                    >
                      <span aria-hidden="true">▼</span>
                    </button>
                  </div>
                  <span aria-hidden="true" className="inbox-dot inbox-dot-large" />
                  <div className="inbox-manage-text">
                    <span className="inbox-manage-name">
                      <strong>{inbox.name}</strong>
                      <span>{countOf(inbox, items)} now</span>
                    </span>
                    <span className="settings-row-detail">{sentence(inbox, login)}</span>
                  </div>
                  <label className="inbox-manage-badge">
                    <span className="sr-only">Count {inbox.name} in the Inbox badge</span>
                    <input
                      type="checkbox"
                      role="switch"
                      className="local-switch"
                      title="Count in the Inbox badge"
                      checked={inbox.badge}
                      onChange={(event) =>
                        save(
                          inboxes.map((one) =>
                            one.id === inbox.id ? { ...one, badge: event.target.checked } : one,
                          ),
                        )
                      }
                    />
                  </label>
                  <Link
                    href={editInboxHref(inbox.id)}
                    className="inbox-btn"
                    aria-label={`Edit ${inbox.name}`}
                  >
                    Edit
                  </Link>
                </li>
              ))}
            </ul>
            <p className="inbox-badge-label">Badge</p>
            <p className="inbox-coverage">
              {uncovered > 0
                ? `${uncovered} ${plural(uncovered, "pull request isn’t", "pull requests aren’t")} in any inbox right now. ${plural(uncovered, "It shows", "They show")} only under Inbox and always ${plural(uncovered, "counts", "count")} in its badge.`
                : "Every open pull request is in at least one inbox."}
            </p>
          </>
        )}
      </div>
    </section>
  );
}

/** The sidebar's two links under the inboxes. */
export function RailLinks({
  page,
  Link,
}: {
  page: string;
  Link: ComponentType<ViewerLinkProps>;
}): ReactNode {
  const link = (current: boolean, href: string, label: string, plus?: boolean) => (
    <Link
      href={href}
      aria-current={current ? "page" : undefined}
      className="local-rail-sub local-rail-extra"
    >
      <span aria-hidden="true" className="local-rail-plus">
        {plus ? "+" : ""}
      </span>
      <span className="local-rail-label">{label}</span>
    </Link>
  );
  return (
    <>
      {link(page === "inbox-new", newInboxHref(), "New inbox", true)}
      {link(page === "inboxes", manageInboxesHref(), "Manage inboxes")}
    </>
  );
}
