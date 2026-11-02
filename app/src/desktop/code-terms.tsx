import type { ReactNode } from "react";

/** `text` with each occurrence of `terms`, such as a path or a command, set
 *  as code. */
export function withCode(text: string, terms: readonly (string | null | undefined)[]): ReactNode {
  const wanted = terms.filter((term): term is string => Boolean(term));
  if (wanted.length === 0) return text;
  const pattern = wanted.map((term) => term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|");
  return text
    .split(new RegExp(`(${pattern})`))
    .map((part, index) => (index % 2 ? <code key={index}>{part}</code> : part));
}
