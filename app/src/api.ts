import { formatUSD } from "@baum/viewer/components/format";
import type { ReviewDocument } from "@baum/viewer/review-document";
export type Clone = {
  id: string;
  path: string;
  repository: string;
  available: boolean;
  settings: {
    automatic: boolean;
    daily_cap: number | null;
    backend: string | null;
    model: string | null;
  };
};
export type PR = {
  repository: string;
  number: number;
  head_sha: string;
  title: string;
  url: string;
  state: string;
  draft: boolean;
  fork: boolean;
  authored_by_me: boolean;
  review_requested_from_me: boolean;
  author_has_write: boolean | null;
  team_membership_known: boolean;
};
export type Entry = {
  pull_request: PR;
  clone_id?: string;
  status: string;
  version_count: number;
  latest: {
    version: number;
    generated_at: string;
    backend: string;
    model: string;
    recorded_cost_usd: number | null;
  } | null;
  risk_score: { score: number | null; status: string } | null;
  historical: boolean;
  stale: boolean;
  failure_reason?: string;
  store_error?: string;
};
export type Job = {
  id: string;
  clone: string;
  repository: string;
  pr: number;
  state: string;
  phase: string;
  calls: number;
  tokens: number;
  repairs: number;
  actual_cost_usd: number | null;
  reason?: string;
};
export type Estimate = {
  id: string;
  estimate_usd: number | null;
  price_version: string;
  expires_at: string;
  items: {
    clone: string;
    pr: number;
    head_sha: string;
    backend: string;
    model: string;
    estimate_usd: number | null;
    requires_isolation_confirmation: boolean;
    blocked_reason?: string;
  }[];
};
export type Snapshot = { entries: Entry[]; problems: { clone_id: string; reason: string }[] };
export async function request<T>(
  path: string,
  body?: unknown,
  method = body === undefined ? "GET" : "POST",
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    method,
    signal,
    cache: "no-store",
    ...(body === undefined
      ? {}
      : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  });
  if (!response.ok)
    throw Error(
      `Local request failed (${response.status}): ${(await response.text()).slice(0, 240)}`,
    );
  if (response.status === 204) return undefined as T;
  return response.json();
}
export function selector(clone: string, pr: number, version?: number) {
  return new URLSearchParams({
    clone,
    pr: String(pr),
    ...(version ? { version: String(version) } : {}),
  }).toString();
}
export function documentRequest(clone: string, pr: number, version?: number, signal?: AbortSignal) {
  return request<ReviewDocument>(
    `/api/document?${selector(clone, pr, version)}`,
    undefined,
    "GET",
    signal,
  ).then((doc) => {
    if (
      doc.schema !== "baum.review/1" ||
      !doc.review ||
      !Array.isArray(doc.sections) ||
      !doc.summary
    )
      throw Error("Invalid review document.");
    return doc;
  });
}
export function rowKey(row: Entry) {
  return `${row.clone_id ?? ""}:${row.pull_request.repository}:${row.pull_request.number}`;
}
export function filtered(entries: Entry[], filter: string, repository: string) {
  return entries.filter(
    (e) =>
      (!repository || e.pull_request.repository === repository) &&
      (filter === "history" ? e.historical : !e.historical) &&
      (filter === "requested"
        ? e.pull_request.review_requested_from_me
        : filter === "mine"
          ? e.pull_request.authored_by_me
          : filter === "outdated" || filter === "no_review"
            ? e.status === filter
            : true),
  );
}
export function cost(value: number | null | undefined) {
  return formatUSD(value);
}
