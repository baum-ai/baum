import { cn } from "../classnames";
import { CheckIcon } from "../icons";
import { Badge } from "../ui";

import { workflowDefinition } from "./catalogue";
import type { CodeDiffFile } from "./code-diff";
import type { TileSourceFile } from "./tile-code-links";
import { TileShell } from "./tile-frame";
import { TileQuestions } from "./tile-questions";

export type WorkflowStep = {
  id: string;
  label: string;
  kind: "start" | "active" | "terminal" | "failure";
  change?: "add";
};

export type WorkflowChange = {
  primaryPath: readonly WorkflowStep[];
  failurePath: readonly WorkflowStep[];
  newTransition: string;
  retry: string;
  terminalState: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

type Lane = "primary" | "failure";

/** A lifecycle change read as movement: the path taken when everything works,
 *  and the one taken when something goes wrong, each as numbered steps on a
 *  line. The markers say what a step is — where it starts, where it fails,
 *  where it ends — and the questions beneath say what changed about the flow,
 *  in the same form as the API tile. */
export function WorkflowTile({ change }: { change: WorkflowChange }) {
  const hasFailure = change.failurePath.length > 0;
  const added = [...change.primaryPath, ...change.failurePath].filter(
    (step) => step.change === "add",
  ).length;

  return (
    <TileShell
      title={workflowDefinition.title}
      source={{ code: change.code, codeDiff: change.codeDiff }}
    >
      <section
        aria-label="Workflow paths"
        className="mt-4 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
      >
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2">
          <h5 className="text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
            How it flows
          </h5>
          <Legend hasFailure={hasFailure} added={added} />
        </div>
        <WorkflowPath
          lane="primary"
          label="Main path"
          caption="When everything works"
          steps={change.primaryPath}
        />
        {hasFailure ? (
          <WorkflowPath
            lane="failure"
            label="Failure path"
            caption="When something goes wrong"
            steps={change.failurePath}
          />
        ) : null}
      </section>
      <TileQuestions
        title="What changes in the flow"
        rows={[
          { question: "What is new", answer: change.newTransition },
          { question: "If it fails, what then?", answer: change.retry },
          { question: "How it can end", answer: change.terminalState },
        ]}
      />
    </TileShell>
  );
}

function WorkflowPath({
  lane,
  label,
  caption,
  steps,
}: {
  lane: Lane;
  label: string;
  caption: string;
  steps: readonly WorkflowStep[];
}) {
  return (
    <section
      aria-label={label}
      className="border-t border-(--border-subtle) px-3 py-3 first-of-type:border-t-0"
    >
      <div className="flex flex-wrap items-baseline gap-x-2">
        <h6 className="text-base font-medium text-(--text-primary)">{label}</h6>
        <span className="text-xs text-(--text-tertiary)">{caption}</span>
      </div>

      {/* Across on wide screens: markers on one line, labels beneath. */}
      <ol
        className="mt-3 hidden sm:grid"
        style={{ gridTemplateColumns: `repeat(${steps.length}, minmax(0, 1fr))` }}
      >
        {steps.map((step, index) => (
          <li key={`${index}:${step.id}`} className="flex min-w-0 flex-col items-center">
            <div className="flex w-full items-center">
              <Connector lane={lane} hidden={index === 0} />
              <StepMarker step={step} lane={lane} number={index + 1} />
              <Connector lane={lane} hidden={index === steps.length - 1} />
            </div>
            <StepLabel step={step} className="mt-2 px-2 text-center" />
          </li>
        ))}
      </ol>

      {/* Down on narrow screens: markers on a vertical line, labels beside. */}
      <ol className="mt-3 grid gap-0 sm:hidden">
        {steps.map((step, index) => (
          <li key={`${index}:${step.id}`} className="flex min-w-0 gap-3">
            <div className="flex flex-col items-center">
              <StepMarker step={step} lane={lane} number={index + 1} />
              {index < steps.length - 1 ? (
                <span
                  aria-hidden
                  className={cn(
                    "min-h-4 w-0 flex-1 border-l",
                    lane === "failure"
                      ? "border-dashed border-(--status-critical)/50"
                      : "border-(--border-strong)",
                  )}
                />
              ) : null}
            </div>
            <StepLabel step={step} className="pb-3 pt-0.5" />
          </li>
        ))}
      </ol>
    </section>
  );
}

function Connector({ lane, hidden }: { lane: Lane; hidden: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "h-0 flex-1 border-t",
        hidden
          ? "border-transparent"
          : lane === "failure"
            ? "border-dashed border-(--status-critical)/50"
            : "border-(--border-strong)",
      )}
    />
  );
}

