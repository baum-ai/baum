/** What `GET /api/sessions` and `GET /api/sessions/{id}` answer on a machine
 *  that has the pull request's sessions: synthetic, in the server's wire
 *  format. */

export const sessionId = "aaaaaaaa-0000-4000-8000-000000000001";
export const firstCommit = "1111111111111111111111111111111111111111";
export const secondCommit = "2222222222222222222222222222222222222222";

export const linkedSessions = {
  available: true,
  sessions: [
    {
      id: sessionId,
      source: "acme-agent",
      vendor: "acme-agent",
      title: "Wire the retry budget",
      branch: "feature/retry-budget",
      started_at: "2026-09-08T08:20:00Z",
      ended_at: "2026-09-08T08:50:00Z",
      outcome: "committed",
      edited_files: 3,
    },
  ],
  commits: [
    {
      oid: firstCommit,
      subject: "Add the retry budget",
      author: "Example Developer",
      committed_at: "2026-09-08T08:45:00Z",
      links: [{ session: sessionId, method: "commit_ref", confidence: 0.95 }],
    },
    { oid: secondCommit, subject: "Tidy the docs", links: [] },
  ],
};

const effects = {
  files_read: 2,
  files_edited: 1,
  reads: 3,
  files_changed: 1,
  diff_hunks: 1,
  added_lines: 4,
  removed_lines: 1,
  tool_calls: 1,
  execute_calls: 1,
  failed_calls: 0,
};

function prompt(ordinal: number, text: string, reply: string | null) {
  return {
    turn_id: `turn-${ordinal}`,
    ordinal,
    started_at: "2026-09-08T08:20:00Z",
    text,
    text_length: text.length,
    truncated: false,
    effects,
    cost_usd: null,
    cost_is_floor: false,
    tokens: 900,
    estimated_tokens: 0,
    replies: reply
      ? [
          {
            text: reply,
            text_length: reply.length,
            truncated: false,
            timestamp: null,
            model: "acme-model-1",
          },
        ]
      : [],
    replies_omitted: 0,
  };
}

export const transcript = {
  session: {
    run_id: sessionId,
    source: "acme-agent",
    title: "Wire the retry budget",
    git_branch: "feature/retry-budget",
    working_dir: "~/src/taskline",
    models: ["acme-model-1"],
    started_at: "2026-09-08T08:20:00Z",
    ended_at: "2026-09-08T08:50:00Z",
    outcome: "committed",
    user_email: "",
    turn_count: 2,
    commit_count: 1,
    confidence: 0.95,
    prompts: [
      prompt(1, "Add a retry budget to the worker", "The budget is wired."),
      prompt(2, "Now cover it with tests", null),
    ],
    prompts_total: 2,
    prompts_truncated: false,
    totals: effects,
    unattributed: effects,
    tokens: 1800,
    estimated_tokens: 0,
    cost_per_prompt: true,
    has_inner_trace: true,
  },
  activity: {
    "turn-1": {
      messages: [
        {
          text: "Reading the worker first.",
          text_length: 25,
          truncated: false,
          timestamp: null,
          model: "acme-model-1",
        },
      ],
      messages_truncated: false,
      invocations: [{ model: "acme-model-1" }],
      token_usage: {
        tokens: 900,
        estimated_tokens: 0,
        uncached_input_tokens: 600,
        output_tokens: 300,
        api_calls: 1,
        models: ["acme-model-1"],
      },
      tool_calls: [
        {
          id: "call-1",
          name: "Bash",
          verb: "execute",
          input: '{"command":"go test ./..."}',
          input_truncated: false,
          output: "ok  \tqueue\t0.2s",
          output_truncated: false,
          status: "ok",
          timestamp: null,
        },
      ],
      tool_calls_truncated: false,
    },
  },
  truncated: false,
  limit_bytes: 4194304,
};
