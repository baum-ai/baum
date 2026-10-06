import type { ReactNode } from "react";

import { cn } from "../classnames";
import { Badge } from "../ui";

import {
  dataMigrationDefinition,
  externalIntegrationDefinition,
  searchSchemaDefinition,
} from "./catalogue";
import type { CodeDiffFile } from "./code-diff";
import { TileShell } from "./tile-frame";
import type { TileSourceFile } from "./tile-code-links";
import type { TileKeyFact } from "./tile-key-facts";

type TileBase = {
  facts: readonly TileKeyFact[];
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

export type DataMigrationChange = TileBase & {
  source: string;
  beforeFields: readonly string[];
  target: string;
  records: string;
  transform: readonly string[];
};

export type SearchSchemaChange = TileBase & {
  index: string;
  fields: readonly { name: string; state: "existing" | "new" | "changed" | "removed" }[];
  queryBefore: string;
  queryAfter: string;
};

export type ExternalIntegrationChange = TileBase & {
  source: string;
  provider: string;
  request: string;
  response: string;
  fallback: string;
  fallbackTarget: string;
};

export function DataMigrationTile({ change }: { change: DataMigrationChange }) {
  return (
    <Tile
      title={dataMigrationDefinition.title}
      facts={change.facts}
      code={change.code}
      codeDiff={change.codeDiff}
    >
      <Frame label="Record transformation">
        <div className="grid items-stretch gap-2 sm:grid-cols-[1fr_auto_1fr] sm:gap-3">
          <RecordCard label="Before" name={change.source} values={change.beforeFields} />
          <Arrow />
          <RecordCard label="After" name={change.target} values={change.transform} added />
        </div>
        <div className="mt-3 rounded-(--radius-sm) bg-(--surface-wash) px-3 py-2 text-xs text-(--text-secondary)">
          <span className="font-semibold text-(--text-primary)">{change.records}</span> records to
          transform
        </div>
      </Frame>
    </Tile>
  );
}

export function SearchSchemaTile({ change }: { change: SearchSchemaChange }) {
  return (
    <Tile
      title={searchSchemaDefinition.title}
      facts={change.facts}
      code={change.code}
      codeDiff={change.codeDiff}
    >
      <Frame label="Index and query">
        <div className="rounded-(--radius-sm) border border-(--border-subtle) bg-(--surface-wash) px-3 py-2">
          <span className="font-mono text-xs font-semibold text-(--text-primary)">
            {change.index}
          </span>
          <div className="mt-2 flex flex-wrap gap-1.5">
            {change.fields.map((field, index) => (
              <Pill key={`${index}:${field.name}`} added={field.state === "new"}>
                {field.name}
                {field.state === "changed"
                  ? " (changed)"
                  : field.state === "removed"
                    ? " (removed)"
                    : ""}
              </Pill>
            ))}
          </div>
        </div>
        <div className="mt-3 grid gap-2 sm:grid-cols-2">
          <Query label="Before" value={change.queryBefore} />
          <Query label="After" value={change.queryAfter} />
        </div>
      </Frame>
    </Tile>
  );
}

export function ExternalIntegrationTile({ change }: { change: ExternalIntegrationChange }) {
  return (
    <Tile
      title={externalIntegrationDefinition.title}
      facts={change.facts}
      code={change.code}
      codeDiff={change.codeDiff}
    >
      <Frame label="Runtime sequence">
        <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 rounded-(--radius-sm) border border-(--border-subtle) px-3 py-2 text-xs font-semibold tracking-[0.08em] uppercase text-(--text-tertiary)">
          <span className="truncate text-right">{change.source}</span>
          <span aria-hidden>↔</span>
          <span className="truncate">{change.provider}</span>
        </div>
        <ol className="mt-2 overflow-hidden rounded-(--radius-sm) border border-(--border-subtle)">
          <SequenceStep from={change.source} to={change.provider} message={change.request} added />
          <SequenceStep from={change.provider} to={change.source} message={change.response} />
          <SequenceStep
            from={change.source}
            to={change.fallbackTarget}
            message={change.fallback}
            caution
          />
        </ol>
      </Frame>
    </Tile>
  );
}

function Tile({
  title,
  facts,
  code,
  codeDiff,
  children,
}: TileBase & { title: string; children: ReactNode }) {
  return (
    <TileShell title={title} facts={facts} source={{ code, codeDiff }}>
      <div className="mt-4">{children}</div>
    </TileShell>
  );
}

function Frame({ label, children }: { label: string; children: ReactNode }) {
  return (
    <section
      aria-label={label}
      className="rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) p-3"
    >
      <h5 className="mb-2 text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        {label}
      </h5>
      {children}
    </section>
  );
}

function RecordCard({
  label,
  name,
  values,
  added = false,
}: {
  label: string;
  name: string;
  values: readonly string[];
  added?: boolean;
}) {
  return (
    <section className="rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) p-3">
      <span className="text-3xs font-semibold tracking-[0.1em] uppercase text-(--text-tertiary)">
        {label}
      </span>
      <span className="mt-1 block font-mono text-xs font-semibold text-(--text-primary)">
        {name}
      </span>
      <ul className="mt-2 space-y-1">
        {values.map((value, index) => (
          <li
            key={`${index}:${value}`}
            className="flex gap-1.5 font-mono text-xs text-(--text-secondary)"
          >
            {added ? (
              <span aria-hidden className="flex-none font-semibold text-(--status-positive)">
                +
              </span>
            ) : null}
            <span className="min-w-0">{value}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Query({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-3 py-2">
      <span className="text-3xs font-semibold tracking-[0.1em] uppercase text-(--text-tertiary)">
        {label}
      </span>
      <span className="mt-1 block break-words text-xs text-(--text-primary)">{value}</span>
    </div>
  );
}

function SequenceStep({
  from,
  to,
  message,
  added = false,
  caution = false,
}: {
  from: string;
  to: string;
  message: string;
  added?: boolean;
  caution?: boolean;
}) {
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 border-b border-(--border-subtle) px-3 py-2 last:border-b-0">
      <span className="truncate text-right text-xs font-medium text-(--text-primary)">{from}</span>
      <span aria-hidden className="text-(--text-tertiary)">
        →
      </span>
      <span className="min-w-0 text-xs text-(--text-secondary)">
        <span className="font-medium text-(--text-primary)">{to}</span> · {message}
        {added ? (
          <Badge tone="positive" size="compact" className="ml-1.5">
            New
          </Badge>
        ) : caution ? (
          <Badge size="compact" className="ml-1.5">
            Fallback
          </Badge>
        ) : null}
      </span>
    </li>
  );
}

function Pill({ children, added = false }: { children: ReactNode; added?: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border border-(--border-default) bg-(--surface-wash) px-2 py-0.5 font-mono text-xs",
        added ? "text-(--text-primary)" : "text-(--text-secondary)",
      )}
    >
      {added ? (
        <span aria-hidden className="font-semibold text-(--status-positive)">
          +
        </span>
      ) : null}
      {children}
    </span>
  );
}
function Arrow() {
  return (
    <span aria-hidden className="flex-none text-center text-lg text-(--text-tertiary)">
      →
    </span>
  );
}
