import { harnessName } from "../agent-names";
import { formatSHA } from "./format";

/** A session in one line: its title, else the agent that ran it. */
export function sessionLabel(session: { vendor: string; title?: string | null }): string {
  return session.title || harnessName(session.vendor);
}

/** A commit in one line: its subject, else its short id. */
export function commitLabel(commit: { id: string; subject: string }): string {
  return commit.subject || formatSHA(commit.id);
}
