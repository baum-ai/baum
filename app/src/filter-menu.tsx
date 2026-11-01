import { useId, useState } from "react";
import { formatCount } from "@baum/viewer/components/format";
import { CloseIcon, FilterIcon, SearchIcon } from "@baum/viewer/components/icons";
import { useDisclosure } from "@baum/viewer/components/use-disclosure";
import type { Entry } from "./api";
import {
  CHECKS,
  chips,
  INVOLVEMENT,
  matchesCriteria,
  MERGES,
  NO_CRITERIA,
  REVIEWS,
  RISKS,
  toggled,
  type Criteria,
} from "./pr-filter";
import "./filter-menu.css";

const ownerOf = (repository: string) => repository.split("/")[0] ?? "";
const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

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

/** The repository part of `criteria` after ticking or clearing an owner:
 *  ticked it is all of the owner's repositories, cleared none of them. */
function toggleOwner(criteria: Criteria, owner: string, on: boolean): Criteria {
  return {
    ...criteria,
    owners: on
      ? [...criteria.owners.filter((one) => !same(one, owner)), owner]
      : criteria.owners.filter((one) => !same(one, owner)),
    repositories: criteria.repositories.filter((one) => !same(ownerOf(one), owner)),
  };
}

/** Ticking or clearing one repository. Clearing one of an owner ticked whole
 *  keeps its other repositories; clearing an owner's last one clears it. */
function toggleRepository(
  criteria: Criteria,
  repository: string,
  siblings: string[],
  on: boolean,
): Criteria {
  const owner = ownerOf(repository);
  const own = criteria.repositories.filter((one) => same(ownerOf(one), owner));
  const whole = own.length === 0 && criteria.owners.some((one) => same(one, owner));
  const others = criteria.repositories.filter((one) => !same(ownerOf(one), owner));
  const kept = on
    ? [...own, repository]
    : (whole ? siblings : own).filter((one) => !same(one, repository));
  return {
    ...criteria,
    owners:
      kept.length === 0 ? criteria.owners.filter((one) => !same(one, owner)) : criteria.owners,
    repositories: [...others, ...kept],
  };
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
  align = "start",
}: {
  criteria: Criteria;
  onChange: (next: Criteria) => void;
  entries: Entry[];
  /** Repositories to offer even with no pull request in `entries`. */
  repositories?: string[];
  /** Offers "Save as inbox…" when given. */
  onSave?: () => void;
  /** Which edge of the button the menu lines up with. */
  align?: "start" | "end";
}) {
  const { detailsProps, close, open } = useDisclosure();
  const [search, setSearch] = useState("");
  const ids = useId();
  const active = chips(criteria);
  const owners = tree([...repositories, ...entries.map((e) => e.pull_request.repository)]);
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
                  const own = criteria.repositories.filter((one) => same(ownerOf(one), owner));
                  const whole = own.length === 0 && criteria.owners.some((one) => same(one, owner));
                  const all = owners.find(([one]) => one === owner)?.[1] ?? [];
                  return (
                    <div key={owner} role="group" aria-labelledby={`${ids}-${owner}`}>
                      <label className="filter-menu-option filter-menu-owner">
                        <input
                          type="checkbox"
                          checked={whole}
                          ref={(box) => {
                            if (box) box.indeterminate = own.length > 0;
                          }}
                          onChange={() =>
                            onChange(toggleOwner(criteria, owner, !whole && !own.length))
                          }
                        />
                        <span id={`${ids}-${owner}`}>{owner}</span>
                        <span className="filter-menu-count">
                          {formatCount(count({ ...criteria, owners: [owner], repositories: [] }))}
                        </span>
                      </label>
                      {names.map((name) => {
                        const on = whole || own.some((one) => same(one, name));
                        return (
                          <label key={name} className="filter-menu-option filter-menu-repository">
                            <input
                              type="checkbox"
                              checked={on}
                              onChange={() => onChange(toggleRepository(criteria, name, all, !on))}
                            />
                            <span>{name.slice(owner.length + 1)}</span>
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
    </div>
  );
}
