import type {
  Anchor,
  BehaviorStep,
  ReviewDocument,
  Risk,
  Section,
} from "@baum/viewer/review-document";
import example from "../../../schema/review-example.json" with { type: "json" };
import { HUNKS } from "./fixture-1303-hunks";
import { withReviewView, type ReviewView } from "./model";

/** baum-ai/baum-monorepo#1303, a phone transport for the daemon, as the
 *  approved review page prototype tells it: its review and its view. The
 *  code is the pull request's own. */

const base = example as unknown as ReviewDocument;
const hunk = (id: string, lines?: [number, number]): Anchor =>
  lines ? { type: "hunk", id, lines } : { type: "hunk", id };

const step = (id: string, title: string, text: string, anchors: Anchor[]): BehaviorStep => ({
  id,
  title,
  text,
  anchors,
  evidence: [],
});

const risk = (
  id: string,
  tier: Risk["tier"],
  category: string,
  text: string,
  anchors: Anchor[],
): Risk => ({ id, tier, category, source: "model", text, anchors, state: "active" });

function section(
  id: string,
  title: string,
  summary: string,
  hunks: string[],
  steps: BehaviorStep[],
  risks: Risk[],
  extra: Partial<Section["derived"]> = {},
): Section {
  const ours = HUNKS.filter((each) => hunks.includes(each.id));
  return {
    id,
    title,
    kind: "logical",
    hunks,
    summary: { text: summary, anchors: hunks.slice(0, 1).map((each) => hunk(each)) },
    attention: null,
    risks,
    evidence: [],
    blocks: steps.length ? [{ kind: "behavior", steps }] : [],
    derived: {
      risk_tier: risks[0]?.tier ?? "unrated",
      additions: ours.reduce((total, each) => total + each.additions, 0),
      deletions: ours.reduce((total, each) => total + each.deletions, 0),
      role_loc: {},
      reviewers: [],
      state: "new",
      evidence_summary: {},
      open_findings: 0,
      ...extra,
    },
  };
}

