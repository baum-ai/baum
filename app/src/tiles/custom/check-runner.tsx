import { useEffect, useRef, useState } from "react";
import { appURL } from "../../api";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { parseReviewView } from "../../review-view/model";
import { tileSourceOf, type TileSize } from "../types";
import { setInPageRunner } from "./check-host";
import {
  HeightGuard,
  HELLO_TIMEOUT,
  initMessage,
  measureMessage,
  READY_TIMEOUT,
  readMessage,
  type ReadyMetrics,
} from "./protocol";
import { resolveRefs } from "./refs";
import { listenToFrame } from "./router";

/** The render check's runner (see "Render check" in oss/ARCHITECTURE.md): it
 *  claims a job from the daemon, draws the candidate in sandboxed frames as
 *  the tiles page mounts them (the same frame route, sandbox, router and
 *  protocol), and reports every problem it saw; the daemon sorts them into
 *  errors and warnings.
 *
 *  It may run in a hidden window, which runs no animation frames, no
 *  ResizeObserver or IntersectionObserver callbacks and slows timers to
 *  about one a second, though its layout and fonts are right. So it never
 *  waits on any of those: the kit settles on its fonts under a check, the
 *  runner reads sizes from the layout and from messages, and timers are
 *  only deadlines. That is also why it drives frames itself rather than
 *  through `TileFrame`, whose mounting waits on an IntersectionObserver and
 *  whose heights follow animation frames. */

export type CheckSample = {
  name: string;
  kind: "data" | "sample" | "minimal" | "maximal";
  data: unknown;
  refs?: { glance: Record<string, unknown>; detail: Record<string, unknown> };
};

export type CheckJob = {
  id: string;
  kind: "check" | "preflight";
  digest: string;
  kit: number;
  title: string;
  sizes: TileSize[];
  context?: { clone: string; pr: number; version: number } | null;
  samples: CheckSample[];
};

export type CheckProblem = {
  code: string;
  message: string;
  sample: string;
  face: "glance" | "detail";
  size: TileSize;
  theme: "light" | "dark";
  line?: number;
  column?: number;
};

export type CheckRun = {
  sample: string;
  face: "glance" | "detail";
  size: TileSize;
  theme: "light" | "dark";
  width: number;
  height: number;
  ready_ms: number;
  content_height: number;
  text_chars: number;
};

export type CheckReport = { problems: CheckProblem[]; runs: CheckRun[]; engine: string };

/** A glance's box at each size, as a card on the tiles page gives its frame
 *  at the grid's reference width (the boxes the tile guide lists). */
export const GLANCE_BOXES: Record<TileSize, { width: number; height: number }> = {
  third: { width: 310, height: 164 },
  half: { width: 488, height: 180 },
  full: { width: 1022, height: 116 },
};
/** A detail's widths: the pane on a desktop and on a phone. */
export const DETAIL_WIDTHS = [720, 360] as const;
/** Frames drawn at once. */
export const AT_ONCE = 8;
/** Over this, drawing is slow. */
export const SLOW_MS = 500;
/** How many times a detail's frame is set to the height it asked for, to
 *  see whether its height follows its frame's. */
const MEASURES = 12;
/** What the runner waits after a frame is ready, for errors it reports late
 *  (a logged error, a blocked request). */
const LATE = 100;

export type FrameSpec = {
  sample: CheckSample;
  face: "glance" | "detail";
  size: TileSize;
  theme: "light" | "dark";
  width: number;
  height: number;
};

/** The frames to draw for a job: for a check, each sample as a glance at
 *  each declared size and as a detail at each width, and the data also in
 *  the dark theme; for a preflight, its data as a light glance at its size
 *  and a light detail in the pane. */
