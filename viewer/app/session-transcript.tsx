import pluralize from "pluralize";
import { useEffect, useMemo, useState } from "react";

import { SessionCard, type SessionCardSource } from "../src/components/session-card";
import { Failed, Skeleton } from "../src/components/states";
import { Button } from "../src/components/ui";
import { useUrlState } from "../src/navigation";
import { fetchTranscript, type LocalTranscript, TranscriptError } from "./local-transcript";

type TranscriptLoad =
  | { state: "loading" }
  | { state: "failed"; reason: TranscriptError["reason"] }
  | { state: "ready"; transcript: LocalTranscript };

/** The turn a `?turn=<n>` link sends the reader to, by its ordinal. */
function linkedTurn(search: URLSearchParams): number | undefined {
  const turn = Number(search.get("turn"));
  return Number.isSafeInteger(turn) && turn > 0 ? turn : undefined;
}

/** One linked session's transcript under its card in the Sessions pane,
 *  asked for only once that session is opened. It is drawn by the session
 *  card the hosted app uses, with every prompt open to read and each
 *  prompt's agent activity a click away. */
export function SessionTranscript({ id }: { id: string }) {
  const { search } = useUrlState();
  const focusTurn = linkedTurn(search);
  const [attempt, setAttempt] = useState(0);
  const [load, setLoad] = useState<TranscriptLoad>({ state: "loading" });

  useEffect(() => {
    const abort = new AbortController();
    setLoad({ state: "loading" });
    fetchTranscript(id, abort.signal).then(
      (transcript) => {
        if (!abort.signal.aborted) setLoad({ state: "ready", transcript });
      },
      (error: unknown) => {
        if (abort.signal.aborted) return;
        setLoad({
          state: "failed",
          reason: error instanceof TranscriptError ? error.reason : "failed",
        });
      },
    );
    return () => abort.abort();
  }, [id, attempt]);

  // The activity arrived with the transcript; the card asks for one prompt's
  // when the reader opens it.
  const source = useMemo<SessionCardSource | undefined>(() => {
    if (load.state !== "ready") return undefined;
    const { activity } = load.transcript;
    return {
      loadTurnActivity: (_runId, turnId) => {
        const found = activity[turnId];
        return found ? Promise.resolve(found) : Promise.reject(new Error("no activity"));
      },
    };
  }, [load]);

  if (load.state === "loading") {
    return (
      <div role="status" aria-label="Loading the transcript" className="mt-4 space-y-3">
        <p className="text-base text-(--text-tertiary)">Reading this session's transcript.</p>
        <Skeleton height={160} />
      </div>
    );
  }
  if (load.state === "failed") {
    return (
      <Failed className="mt-4">
        <p>
          {load.reason === "not-linked"
            ? "This session's transcript is not available here: it is no longer linked to the pull request."
            : "The transcript could not be loaded."}
        </p>
        <Button variant="secondary" onClick={() => setAttempt((count) => count + 1)}>
          Retry
        </Button>
      </Failed>
    );
  }

  const { transcript } = load;
  const { session } = transcript;
  return (
    <section aria-label="Transcript" className="mt-4 space-y-3">
      <p className="text-base text-(--text-secondary)">
        The full transcript from this machine, with secrets and e-mail addresses masked.
      </p>
      {transcript.truncated ? (
        <p
          role="note"
          className="rounded-(--radius-sm) bg-(--surface-wash) px-3 py-2 text-base text-(--text-secondary)"
        >
          {session.prompts_truncated
            ? `This transcript is too large to show whole: it shows the first ${pluralize("prompt", session.prompts.length, true)} of ${transcript.promptsTotal}, and long prompts, messages and tool calls are cut short.`
            : "Long prompts, messages and tool calls are cut short to keep this transcript quick to read."}
        </p>
      ) : null}
      <SessionCard
        key={session.run_id}
        session={session}
        source={source}
        defaultOpen
        focusTurn={focusTurn}
      />
    </section>
  );
}