const SECTIONS: Section[] = [
  section(
    "sec_pairing",
    "Pairing",
    "A pairing code holds a one-time secret that lasts 5 minutes and is never logged. Up to 4 codes can be live. A phone that presents a live code during the handshake is stored by its key, and at most 16 phones can be paired.",
    ["h_pairing", "h_devices"],
    [
      step(
        "bhv_code_expiry",
        "A code works once, for five minutes",
        "`NewOffer` makes a secret that expires after 5 minutes; at most 4 can be live.",
        [hunk("h_pairing", [21, 30])],
      ),
      step(
        "bhv_device_limit",
        "At most 16 phones",
        "`maxDevices` caps the paired phones at 16; a seventeenth is refused and the code stays usable.",
        [hunk("h_devices")],
      ),
    ],
    [
      risk(
        "rsk_paircode",
        "medium",
        "security",
        "The phone's relay token is derived from the pairing secret, so someone who saw the code within its 5 minutes can later connect to the relay as that phone. They can't finish a handshake or read anything, but they can take its connection slots until it is paired again.",
        [hunk("h_pairing", [21, 30])],
      ),
    ],
    { review_score: 3 },
  ),
  section(
    "sec_relay",
    "Relay connection",
    "The daemon keeps one outbound WebSocket to the relay, and only while something needs it: with no phone and no code there's no connection and no timer. Redirects are refused, so the daemon and its credential can't be sent elsewhere.",
    ["h_relay", "h_redirect"],
    [
      step(
        "bhv_relay_wanted",
        "Connected only while a phone needs it",
        "`wanted` is true only while a phone is paired or a code is live.",
        [hunk("h_relay")],
      ),
      step(
        "bhv_no_redirect",
        "Redirects are refused",
        "The dialer's `CheckRedirect` refuses every redirect.",
        [hunk("h_redirect")],
      ),
    ],
    [
      risk(
        "rsk_real_relay",
        "medium",
        "testing",
        "Every test uses an in-process stand-in relay written from the same README. Nothing has run against a deployed relay or on Windows.",
        [hunk("h_relay")],
      ),
    ],
    { review_score: 3 },
  ),
  section(
    "sec_encryption",
    "Encryption",
    "Phone and daemon run a Noise IK handshake that the relay isn't part of. A phone is known by its key, and a message that fails to decrypt ends the session.",
    ["h_session"],
    [
      step(
        "bhv_known_keys",
        "Phones are known by their key",
        "An unknown key without a live pairing code gets nothing back.",
        [hunk("h_session")],
      ),
    ],
    [],
    { review_score: 3 },
  ),
  section(
    "sec_requests",
    "Requests",
    "Inside the session, frames carry HTTP-like requests on numbered streams to the existing API handlers. Each phone gets 16 streams and 1 MiB of request body.",
    ["h_serve"],
    [
      step(
        "bhv_bad_message",
        "A bad message ends the session",
        "A message that fails to decrypt closes the session, and a phone counts as connected only after its first message decrypts.",
        [hunk("h_serve")],
      ),
    ],
    [],
    { review_score: 2 },
  ),
  section(
    "sec_allowlist",
    "What a phone can do",
    "A phone reaches the API only through `RemoteHandler`, which lists 37 routes by method and exact path. Everything else gets 403.",
    ["h_routes", "h_refusals", "h_remote_test", "h_security"],
    [
      step(
        "bhv_deny_default",
        "Deny by default",
        "`remoteRoutes` lists what a phone may ask; any other route answers 403 `not_available_remotely`.",
        [hunk("h_routes")],
      ),
      step(
        "bhv_path_tricks",
        "Path tricks are refused",
        "Encoded slashes, dot segments and a trailing slash are refused before matching.",
        [hunk("h_refusals")],
      ),
    ],
    [
      risk(
        "rsk_always_allow",
        "high",
        "security",
        "A phone can answer an agent's permission request with Always allow, which changes that agent's settings for good. On a stolen, unlocked phone the change outlives removing the phone.",
        [hunk("h_routes", [60, 64])],
      ),
    ],
    { review_score: 4 },
  ),
  section(
    "sec_lifecycle",
    "Daemon lifecycle",
    "The daemon builds the phone transport with the rest of the local application, starts it with the background work and closes it before the providers its requests read.",
    ["h_daemon", "h_shutdown"],
    [
      step(
        "bhv_started",
        "Started with the background work",
        "`phones.Start` runs in the daemon's `Run`.",
        [hunk("h_daemon")],
      ),
      step(
        "bhv_closed_first",
        "Closed before the job queue",
        "`Shutdown` closes the phones before the queue, and `Close` before the providers.",
        [hunk("h_shutdown")],
      ),
    ],
    [],
    { review_score: 2 },
  ),
  {
    ...section(
      "sec_deps",
      "Dependencies",
      "Adds the WebSocket and Noise libraries.",
      ["h_gosum"],
      [],
      [],
    ),
    kind: "mechanical",
  },
];

export const REVIEW_1303: ReviewDocument = {
  ...base,
  review: {
    ...base.review,
    repository: "baum-ai/baum-monorepo",
    pull_request: 1303,
    pr_state: "open",
    merged_commit: null,
    version: 2,
    previous_version: 1,
  },
  summary: {
    ...base.summary,
    title: "Phones reach the daemon through an encrypted relay",
    text: "A paired phone can now reach the daemon through a relay, over an end-to-end encrypted session. This is a third way in, beside loopback HTTP and the control socket. Phones can read reviews and act on them through a short allowlist of the existing API.",
    context: {
      area: {
        text: "Phones don't get a new API. They reach the daemon's existing local API through a second entry point, `RemoteHandler`, beside the loopback `Handler`.",
        refs: [
          { path: "oss/internal/uiserver/remote.go", symbol: "RemoteHandler" },
          { path: "oss/internal/uiserver/server.go", symbol: "Server.Handler" },
        ],
      },
      contracts: {
        text: "The daemon's lifecycle: `Run` starts background work, `Shutdown` and `Close` stop it.",
        refs: [
          { path: "oss/internal/daemon/server.go", symbol: "App" },
          { path: "oss/internal/cli/daemon.go", symbol: "localApplication" },
        ],
      },
    },
    risk_score: {
      rubric: "risk-v1",
      status: "assessed",
      score: 4,
      reasons: [
        {
          section: "sec_allowlist",
          risk: "rsk_always_allow",
          text: "Phones can approve pull requests and answer agents through a third-party relay.",
        },
      ],
      limitations: [],
    },
    recommendation: null,
  },
  sections: SECTIONS,
  unassigned_hunks: [],
  index: {
    ...base.index,
    hunks: HUNKS,
    attachments: [],
    sessions: [
      {
        id: "ses_mobile",
        vendor: "claude",
        title: "Review on mobile",
        restricted: false,
        prompts: [
          { index: 1, id: "p1", at: "2026-11-14T03:12:00Z", text: "allows to review on mobile" },
          {
            index: 2,
            id: "p2",
            at: "2026-11-14T03:19:00Z",
            text: "use an encrypted relay that we run",
          },
        ],
      },
    ],
  },
};

