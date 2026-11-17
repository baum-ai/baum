import { createElement, Profiler, type ReactNode } from "react";
import {
  createCollector,
  keyLabel,
  requestPath,
  targetLabel,
  type Collector,
  type Interaction,
} from "./perf-collector";

declare global {
  interface Window {
    /** The profiling build's measurements; see `perf-collector.ts`. */
    __baumPerf?: Collector;
  }
}

/** The collector in a profiling build (`pnpm build:profile`), undefined in
 *  every other: call sites write `perf?.…`, and the build leaves out the rest. */
export const perf: Collector | undefined =
  import.meta.env.MODE === "profile" ? install() : undefined;

/** Counts the renders of `children` as region `id` in a profiling build. */
export function Profiled({ id, children }: { id: string; children: ReactNode }) {
  if (!perf) return children;
  return createElement(Profiler, { id, onRender: onRender }, children);
}
function onRender(
  id: string,
  phase: "mount" | "update" | "nested-update",
  actual: number,
  base: number,
) {
  perf?.render(id, phase, actual, base);
}

function install(): Collector {
  const collector = createCollector({
    now: () => performance.now(),
    page: () => new URLSearchParams(location.search).get("page") ?? "inbox",
    userAgent: navigator.userAgent,
    dom: () => document.getElementsByTagName("*").length,
    heap: () =>
      (performance as Performance & { memory?: { usedJSHeapSize: number } }).memory
        ?.usedJSHeapSize ?? null,
  });
  window.__baumPerf = collector;
  collector.navigation(location.search);

  // Navigations: the app writes the address through history.
  let search = location.search;
  const changed = () => {
    if (location.search === search) return;
    search = location.search;
    collector.navigation(search);
  };
  for (const method of ["pushState", "replaceState"] as const) {
    const original = history[method].bind(history);
    history[method] = (...args: Parameters<History["pushState"]>) => {
      original(...args);
      changed();
    };
  }
  addEventListener("popstate", changed);

  // Requests, to their headers: a body may stream for as long as the page lives.
  const fetchOriginal = window.fetch.bind(window);
  window.fetch = async (input, init) => {
    const url = input instanceof Request ? input.url : String(input);
    const method = init?.method ?? (input instanceof Request ? input.method : "GET");
    const done = collector.request(method, requestPath(url, location.href));
    try {
      const response = await fetchOriginal(input, init);
      const length = response.headers.get("Content-Length");
      done(response.status, length === null ? null : Number(length));
      return response;
    } catch (error) {
      done(0, null);
      throw error;
    }
  };

  // Interactions: from the input to the frame drawn after it. The capture
  // listener runs first and the window's bubble listener after React's root,
  // so between them are the handlers and any render they flushed.
  const pending = new WeakMap<Event, Interaction & { seen: number }>();
  const capture = (event: Event) => {
    const seen = performance.now();
    const record = {
      type: event.type as Interaction["type"],
      target: event instanceof KeyboardEvent ? keyLabel(event) : targetLabel(event.target),
      start: event.timeStamp,
      delay: seen - event.timeStamp,
      duration: 0,
      seen,
    };
    pending.set(event, record);
    requestAnimationFrame(() =>
      afterPaint(() => {
        const { seen: _, ...done } = record;
        collector.interaction({ ...done, duration: performance.now() - record.start });
      }),
    );
  };
  const bubble = (event: Event) => {
    const record = pending.get(event);
    if (record) record.processing = performance.now() - record.seen;
  };
  for (const type of ["keydown", "click"]) {
    addEventListener(type, capture, { capture: true });
    addEventListener(type, bubble);
  }

  const tick = (at: number) => {
    collector.frame(at);
    requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
  return collector;
}

/** Runs `then` once the frame being prepared has been drawn: a message posted
 *  from an animation frame callback is handled after that frame's paint. */
function afterPaint(then: () => void) {
  const channel = new MessageChannel();
  channel.port1.onmessage = () => {
    channel.port1.close();
    then();
  };
  channel.port2.postMessage(null);
}
