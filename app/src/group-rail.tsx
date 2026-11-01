import { useEffect, useMemo, useRef, useState } from "react";
import { sessionsSlot, commitsSlot } from "@baum/viewer/components/list-slots";
import type { ReviewViewerProps } from "@baum/viewer/components/review-viewer";
import {
  parseSessionsAnswer,
  sessionLists,
  type LocalCommit,
  type LocalSession,
  type SessionsLoad,
} from "@baum/viewer/local-sessions";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { liveFor, request, selector, type LiveSession } from "./api";
import { withCommitRows } from "./commit-rows";
import { PullRequestDecisionsPane, useGroupDecisions } from "./decisions";
import { Transcript, routedCommitLoaders, withRunning } from "./review";

/** A stack member whose saved review the group's rail reads sessions and
 *  commits from: the PR page's own routes, with its latest version. */
export type RailMember = { number: number; clone: string };

type Read = { number: number; query: string; answer: ReturnType<typeof parseSessionsAnswer> | null };

/** What the daemon's per-pull-request session routes answered for a group's
 *  members, as one answer: sessions by id and commits by oid, once each,
 *  commits oldest first. A session or commit remembers the query of the first
 *  member that links it, which reads its transcript or detail. */
export function mergeReads(reads: Read[]) {
  const answered = reads.filter((r) => r.answer);
  if (answered.length === 0) return null;
  const available = answered.filter(
    (r): r is Read & { answer: { available: true; sessions: LocalSession[]; commits: LocalCommit[] } } =>
      r.answer?.available === true,
  );
  if (available.length === 0) return { answer: answered[0]!.answer!, sessionQuery: new Map(), commitQuery: new Map() };
  const sessions = new Map<string, LocalSession>();
  const commits = new Map<string, LocalCommit>();
  const sessionQuery = new Map<string, string>();
  const commitQuery = new Map<string, string>();
  for (const { query, answer } of available) {
    for (const s of answer.sessions) {
      if (sessions.has(s.id)) continue;
      sessions.set(s.id, s);
      sessionQuery.set(s.id, query);
    }
    for (const c of answer.commits) {
      if (commits.has(c.oid)) continue;
      commits.set(c.oid, c);
      commitQuery.set(c.oid, query);
    }
  }
  const ordered = [...commits.values()]
    .map((commit, position) => ({ commit, position }))
    .sort(
      (a, b) =>
        Date.parse(a.commit.committedAt ?? "") - Date.parse(b.commit.committedAt ?? "") ||
        a.position - b.position,
    )
    .map(({ commit }) => commit);
  return {
    answer: { available: true as const, sessions: [...sessions.values()], commits: ordered },
    sessionQuery,
    commitQuery,
  };
}

/** The Decisions, Sessions and Commits rows of a stack's page, as a pull
 *  request's page has them, read across the members that have a saved review. */
export function useGroupRail({
  repository,
  members,
  numbers,
  running,
  document,
  revision,
}: {
  repository: string;
  members: RailMember[];
  /** Every open member, for the decisions. */
  numbers: number[];
  running: LiveSession[];
  document: ReviewDocument | null;
  revision: string;
}): Pick<ReviewViewerProps, "sessions" | "commits" | "decisions"> {
  // A session that ends is read from disk like any other.
  const [ended, setEnded] = useState(0);
  const liveKeys = running.map((s) => `${s.agent}:${s.session_id}`).join("\n");
  const previousKeys = useRef(liveKeys);
  useEffect(() => {
    const now = new Set(liveKeys.split("\n"));
    if (previousKeys.current.split("\n").some((key) => key && !now.has(key)))
      setEnded((n) => n + 1);
    previousKeys.current = liveKeys;
  }, [liveKeys]);

  const queries = members.map((m) => selector(m.clone, m.number));
  const queriesKey = queries.join("\n");
  const [loaded, setLoaded] = useState<{ key: string; reads: Read[] } | null>(null);
  useEffect(() => {
    if (queries.length === 0) return;
    const abort = new AbortController();
    void Promise.all(
      members.map(async (member, i): Promise<Read> => {
        try {
          const body = await request<unknown>(
            `api/sessions?${queries[i]}`,
            undefined,
            "GET",
            abort.signal,
          );
          return { number: member.number, query: queries[i]!, answer: parseSessionsAnswer(body) };
        } catch {
          return { number: member.number, query: queries[i]!, answer: null };
        }
      }),
    ).then((reads) => {
      if (!abort.signal.aborted) setLoaded({ key: queriesKey, reads });
    });
    return () => abort.abort();
    // `queriesKey` stands for the members' queries.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queriesKey, revision, ended]);

  const merged = useMemo(
    () => (loaded?.key === queriesKey ? mergeReads(loaded.reads) : null),
    [loaded, queriesKey],
  );
  const commitQuery = merged?.commitQuery;
  const commitLoaders = useMemo(
    () => routedCommitLoaders("api", (oid) => commitQuery?.get(oid) ?? queries[0] ?? ""),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [commitQuery, queriesKey],
  );

  const decisions = useGroupDecisions(repository, numbers, `${ended}:${revision}`);

  const load: SessionsLoad | null =
    queries.length === 0
      ? null
      : loaded?.key !== queriesKey
        ? { state: "loading" }
        : merged
          ? { state: "answered", answer: merged.answer }
          : { state: "request-failed" };
  const lists = load ? sessionLists(load, document ?? { index: { sessions: [] } }) : null;
  const sessionQuery = merged?.sessionQuery;

  return {
    sessions:
      lists || running.length > 0
        ? withRunning(
            sessionsSlot(lists ? lists.sessions : { status: "ready", sessions: [] }, {
              renderSession: (session) => (
                <Transcript
                  id={session.id}
                  query={sessionQuery?.get(session.id) ?? queries[0] ?? ""}
                  title={session.title ?? undefined}
                />
              ),
            }),
            running,
          )
        : undefined,
    commits: lists?.commits
      ? withCommitRows(commitsSlot(lists.commits, commitLoaders), lists.commits)
      : undefined,
    decisions: decisions
      ? {
          count: decisions.total,
          renderPane: () => (
            <PullRequestDecisionsPane
              read={decisions}
              live={running}
              heading
              scope="this stack"
            />
          ),
        }
      : undefined,
  };
}

/** Sessions in progress on any of a group's open members, once each. */
export function liveForGroup(live: LiveSession[], repository: string, numbers: number[]) {
  const seen = new Set<string>();
  return numbers
    .flatMap((number) => liveFor(live, { repository, number }))
    .filter((s) => {
      const key = `${s.agent}:${s.session_id}`;
      return !seen.has(key) && seen.add(key);
    });
}
