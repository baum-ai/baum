import { Fragment } from "react";
import { CloseIcon } from "@baum/viewer/components/icons";
import { queryParts } from "./pr-filter";

/** A query's conditions as chips, each taken out by its ×: what must all
 *  hold, or with an `OR` at the top the choices, with "or" between them.
 *  `empty` says what no condition means; nothing shows for a query that
 *  does not read, whose bar says why. */
export function FilterChips({
  query,
  onChange,
  empty,
}: {
  query: string;
  onChange: (next: string) => void;
  empty: string;
}) {
  const { joiner, parts } = queryParts(query);
  if (!query.trim()) return <p className="filter-chips-empty">{empty}</p>;
  if (parts.length === 0) return null;
  return (
    <ul className="filter-chips" aria-label="Conditions">
      {parts.map((part, index) => (
        <Fragment key={`${index}:${part.label}`}>
          {index > 0 && joiner === "or" ? (
            <li aria-hidden="true" className="filter-chips-or">
              or
            </li>
          ) : null}
          <li className="filter-chip">
            <span className="filter-chip-label">{part.label}</span>
            <button
              type="button"
              className="filter-chip-remove"
              aria-label={`Remove ${part.label}`}
              title="Remove"
              onClick={() => onChange(part.without)}
            >
              <CloseIcon />
            </button>
          </li>
        </Fragment>
      ))}
    </ul>
  );
}
