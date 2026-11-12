import { LocalRequestError, request } from "./api";
import type { PageContext } from "./page-context";

/** The agents a session panel conversation runs. */
export type PanelAgent = "claude-code" | "codex";
/** What a conversation's agent may do without asking (`agent.Access`). */
export type PanelMode = "full" | "workspace";
export type ConversationState = "idle" | "running" | "failed" | "closed";

/** A conversation as the panel's picker lists it
 *  (`GET api/console/conversations`). */
export type ConversationSummary = {
  id: string;
  title: string;
  agent: PanelAgent;
  /** Empty for the agent's own default. */
  model: string;
  mode: PanelMode;
  state: ConversationState;
  directory: string;
  /** Why a pull request's conversation works in an empty directory. */
  directory_note?: string;
  repository?: string;
  pull_request?: number;
  updated_at: string;
  /** The agent's session, `agent:id`, once known. */
  session?: string;
};

/** The running or last turn of a conversation since the daemon started. */
export type Progress = {
  conversation: string;
  state: ConversationState;
  session?: string;
  /** The answer so far: its finished messages, then the one streaming;
   *  after the turn, its final message. */
  text: string;
  /** The turn's latest message on its own: the one streaming, else the last
   *  finished. Absent from an older daemon. */
  message?: string;
  /** The tool the agent runs now. */
  tool?: string;
  cost_usd?: number;
};

/** One conversation (`GET api/console/conversations/{id}`). */
export type Conversation = ConversationSummary & {
  branch?: string;
  created_worktree: boolean;
  created_branch: boolean;
  pages: PageContext[];
  created_at: string;
  /** Why the last turn failed or stopped. */
  error?: string;
  cost_partial?: boolean;
  cost_usd?: number;
  session_key?: string;
  progress: Progress | null;
};

/** A labelled part of an operation's summary. `quoted` text is not baum's
 *  (read from a route, or `by_agent` written by the agent) and is shown as
 *  data; `multiline` and `by_agent` fields are shown in full. */
export type SummaryField = {
  label: string;
  value: string;
  quoted?: boolean;
  multiline?: boolean;
  by_agent?: boolean;
};
export type OperationSummary = {
  action: string;
  details?: SummaryField[];
  model?: string;
  effort?: string;
  paid: boolean;
  irreversible: boolean;
};
/** What a conversation's agent proposes, as the MCP server keeps it: a change
 *  to save (`diff`) or an operation to run (`operation`). */
export type Proposal = {
  id: string;
  kind: "diff" | "operation";
  page: string;
  target: string;
  before?: unknown;
  after?: unknown;
  revision: string;
  context: PageContext;
  operation?: {
    name: string;
    request: unknown;
    summary: OperationSummary;
    /** The summary's revision, which a confirm sends back. */
    revision: string;
  };
};

/** The session panel's setting (`api/settings/panel`). An agent or model the
 *  person did not choose follows the fix agent's (`fix`). */
export type PanelSettings = {
  agent: PanelAgent;
  model: string;
  mode: PanelMode;
  position: "bottom" | "top";
  agent_source: "fix" | "panel";
  model_source: "fix" | "panel";
};

const base = "api/console";
const conversationPath = (id: string) => `${base}/conversations/${encodeURIComponent(id)}`;

/** The conversations, newest first, or null when the daemon has no session
 *  panel (`available: false`). */
export async function listConversations(): Promise<ConversationSummary[] | null> {
  const answer = await request<{ conversations?: ConversationSummary[]; available?: boolean }>(
    `${base}/conversations`,
  );
  if (answer.available === false) return null;
  return Array.isArray(answer.conversations) ? answer.conversations : [];
}

/** One conversation; null when the daemon has no session panel. */
export async function readConversation(id: string): Promise<Conversation | null> {
  const answer = await request<Conversation & { available?: boolean }>(conversationPath(id));
  return answer.available === false || typeof answer.id !== "string" ? null : answer;
}

export function startConversation(input: {
  mode?: PanelMode;
  page?: PageContext;
}): Promise<Conversation> {
  return request(`${base}/conversations`, input);
}

export function sendTurn(id: string, text: string, page?: PageContext): Promise<Conversation> {
  return request(`${conversationPath(id)}/messages`, page ? { text, page } : { text });
}

export function stopTurn(id: string): Promise<Conversation> {
  return request(`${conversationPath(id)}/cancel`, {});
}

export function closeConversation(id: string): Promise<Conversation> {
  return request(`${conversationPath(id)}/close`, {});
}

/** Tells the daemon the page the person has open. */
export function reportPage(page: PageContext): Promise<unknown> {
  return request(`${base}/page`, page, "PUT");
}

export async function listProposals(id: string): Promise<Proposal[]> {
  const answer = await request<{ proposals?: Proposal[] }>(`${conversationPath(id)}/proposals`);
  return Array.isArray(answer.proposals) ? answer.proposals : [];
}

const proposalPath = (id: string, proposal: string, action: string) =>
  `${conversationPath(id)}/proposals/${encodeURIComponent(proposal)}/${action}`;

/** Saves a change; `value` is what the person typed, when it takes one. */
export function saveProposal(id: string, proposal: string, value?: string): Promise<unknown> {
  return request(proposalPath(id, proposal, "save"), value === undefined ? {} : { value });
}

export function dismissProposal(id: string, proposal: string): Promise<unknown> {
  return request(proposalPath(id, proposal, "dismiss"), {});
}

/** What a confirm came to: the operation ran (its route's answer), or its
 *  summary changed since it was shown and nothing ran (`changed`, with the
 *  proposal holding the new summary to ask about again). */
export type ConfirmOutcome =
  | { kind: "ran"; status: number; message: string }
  | { kind: "changed"; proposal: Proposal; message: string };

/** Runs an operation at the summary's `revision`, the one shown. */
export async function confirmProposal(
  id: string,
  proposal: string,
  revision: string,
): Promise<ConfirmOutcome> {
  try {
    const answer = await request<{ outcome?: { status: number; message: string } }>(
      proposalPath(id, proposal, "confirm"),
      { revision },
    );
    return {
      kind: "ran",
      status: answer.outcome?.status ?? 200,
      message: answer.outcome?.message ?? "",
    };
  } catch (error) {
    const body = error instanceof LocalRequestError ? error.body : undefined;
    const changed = (body as { proposal?: Proposal } | undefined)?.proposal;
    if (error instanceof LocalRequestError && error.code === "operation_changed" && changed)
      return { kind: "changed", proposal: changed, message: error.message };
    throw error;
  }
}

export function readPanelSettings(): Promise<PanelSettings> {
  return request("api/settings/panel");
}

export function savePanelSettings(change: Partial<PanelSettings>): Promise<PanelSettings> {
  return request("api/settings/panel", change, "PUT");
}

/** The agent session a conversation runs, split from its `agent:id` key. */
export function sessionOf(key: string | undefined): { agent: string; id: string } | null {
  if (!key) return null;
  const at = key.indexOf(":");
  return at > 0 ? { agent: key.slice(0, at), id: key.slice(at + 1) } : null;
}
