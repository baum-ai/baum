import type { Anchor, ReviewDocument, Section } from "@baum/viewer/review-document";
import example from "../../../schema/review-example.json" with { type: "json" };
import { chromeOf } from "./chrome";
import {
  parseReviewV2,
  REVIEW_V2,
  type ReviewV2,
  type V2Risk,
  type V2Section,
  type V2Step,
} from "./document";
import { HUNKS } from "./fixture-1303-hunks";
import { withReview } from "./model";

/** acme/demo#7, a phone transport for the daemon, as the approved review
 *  page prototype tells it: its baum.review/2 document, as the daemon serves
 *  it on the tile route. The code is the pull request's own. */

const base = example as unknown as ReviewDocument;
const hunk = (id: string, lines?: [number, number]): Anchor =>
  lines ? { type: "hunk", id, lines } : { type: "hunk", id };

const step = (id: string, title: string, text: string, cites: Anchor[]): V2Step => ({
  id,
  title,
  text,
  cites,
});

const risk = (
  id: string,
  tier: V2Risk["tier"],
  category: string,
  text: string,
  cites: Anchor[],
): V2Risk => ({ id, tier, category, text, cites, state: "active" });

function section(
  id: string,
  title: string,
  gist: string,
  summary: string,
  hunks: string[],
  steps: V2Step[],
  risks: V2Risk[],
  extra: Partial<Section["derived"]> = {},
): V2Section {
  const ours = HUNKS.filter((each) => hunks.includes(each.id));
  return {
    id,
    title,
    kind: "logical",
    hunks,
    gist,
    level: extra.review_score ?? null,
    level_reasons: [],
    summary,
    steps,
    risks,
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

const SECTIONS: V2Section[] = [
  section(
    "sec_pairing",
    "Pairing",
    "A one-time code pairs up to 16 phones.",
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
    "One outbound connection, only while a phone needs it.",
    "The daemon keeps one outbound WebSocket to the relay, and only while something needs it: with no phone and no code there's no connection and no timer. Redirects are refused, so the daemon and its credential can't be sent elsewhere.",
    ["h_relay", "h_redirect"],
    [
      step(
        "bhv_relay_wanted",
        "Connected only while a phone needs it",
        "`wanted` is true only while a phone is paired or a code is live.",
        [hunk("h_relay"), { type: "prompt", session: "ses_mobile", index: 2 }],
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
        [hunk("h_relay"), { type: "session_event", session: "ses_mobile", event: "ev_standin" }],
      ),
    ],
    { review_score: 3 },
  ),
  section(
    "sec_encryption",
    "Encryption",
    "The relay never sees the keys.",
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
    "Requests travel on numbered streams to the existing handlers.",
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
    "37 routes are allowed and everything else is refused.",
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
    "Started with the background work, stopped before the providers.",
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
      "",
      "Adds the WebSocket and Noise libraries.",
      ["h_gosum"],
      [],
      [],
    ),
    kind: "mechanical",
  },
];

const flow = (lanes: { label?: string; steps: Record<string, string>[] }[]) => ({ lanes });

export const REVIEW_1303: ReviewV2 = {
  schema: REVIEW_V2,
  review: {
    ...base.review,
    repository: "acme/demo",
    pull_request: 1303,
    pr_state: "open",
    merged_commit: null,
    version: 2,
    previous_version: 1,
  },
  summary: {
    title: "Phones reach the daemon through an encrypted relay",
    text: "A paired phone can now reach the daemon through a relay, over an end-to-end encrypted session. This is a third way in, beside loopback HTTP and the control socket. Phones can read reviews and act on them through a short allowlist of the existing API.",
    change_kind: base.summary.change_kind,
    verdict: {
      decision: "look_first",
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
    risk_why:
      "This adds a network entry point to the daemon, which phones reach through a third-party relay and use to approve pull requests and answer agents. It stays off until a phone is paired, refuses by default and is end-to-end encrypted.",
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
    review_minutes: null,
  },
  overview: {
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
  context: {
    area: {
      text: "Phones don't get a new API. They reach the daemon's existing local API through a second entry point, `RemoteHandler`, beside the loopback `Handler`.",
      files: ["oss/internal/uiserver/remote.go", "oss/internal/uiserver/server.go"],
    },
    reached_by: null,
    contracts: {
      text: "The daemon's lifecycle: `Run` starts background work, `Shutdown` and `Close` stop it.",
      files: ["oss/internal/daemon/server.go", "oss/internal/cli/daemon.go"],
    },
    related: null,
  },
  sections: SECTIONS,
  // Every tile, placed by its owner, in the order each place draws them. A
  // native tile's caption and citations stand on the tile; a code tile is
  // its hunk and lines with the text the host filled.
  tiles: [
    {
      id: "dg_shape",
      type: "flow",
      owner: "overview",
      title: "How a phone reaches the daemon",
      state: "ready",
      caption: "The daemon dials out to the relay. Nothing on the computer listens for the phone.",
      cites: [hunk("h_routes")],
      data: flow([
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
      ]),
    },
    {
      id: "dg_context",
      type: "flow",
      owner: "context",
      title: "Where the transport sits in the daemon",
      state: "ready",
      cites: [],
      data: flow([
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
      ]),
    },
    {
      id: "dg_pairing",
      type: "flow",
      owner: "sec_pairing",
      title: "How a phone is paired",
      state: "ready",
      cites: [hunk("h_pairing")],
      data: flow([
        {
          steps: [
            { title: "Desktop makes a code", note: "Lasts 5 minutes" },
            { title: "QR code" },
            { title: "Handshake carries the secret", tone: "new" },
            { title: "Phone stored by its key", tone: "good" },
            { title: "Code used up" },
          ],
        },
      ]),
    },
    {
      id: "dg_relay_runs",
      type: "table",
      owner: "sec_relay",
      title: "Where the daemon has met a relay",
      state: "ready",
      cites: [],
      data: {
        columns: ["Run", "Relay", "Result"],
        rows: [
          ["This pull request's tests", "In-process stand-in", "Pass"],
          ["The relay Worker under wrangler dev", "Real Worker, in Docker", "Pass, run 8 times"],
          ["relay.usebaum.com", "Not deployed", "Not run"],
        ],
      },
    },
    {
      id: "dg_encryption",
      type: "sequence",
      owner: "sec_encryption",
      title: "What the relay can see",
      state: "ready",
      caption: "A message that fails to decrypt ends the session.",
      cites: [hunk("h_session")],
      data: {
        actors: ["Phone", "Relay", "Daemon"],
        steps: [
          { from: 0, to: 2, text: "Noise IK handshake", tone: "new" },
          { from: 0, to: 1, text: "Ciphertext and a token hash" },
          { from: 1, to: 2, text: "Ciphertext, passed on" },
          { from: 1, to: 1, text: "No keys", tone: "bad" },
          { from: 2, to: 0, text: "Encrypted answers", tone: "good" },
        ],
      },
    },
    {
      id: "dg_requests",
      type: "flow",
      owner: "sec_requests",
      title: "How a request travels",
      state: "ready",
      cites: [hunk("h_serve")],
      data: flow([
        {
          steps: [
            { title: "Request frame", note: "On stream n" },
            { title: "Body", note: "Up to 1 MiB" },
            { title: "Allowlist", tone: "new" },
            { title: "API handler" },
            { title: "Response frames", tone: "good" },
          ],
        },
      ]),
    },
    {
      id: "dg_allowlist",
      type: "columns",
      owner: "sec_allowlist",
      title: "What a phone can reach",
      state: "ready",
      caption:
        "A new API route fails `TestRemoteRoutesCoverLocalAPI` until it's put in one of the two lists.",
      cites: [hunk("h_routes"), hunk("h_remote_test")],
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
      },
    },
    {
      id: "dg_always_allow",
      type: "flow",
      owner: "sec_allowlist",
      title: "What Always allow changes",
      state: "ready",
      cites: [hunk("h_routes", [60, 64])],
      data: flow([
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
      ]),
    },
    {
      id: "lib_api_change",
      type: "custom",
      owner: "sec_allowlist",
      title: "Interface changes",
      data: null,
      state: "pending",
      cites: [],
      covers: ["h_routes"],
    },
    {
      id: "code_remote_routes",
      type: "code",
      owner: "sec_allowlist",
      title: "The writes a phone may make",
      state: "ready",
      cites: [],
      covers: ["h_routes"],
      data: {
        hunk: "h_routes",
        lines: [61, 64],
        text: '\t{"POST", "/api/prs/review"},\n\t{"POST", "/api/prs/resolve-thread"},\n\t{"POST", "/api/prs/refresh"},\n\t{"POST", "/api/agent-requests/{id}/answer"},',
      },
    },
    {
      id: "dg_stop",
      type: "flow",
      owner: "sec_lifecycle",
      title: "How the daemon stops",
      state: "failed",
      cites: [],
      data: null,
    },
  ],
  stats: base.stats,
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
        events: [
          {
            id: "ev_standin",
            at: "2026-11-14T04:26:00Z",
            verb: "tested",
            summary:
              "Ran the relay tests against an in-process stand-in written from the relay's README; no deployed relay was available.",
          },
        ],
      },
    ],
  },
};