const LIBRARY_PENDING = {
  id: "lib_api_change",
  type: "custom",
  title: "Interface changes",
  data: null,
  state: "pending" as const,
  covers: ["h_routes"],
};

export const VIEW_1303: ReviewView = {
  schema: "baum.view/1",
  overview: {
    risk_summary:
      "This adds a network entry point to the daemon, which phones reach through a third-party relay and use to approve pull requests and answer agents. It stays off until a phone is paired, refuses by default and is end-to-end encrypted.",
    verdict: {
      text: "Approve into the mobile branch once you've decided on Always allow.",
      points: [
        {
          text: "A phone can answer an agent's permission request with Always allow, which changes that agent's permissions for good. Decide whether phones keep it.",
          section: "sec_allowlist",
        },
        {
          text: "Two later pull requests fixed seven bugs in this code. Merge both into the mobile branch before it goes to main.",
          section: "sec_relay",
        },
      ],
    },
    screenshots: [],
    diagram: "dg_shape",
    intent: [
      {
        asked: "Review on the phone",
        got: "Inbox, reviews, approve, comment, request changes and resolve threads.",
        session: "ses_mobile",
        prompt: 1,
      },
      {
        asked: "An encrypted relay run by baum",
        got: "The relay sees only ciphertext and token hashes.",
        session: "ses_mobile",
        prompt: 2,
      },
    ],
  },
  sections: [
    { id: "context", tiles: ["dg_context"] },
    { id: "sec_pairing", tiles: ["dg_pairing"] },
    { id: "sec_relay", tiles: ["dg_relay_runs"] },
    { id: "sec_encryption", tiles: ["dg_encryption"] },
    { id: "sec_requests", tiles: ["dg_requests"] },
    // More than the view call's three: the host places the library and
    // authored tiles it left out on the section whose hunks they cover.
    {
      id: "sec_allowlist",
      tiles: ["dg_allowlist", "dg_always_allow", "lib_api_change", "code_remote_routes"],
    },
    { id: "sec_lifecycle", tiles: ["dg_stop"] },
    { id: "sec_deps", tiles: [] },
  ],
  tiles: [
    {
      id: "dg_shape",
      type: "flow",
      title: "How a phone reaches the daemon",
      state: "ready",
      covers: ["h_routes"],
      data: {
        lanes: [
          {
            label: "Phone",
            steps: [
              { title: "Phone app" },
              { title: "Relay", note: "Sees only ciphertext", tone: "muted", join: "both" },
              { title: "Daemon's new transport", note: "Noise session", tone: "new", join: "both" },
              { title: "Allowlist", note: "37 routes, the rest refused", tone: "good" },
              { title: "Existing API handlers" },
            ],
          },
          {
            label: "Desktop",
            steps: [
              { title: "Desktop app" },
              { title: "Loopback HTTP", note: "Host, Origin, cookie and launch token" },
              { title: "Existing API handlers" },
              { title: "Unchanged", tone: "muted", join: "none" },
            ],
          },
        ],
        caption:
          "The daemon dials out to the relay. Nothing on the computer listens for the phone.",
        source: [hunk("h_routes")],
      },
    },
    {
      id: "dg_context",
      type: "flow",
      title: "Where the transport sits in the daemon",
      state: "ready",
      data: {
        lanes: [
          {
            label: "Built by",
            steps: [
              { title: "cli/daemon.go", note: "localApplication" },
              { title: "openRemoteTransport", tone: "new" },
              { title: "daemon.App", note: "Run, Shutdown and Close" },
            ],
          },
          {
            label: "Serves",
            steps: [
              { title: "uiserver.Server" },
              { title: "Handler", note: "Loopback, unchanged" },
              { title: "RemoteHandler", note: "Phones", tone: "new", join: "none" },
            ],
          },
        ],
        source: [],
      },
    },
    {
      id: "dg_pairing",
      type: "flow",
      title: "How a phone is paired",
      state: "ready",
      data: {
        lanes: [
          {
            steps: [
              { title: "Desktop makes a code", note: "Lasts 5 minutes" },
              { title: "QR code" },
              { title: "Handshake carries the secret", tone: "new" },
              { title: "Phone stored by its key", tone: "good" },
              { title: "Code used up" },
            ],
          },
        ],
        source: [hunk("h_pairing")],
      },
    },
    {
      id: "dg_relay_runs",
      type: "table",
      title: "Where the daemon has met a relay",
      state: "ready",
      data: {
        columns: ["Run", "Relay", "Result"],
        rows: [
          ["This pull request's tests", "In-process stand-in", "Pass"],
          ["The relay Worker under wrangler dev", "Real Worker, in Docker", "Pass, run 8 times"],
          ["relay.usebaum.com", "Not deployed", "Not run"],
        ],
        source: [],
      },
    },
    {
      id: "dg_encryption",
      type: "sequence",
      title: "What the relay can see",
      state: "ready",
      data: {
        actors: ["Phone", "Relay", "Daemon"],
        steps: [
          { from: 0, to: 2, text: "Noise IK handshake", tone: "new" },
          { from: 0, to: 1, text: "Ciphertext and a token hash" },
          { from: 1, to: 2, text: "Ciphertext, passed on" },
          { from: 1, to: 1, text: "No keys", tone: "bad" },
          { from: 2, to: 0, text: "Encrypted answers", tone: "good" },
        ],
        caption: "A message that fails to decrypt ends the session.",
        source: [hunk("h_session")],
      },
    },
    {
      id: "dg_requests",
      type: "flow",
      title: "How a request travels",
      state: "ready",
      data: {
        lanes: [
          {
            steps: [
              { title: "Request frame", note: "On stream n" },
              { title: "Body", note: "Up to 1 MiB" },
              { title: "Allowlist", tone: "new" },
              { title: "API handler" },
              { title: "Response frames", tone: "good" },
            ],
          },
        ],
        source: [hunk("h_serve")],
      },
    },
    {
      id: "dg_allowlist",
      type: "columns",
      title: "What a phone can reach",
      state: "ready",
      data: {
        columns: [
          {
            label: "A phone can",
            tone: "good",
            items: [
              { title: "Read the inbox, reviews and their versions" },
              { title: "Read sessions linked to the pull request", note: "Redacted" },
              { title: "Submit a review" },
              { title: "Answer an agent" },
            ],
          },
          {
            label: "A phone can't",
            tone: "bad",
            items: [
              { title: "Change settings" },
              { title: "Start, cancel or fix a job" },
              { title: "Merge or update a pull request" },
            ],
          },
        ],
        caption:
          "A new API route fails `TestRemoteRoutesCoverLocalAPI` until it's put in one of the two lists.",
        source: [hunk("h_routes"), hunk("h_remote_test")],
      },
    },
    {
      id: "dg_always_allow",
      type: "flow",
      title: "What Always allow changes",
      state: "ready",
      data: {
        lanes: [
          {
            label: "Once",
            steps: [
              { title: "Agent asks permission" },
              { title: "Phone: Allow" },
              { title: "That one action runs", tone: "good" },
            ],
          },
          {
            label: "Always",
            steps: [
              { title: "Agent asks permission" },
              { title: "Phone: Always allow", tone: "new" },
              { title: "Agent settings changed for good", tone: "bad" },
            ],
          },
        ],
        source: [hunk("h_routes", [60, 64])],
      },
    },
    LIBRARY_PENDING,
    {
      id: "code_remote_routes",
      type: "code",
      title: "The writes a phone may make",
      state: "ready",
      covers: ["h_routes"],
      data: {
        language: "go",
        content:
          '\t{"POST", "/api/prs/review"},\n\t{"POST", "/api/prs/resolve-thread"},\n\t{"POST", "/api/prs/refresh"},\n\t{"POST", "/api/agent-requests/{id}/answer"},',
        source: [hunk("h_routes", [61, 64])],
      },
    },
    {
      id: "dg_stop",
      type: "flow",
      title: "How the daemon stops",
      state: "failed",
      data: null,
    },
  ],
};

/** The review as the tile route serves it, carrying its view. */
export function review1303(view: ReviewView | null = VIEW_1303): ReviewDocument {
  return withReviewView(structuredClone(REVIEW_1303), view);
}
