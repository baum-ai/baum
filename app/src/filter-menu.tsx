import { useId, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { CloseIcon, FilterIcon, SearchIcon } from "@baum/viewer/components/icons";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import type { Entry } from "./api";
import {
  CHECKS,
  chips,
  inWhere,
  INVOLVEMENT,
  matchesCriteria,
  MERGES,
  NO_CRITERIA,
  ownerOf,
  REVIEWS,
  RISKS,
  toggled,
  whereFor,
  type Criteria,
} from "./pr-filter";
import "./filter-menu.css";

/** The repositories to offer, grouped by owner, both in alphabetical order. */
function tree(repositories: string[]): [string, string[]][] {
  const owners = new Map<string, { owner: string; repositories: Map<string, string> }>();
  for (const repository of repositories) {
    const owner = ownerOf(repository);
    if (!owner) continue;
    const group = owners.get(owner.toLowerCase()) ?? { owner, repositories: new Map() };
    group.repositories.set(repository.toLowerCase(), repository);
    owners.set(owner.toLowerCase(), group);
  }
  return [...owners.values()]
    .sort((a, b) => a.owner.localeCompare(b.owner))
    .map(({ owner, repositories }) => [
      owner,
      [...repositories.values()].sort((a, b) => a.localeCompare(b)),
    ]);
}

/** The filter icon and its menu, with a chip for each part in force beside
 *  it. `entries` are the page's pull requests before the filter, which the
 *  counts and the repositories offered come from. */
export function FilterMenu({
  criteria,
  onChange,
  entries,
  repositories = [],
  onSave,
  error,
  retry,
  align = "start",
}: {
  criteria: Criteria;
  onChange: (next: Criteria) => void;
  entries: Entry[];
  /** Repositories to offer even with no pull request in `entries`. */
  repositories?: string[];
  /** Offers "Save as inbox…" when given. */
  onSave?: () => void;
  /** Why the filter could not be saved, with a way to try again. */
  error?: string;
  retry?: () => void;
  /** Which edge of the button the menu lines up with. */
  align?: "start" | "end";
}) {
  const { detailsProps, close, open } = useDisclosure();
  const [search, setSearch] = useState("");
  const ids = useId();
  const [tooMany, setTooMany] = useState(false);
  const owners = tree([...repositories, ...entries.map((e) => e.pull_request.repository)]);
  const known = owners.flatMap(([, names]) => names);
  const active = chips(criteria, known);
  // What is ticked: nothing while every repository is in.
  const narrowed = criteria.owners.length > 0 || criteria.repositories.length > 0;
  const picked = narrowed ? known.filter((one) => inWhere(criteria, one)) : [];
  const pick = (next: string[]) => {
    const where = whereFor(next, known);
    setTooMany(!where);
    if (where) onChange({ ...criteria, ...where });
  };
  const count = (next: Criteria) => entries.filter((e) => matchesCriteria(next, e)).length;
  const query = search.trim().toLowerCase();
  const shown = owners
    .map(([owner, names]): [string, string[]] => [
      owner,
      owner.toLowerCase().includes(query)
        ? names
        : names.filter((name) => name.toLowerCase().includes(query)),
    ])
    .filter(([owner, names]) => names.length > 0 || owner.toLowerCase().includes(query));

  const section = <K extends "involvement" | "review" | "checks" | "merge">(
    field: K,
    title: string,
    options: [Criteria[K][number], string][],
  ) => (
    <fieldset className="filter-menu-section">
      <legend>{title}</legend>
      {options.map(([value, label]) => {
        const list = criteria[field] as string[];
        const on = list.includes(value);
        return (
          <label key={value} className="filter-menu-option">
            <input
              type="checkbox"
              checked={on}
              onChange={() => onChange({ ...criteria, [field]: toggled(list, value) })}
            />
            <span>{label}</span>
            <span className="filter-menu-count">
              {formatCount(count({ ...criteria, [field]: [value] }))}
            </span>
          </label>
        );
      })}
    </fieldset>
  );

  return (
    <div className={align === "end" ? "filter-menu-bar end" : "filter-menu-bar"}>
      <details {...detailsProps} className="filter-menu">
        <summary
          aria-label={active.length ? `Filter, ${active.length} in force` : "Filter"}
          title="Filter"
          className={active.length ? "filter-menu-button on" : "filter-menu-button"}
        >
          <FilterIcon size={14} />
          <span>Filter</span>
          {active.length ? <span className="filter-menu-badge">{active.length}</span> : null}
        </summary>
        {/* Drawn only while open: a closed menu's controls are no Tab stops. */}
        {open ? (
          <div className="filter-menu-panel" role="group" aria-label="Filter pull requests">
            <fieldset className="filter-menu-section filter-menu-where">
              <legend>Organizations and repositories</legend>
              {owners.length > 4 ? (
                <label className="filter-menu-search">
                  <SearchIcon size={13} />
                  <span className="sr-only">Find an organization or repository</span>
                  <input
                    type="search"
                    value={search}
                    placeholder="Find an organization or repository"
                    onChange={(event) => setSearch(event.target.value)}
                  />
                </label>
              ) : null}
              <div className="filter-menu-tree">
                {shown.map(([owner, names]) => {
                  const all = owners.find(([one]) => one === owner)?.[1] ?? [];
                  const on = all.filter((one) => picked.includes(one));
                  const whole = on.length === all.length;
                  return (
                    <div key={owner} role="group" aria-labelledby={`${ids}-${owner}`}>
                      <label className="filter-menu-option filter-menu-owner">
                        <input
                          type="checkbox"
                          checked={whole}
                          ref={(box) => {
                            if (box) box.indeterminate = on.length > 0 && !whole;
                          }}
                          onChange={() =>
                            pick(
                              whole
                                ? picked.filter((one) => !all.includes(one))
                                : [...picked.filter((one) => !all.includes(one)), ...all],
                            )
                          }
                        />
                        <span id={`${ids}-${owner}`}>{owner}</span>
                        <span className="filter-menu-count">
                          {formatCount(count({ ...criteria, owners: [owner], repositories: [] }))}
                        </span>
                      </label>
                      {names.map((name) => {
                        const ticked = picked.includes(name);
                        return (
                          <label key={name} className="filter-menu-option filter-menu-repository">
                            <input
                              type="checkbox"
                              checked={ticked}
                              onChange={() =>
                                pick(
                                  ticked ? picked.filter((one) => one !== name) : [...picked, name],
                                )
                              }
                            />
                            <span>{name.split("/").pop()}</span>
                            <span className="filter-menu-count">
                              {formatCount(
                                count({
                                  ...criteria,
                                  owners: [],
                                  repositories: [name],
                                }),
                              )}
                            </span>
                          </label>
                        );
                      })}
                    </div>
                  );
                })}
                {shown.length === 0 ? (
                  <p className="filter-menu-empty">
                    {owners.length ? "Nothing matches." : "No repositories yet."}
                  </p>
                ) : null}
              </div>
              {tooMany ? (
                <p className="filter-menu-empty" role="status">
                  Pick at most 50 repositories one by one, or whole organizations.
                </p>
              ) : null}
            </fieldset>
            <div className="filter-menu-columns">
              {section("involvement", "Involvement", INVOLVEMENT)}
              {section("review", "Review", REVIEWS)}
              {section("checks", "Checks", CHECKS)}
              <fieldset className="filter-menu-section">
                <legend>State</legend>
                {MERGES.map(([value, label]) => {
                  const on = criteria.merge.includes(value);
                  return (
                    <label key={value} className="filter-menu-option">
                      <input
                        type="checkbox"
                        checked={on}
                        onChange={() =>
                          onChange({ ...criteria, merge: toggled(criteria.merge, value) })
                        }
                      />
                      <span>{label}</span>
                      <span className="filter-menu-count">
                        {formatCount(count({ ...criteria, merge: [value] }))}
                      </span>
                    </label>
                  );
                })}
                <label className="filter-menu-option">
                  <input
                    type="checkbox"
                    checked={criteria.no_drafts}
                    onChange={() => onChange({ ...criteria, no_drafts: !criteria.no_drafts })}
                  />
                  <span>Hide drafts</span>
                </label>
              </fieldset>
              <fieldset className="filter-menu-section">
                <legend>Risk</legend>
                {RISKS.map(([value, label]) => (
                  <label key={value} className="filter-menu-option">
                    <input
                      type="radio"
                      name={`${ids}-risk`}
                      checked={criteria.risk === value}
                      onChange={() => onChange({ ...criteria, risk: value })}
                    />
                    <span>{label}</span>
                    <span className="filter-menu-count">
                      {formatCount(count({ ...criteria, risk: value }))}
                    </span>
                  </label>
                ))}
              </fieldset>
            </div>
            <div className="filter-menu-foot">
              <span className="filter-menu-shown">
                {formatCount(count(criteria))} of {formatCount(entries.length)} shown
              </span>
              <button
                type="button"
                className="filter-menu-text-button"
                disabled={active.length === 0}
                onClick={() => onChange(NO_CRITERIA)}
              >
                Clear all
              </button>
              {onSave ? (
                <button
                  type="button"
                  className="filter-menu-save"
                  disabled={active.length === 0}
                  onClick={() => {
                    close();
                    onSave();
                  }}
                >
                  Save as inbox…
                </button>
              ) : null}
            </div>
          </div>
        ) : null}
      </details>
      {active.length ? (
        <ul className="filter-menu-chips" aria-label="Filters in force">
          {active.map((chip) => (
            <li key={chip.key}>
              <span>{chip.label}</span>
              <button
                type="button"
                aria-label={`Remove ${chip.label}`}
                onClick={() => onChange(chip.without)}
              >
                <CloseIcon size={11} />
              </button>
            </li>
          ))}
          <li className="filter-menu-clear">
            <button type="button" onClick={() => onChange(NO_CRITERIA)}>
              Clear all
            </button>
          </li>
        </ul>
      ) : null}
      {error ? (
        <p role="alert" className="filter-menu-error">
          {error}{" "}
          {retry ? (
            <button type="button" onClick={retry}>
              Retry
            </button>
          ) : null}
        </p>
      ) : null}
    </div>
  );
}
