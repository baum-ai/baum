import type { ReactNode } from "react";

/** Splits review prose into the separate points it makes, so a run of
 *  "403 for readers; 404 for …; 409 if …" reads as a list rather than a line. */
function points(text: string): string[] {
  return text
    .split(/;\s+|\.\s+(?=[A-Z0-9])/)
    .map((point) => point.trim().replace(/\.$/, ""))
    .filter((point) => point.length > 0)
    .map((point) => point.charAt(0).toUpperCase() + point.slice(1));
}

/** A leading HTTP status, told apart from any other leading number ("200
 *  rows per page") by what follows it: a condition word, a reason phrase, or
 *  nothing at all. */
const STATUS =
  /^([1-5]\d\d)(?=\s*$|\s+(?:for|if|when|on|after|unless|once|while|with|to)\b|\s*[:\u2013\u2014-]\s|\s+(?:OK|Created|Accepted|No Content|Moved|Found|Not Modified|Bad Request|Unauthorized|Forbidden|Not Found|Conflict|Gone|Too Many|Internal|Service Unavailable|Unprocessable)\b)\s*(.*)$/;
const ROUTE = /\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(\/\S+)/g;

/** One point, with an HTTP status leading it set as a code, and any
 *  `METHOD /path` inside it set in monospace, so the parts a reader acts on
 *  stand out from the prose around them. */
function Point({ text }: { text: string }) {
  const status = STATUS.exec(text);
  const body = status ? status[2]! : text;
  const parts: (string | { method: string; path: string })[] = [];
  let last = 0;
  for (const match of body.matchAll(ROUTE)) {
    parts.push(body.slice(last, match.index));
    parts.push({ method: match[1]!, path: match[2]! });
    last = (match.index ?? 0) + match[0].length;
  }
  parts.push(body.slice(last));

  return (
    <span className="flex min-w-0 items-baseline gap-2">
      {status ? (
        <code className="flex-none rounded-(--radius-sm) border border-(--border-default) bg-(--surface-wash) px-1.5 font-mono text-xs text-(--text-primary)">
          {status[1]}
        </code>
      ) : null}
      <span className="min-w-0">
        {parts.map((part, index) =>
          typeof part === "string" ? (
            part
          ) : (
            <code
              // Positions are stable for a given text, which never reorders.
              key={index}
              className="font-mono text-xs text-(--text-primary)"
            >
              {part.method} {part.path}
            </code>
          ),
        )}
      </span>
    </span>
  );
}

export type TileQuestion = { question: string; answer: string; badge?: ReactNode };

/** The facts a tile carries, as the plain questions a reviewer would ask with
 *  the review's answers beside them, each answer split into its points. */
export function TileQuestions({ title, rows }: { title: string; rows: readonly TileQuestion[] }) {
  return (
    <section
      aria-label={title}
      className="mt-4 overflow-hidden rounded-(--radius-sm) border border-(--border-default) bg-(--surface-card)"
    >
      <h5 className="border-b border-(--border-subtle) bg-(--surface-wash) px-3 py-2 text-xs font-semibold tracking-[0.12em] uppercase text-(--text-tertiary)">
        {title}
      </h5>
      <dl className="divide-y divide-(--border-subtle)">
        {rows.map((row) => {
          const list = points(row.answer);
          return (
            <div
              key={row.question}
              className="grid gap-1.5 px-3 py-2.5 sm:grid-cols-[13rem_minmax(0,1fr)] sm:gap-4"
            >
              <dt className="flex flex-wrap items-center gap-2 self-start text-base font-medium text-(--text-primary)">
                {row.question}
                {row.badge}
              </dt>
              <dd className="text-base leading-[1.5] text-(--text-secondary)">
                {list.length > 1 ? (
                  <ul className="grid gap-1">
                    {list.map((point, index) => (
                      <li key={`${index}:${point}`} className="flex gap-2">
                        <span aria-hidden className="flex-none text-(--text-tertiary)">
                          ·
                        </span>
                        <Point text={point} />
                      </li>
                    ))}
                  </ul>
                ) : (
                  <Point text={list[0] ?? row.answer} />
                )}
              </dd>
            </div>
          );
        })}
      </dl>
    </section>
  );
}