export function matrix(job: CheckJob): FrameSpec[] {
  const specs: FrameSpec[] = [];
  const sizes = job.sizes.length > 0 ? job.sizes : (["full"] as TileSize[]);
  const glance = (sample: CheckSample, size: TileSize, theme: "light" | "dark") =>
    specs.push({ sample, face: "glance", size, theme, ...GLANCE_BOXES[size] });
  const detail = (sample: CheckSample, width: number, theme: "light" | "dark") =>
    specs.push({ sample, face: "detail", size: "full", theme, width, height: 120 });
  for (const sample of job.samples) {
    if (job.kind === "preflight") {
      glance(sample, sizes[0]!, "light");
      detail(sample, DETAIL_WIDTHS[0], "light");
      continue;
    }
    for (const size of sizes) glance(sample, size, "light");
    for (const width of DETAIL_WIDTHS) detail(sample, width, "light");
    if (sample.kind === "data") {
      glance(sample, sizes[0]!, "dark");
      detail(sample, DETAIL_WIDTHS[0], "dark");
    }
  }
  return specs;
}

type Outcome = {
  problems: Omit<CheckProblem, "sample" | "face" | "size" | "theme">[];
  run: CheckRun;
};

/** The codes the kit reports that the check passes on as they are. */
const KIT_CODES = new Set([
  "script_error",
  "unhandled_rejection",
  "console_error",
  "csp_violation",
  "timeout",
  "layout_loop",
]);

/** The problems a ready frame's measures show. */
export function measured(
  spec: Pick<FrameSpec, "face" | "width" | "height">,
  metrics: ReadyMetrics,
): Outcome["problems"] {
  const problems: Outcome["problems"] = [];
  if (metrics.failed)
    problems.push({ code: "render_failed", message: "The tile drew its error state (baum.fail)." });
  else if (metrics.text_chars === 0 && metrics.media === 0 && !metrics.empty_state)
    problems.push({
      code: "empty",
      message: `The ${spec.face} drew no text, image or drawing, and did not call baum.empty.`,
    });
  if (spec.face === "glance" && metrics.overflow_y)
    problems.push({
      code: "overflow_glance",
      message: `The glance is ${metrics.height}px tall in a ${spec.width}×${spec.height}px box.`,
    });
  if (metrics.overflow_x)
    problems.push({
      code: "overflow_x",
      message: `The ${spec.face} is wider than its ${spec.width}px frame.`,
    });
  if (metrics.unresolved > 0)
    problems.push({
      code: "unresolved_citation",
      message: `${metrics.unresolved} citation${metrics.unresolved === 1 ? "" : "s"} drawn without anything the review holds.`,
    });
  if (metrics.render_ms > SLOW_MS)
    problems.push({ code: "slow", message: `Drawing took ${metrics.render_ms} ms.` });
  return problems;
}

