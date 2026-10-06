import type { ReactNode } from "react";

import { CheckIcon } from "../icons";
import { Badge } from "../ui";

import {
  alternativeApproachDefinition,
  bugFixDefinition,
  performanceDefinition,
  refactorDefinition,
} from "./catalogue";
import type { CodeDiffFile } from "./code-diff";
import { TileGlance, TileShell } from "./tile-frame";
import type { TileSourceFile } from "./tile-code-links";

type ComparedState = {
  label: string;
  detail: string;
};

export type BugFixChange = {
  bug: string;
  fix: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

export type RefactorChange = {
  was: string;
  now: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

export type PerformanceChange = {
  overview: { headline: string; description: string };
  /** True when a reviewer raised the cost on a change that was not about speed. */
  raisedOnReview?: boolean;
  before: ComparedState;
  after: ComparedState;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

export type AlternativeChange = {
  overview: { headline: string; description: string };
  landed: ComparedState;
  proposed: ComparedState;
  whyItMatters: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

/** A defect and its correction, read as before and after: how the code
 *  behaved before this pull request on the left, what this pull request
 *  changes on the right. The header says outright that the fix is part of the
 *  pull request, so the tile cannot be read as a bug report still open. */
export function BugFixTile({ change }: { change: BugFixChange }) {
  return (
    <TileShell
      title={bugFixDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <section
        aria-label="What this pull request fixes"
        className="mt-4 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
      >
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2">
          <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
            What this pull request fixes
          </h5>
          <Badge tone="positive" size="compact">
            <CheckIcon size={11} />
            <span className="ml-1">Fixed by this PR</span>
          </Badge>
        </div>
        <div className="grid gap-px bg-(--border-subtle) sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
          <BugFixSide
            label="The bug"
            when="Before this PR"
            text={change.bug}
            mark={
              <span className="flex h-5 w-5 items-center justify-center rounded-full border border-(--status-critical) bg-(--status-critical-bg) text-3xs font-bold text-(--status-critical)">
                !
              </span>
            }
          />
          <span
            aria-hidden
            className="flex items-center justify-center bg-(--surface-card) px-1 py-1 text-(--text-tertiary)"
          >
            <span className="hidden sm:inline">→</span>
            <span className="sm:hidden">↓</span>
          </span>
          <BugFixSide
            label="The fix"
            when="In this PR"
            text={change.fix}
            mark={
              <span className="flex h-5 w-5 items-center justify-center rounded-full border border-(--status-positive) bg-(--status-positive-bg) text-(--status-positive)">
                <CheckIcon size={11} />
              </span>
            }
          />
        </div>
      </section>
    </TileShell>
  );
}

function BugFixSide({
  label,
  when,
  text,
  mark,
}: {
  label: string;
  when: string;
  text: string;
  mark: ReactNode;
}) {
  return (
    <article aria-label={label} className="min-w-0 bg-(--surface-card) px-4 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <span aria-hidden className="flex">
          {mark}
        </span>
        <span className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
          {label}
        </span>
        <span className="text-xs text-(--text-tertiary)">· {when}</span>
      </div>
      <p className="mt-2 text-base font-medium leading-[1.45] tracking-[-0.01em] text-(--text-primary)">
        {text}
      </p>
    </article>
  );
}

export function RefactorTile({ change }: { change: RefactorChange }) {
  return (
    <TileShell
      title={refactorDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <section aria-label="At a glance" className="mt-4">
        <div className="grid gap-2 sm:grid-cols-2">
          <article
            aria-label="Was"
            className="min-w-0 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) px-4 py-3.5"
          >
            <p className="text-xs font-semibold tracking-[0.14em] uppercase text-(--text-tertiary)">
              Was
            </p>
            <p className="mt-2 text-base font-medium leading-[1.4] tracking-[-0.015em] text-(--text-primary)">
              {change.was}
            </p>
          </article>
          <article
            aria-label="Now"
            className="min-w-0 rounded-(--radius-md) border border-(--border-default) bg-(--surface-card) px-4 py-3.5"
          >
            <p className="text-xs font-semibold tracking-[0.14em] uppercase text-(--text-tertiary)">
              Now
            </p>
            <p className="mt-2 text-base font-medium leading-[1.4] tracking-[-0.015em] text-(--text-primary)">
              {change.now}
            </p>
          </article>
        </div>
      </section>
    </TileShell>
  );
}

export function PerformanceTile({ change }: { change: PerformanceChange }) {
  return (
    <TileShell
      title={performanceDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <TileGlance
        headline={change.overview.headline}
        description={change.overview.description}
        badges={[
          change.raisedOnReview && (
            <Badge key="raised" size="compact">
              Raised on review
            </Badge>
          ),
        ]}
      />
      <Compare before={change.before} after={change.after} />
    </TileShell>
  );
}

export function AlternativeApproachTile({ change }: { change: AlternativeChange }) {
  return (
    <TileShell
      title={alternativeApproachDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <TileGlance headline={change.overview.headline} description={change.overview.description} />
      <div className="mt-3 grid gap-2 sm:grid-cols-2">
        <StateCard caption="Implemented" state={change.landed} />
        <StateCard caption="Proposed" state={change.proposed} />
      </div>
      <p className="mt-3 text-base leading-[1.5] text-(--text-secondary)">{change.whyItMatters}</p>
    </TileShell>
  );
}

function Compare({ before, after }: { before: ComparedState; after: ComparedState }) {
  return (
    <div className="mt-3 grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-stretch">
      <StateCard caption="Before" state={before} />
      <span aria-hidden className="hidden items-center text-(--text-tertiary) sm:flex">
        →
      </span>
      <StateCard caption="After" state={after} />
    </div>
  );
}

function StateCard({ caption, state }: { caption: string; state: ComparedState }) {
  return (
    <article className="min-w-0 rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card) px-3 py-2.5">
      <p className="text-3xs font-semibold tracking-[0.1em] uppercase text-(--text-tertiary)">
        {caption}
      </p>
      <h6 className="mt-1 text-base font-semibold text-(--text-primary)">{state.label}</h6>
      <p className="mt-1 text-xs leading-[1.45] text-(--text-secondary)">{state.detail}</p>
    </article>
  );
}
