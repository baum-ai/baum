import { cn } from "../classnames";
import { Badge } from "../ui";

import { fieldChange } from "./field-change";
import { apiContractDefinition } from "./catalogue";
import { TileShell } from "./tile-frame";
import { type TileQuestion, TileQuestions } from "./tile-questions";
import type { CodeDiffFile } from "./code-diff";
import type { TileSourceFile } from "./tile-code-links";

export type ApiField = {
  name: string;
  type: string;
  requirement: "required" | "optional";
  change?: "add" | "remove" | "alter" | "rename";
};

export type ApiOperation = {
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD" | "OPTIONS";
  path: string;
  request: readonly ApiField[];
  response: readonly ApiField[];
  responseStatus?: string;
  breakingChange: string;
  compatibility: string;
  operations: string;
  rollout: string;
  code: readonly TileSourceFile[];
  codeDiff: readonly CodeDiffFile[];
};

const methodTone: Record<ApiOperation["method"], "info" | "positive" | "caution" | "critical"> = {
  GET: "info",
  HEAD: "info",
  OPTIONS: "info",
  POST: "positive",
  PUT: "caution",
  PATCH: "caution",
  DELETE: "critical",
};

/** A canonical, caller-first view of an HTTP request/response contract change. */
export function ApiContractTile({ operation }: { operation: ApiOperation }) {
  const newFields = [...operation.request, ...operation.response].filter(
    (field) => field.change === "add",
  );

  return (
    <TileShell
      title={apiContractDefinition.title}
      source={{ code: operation.code, codeDiff: operation.codeDiff }}
    >
      <section
        aria-label="API call flow"
        className="mt-4 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
      >
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Badge tone={methodTone[operation.method]} size="compact">
              {operation.method}
            </Badge>
            <code className="min-w-0 break-all text-base font-semibold text-(--text-primary)">
              {operation.path}
            </code>
          </div>
          {newFields.length > 0 && (
            <span className="text-xs text-(--text-tertiary)">
              {newFields.length} new {newFields.length === 1 ? "field" : "fields"}
            </span>
          )}
        </div>
        <div className="grid items-stretch gap-3 p-3 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)]">
          <ApiShape label="Client sends" fields={operation.request} />
          <div
            aria-hidden
            className="flex items-center justify-center text-lg font-medium text-(--text-tertiary) sm:px-1"
          >
            →
          </div>
          <ApiShape
            label="Client receives"
            fields={operation.response}
            status={operation.responseStatus}
          />
        </div>
      </section>
      <CallerImpact operation={operation} />
    </TileShell>
  );
}

function ApiShape({
  label,
  fields,
  status,
}: {
  label: string;
  fields: readonly ApiField[];
  status?: string;
}) {
  return (
    <article className="overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)">
      <h5 className="border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2 text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        {label}
      </h5>
      {status && (
        <p className="px-3 pt-3 font-mono text-base font-medium text-(--text-primary)">{status}</p>
      )}
      {fields.length === 0 && (
        <p className="px-3 py-3 text-xs text-(--text-secondary)">
          {status ? "No response body." : "No fields."}
        </p>
      )}
      <ul className="divide-y divide-(--border-subtle)">
        {fields.map((field, index) => (
          <li
            key={`${index}:${field.name}`}
            className="flex min-w-0 flex-wrap items-center gap-2 px-3 py-1.5 text-xs"
          >
            <span
              className={cn(
                "min-w-0 flex-1 truncate font-mono text-(--text-primary)",
                field.change === "remove" && "line-through",
              )}
            >
              {field.name}
            </span>
            <span className="min-w-0 break-words font-mono text-(--text-tertiary)">
              {field.type}
            </span>
            <span className="flex-none text-3xs font-semibold tracking-[0.08em] text-(--text-tertiary)">
              {field.requirement}
            </span>
            {field.change && (
              <Badge size="compact" tone={fieldChange[field.change].tone}>
                {fieldChange[field.change].label}
              </Badge>
            )}
          </li>
        ))}
      </ul>
    </article>
  );
}

type BreakingVerdict = "breaking" | "not-breaking" | null;

/** Wording that already states its own negation ("not breaking", "no
 *  breaking changes"); the NEGATION guard below must not apply to these, or
 *  it would cancel the very negation the pattern requires. */
const NOT_BREAKING_EXPLICIT = [
  /\bnon[- ]?breaking\b/i,
  /\bnot (a )?breaking\b/i,
  /\bno breaking\b/i,
  /\bbreaking( changes?)?\s*[:\u2013\u2014-]\s*(none|no)\b/i,
  /\bnothing (breaks|is broken)\b/i,
];

/** A bare claim of safety with no negation of its own, so "not backwards
 *  compatible" must not read as "backwards compatible": these need the
 *  NEGATION guard below just like the "breaking" check does. */
const NOT_BREAKING_AFFIRMATIVE = [
  /\bbackwards?[- ]compatible\b/i,
  /\badditive\b/i,
  /\bunaffected\b/i,
  /^\s*new (read |write )?endpoint\b/i,
];

/** Any negation in the same sentence means "break" may be the thing denied. */
const NEGATION = /\b(not|no|none|nothing|never|without|cannot|won't|doesn't|isn't)\b/i;

/** A yes/no answer to "will existing callers break?", given only when it is
 *  certain. The review's own wording decides first: an explicit "not
 *  breaking" wins even over a removed field — an optional legacy field the
 *  server ignores is removed without breaking anyone — and an explicit
 *  "breaks" wins likewise. Only when the wording commits to neither does a
 *  removed or renamed field decide it; otherwise there is no badge rather
 *  than a guess. */
function breakingVerdict(operation: ApiOperation): BreakingVerdict {
  const text = operation.breakingChange;
  if (NOT_BREAKING_EXPLICIT.some((pattern) => pattern.test(text))) return "not-breaking";
  if (!NEGATION.test(text) && NOT_BREAKING_AFFIRMATIVE.some((pattern) => pattern.test(text))) {
    return "not-breaking";
  }
  if (/\bbreak(s|ing)?\b/i.test(text) && !NEGATION.test(text)) return "breaking";
  const fields = [...operation.request, ...operation.response];
  if (fields.some((field) => field.change === "remove" || field.change === "rename")) {
    return "breaking";
  }
  return null;
}

/** The four things a reviewer asks of an endpoint change, as plain questions
 *  with the review's answers beneath: does it break anyone, who may call it,
 *  what callers will see, and what has to be true before it ships. */
function CallerImpact({ operation }: { operation: ApiOperation }) {
  const verdict = breakingVerdict(operation);
  const rows: TileQuestion[] = [
    {
      question: "Will existing callers break?",
      answer: operation.breakingChange,
      badge:
        verdict === "breaking" ? (
          <Badge tone="critical" size="compact">
            Breaking
          </Badge>
        ) : verdict === "not-breaking" ? (
          <Badge tone="positive" size="compact">
            Not breaking
          </Badge>
        ) : undefined,
    },
    { question: "Who can call it?", answer: operation.compatibility },
    { question: "What callers will see", answer: operation.operations },
    { question: "Before it ships", answer: operation.rollout },
  ];

  return <TileQuestions title="What this means for callers" rows={rows} />;
}
