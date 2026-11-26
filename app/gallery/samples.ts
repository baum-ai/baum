import { HUNKS } from "../src/review-view/fixture-1303-hunks";

/** Hand-written diagrams for the gallery: per type the smallest one, the
 *  largest one the composition schema (`oss/schema/composition-2.json`) and
 *  the host's check (`oss/tiles/view/native.go`) allow, with every count and
 *  text at its limit, a few realistic ones, and one the page cannot read.
 *  `expect` is what the page's own check (`NATIVE[type].valid`) says. */

export type Sample = {
  id: string;
  type: string;
  title: string;
  /** "overview", "context" or a section's ID, as a composition places it. */
  placement: string;
  data: unknown;
  expect: "valid" | "invalid";
};

/** `phrase` repeated and cut to exactly `length` characters, never ending on
 *  a space or inside a code span: text at its limit. */
export function exactly(length: number, phrase: string): string {
  let text = "";
  while (text.length < length) text += `${phrase} `;
  text = text.slice(0, length);
  if (text.endsWith(" ")) text = `${text.slice(0, -1)}x`;
  // A cut through a `code` span leaves it open: close it on the last character
  // (or drop it if it opens there).
  if ((text.match(/`/g) ?? []).length % 2 === 1)
    text = `${text.slice(0, -1)}${text.endsWith("`") ? "x" : "`"}`;
  return text;
}

const LONG = "NotificationDeliveryCoordinator retries `webhook_signature_verification` after";
const WORDS = "SubscriptionReconciliationWorker";

const range = (count: number) => Array.from({ length: count }, (_, at) => at);
const TONES = ["plain", "new", "good", "bad", "muted", "missing"] as const;
const tone = (at: number) => TONES[at % TONES.length];
const JOINS = ["arrow", "both", "none"] as const;

const SECTION = "sec_sample";

// ---------- flow ----------

const flowMinimal: Sample = {
  id: "dg_flow_min",
  type: "flow",
  title: "A",
  placement: SECTION,
  data: { source: [], lanes: [{ steps: [{ title: "A" }] }] },
  expect: "valid",
};

const flowMaximal: Sample = {
  id: "dg_flow_max",
  type: "flow",
  title: exactly(80, LONG),
  placement: SECTION,
  data: {
    caption: exactly(200, LONG),
    source: range(20).map((at) => ({
      type: "hunk",
      id: `h_${String(at).padStart(12, "0")}`,
      lines: [1, 9],
    })),
    lanes: range(4).map((lane) => ({
      label: exactly(24, WORDS),
      ...(lane > 0 ? { from: lane + 1 } : {}),
      steps: range(6).map((step) => ({
        title: exactly(60, LONG),
        note: exactly(80, WORDS),
        more: exactly(300, LONG),
        tone: tone(lane + step),
        join: JOINS[step % JOINS.length],
      })),
    })),
  },
  expect: "valid",
};

const flowRealistic: Sample = {
  id: "dg_flow_checkout",
  type: "flow",
  title: "How a checkout is paid",
  placement: "overview",
  data: {
    caption: "A declined card no longer retries: the order waits for the customer instead.",
    source: [],
    lanes: [
      {
        label: "Storefront",
        steps: [
          { title: "Cart" },
          { title: "Checkout form", note: "Address and card" },
          {
            title: "`PaymentIntent` created",
            tone: "new",
            more: "Created once per order and reused on retry, so a double click no longer charges twice.",
          },
          { title: "Payment provider", join: "both", tone: "muted" },
          { title: "Order confirmed", tone: "good" },
        ],
      },
      {
        label: "Declined",
        from: 4,
        steps: [
          { title: "Decline recorded", tone: "bad" },
          { title: "Retry with backoff", tone: "missing", note: "Removed" },
          { title: "Email to customer", tone: "new" },
        ],
      },
      {
        label: "Back office",
        steps: [
          { title: "Webhook" },
          { title: "Ledger entry", join: "none" },
          { title: "Daily export", tone: "muted" },
        ],
      },
    ],
  },
  expect: "valid",
};

const flowInvalid: Sample = {
  id: "dg_flow_invalid",
  type: "flow",
  title: "A flow without lanes",
  placement: SECTION,
  data: { source: [], lanes: [] },
  expect: "invalid",
};

// ---------- columns ----------

const columnsMinimal: Sample = {
  id: "dg_columns_min",
  type: "columns",
  title: "A",
  placement: SECTION,
  data: {
    source: [],
    columns: [
      { label: "A", items: [{ title: "A" }] },
      { label: "B", items: [{ title: "B" }] },
    ],
  },
  expect: "valid",
};

const columnsMaximal: Sample = {
  id: "dg_columns_max",
  type: "columns",
  title: exactly(80, LONG),
  placement: SECTION,
  data: {
    caption: exactly(200, LONG),
    source: [],
    columns: range(3).map((column) => ({
      label: exactly(40, WORDS),
      tone: tone(column + 1),
      items: range(8).map(() => ({ title: exactly(80, LONG), note: exactly(80, WORDS) })),
    })),
  },
  expect: "valid",
};

const columnsRealistic: Sample = {
  id: "dg_columns_access",
  type: "columns",
  title: "Who can change a project's settings",
  placement: SECTION,
  data: {
    caption: "Maintainers lose billing; it moves to owners only.",
    source: [],
    columns: [
      {
        label: "Owners",
        tone: "good",
        items: [
          { title: "Billing", note: "Moved here from maintainers" },
          { title: "Members and roles" },
          { title: "Delete the project" },
        ],
      },
      {
        label: "Maintainers",
        items: [{ title: "Branch protection" }, { title: "Webhooks" }],
      },
      {
        label: "Nobody",
        tone: "missing",
        items: [{ title: "Transfer to another organization", note: "Not built yet" }],
      },
    ],
  },
  expect: "valid",
};

const columnsCompared: Sample = {
  id: "dg_columns_compared",
  type: "columns",
  title: "Clips beside still images",
  placement: SECTION,
  data: {
    caption:
      "A clip is labelled and retried like an image, but it plays in place and has no full-size view.",
    source: [],
    columns: [
      { label: "Still image", tone: "muted" },
      { label: "Description clip", tone: "new" },
    ],
    rows: [
      [
        { title: "Shown at its size", note: "Sized by its width and height." },
        {
          title: "Plays in place with controls",
          note: "Muted, no autoplay, sized by its width and height.",
        },
      ],
      [
        {
          title: "Label from its caption or alt text",
          note: "Falls back to a numbered image label.",
        },
        {
          title: "Label from its caption or alt text",
          note: "Falls back to a numbered image label.",
        },
      ],
      [
        { title: "Opens full size on click", note: "A dialog shows the image larger." },
        { title: "No full-size dialog", note: "The clip plays in its own box on the page." },
      ],
      [
        { title: "Failure says Image unavailable", note: "Retry reloads the image." },
        { title: "Failure says Video unavailable", note: "Retry reloads the clip." },
      ],
      [
        { title: "Retry takes focus after a failure", note: "Focus moves to the retry button." },
        { title: "Retry takes focus after a failure", note: "Focus moves to the retry button." },
      ],
      [
        null,
        { title: "Other clips keep showing", note: "One failed clip leaves the rest playing." },
      ],
      [{ title: "Copies the image link", note: "From the context menu." }, null],
    ],
  },
  expect: "valid",
};

const columnsComparedMaximal: Sample = {
  id: "dg_columns_compared_max",
  type: "columns",
  title: exactly(80, LONG),
  placement: SECTION,
  data: {
    caption: exactly(200, LONG),
    source: [],
    columns: range(3).map((column) => ({ label: exactly(40, WORDS), tone: tone(column + 1) })),
    rows: range(8).map((row) =>
      range(3).map((column) =>
        row === 7 && column === 1
          ? null
          : { title: exactly(80, row % 2 ? LONG : WORDS), note: exactly(80, WORDS) },
      ),
    ),
  },
  expect: "valid",
};

const columnsInvalid: Sample = {
  id: "dg_columns_invalid",
  type: "columns",
  title: "A column without a label",
  placement: SECTION,
  data: {
    source: [],
    columns: [{ items: [{ title: "A" }] }, { label: "B", items: [{ title: "B" }] }],
  },
  expect: "invalid",
};

// ---------- sequence ----------

const sequenceMinimal: Sample = {
  id: "dg_sequence_min",
  type: "sequence",
  title: "A",
  placement: SECTION,
  data: { source: [], actors: ["A", "B"], steps: [{ from: 0, to: 1, text: "A" }] },
  expect: "valid",
};

const sequenceMaximal: Sample = {
  id: "dg_sequence_max",
  type: "sequence",
  title: exactly(80, LONG),
  placement: SECTION,
  data: {
    caption: exactly(200, LONG),
    source: [],
    actors: range(4).map(() => exactly(24, WORDS)),
    // Every pair, both ways, and a note on itself.
    steps: [
      [0, 3],
      [3, 0],
      [0, 1],
      [1, 2],
      [2, 3],
      [3, 2],
      [1, 1],
      [2, 0],
      [0, 0],
      [3, 3],
    ].map(([from, to], at) => ({ from, to, text: exactly(80, LONG), tone: tone(at) })),
  },
  expect: "valid",
};

const sequenceRealistic: Sample = {
  id: "dg_sequence_login",
  type: "sequence",
  title: "How a login is confirmed",
  placement: SECTION,
  data: {
    caption: "The session cookie is set only after the identity provider answers.",
    source: [],
    actors: ["Browser", "App server", "Identity provider", "Database"],
    steps: [
      { from: 0, to: 1, text: "POST /login" },
      { from: 1, to: 2, text: "Authorization request", tone: "new" },
      { from: 2, to: 0, text: "Consent screen" },
      { from: 0, to: 2, text: "Approve" },
      { from: 2, to: 1, text: "Code, then `id_token`", tone: "good" },
      { from: 1, to: 3, text: "Store session" },
      { from: 1, to: 1, text: "No password kept", tone: "muted" },
      { from: 1, to: 0, text: "Set-Cookie, redirect", tone: "good" },
    ],
  },
  expect: "valid",
};

const sequenceInvalid: Sample = {
  id: "dg_sequence_invalid",
  type: "sequence",
  title: "A message to an actor that isn't there",
  placement: SECTION,
  data: { source: [], actors: ["A", "B"], steps: [{ from: 0, to: 4, text: "Lost" }] },
  expect: "invalid",
};

// ---------- table ----------

const tableMinimal: Sample = {
  id: "dg_table_min",
  type: "table",
  title: "A",
  placement: SECTION,
  data: { source: [], columns: ["A", "B"], rows: [["", ""]] },
  expect: "valid",
};

const tableMaximal: Sample = {
  id: "dg_table_max",
  type: "table",
  title: exactly(80, LONG),
  placement: SECTION,
  data: {
    caption: exactly(200, LONG),
    source: [],
    columns: range(5).map(() => exactly(40, WORDS)),
    rows: range(12).map((row) =>
      range(5).map((column) =>
        (row + column) % 2 === 0
          ? exactly(120, LONG)
          : { text: exactly(120, LONG), was: exactly(120, WORDS) },
      ),
    ),
  },
  expect: "valid",
};

const ENDPOINTS = [
  ["/v1/orders", "GET", "60/min", "120/min"],
  ["/v1/orders", "POST", "10/min", "10/min"],
  ["/v1/orders/{id}", "GET", "60/min", "120/min"],
  ["/v1/orders/{id}", "PATCH", "10/min", "20/min"],
  ["/v1/customers", "GET", "60/min", "60/min"],
  ["/v1/customers", "POST", "5/min", "10/min"],
  ["/v1/invoices", "GET", "30/min", "60/min"],
  ["/v1/invoices/{id}/pay", "POST", "5/min", "5/min"],
  ["/v1/refunds", "POST", "5/min", "10/min"],
  ["/v1/webhooks", "GET", "10/min", "10/min"],
  ["/v1/webhooks", "POST", "2/min", "5/min"],
  ["/v1/export", "POST", "1/hour", "1/hour"],
];

const tableRealistic: Sample = {
  id: "dg_table_limits",
  type: "table",
  title: "Rate limits per endpoint",
  placement: SECTION,
  data: {
    caption: "Reads double; writes stay where they were except order updates and refunds.",
    source: [],
    columns: ["Endpoint", "Method", "Limit", "Burst", "Note"],
    rows: ENDPOINTS.map(([endpoint, method, before, after]) => [
      `\`${endpoint}\``,
      method,
      before === after ? after : { text: after, was: before },
      method === "GET" ? "20" : "5",
      before === after ? "" : "Raised",
    ]),
  },
  expect: "valid",
};

