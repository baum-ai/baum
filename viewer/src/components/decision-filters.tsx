import { type ReactNode, useEffect, useId, useRef, useState } from "react";

import {
  type AuthorCount,
  CATEGORIES,
  type DecisionCategory,
  type DecisionFile,
  type DecisionFilters,
  FEATURE_STATUSES,
  type FeatureStatus,
  hasDecisionFilters,
  hasFeatureFilters,
  personLabel,
  WHO_OPTIONS,
  withDecisionFilters,
} from "../decision-log";
import { humanize } from "../review-document";
import { cn, FOCUS_RING } from "./classnames";
import { Combobox } from "./combobox";
import { CategoryTag } from "./decision-log";
import { formatCount } from "./format";
import { CheckIcon, ChevronDownIcon, CloseIcon, FilterIcon } from "./icons";
import { SEGMENTED_TRACK, segmentedTileClass } from "./segmented";
import { Button } from "./ui";
import { useDisclosure } from "./use-disclosure";

/** The decision log's filter controls, shared by the hosted log and the local
 *  app's Timeline. The host owns where the filters live and how suggestions
 *  and authors load; these only show them and report changes. */

const FIELD = cn(
  "rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-2 py-1 text-base text-(--text-primary)",
  FOCUS_RING,
);

const SEARCH_DELAY_MS = 300;
const SUGGEST_DELAY_MS = 150;

export type DecisionFilterControlsProps = {
  filters: DecisionFilters;
  /** The log's file and folder suggestions for what is typed, a moment after
   *  typing stops; `signal` aborts when the text changes again. */
  files: (prefix: string, signal?: AbortSignal) => Promise<DecisionFile[]>;
  /** The authors the person filter offers, loaded by the host: by decisions
   *  on All decisions, by features on Features. */
  people: AuthorCount[];
  onChange: (changes: Partial<DecisionFilters>) => void;
  onClear: () => void;
  /** Counts "Clear filters" presses, which drop what is typed but not yet
   *  applied as well as the applied filters. */
  resets: number;
  /** The feature statuses shown, for the Features tab; absent elsewhere.
   *  On the Features tab the filters that pick decisions alone (category,
   *  decided by, superseded and agent-only) are neither offered nor counted;
   *  they stay in the address for All decisions. */
  statuses?: FeatureStatus[];
  onStatuses?: (statuses: FeatureStatus[]) => void;
};

/** The log's filters after the host's own scope picker: keyword search, the
 *  Filters popover, a pill per filter in force and "Clear filters". A
 *  fragment, so the host lays it in its own bar after its picker. Each
 *  control reports a change and nothing else; the host keeps the filters
 *  (hosted in the address) and hands them back. */