/** Draws one frame in `stage` and reports what it saw. */
export function drawFrame(
  stage: HTMLElement,
  spec: FrameSpec,
  job: Pick<CheckJob, "id" | "digest" | "title">,
  refs: Record<string, unknown>,
): Promise<Outcome> {
  return new Promise((resolve) => {
    const iframe = document.createElement("iframe");
    iframe.setAttribute("sandbox", "allow-scripts");
    iframe.setAttribute("referrerpolicy", "no-referrer");
    iframe.setAttribute("allow", "");
    iframe.tabIndex = -1;
    iframe.title = job.title || "Tile";
    iframe.style.cssText = `display:block;border:0;width:${spec.width}px;height:${spec.height}px;flex:none`;
    stage.append(iframe);
    const problems: Outcome["problems"] = [];
    const run: CheckRun = {
      sample: spec.sample.name,
      face: spec.face,
      size: spec.size,
      theme: spec.theme,
      width: spec.width,
      height: spec.height,
      ready_ms: 0,
      content_height: 0,
      text_chars: 0,
    };
    let done = false;
    let registered = true;
    let inited = 0;
    let ready = false;
    let helloTimer = 0;
    let readyTimer = 0;
    let lateTimer = 0;
    /** Waiting for a resize after a measure. */
    let measuring: ((height: number) => void) | null = null;
    const finish = () => {
      if (done) return;
      done = true;
      window.clearTimeout(helloTimer);
      window.clearTimeout(readyTimer);
      window.clearTimeout(lateTimer);
      unlisten();
      iframe.remove();
      resolve({ problems, run });
    };
    const target = iframe.contentWindow;
    if (!target) {
      problems.push({ code: "no_hello", message: "The frame could not be made." });
      return finish();
    }
    const send = (message: unknown) => {
      try {
        target.postMessage(message, "*");
      } catch {
        // A value that cannot be cloned is not sent; the frame times out.
      }
    };
    /** A detail is set to the height it asks for, again and again: a height
     *  that grows with its frame by the same amount is a layout loop. */
    const followHeight = async (first: number) => {
      const guard = new HeightGuard();
      let height = first;
      guard.next(height, 0);
      for (let i = 0; i < MEASURES && !done; i++) {
        iframe.style.height = `${height}px`;
        // The page's layout first, so the frame measures in its new box.
        iframe.getBoundingClientRect();
        // A frame in a process of its own learns its new size a moment
        // later: an unchanged answer is asked again, after message turns
        // (which a hidden window does not slow) and once after a short
        // timer. In a hidden window the frame shares the page's process and
        // the first answer is already right; a loop missed here is still
        // reported by the kit itself wherever ResizeObserver runs.
        let next: number | null = height;
        for (let ask = 0; ask < 4 && next === height && !done; ask++) {
          if (ask === 3) await new Promise((wait) => window.setTimeout(wait, 50));
          else if (ask > 0) await turn();
          next = await new Promise<number | null>((answer) => {
            measuring = answer;
            send(measureMessage());
            window.setTimeout(() => answer(null), 1000);
          });
          measuring = null;
        }
        if (next === null || next === height) break;
        if (guard.next(next, 0) === null) {
          problems.push({
            code: "layout_loop",
            message: `The detail's height kept growing with its frame, by ${next - height}px each time.`,
          });
          break;
        }
        height = next;
      }
      run.content_height = height;
      run.height = height;
    };
    const heard = (data: unknown) => {
      if (done) return;
      const message = readMessage(data);
      if (!message) return;
      switch (message.kind) {
        case "hello":
          if (inited) return;
          window.clearTimeout(helloTimer);
          registered = message.registered;
          inited = performance.now();
          send(
            initMessage({
              face: spec.face,
              size: spec.size,
              theme: spec.theme,
              lang: "en",
              data: spec.sample.data,
              refs,
              // The kit needs only to know it is under a check, not which.
              check: { id: "" },
            }),
          );
          readyTimer = window.setTimeout(() => {
            problems.push(
              registered
                ? {
                    code: "timeout",
                    message: "The tile was not drawn 3 seconds after it was given its data.",
                  }
                : {
                    code: "render_failed",
                    message: "baum.tile() was never called, so nothing was drawn.",
                  },
            );
            finish();
          }, READY_TIMEOUT);
          return;
        case "ready": {
          if (!inited || ready) return;
          ready = true;
          window.clearTimeout(readyTimer);
          run.ready_ms = Math.round(performance.now() - inited);
          run.content_height = message.metrics.height;
          run.text_chars = message.metrics.text_chars;
          problems.push(...measured(spec, message.metrics));
          const settle = () => {
            lateTimer = window.setTimeout(finish, LATE);
          };
          if (spec.face === "detail") void followHeight(message.metrics.height).then(settle);
          else settle();
          return;
        }
        case "resize":
          measuring?.(message.height);
          return;
        case "error": {
          if (!KIT_CODES.has(message.code)) return;
          const problem: Outcome["problems"][number] = {
            code: message.code,
            message: message.message,
          };
          if (message.line !== undefined) problem.line = message.line;
          if (message.column !== undefined) problem.column = message.column;
          problems.push(problem);
          // A render that threw or ran out of time never draws.
          if (!ready && (message.code === "script_error" || message.code === "timeout")) finish();
          return;
        }
      }
    };
    const unlisten = listenToFrame(target, {
      heard,
      flooded: () => {
        problems.push({
          code: "render_failed",
          message: "The tile sent the page too many messages.",
        });
        finish();
      },
    });
    // Outside the session namespace, as the tiles page loads it.
    iframe.src = `/tile-frames/${job.digest.slice("sha256:".length)}`;
    helloTimer = window.setTimeout(() => {
      problems.push({ code: "no_hello", message: "The frame did not start within 5 seconds." });
      finish();
    }, HELLO_TIMEOUT);
  });
}

/** The citations of the pull request's data, resolved against that
 *  version's review and view as the review page resolves them; null when the
 *  document could not be read. */