const tableInvalid: Sample = {
  id: "dg_table_invalid",
  type: "table",
  title: "A cell that isn't text",
  placement: SECTION,
  data: { source: [], columns: ["A", "B"], rows: [["one", 2]] },
  expect: "invalid",
};

// ---------- not a native type ----------

const unknownType: Sample = {
  id: "dg_timeline",
  type: "timeline",
  title: "A type the page doesn't draw",
  placement: SECTION,
  data: { source: [], events: [{ at: "09:00", text: "Deployed" }] },
  expect: "invalid",
};

// ---------- code ----------

/** Code tiles as baum.review/2 stores them, on the fixture's hunks (the
 *  composition has no code tile, so no schema bounds apply): the page draws
 *  each as its hunk's diff. `text` is the new side the host fills. */
export type CodeSample = Omit<Sample, "data"> & {
  data: { hunk: string; lines?: [number, number]; focus?: number[]; text?: string };
};

const newSide = (hunk: string, first: number, last: number) =>
  (HUNKS.find((each) => each.id === hunk)?.diff?.lines ?? [])
    .flatMap((line) =>
      line.kind !== "remove" && line.new_line >= first && line.new_line <= last ? [line.text] : [],
    )
    .join("\n");

const codeExcerpt = (
  id: string,
  title: string,
  hunk: string,
  lines: [number, number],
  focus?: number[],
): CodeSample => ({
  id,
  type: "code",
  title,
  placement: SECTION,
  data: { hunk, lines, ...(focus ? { focus } : {}), text: newSide(hunk, lines[0], lines[1]) },
  expect: "valid",
});

export const CODE_SAMPLES: CodeSample[] = [
  codeExcerpt("code_shutdown", "Phones close first", "h_shutdown", [992, 1003], [997, 998]),
  codeExcerpt("code_plain", "The device list", "h_devices", [21, 26]),
  codeExcerpt("code_long", "The reads a phone may make", "h_routes", [17, 49], [45, 46]),
  {
    id: "code_unfilled",
    type: "code",
    title: "An excerpt the host didn't fill",
    placement: SECTION,
    data: { hunk: "h_shutdown", lines: [992, 994] },
    expect: "invalid",
  },
];

export const SAMPLES: Sample[] = [
  flowMinimal,
  flowMaximal,
  flowRealistic,
  flowInvalid,
  columnsMinimal,
  columnsMaximal,
  columnsRealistic,
  columnsCompared,
  columnsComparedMaximal,
  columnsInvalid,
  sequenceMinimal,
  sequenceMaximal,
  sequenceRealistic,
  sequenceInvalid,
  tableMinimal,
  tableMaximal,
  tableRealistic,
  tableInvalid,
  unknownType,
];