/** The review as the page reads it from the tile route: the review the
 *  chrome reads, made from `review` (by default #1303's document, read as
 *  the page parses it), carrying it. */
export function review1303(review: ReviewV2 = REVIEW_1303): ReviewDocument {
  const parsed = parseReviewV2(JSON.parse(JSON.stringify(review)), review.drafts)!;
  return withReview(chromeOf(parsed), parsed);
}

/** TEMPORARY, with from-v1.ts: #1303 as the daemon answers the tile route
 *  until it converts every version, a baum.review/1 document with its
 *  `baum.view/1` view beside it (a native tile's caption and citations in
 *  its data, a code tile as its excerpt). */
export function legacyAnswer1303(): { document: ReviewDocument; view: unknown } {
  const review = parseReviewV2(JSON.parse(JSON.stringify(REVIEW_1303)))!;
  const owners = ["context", ...review.sections.map((section) => section.id)];
  return JSON.parse(
    JSON.stringify({
      document: { ...chromeOf(review), schema: "baum.review/1" },
      view: {
        schema: "baum.view/1",
        overview: {
          risk_summary: review.summary.risk_why,
          verdict: review.summary.verdict,
          screenshots: review.overview.screenshots,
          diagram: review.overview.diagram,
          intent: review.overview.intent,
        },
        sections: owners.map((id) => ({
          id,
          tiles: review.tiles.filter((tile) => tile.owner === id).map((tile) => tile.id),
        })),
        tiles: review.tiles.map(({ owner: _o, caption: _c, cites: _s, ...tile }) => tile),
      },
    }),
  );
}