export function DecisionFilterControls({
  filters,
  files,
  people,
  onChange,
  onClear,
  resets,
  statuses,
  onStatuses,
}: DecisionFilterControlsProps) {
  // The controls show a change at once. The address it is written to lands
  // with the list it loads, and until then a controlled box would spring back.
  // No statuses (another tab) and none chosen (Features) are different keys,
  // so switching tab resets what the bar shows.
  const key = `${withDecisionFilters(new URLSearchParams(), filters)}|${statuses ? `s:${statuses.join(",")}` : "-"}`;
  const [shown, setShown] = useState(filters);
  const [shownStatuses, setShownStatuses] = useState(statuses);
  const [shownFor, setShownFor] = useState(key);
  if (shownFor !== key) {
    setShownFor(key);
    setShown(filters);
    setShownStatuses(statuses);
  }
  const change = (changes: Partial<DecisionFilters>) => {
    setShown((current) => ({ ...current, ...changes }));
    onChange(changes);
  };
  const toggleStatus = (value: FeatureStatus) => {
    const current = shownStatuses ?? [];
    const next = current.includes(value)
      ? current.filter((each) => each !== value)
      : [...current, value];
    setShownStatuses(next);
    onStatuses?.(next);
  };
  const toggleWho = (value: (typeof WHO_OPTIONS)[number]["value"]) =>
    change({
      who: shown.who.includes(value)
        ? shown.who.filter((each) => each !== value)
        : [...shown.who, value],
    });
  const decisionOnly = statuses === undefined;
  // Search stays in the bar; the rest sits in the Filters popover, and each
  // one in force shows as a pill beside it that removes it.
  const filtersPopover = useDisclosure();
  const pills = activeFilterPills({
    shown,
    decisionOnly,
    shownStatuses,
    people,
    change,
    toggleWho,
    toggleStatus,
  });
  return (
    <>
      <KeywordSearch
        key={resets}
        label={decisionOnly ? "Search decisions" : "Search features"}
        value={shown.q}
        onChange={(q) => change({ q })}
      />
      <details {...filtersPopover.detailsProps} className="group relative">
        <summary
          className={cn(
            "flex cursor-pointer list-none items-center gap-1.5 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-2.5 py-1 text-base text-(--text-secondary) group-open:bg-(--surface-pressed) group-open:text-(--text-primary) hover:bg-(--surface-hover) hover:text-(--text-primary) [&::-webkit-details-marker]:hidden",
            FOCUS_RING,
          )}
        >
          <FilterIcon size={14} />
          Filters
          {pills.length > 0 ? (
            <span className="rounded-full bg-(--surface-wash) px-1.5 text-xs font-medium text-(--text-primary) tabular-nums">
              {pills.length}
            </span>
          ) : null}
          <ChevronDownIcon
            size={12}
            className="text-(--text-tertiary) transition-transform duration-150 group-open:rotate-180 motion-reduce:transition-none"
          />
        </summary>
        {/* Kept mounted while closed, so a half-typed path survives. */}
        <div className="absolute left-0 z-20 mt-1 grid w-[min(34rem,calc(100vw-2rem))] grid-cols-[auto_minmax(0,1fr)] items-center gap-x-4 gap-y-3 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) p-4 shadow-(--shadow-overlay)">
          <FilterRowLabel>File</FilterRowLabel>
          <FileFilter files={files} value={shown.file} onChange={(file) => change({ file })} />
          <FilterRowLabel>Author</FilterRowLabel>
          <PersonSelect
            people={people}
            value={shown.person}
            onChange={(person) => change({ person })}
          />
          {decisionOnly ? (
            <>
              <FilterRowLabel>Category</FilterRowLabel>
              <div>
                <CategoryPicker
                  selected={shown.categories}
                  onChange={(categories) => change({ categories })}
                />
              </div>
              <FilterRowLabel>Decided by</FilterRowLabel>
              <div
                role="group"
                aria-label="Decided by"
                className={cn(SEGMENTED_TRACK, "justify-self-start")}
              >
                {WHO_OPTIONS.map((option) => {
                  const pressed = shown.who.includes(option.value);
                  // Hiding agent-only decisions and asking for them cannot both hold.
                  const blocked = option.value === "agent" && shown.hideAgent;
                  return (
                    <button
                      key={option.value}
                      type="button"
                      aria-pressed={pressed}
                      disabled={blocked}
                      title={blocked ? "Agent-only decisions are hidden" : undefined}
                      onClick={() => toggleWho(option.value)}
                      className={cn(
                        segmentedTileClass(pressed),
                        "disabled:cursor-not-allowed disabled:opacity-50",
                      )}
                    >
                      {pressed ? <CheckIcon size={12} /> : null}
                      {option.label}
                    </button>
                  );
                })}
              </div>
            </>
          ) : null}
          {/* Offered by the tab, whatever was last shown. */}
          {statuses && shownStatuses ? (
            <>
              <FilterRowLabel>Status</FilterRowLabel>
              <div
                role="group"
                aria-label="Feature status"
                className={cn(SEGMENTED_TRACK, "justify-self-start")}
              >
                {FEATURE_STATUSES.map((option) => {
                  const pressed = shownStatuses.includes(option.value);
                  return (
                    <button
                      key={option.value}
                      type="button"
                      aria-pressed={pressed}
                      onClick={() => toggleStatus(option.value)}
                      className={segmentedTileClass(pressed)}
                    >
                      {pressed ? <CheckIcon size={12} /> : null}
                      {option.label}
                    </button>
                  );
                })}
              </div>
            </>
          ) : null}
          <FilterRowLabel>Dates</FilterRowLabel>
          <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Date range">
            <label className="flex items-center gap-1.5 text-base text-(--text-tertiary)">
              From
              <input
                type="date"
                value={shown.from ?? ""}
                max={shown.to ?? undefined}
                onChange={(event) => change({ from: event.target.value || null })}
                className={FIELD}
              />
            </label>
            <label className="flex items-center gap-1.5 text-base text-(--text-tertiary)">
              To
              <input
                type="date"
                value={shown.to ?? ""}
                min={shown.from ?? undefined}
                onChange={(event) => change({ to: event.target.value || null })}
                className={FIELD}
              />
            </label>
          </div>
          {decisionOnly ? (
            <>
              <FilterRowLabel>Show</FilterRowLabel>
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
                <label className="flex cursor-pointer items-center gap-1.5 text-base text-(--text-secondary)">
                  <input
                    type="checkbox"
                    checked={shown.superseded}
                    onChange={(event) => change({ superseded: event.target.checked })}
                  />
                  Superseded
                </label>
                <label className="flex cursor-pointer items-center gap-1.5 text-base text-(--text-secondary)">
                  <input
                    type="checkbox"
                    checked={shown.hideAgent}
                    onChange={(event) =>
                      change(
                        event.target.checked
                          ? {
                              hideAgent: true,
                              who: shown.who.filter((value) => value !== "agent"),
                            }
                          : { hideAgent: false },
                      )
                    }
                  />
                  Hide agent-only
                </label>
              </div>
            </>
          ) : null}
        </div>
      </details>
      {pills.length > 0 ? (
        <ul aria-label="Active filters" className="flex flex-wrap items-center gap-1.5">
          {pills.map((pill) => (
            <li key={pill.key}>
              <button
                type="button"
                onClick={pill.remove}
                aria-label={`Remove filter: ${pill.label}`}
                title="Remove this filter"
                className={cn(
                  "inline-flex max-w-72 cursor-pointer items-center gap-1 rounded-full border border-(--border-default) bg-(--surface-wash) py-0.5 pr-1.5 pl-2.5 text-xs text-(--text-primary) hover:bg-(--surface-hover)",
                  FOCUS_RING,
                )}
              >
                <span className="truncate">{pill.label}</span>
                <CloseIcon size={10} className="flex-none text-(--text-tertiary)" />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      {(decisionOnly ? hasDecisionFilters(shown) : hasFeatureFilters(shown)) ||
      (shownStatuses?.length ?? 0) > 0 ? (
        <Button variant="ghost" size="tiny" onClick={onClear}>
          Clear filters
        </Button>
      ) : null}
    </>
  );
}

function FilterRowLabel({ children }: { children: ReactNode }) {
  return <span className="text-base text-(--text-tertiary)">{children}</span>;
}

type FilterPill = { key: string; label: string; remove: () => void };

/** One pill per filter in force that the popover holds, the keyword aside: it
 *  stays visible in its own box. */
function activeFilterPills({
  shown,
  decisionOnly,
  shownStatuses,
  people,
  change,
  toggleWho,
  toggleStatus,
}: {
  shown: DecisionFilters;
  /** Whether the filters that pick decisions alone apply. */
  decisionOnly: boolean;
  shownStatuses: FeatureStatus[] | undefined;
  people: AuthorCount[];
  change: (changes: Partial<DecisionFilters>) => void;
  toggleWho: (value: (typeof WHO_OPTIONS)[number]["value"]) => void;
  toggleStatus: (value: FeatureStatus) => void;
}): FilterPill[] {
  const pills: FilterPill[] = [];
  if (shown.file) {
    pills.push({ key: "file", label: `File: ${shown.file}`, remove: () => change({ file: "" }) });
  }
  if (shown.person) {
    const person = people.find(
      (candidate) => candidate.email.toLowerCase() === shown.person.toLowerCase(),
    );
    pills.push({
      key: "person",
      label: `Author: ${person ? personLabel(person) : shown.person}`,
      remove: () => change({ person: "" }),
    });
  }
  for (const category of decisionOnly ? shown.categories : []) {
    pills.push({
      key: `category:${category}`,
      label: humanize(category),
      remove: () => change({ categories: shown.categories.filter((each) => each !== category) }),
    });
  }
  for (const option of WHO_OPTIONS) {
    if (!decisionOnly || !shown.who.includes(option.value)) continue;
    pills.push({
      key: `who:${option.value}`,
      label: `Decided by ${option.label.toLowerCase()}`,
      remove: () => toggleWho(option.value),
    });
  }
  for (const option of FEATURE_STATUSES) {
    if (!shownStatuses?.includes(option.value)) continue;
    pills.push({
      key: `status:${option.value}`,
      label: option.label,
      remove: () => toggleStatus(option.value),
    });
  }
  if (shown.from) {
    pills.push({ key: "from", label: `From ${shown.from}`, remove: () => change({ from: null }) });
  }
  if (shown.to) {
    pills.push({ key: "to", label: `To ${shown.to}`, remove: () => change({ to: null }) });
  }
  if (decisionOnly && shown.superseded) {
    pills.push({
      key: "superseded",
      label: "Superseded shown",
      remove: () => change({ superseded: false }),
    });
  }
  if (decisionOnly && shown.hideAgent) {
    pills.push({
      key: "hide-agent",
      label: "Agent-only hidden",
      remove: () => change({ hideAgent: false }),
    });
  }
  return pills;
}

/** Applied a moment after typing stops, so a word is one request, not one per
 *  letter. A value arriving from outside replaces the text unless it is the
 *  one this box just sent. */
function KeywordSearch({
  label,
  value,
  onChange,
}: {
  /** What is searched, as the box's label and placeholder. */
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const id = useId();
  const [text, setText] = useState(value);
  const sent = useRef(value);
  const latest = useRef(onChange);
  latest.current = onChange;
  useEffect(() => {
    if (value !== sent.current) {
      sent.current = value;
      setText(value);
    }
  }, [value]);
  useEffect(() => {
    if (text.trim() === sent.current) return;
    const timer = setTimeout(() => {
      sent.current = text.trim();
      latest.current(text.trim());
    }, SEARCH_DELAY_MS);
    return () => clearTimeout(timer);
  }, [text]);
  return (
    <div className="w-full sm:w-56">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <input
        id={id}
        type="search"
        value={text}
        placeholder={label}
        autoComplete="off"
        onChange={(event) => setText(event.target.value)}
        className={cn(FIELD, "w-full placeholder:text-(--text-tertiary)")}
      />
    </div>
  );
}

/** A path prefix, with the files and folders that have decisions suggested as
 *  the reader types. */
function FileFilter({
  files: suggest,
  value,
  onChange,
}: {
  files: (prefix: string, signal?: AbortSignal) => Promise<DecisionFile[]>;
  value: string;
  onChange: (value: string) => void;
}) {
  const [prefix, setPrefix] = useState<string | null>(null);
  const [files, setFiles] = useState<DecisionFile[]>([]);
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    if (prefix === null) return;
    const controller = new AbortController();
    setLoading(true);
    const timer = setTimeout(() => {
      suggest(prefix, controller.signal)
        .then((found) => {
          setFiles(found);
          setLoading(false);
        })
        .catch(() => {
          if (controller.signal.aborted) return;
          setFiles([]);
          setLoading(false);
        });
    }, SUGGEST_DELAY_MS);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [suggest, prefix]);
  return (
    <Combobox
      label="File or folder"
      className="w-full sm:w-64"
      value={value}
      placeholder="File or folder"
      loading={loading}
      empty="No files with decisions"
      options={files.map((file) => ({
        value: file.path,
        label: <span className="font-mono">{file.path}</span>,
        detail: formatCount(file.decision_count),
      }))}
      onQuery={setPrefix}
      onChange={onChange}
    />
  );
}

/** How many decisions, or on Features how many features, the author has. */
function authorCount(person: AuthorCount): number {
  return "feature_count" in person ? person.feature_count : person.decision_count;
}

function PersonSelect({
  people,
  value,
  onChange,
}: {
  people: AuthorCount[];
  value: string;
  onChange: (value: string) => void;
}) {
  // A person from a shared link who has no decisions or features any more
  // still reads as the filter in force.
  const listed = people.find((person) => person.email.toLowerCase() === value.toLowerCase());
  return (
    <select
      aria-label="Author"
      value={listed?.email ?? value}
      onChange={(event) => onChange(event.target.value)}
      className={cn(FIELD, "max-w-56 cursor-pointer")}
    >
      <option value="">Any author</option>
      {!listed && value ? <option value={value}>{value}</option> : null}
      {people.map((person) => (
        <option key={person.email} value={person.email}>
          {personLabel(person)} ({formatCount(authorCount(person))})
        </option>
      ))}
    </select>
  );
}

function CategoryPicker({
  selected,
  onChange,
}: {
  selected: DecisionCategory[];
  onChange: (categories: DecisionCategory[]) => void;
}) {
  const { detailsProps } = useDisclosure();
  const label =
    selected.length === 0
      ? "All categories"
      : selected.length === 1
        ? humanize(selected[0]!)
        : `${selected.length} categories`;
  return (
    <details {...detailsProps} className="group relative">
      <summary
        className={cn(
          "flex cursor-pointer list-none items-center gap-1.5 rounded-(--radius-sm) border px-2.5 py-1 text-base [&::-webkit-details-marker]:hidden",
          FOCUS_RING,
          selected.length === 0
            ? "border-(--border-default) bg-(--surface-card) text-(--text-secondary) hover:bg-(--surface-hover)"
            : "border-(--action-accent) bg-(--surface-pressed) font-medium text-(--text-primary)",
        )}
      >
        <span className="sr-only">Category: </span>
        {label}
        <ChevronDownIcon
          size={12}
          className="flex-none text-(--text-tertiary) transition-transform duration-150 group-open:rotate-180"
        />
      </summary>
      <fieldset className="absolute left-0 z-20 mt-1 w-56 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) p-1 shadow-(--shadow-overlay)">
        <legend className="sr-only">Categories</legend>
        {CATEGORIES.map((category) => (
          <label
            key={category}
            className="flex cursor-pointer items-center gap-2 rounded-(--radius-sm) px-2 py-1.5 text-base hover:bg-(--surface-hover)"
          >
            <input
              type="checkbox"
              checked={selected.includes(category)}
              onChange={() =>
                onChange(
                  selected.includes(category)
                    ? selected.filter((each) => each !== category)
                    : [...selected, category],
                )
              }
            />
            <CategoryTag category={category} />
          </label>
        ))}
      </fieldset>
    </details>
  );
}