async function dataRefs(
  job: CheckJob,
  data: unknown,
): Promise<{ glance: Record<string, unknown>; detail: Record<string, unknown> } | null> {
  const context = job.context;
  if (!context) return null;
  try {
    const query = new URLSearchParams({
      clone: context.clone,
      pr: String(context.pr),
      ...(context.version ? { version: String(context.version) } : {}),
    });
    const response = await fetch(appURL(`api/tiles?${query}`), { cache: "no-store" });
    if (!response.ok) return null;
    const body = (await response.json()) as { document?: ReviewDocument; view?: unknown };
    const review = body.document;
    if (!review?.review || !Array.isArray(review.sections) || !review.index) return null;
    const source = tileSourceOf(review, parseReviewView(body.view)?.tiles ?? []);
    const labels = new Map<string, string>();
    return {
      glance: resolveRefs(data, source, "glance", labels).refs,
      detail: resolveRefs(data, source, "detail", labels).refs,
    };
  } catch {
    return null;
  }
}

/** Draws a job's matrix in `stage`, AT_ONCE frames at a time. */
export async function runJob(
  job: CheckJob,
  stage: HTMLElement,
  draw: typeof drawFrame = drawFrame,
  onFrame: () => void = () => {},
): Promise<CheckReport> {
  const refs = new Map<
    CheckSample,
    { glance: Record<string, unknown>; detail: Record<string, unknown> } | null
  >();
  for (const sample of job.samples)
    refs.set(sample, sample.refs ?? (await dataRefs(job, sample.data)));
  const specs = matrix(job);
  const problems: CheckProblem[] = [];
  const runs: CheckRun[] = [];
  let next = 0;
  const worker = async () => {
    while (next < specs.length) {
      const spec = specs[next++]!;
      const resolved = refs.get(spec.sample);
      const outcome = await draw(stage, spec, job, resolved?.[spec.face] ?? {});
      runs.push(outcome.run);
      onFrame();
      for (const problem of outcome.problems) {
        // Without the version's document, the data's citations could not be
        // resolved: that is not the tile's fault.
        if (problem.code === "unresolved_citation" && !resolved) continue;
        problems.push({
          ...problem,
          sample: spec.sample.name,
          face: spec.face,
          size: spec.size,
          theme: spec.theme,
        });
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(AT_ONCE, specs.length) }, worker));
  return { problems, runs, engine: navigator.userAgent };
}

const isJob = (value: unknown): value is CheckJob =>
  typeof value === "object" &&
  value !== null &&
  typeof (value as CheckJob).id === "string" &&
  typeof (value as CheckJob).digest === "string" &&
  Array.isArray((value as CheckJob).samples) &&
  Array.isArray((value as CheckJob).sizes);

/** Claims the next job, waiting up to `wait` seconds; null when none came. */
export async function claim(wait: number, signal?: AbortSignal): Promise<CheckJob | null> {
  const response = await fetch(appURL(`api/tile-checks/next?wait=${wait}`), {
    cache: "no-store",
    signal,
  });
  if (response.status !== 200) return null;
  const body: unknown = await response.json();
  return isJob(body) ? body : null;
}

export async function report(id: string, body: CheckReport): Promise<void> {
  await fetch(appURL(`api/tile-checks/${encodeURIComponent(id)}/result`), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

/** A turn of the event loop that is neither a timer nor an animation frame,
 *  which a hidden window does not slow. */
export function turn(): Promise<void> {
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    channel.port1.onmessage = () => {
      channel.port1.close();
      resolve();
    };
    channel.port2.postMessage(null);
  });
}

/** How often, at most and at least, the runner tells the daemon it is still
 *  drawing a job. A hidden window runs timers about once a second, which is
 *  enough; a page that froze sends nothing, and the daemon calls it a hang. */
const BEAT = 2000;

/** Tells the daemon the runner is alive while it draws job `id`: every BEAT
 *  ms and as frames finish (at most once a second). If the page goes away
 *  meanwhile, it reports the job abandoned, so the daemon ends it as
 *  unavailable rather than blocking a type that did nothing wrong. Returns
 *  the function that stops it, with `frame` to call as each frame ends. */
