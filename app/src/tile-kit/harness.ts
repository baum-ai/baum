import { readFileSync } from "node:fs";
import { resolve } from "node:path";

/** The custom tile kit's sources, as the daemon embeds them. */
const KIT = resolve(import.meta.dirname, "../../../tiles/kit");

export const read = (path: string) => readFileSync(resolve(KIT, path), "utf8");

export type Manifest = {
  kit: number;
  runtime: { scripts: string[]; styles: string[]; shared: string[] };
  api: { name: string; kind: string; signature: string; description: string }[];
  view: { name: string }[];
  elements: { name: string; created_by: string }[];
  classes: { name: string; attributes?: { name: string; values?: string[] }[] }[];
  tokens: { name: string; group: string }[];
};

export const manifest = JSON.parse(read("v1/manifest.json")) as Manifest;

/** The kit's scripts in one strict function, as Compose inlines them (less
 *  the compaction, which only drops comments and spaces). */
export const kitScript = `(function () {\n"use strict";\n${manifest.runtime.scripts
  .map((name) => read(`v1/${name}`))
  .join("\n")}\n})();`;

export type Sent = { protocol: string; kind: string; [field: string]: unknown };

/** A frame as the page sees one: a sandbox-like iframe running the kit, then
 *  the author's scripts, with what it posts to its parent collected. */
export type Frame = {
  window: Window & typeof globalThis & { baum: Baum };
  document: Document;
  /** What the frame posted to its parent, in order. */
  sent: Sent[];
  /** Posts a message to the frame as its parent does, after a structured
   *  clone into the frame's realm. */
  send: (message: unknown, source?: unknown) => void;
  /** Sends `init` with defaults for anything not given. */
  init: (fields?: Record<string, unknown>) => void;
  /** Runs a script in the frame, as an author's <script> would. */
  run: (code: string) => void;
  /** Waits for the count-th message of a kind (the first by default). */
  next: (kind: string, count?: number) => Promise<Sent>;
  root: () => HTMLElement;
  remove: () => void;
};

// The author API, loosely typed: tests call it as an author's script would.
// oxlint-disable-next-line typescript/no-explicit-any
export type Baum = Record<string, any>;

export const tick = (ms = 0) => new Promise((resolve) => setTimeout(resolve, ms));

/** A frame with the kit loaded. `before` runs in the frame first, to stub
 *  what jsdom lacks (layout, ResizeObserver). */
export function frame(author = "", before?: (window: Window & typeof globalThis) => void): Frame {
  const iframe = document.createElement("iframe");
  document.body.append(iframe);
  const win = iframe.contentWindow as Window & typeof globalThis & { baum: Baum };
  const doc = win.document;
  doc.documentElement.lang = "en";
  doc.documentElement.dataset.theme = "light";
  doc.documentElement.dataset.face = "";
  const root = doc.createElement("div");
  root.id = "baum-root";
  doc.body.append(root);
  const sent: Sent[] = [];
  // The frame's parent is this window; what it posts there is collected.
  const host = {
    postMessage(message: unknown) {
      sent.push(JSON.parse(JSON.stringify(message)) as Sent);
    },
  };
  Object.defineProperty(win, "parent", { value: host, configurable: true });
  if (!win.requestAnimationFrame) {
    win.requestAnimationFrame = (callback) => win.setTimeout(() => callback(Date.now()), 16);
  }
  before?.(win);
  const run = (code: string) => {
    const script = doc.createElement("script");
    script.textContent = code;
    doc.body.append(script);
  };
  run(kitScript);
  if (author) run(author);
  const send = (message: unknown, source: unknown = host) => {
    const data = win.JSON.parse(JSON.stringify(message)) as unknown;
    const event = new win.MessageEvent("message", { data });
    Object.defineProperty(event, "source", { value: source });
    win.dispatchEvent(event);
  };
  const next = async (kind: string, count = 1) => {
    for (let waited = 0; waited < 3000; waited += 5) {
      const found = sent.filter((message) => message.kind === kind);
      if (found.length >= count) return found[count - 1]!;
      await tick(5);
    }
    throw new Error(`no ${kind} message; got ${sent.map((message) => message.kind).join(", ")}`);
  };
  return {
    window: win,
    document: doc,
    sent,
    send,
    init: (fields = {}) =>
      send({
        protocol: "baum.tile/1",
        kind: "init",
        face: "detail",
        size: "full",
        theme: "light",
        lang: "en",
        data: {},
        refs: {},
        ...fields,
      }),
    run,
    next,
    root: () => doc.getElementById("baum-root")!,
    remove: () => iframe.remove(),
  };
}

/** A frame whose author code registers glance and detail render functions
 *  given as source text. */
export function tileFrame(glance: string, detail = glance) {
  return frame(`baum.tile({ glance: ${glance}, detail: ${detail} });`);
}