/** The marker says what the step is, so colour is only ever meaning: a tick
 *  where the main path succeeds, an exclamation where the failure begins, a
 *  filled square where a failure path stops; every other step is its number. */
function StepMarker({ step, lane, number }: { step: WorkflowStep; lane: Lane; number: number }) {
  const ending = step.kind === "terminal";
  const successEnd = ending && lane === "primary";
  const failureEnd = ending && lane === "failure";
  const trigger = step.kind === "failure";
  const described = successEnd
    ? "Succeeds"
    : failureEnd
      ? "Stops"
      : trigger
        ? "Fails"
        : step.kind === "start"
          ? "Starts"
          : `Step ${number}`;

  return (
    <span
      title={described}
      className={cn(
        "flex h-7 w-7 flex-none items-center justify-center rounded-full border text-xs font-semibold",
        successEnd
          ? "border-(--status-positive) bg-(--status-positive-bg) text-(--status-positive)"
          : trigger
            ? "border-(--status-critical) bg-(--status-critical-bg) text-(--status-critical)"
            : failureEnd
              ? "border-(--text-secondary) bg-(--text-secondary) text-(--surface-card)"
              : step.kind === "start"
                ? "border-(--text-secondary) bg-(--surface-card) text-(--text-primary)"
                : "border-(--border-default) bg-(--surface-wash) text-(--text-secondary)",
      )}
    >
      <span className="sr-only">{described}: </span>
      {successEnd ? (
        <CheckIcon size={14} />
      ) : trigger ? (
        <span aria-hidden>!</span>
      ) : failureEnd ? (
        <span aria-hidden className="h-2 w-2 rounded-[2px] bg-(--surface-card)" />
      ) : (
        <span aria-hidden>{number}</span>
      )}
    </span>
  );
}

function StepLabel({ step, className }: { step: WorkflowStep; className?: string }) {
  return (
    <span className={cn("flex min-w-0 flex-col gap-1 sm:items-center", className)}>
      <span className="text-xs font-medium leading-[1.35] text-(--text-primary)">{step.label}</span>
      {step.change === "add" ? (
        <Badge tone="positive" size="compact">
          New
        </Badge>
      ) : null}
    </span>
  );
}

/** Names each marker once, and only those this tile can show. */
function Legend({ hasFailure, added }: { hasFailure: boolean; added: number }) {
  const item = "flex items-center gap-1.5";
  const dot =
    "flex h-3.5 w-3.5 items-center justify-center rounded-full border text-3xs leading-none font-bold";
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-3xs font-semibold tracking-[0.08em] uppercase text-(--text-tertiary)">
      <span className={item}>
        <span
          aria-hidden
          className={cn(
            dot,
            "border-(--status-positive) bg-(--status-positive-bg) text-(--status-positive)",
          )}
        >
          <CheckIcon size={9} />
        </span>
        Succeeds
      </span>
      {hasFailure ? (
        <>
          <span className={item}>
            <span
              aria-hidden
              className={cn(
                dot,
                "border-(--status-critical) bg-(--status-critical-bg) text-(--status-critical)",
              )}
            >
              !
            </span>
            Fails
          </span>
          <span className={item}>
            <span
              aria-hidden
              className={cn(dot, "border-(--text-secondary) bg-(--text-secondary)")}
            >
              <span className="h-1 w-1 rounded-[1px] bg-(--surface-card)" />
            </span>
            Stops
          </span>
        </>
      ) : null}
      {added > 0 ? (
        <span>
          {added} new {added === 1 ? "step" : "steps"}
        </span>
      ) : null}
    </p>
  );
}