export function heartbeat(id: string): (() => void) & { frame: () => void } {
  let last = 0;
  const beat = () => {
    last = performance.now();
    void fetch(appURL(`api/tile-checks/${encodeURIComponent(id)}/alive`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    }).catch(() => undefined);
  };
  const timer = window.setInterval(beat, BEAT);
  const gone = () => {
    void fetch(appURL(`api/tile-checks/${encodeURIComponent(id)}/result`), {
      method: "PUT",
      keepalive: true,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        problems: [],
        runs: [],
        engine: navigator.userAgent,
        abandoned: true,
      }),
    }).catch(() => undefined);
  };
  window.addEventListener("pagehide", gone);
  const stop = () => {
    window.clearInterval(timer);
    window.removeEventListener("pagehide", gone);
  };
  return Object.assign(stop, {
    frame: () => {
      if (performance.now() - last >= 1000) beat();
    },
  });
}

/** A container for check frames: fixed inside the viewport (engines throttle
 *  frames off screen), invisible and out of the reader's way. */
export function makeStage(): HTMLElement {
  const stage = document.createElement("div");
  stage.dataset.tileCheckStage = "";
  stage.setAttribute("aria-hidden", "true");
  stage.style.cssText =
    "position:fixed;inset:0;opacity:0;pointer-events:none;z-index:-1;overflow:hidden;display:flex;flex-wrap:wrap;align-items:flex-start;gap:8px";
  return stage;
}

/** Claims and draws jobs until none comes within `wait` seconds (or, with
 *  `forever`, until `signal` ends). */
export async function drain(
  stage: HTMLElement,
  {
    wait,
    forever = false,
    signal,
    onJob,
  }: {
    wait: number;
    forever?: boolean;
    signal?: AbortSignal;
    onJob?: (job: CheckJob | null) => void;
  },
): Promise<void> {
  while (!signal?.aborted) {
    let job: CheckJob | null = null;
    try {
      job = await claim(wait, signal);
    } catch {
      if (signal?.aborted || !forever) return;
      // The daemon is restarting: try again in a moment.
      await new Promise((resolve) => setTimeout(resolve, 2000));
      continue;
    }
    if (!job) {
      if (forever) continue;
      return;
    }
    onJob?.(job);
    const stopBeating = heartbeat(job.id);
    const result = await runJob(job, stage, drawFrame, stopBeating.frame);
    stopBeating();
    try {
      await report(job.id, result);
    } catch {
      // The job hung or the daemon went: nothing to tell it.
    }
    onJob?.(null);
  }
}

/** Draws checks inside the page, from the first `tile_check` event until
 *  the queue is empty (a browser, the macOS shell). A hang there freezes the
 *  page until the daemon gives up on it; only the check window avoids that. */
let draining: Promise<void> | null = null;
let again = false;
export function runInPage() {
  if (draining) {
    again = true;
    return;
  }
  const stage = makeStage();
  document.body.append(stage);
  draining = (async () => {
    do {
      again = false;
      await drain(stage, { wait: 1 });
    } while (again);
  })().finally(() => {
    stage.remove();
    draining = null;
  });
}
setInPageRunner(() => runInPage());

/** `?page=tile-check`: the page the desktop shell loads in its hidden check
 *  window. It claims and draws jobs for as long as it is open. */
export function TileCheckPage() {
  const stage = useRef<HTMLDivElement>(null);
  const [current, setCurrent] = useState<string | null>(null);
  useEffect(() => {
    const element = stage.current;
    if (!element) return;
    const stop = new AbortController();
    void drain(element, {
      wait: 25,
      forever: true,
      signal: stop.signal,
      onJob: (job) => setCurrent(job?.id ?? null),
    });
    return () => stop.abort();
  }, []);
  return (
    <main className="p-6 text-base text-secondary">
      <h1 className="text-lg font-semibold text-primary">Checking tiles</h1>
      <p data-tile-check-current={current ?? ""}>
        {current ? "Drawing a tile to check it." : "Waiting for a tile to check."}
      </p>
      <div
        ref={stage}
        data-tile-check-stage=""
        aria-hidden="true"
        style={{
          position: "fixed",
          inset: 0,
          opacity: 0,
          pointerEvents: "none",
          zIndex: -1,
          overflow: "hidden",
          display: "flex",
          flexWrap: "wrap",
          alignItems: "flex-start",
          gap: 8,
        }}
      />
    </main>
  );
}
