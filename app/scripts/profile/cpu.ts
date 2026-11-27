// CPU profiles from `just desktop::profile`, apart from the
// browser so they can be tested (cpu.test.ts): reading the build's source
// maps, naming each sampled function by its source, adding up self and total
// time, lining the profile's clock up with the page's, and the stacks behind
// the slowest interactions and frames. electron.ts records the profiles.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import type { Interaction, LongFrame, Snapshot } from "../../src/perf-collector.ts";
import { table } from "./summary.ts";

/** A CPU profile as Chrome DevTools Protocol's Profiler.stop returns it, and
 *  as a .cpuprofile file holds it. Times are microseconds. */
export type CpuProfile = {
  nodes: CpuNode[];
  startTime: number;
  endTime: number;
  samples?: number[];
  timeDeltas?: number[];
  /** Added by the runner: the profile's clock less the page's
   *  performance.now(), in microseconds (clockOffset). */
  baumClockOffset?: number;
};
export type CpuNode = {
  id: number;
  callFrame: CallFrame;
  children?: number[];
};
export type CallFrame = {
  functionName: string;
  url: string;
  /** 0-based, as are columns. */
  lineNumber: number;
  columnNumber: number;
  scriptId?: string;
};

/** What a sample was doing, for the breakdowns. */
export type Category = "js" | "program" | "gc" | "idle";

/** A function as the summary names it. */
export type Frame = {
  name: string;
  /** `oss/app/src/review/diff.tsx:120`, a package (`react-dom`), `browser`
   *  for the DOM's and V8's own functions, or a bundle location when no
   *  source map knows it. */
  where: string;
  /** The npm package it comes from, when it does. */
  package?: string;
  category: Category;
  /** What tells it apart from other functions of the same name and place:
   *  a package's functions by their original file and line, which `where`
   *  leaves out. */
  key?: string;
};

// --- Source maps -----------------------------------------------------------

const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const DIGIT = new Int8Array(128).fill(-1);
for (let i = 0; i < BASE64.length; i++) DIGIT[BASE64.charCodeAt(i)] = i;

/** One mapping: the generated column, then (when it has one) the source
 *  index, original line and column, and name index, all 0-based. */
export type Segment = number[];

/** Decodes a source map's `mappings` (Base64 VLQ) into segments per
 *  generated line, each line's in column order. */
export function decodeMappings(mappings: string): Segment[][] {
  const lines: Segment[][] = [];
  let line: Segment[] = [];
  // Every field but the generated column carries on across lines.
  let source = 0;
  let sourceLine = 0;
  let sourceColumn = 0;
  let name = 0;
  let column = 0;
  let segment: number[] = [];
  let value = 0;
  let shift = 0;
  const end = () => {
    if (segment.length === 0) return;
    column += segment[0]!;
    const out = [column];
    if (segment.length >= 4) {
      source += segment[1]!;
      sourceLine += segment[2]!;
      sourceColumn += segment[3]!;
      out.push(source, sourceLine, sourceColumn);
      if (segment.length >= 5) {
        name += segment[4]!;
        out.push(name);
      }
    }
    line.push(out);
    segment = [];
  };
  for (let i = 0; i < mappings.length; i++) {
    const code = mappings.charCodeAt(i);
    if (code === 44 /* , */) {
      end();
      continue;
    }
    if (code === 59 /* ; */) {
      end();
      lines.push(line);
      line = [];
      column = 0;
      continue;
    }
    const digit = code < 128 ? DIGIT[code]! : -1;
    if (digit < 0) throw new Error(`invalid source map mappings at ${i}`);
    value += (digit & 31) << shift;
    if (digit & 32) {
      shift += 5;
      continue;
    }
    segment.push(value & 1 ? -(value >>> 1) : value >>> 1);
    value = 0;
    shift = 0;
  }
  end();
  lines.push(line);
  for (const l of lines) l.sort((a, b) => a[0]! - b[0]!);
  return lines;
}

export type RawSourceMap = {
  version: number;
  sources: string[];
  names?: string[];
  mappings: string;
  sourceRoot?: string;
};

export type Original = { source: string; line: number; column: number; name?: string };

/** A bundle's source map, with the bundle's text to find names in. */
export class SourceMap {
  readonly sources: string[];
  readonly names: string[];
  private readonly lines: Segment[][];
  private readonly generated: string[];

  /** `sources` come resolved (see `resolveSources`); `code` is the bundle. */
  constructor(raw: RawSourceMap, code = "", sources = raw.sources) {
    this.sources = sources;
    this.names = raw.names ?? [];
    this.lines = decodeMappings(raw.mappings);
    this.generated = code.split("\n");
  }

  /** The last mapping at or before 0-based `line` and `column`, and its
   *  index among the line's mappings. */
  private at(line: number, column: number): [Segment[], number] | undefined {
    const segments = this.lines[line];
    if (!segments?.length) return undefined;
    let low = 0;
    let high = segments.length - 1;
    let found = -1;
    while (low <= high) {
      const mid = (low + high) >> 1;
      if (segments[mid]![0]! <= column) {
        found = mid;
        low = mid + 1;
      } else high = mid - 1;
    }
    return found < 0 ? undefined : [segments, found];
  }

  /** Where 0-based `line` and `column` of the bundle came from, its line
   *  1-based. */
  original(line: number, column: number): Original | undefined {
    const hit = this.at(line, column);
    if (!hit) return undefined;
    const segment = hit[0][hit[1]]!;
    if (segment.length < 4) return undefined;
    return {
      source: this.sources[segment[1]!] ?? "?",
      line: segment[2]! + 1,
      column: segment[3]!,
      ...(segment.length >= 5 ? { name: this.names[segment[4]!] } : {}),
    };
  }

  /** The original name of the function V8 calls `minified`, which starts at
   *  0-based `line` and `column` of the bundle. V8 places a function at its
   *  parameters, so its name is one of the mappings just before: the one
   *  whose text in the bundle is `minified`. A name the minifier kept
   *  (a method, a property) maps to no name and stays. Undefined when no
   *  mapping nearby has the name's text. */
  functionName(line: number, column: number, minified: string): string | undefined {
    const token = minified.split(/[ .]/).at(-1) ?? "";
    if (!token) return minified;
    const hit = this.at(line, column);
    const text = this.generated[line];
    if (!hit || text === undefined) return undefined;
    const [segments, index] = hit;
    for (let i = index; i >= 0 && i > index - 8; i--) {
      const segment = segments[i]!;
      const at = segment[0]!;
      if (text.startsWith(token, at) && !/[\w$]/.test(text[at + token.length] ?? "")) {
        const name = segment.length >= 5 ? this.names[segment[4]!] : undefined;
        return name && name !== token
          ? minified.replace(new RegExp(`${escape(token)}$`), name)
          : minified;
      }
    }
    return undefined;
  }
}

const escape = (text: string) => text.replace(/[$^.*+?()[\]{}|\\]/g, "\\$&");

/** Sources as absolute paths: against the map's own directory and its
 *  sourceRoot, as a browser resolves them. Virtual modules (`\0…`) stay. */
export function resolveSources(raw: RawSourceMap, mapFile: string): string[] {
  const base = join(dirname(mapFile), raw.sourceRoot ?? "");
  return raw.sources.map((source) =>
    source.startsWith("\0") || /^[a-z]+:/i.test(source) ? source : resolve(base, source),
  );
}

/** The npm package a source path is in: the part after its last
 *  node_modules, scoped packages whole. */
export function packageOf(path: string): string | undefined {
  const at = path.lastIndexOf("node_modules/");
  if (at < 0) return undefined;
  const parts = path.slice(at + "node_modules/".length).split("/");
  return parts[0]!.startsWith("@") ? `${parts[0]}/${parts[1]}` : parts[0];
}

/** A source path as the summary shows it: from the repository's oss/ on. */
export function displayPath(path: string): string {
  const at = path.indexOf("/oss/");
  if (at >= 0) return path.slice(at + 1);
  return path.startsWith("\0") ? path.slice(1) : path;
}

// --- Naming the profile's functions -------------------------------------------

const SPECIAL: Record<string, Category> = {
  "(program)": "program",
  "(garbage collector)": "gc",
  "(idle)": "idle",
  "(root)": "idle",
};

/** Names call frames by their source, through the build's source maps,
 *  which it loads as it meets each bundle. */
export class Symbolizer {
  private readonly maps = new Map<string, SourceMap | null>();
  private readonly frames = new Map<string, Frame>();
  private readonly load: (file: string) => SourceMap | null;

  /** `load(file)` returns the source map and code of the bundle file named
   *  `file` (`index-abc.js`), or null when there is none. */
  constructor(load: (file: string) => SourceMap | null) {
    this.load = load;
  }

  /** For the build in `dist` (the profiling build, which has maps). */
  static forDist(dist: string | undefined) {
    return new Symbolizer((file) => {
      if (!dist) return null;
      const path = join(dist, "assets", file);
      if (!existsSync(`${path}.map`)) return null;
      const raw = JSON.parse(readFileSync(`${path}.map`, "utf8")) as RawSourceMap;
      const code = existsSync(path) ? readFileSync(path, "utf8") : "";
      return new SourceMap(raw, code, resolveSources(raw, `${path}.map`));
    });
  }

  private map(url: string): SourceMap | null {
    const file = url.match(/\/assets\/([^/?#]+\.js)(?:[?#]|$)/)?.[1];
    if (!file) return null;
    if (!this.maps.has(file)) this.maps.set(file, this.load(file));
    return this.maps.get(file) ?? null;
  }

  frame(call: CallFrame): Frame {
    const key = `${call.url}\n${call.lineNumber}\n${call.columnNumber}\n${call.functionName}`;
    let frame = this.frames.get(key);
    if (!frame) {
      frame = this.name(call);
      this.frames.set(key, frame);
    }
    return frame;
  }

  private name(call: CallFrame): Frame {
    const special = SPECIAL[call.functionName];
    if (special && !call.url) return { name: call.functionName, where: "", category: special };
    const anonymous = call.functionName || "(anonymous)";
    // The browser's own functions JavaScript calls (getBoundingClientRect,
    // scrollIntoView): their time includes the style and layout they force.
    if (!call.url) return { name: anonymous, where: "browser", category: "js" };
    const map = this.map(call.url);
    const original = map?.original(call.lineNumber, call.columnNumber);
    const bundled = `${call.url.replace(/^[a-z]+:\/\/[^/]+/, "").replace(/^\/s\/[^/]+\//, "/")}:${call.lineNumber + 1}:${call.columnNumber + 1}`;
    if (!map || !original) return { name: anonymous, where: bundled, category: "js" };
    const resolved = call.functionName
      ? map.functionName(call.lineNumber, call.columnNumber, call.functionName)
      : anonymous;
    const name = resolved ?? call.functionName;
    const pkg = packageOf(original.source);
    const source = `${displayPath(original.source)}:${original.line}`;
    // A minified name the map could not resolve says nothing on its own.
    const where = (place: string) => (resolved === undefined ? `${place}, ${bundled}` : place);
    return pkg
      ? { name, where: where(pkg), package: pkg, category: "js", key: `${source}\n${name}` }
      : { name, where: where(source), category: "js" };
  }
}

// --- Samples ---------------------------------------------------------------

/** A profile ready to add up: each sample's node and time, and each
 *  node's frame and parent. */
export type Timeline = {
  /** Sample times, microseconds on the profile's clock. */
  times: Float64Array;
  /** Each sample's node, as an index into `frames` and `parents`. */
  nodes: Int32Array;
  /** What each sample stands for: until the next sample (the last, the
   *  median interval), as DevTools counts it. */
  weights: Float64Array;
  frames: Frame[];
  /** Each node's parent index, -1 for the root. */
  parents: Int32Array;
  interval: number;
};

export function timeline(profile: CpuProfile, symbolize: Symbolizer): Timeline {
  const index = new Map<number, number>();
  profile.nodes.forEach((node, i) => index.set(node.id, i));
  const parents = new Int32Array(profile.nodes.length).fill(-1);
  profile.nodes.forEach((node, i) => {
    for (const child of node.children ?? []) {
      const at = index.get(child);
      if (at !== undefined) parents[at] = i;
    }
  });
  const frames = profile.nodes.map((node) => symbolize.frame(node.callFrame));
  const samples = profile.samples ?? [];
  const deltas = profile.timeDeltas ?? [];
  const times = new Float64Array(samples.length);
  const nodes = new Int32Array(samples.length);
  let at = profile.startTime;
  for (let i = 0; i < samples.length; i++) {
    // A negative delta (a clock step) would put samples out of order.
    at += Math.max(0, deltas[i] ?? 0);
    times[i] = at;
    nodes[i] = index.get(samples[i]!) ?? 0;
  }
  const gaps = deltas
    .slice(1)
    .filter((d) => d > 0)
    .sort((a, b) => a - b);
  const interval = gaps.length ? gaps[gaps.length >> 1]! : 0;
  const weights = new Float64Array(samples.length);
  for (let i = 0; i < samples.length; i++)
    weights[i] = i + 1 < samples.length ? times[i + 1]! - times[i]! : interval;
  return { times, nodes, weights, frames, parents, interval };
}

/** The first sample at or after `time`. */
function firstAt(times: Float64Array, time: number) {
  let low = 0;
  let high = times.length;
  while (low < high) {
    const mid = (low + high) >> 1;
    if (times[mid]! < time) low = mid + 1;
    else high = mid;
  }
  return low;
}

/** The function the runner runs in the page to line the clocks up. */
export const CLOCK_FUNCTION = "__baumProfileClock";

/** The page script that runs CLOCK_FUNCTION, busy for `ms`, and returns
 *  the performance.now() it started and ended at. */
export const clockScript = (ms = 30) => `
  function ${CLOCK_FUNCTION}() {
    const start = performance.now();
    let end = start;
    while (end - start < ${ms}) end = performance.now();
    return [start, end];
  }
  return ${CLOCK_FUNCTION}();`;

/** The fewest samples of CLOCK_FUNCTION to line the clocks up from: its
 *  30 ms give some 120 at 250 µs. */
export const CLOCK_MIN_SAMPLES = 10;

/** The profile's clock less the page's performance.now(), in microseconds,
 *  from the samples taken while CLOCK_FUNCTION ran from `start` to `end`
 *  (page milliseconds). Its first sample is no earlier than `start` and its
 *  last no later than `end`, so the mean of the two is within half a
 *  sampling interval. Undefined when too few samples caught it or they
 *  span too little of it. */
export function clockOffset(line: Timeline, start: number, end: number): number | undefined {
  const marked = new Uint8Array(line.frames.length);
  line.frames.forEach((frame, i) => {
    if (frame.name === CLOCK_FUNCTION) marked[i] = 1;
  });
  const onStack = (node: number) => {
    for (let n = node; n >= 0; n = line.parents[n]!) if (marked[n]) return true;
    return false;
  };
  let first: number | undefined;
  let last: number | undefined;
  let count = 0;
  for (let i = 0; i < line.times.length; i++) {
    if (!onStack(line.nodes[i]!)) continue;
    count++;
    first ??= line.times[i]!;
    last = line.times[i]!;
  }
  // Too few samples, or a sampler starved for part of the spin (shared
  // CPUs), would put the offset milliseconds out: such a run is left
  // unaligned rather than attributed wrongly.
  const span = (end - start) * 1000;
  if (first === undefined || last === undefined || count < CLOCK_MIN_SAMPLES) return undefined;
  if (last - first < 0.8 * span) return undefined;
  return (first - start * 1000 + (last - end * 1000)) / 2;
}

// --- Adding up ---------------------------------------------------------------

const frameKey = (frame: Frame) => frame.key ?? `${frame.name}\n${frame.where}`;

export type FunctionTime = Frame & {
  /** Microseconds, summed over the windows given. */
  self: number;
  total: number;
};

/** Self and total time per function over the samples of each timeline from
 *  `from` to `to` (profile microseconds; all of it when absent). A function
 *  on a stack more than once counts once toward its total. */
export function aggregate(
  windows: { line: Timeline; from?: number; to?: number }[],
): FunctionTime[] {
  const out = new Map<string, FunctionTime>();
  for (const { line, from = -Infinity, to = Infinity } of windows) {
    const self = new Float64Array(line.frames.length);
    for (let i = firstAt(line.times, from); i < line.times.length && line.times[i]! < to; i++)
      self[line.nodes[i]!]! += line.weights[i]!;
    const keys = line.frames.map(frameKey);
    for (let n = 0; n < line.frames.length; n++) {
      if (!self[n]) continue;
      const seen = new Set<string>();
      for (let a = n; a >= 0; a = line.parents[a]!) {
        const key = keys[a]!;
        let entry = out.get(key);
        if (!entry) {
          entry = { ...line.frames[a]!, self: 0, total: 0 };
          out.set(key, entry);
        }
        if (a === n) entry.self += self[n]!;
        if (!seen.has(key)) {
          seen.add(key);
          entry.total += self[n]!;
        }
      }
    }
  }
  return [...out.values()].filter((f) => f.category !== "idle").toSorted((a, b) => b.self - a.self);
}

export type Breakdown = Record<Category, number>;

export function breakdown(line: Timeline, from = -Infinity, to = Infinity): Breakdown {
  const out: Breakdown = { js: 0, program: 0, gc: 0, idle: 0 };
  for (let i = firstAt(line.times, from); i < line.times.length && line.times[i]! < to; i++)
    out[line.frames[line.nodes[i]!]!.category] += line.weights[i]!;
  return out;
}

/** How a stack reads: innermost first, up to `depth` frames. A run of
 *  frames from the same package after the first shows as one. */
export function stackText(line: Timeline, node: number, depth = 8): string {
  const parts: string[] = [];
  let previous: string | undefined;
  let run: Frame[] = [];
  const flush = () => {
    if (run.length === 1) parts.push(frameText(run[0]!));
    else if (run.length > 1) parts.push(`[${previous} ×${run.length}]`);
    run = [];
  };
  for (let n = node; n >= 0 && parts.length < depth; n = line.parents[n]!) {
    const frame = line.frames[n]!;
    if (frame.category === "idle") break;
    if (frame.package && frame.package === previous && parts.length > 0) {
      run.push(frame);
      continue;
    }
    flush();
    parts.push(frameText(frame));
    previous = frame.package;
  }
  flush();
  return parts.slice(0, depth).join(" ← ");
}

export const frameText = (frame: Frame) =>
  frame.where ? `\`${frame.name}\` (${frame.where})` : `\`${frame.name}\``;

/** The heaviest JavaScript stacks from `from` to `to`. */
export function topStacks(line: Timeline, from: number, to: number, limit = 3, depth = 8) {
  const byNode = new Map<number, number>();
  for (let i = firstAt(line.times, from); i < line.times.length && line.times[i]! < to; i++) {
    const node = line.nodes[i]!;
    if (line.frames[node]!.category !== "js") continue;
    byNode.set(node, (byNode.get(node) ?? 0) + line.weights[i]!);
  }
  const byText = new Map<string, number>();
  for (const [node, weight] of byNode) {
    const text = stackText(line, node, depth);
    byText.set(text, (byText.get(text) ?? 0) + weight);
  }
  return [...byText]
    .map(([text, time]) => ({ text, time }))
    .toSorted((a, b) => b.time - a.time)
    .slice(0, limit);
}

// --- The summary's section ---------------------------------------------------------

/** A measured run's profile and what the page recorded. */
export type CpuRun = {
  index: number;
  line: Timeline;
  /** Undefined when the clocks could not be lined up. */
  offset?: number;
  snapshot: Snapshot;
  file: string;
  /** Page time the journey starts at, when not the collector's reset: 0,
   *  the page's own start, for a run that launched the browser. */
  from?: number;
};

const ms = (us: number) => (us / 1000).toFixed(1);
const share = (part: number, whole: number) =>
  whole > 0 ? `${Math.round((100 * part) / whole)}%` : "–";

/** A window of page time (ms) on the run's profile clock (µs). */
const window = (run: CpuRun, start: number, duration: number) =>
  [start * 1000 + run.offset!, (start + duration) * 1000 + run.offset!] as const;

function explain(run: CpuRun, from: number, to: number, stacks: number): string[] {
  const parts = breakdown(run.line, from, to);
  const lines = [
    `JavaScript ${ms(parts.js)} ms, outside it (style, layout, paint, the browser) ${ms(parts.program)} ms, ` +
      `garbage collection ${ms(parts.gc)} ms, idle ${ms(parts.idle)} ms.`,
  ];
  const top = aggregate([{ line: run.line, from, to }])
    .filter((f) => f.category === "js")
    .slice(0, 3);
  if (top.length)
    lines.push(`Self: ${top.map((f) => `${frameText(f)} ${ms(f.self)} ms`).join(", ")}.`);
  for (const stack of topStacks(run.line, from, to, stacks))
    lines.push(`- ${ms(stack.time)} ms: ${stack.text}`);
  return lines;
}

/** Lines of summary.md for the profiles of the measured runs. */
export function cpuSection(
  runs: CpuRun[],
  intervalUs: number,
  limits = { functions: 20, interactions: 5, frames: 3 },
): string {
  if (!runs.length) return "";
  // The journey: from the collector's reset to its snapshot.
  const journeys = runs.map((run) =>
    run.offset === undefined
      ? { line: run.line }
      : {
          line: run.line,
          from: (run.from ?? run.snapshot.since) * 1000 + run.offset,
          to: run.snapshot.now * 1000 + run.offset,
        },
  );
  const sums: Breakdown = { js: 0, program: 0, gc: 0, idle: 0 };
  for (const j of journeys) {
    const b = breakdown(j.line, j.from, j.to);
    for (const key of Object.keys(sums) as Category[]) sums[key] += b[key];
  }
  const all = sums.js + sums.program + sums.gc + sums.idle;
  const per = (us: number) => ms(us / runs.length);
  const lines: string[] = [
    "",
    "## CPU (Chromium)",
    "",
    `The renderer's main thread, sampled every ${intervalUs} µs in each measured run (not the warm-up), ` +
      "from the collector's reset (a cold start's page load) to its snapshot. Functions are named by the profiling build's " +
      "source maps, at the line the function starts on, not the hot line inside it; a package's frames by their package; `browser` marks the DOM's and V8's own " +
      "functions, whose time includes the style and layout they force. Times are per run. " +
      `${per(all)} ms a run: JavaScript ${share(sums.js, all)}, outside it ${share(sums.program, all)} ` +
      `(style, layout, paint and the browser's own work, \`(program)\`), garbage collection ${share(sums.gc, all)}, ` +
      `idle ${share(sums.idle, all)}. Idle time within an interaction or frame is spent off this ` +
      "thread (raster, compositing, the driver). Load " +
      `${runs.map((r) => `\`${r.file}\``).join(", ")} in DevTools (Performance, Load profile) or ` +
      "speedscope for the full timeline.",
  ];
  const unaligned = runs.filter((r) => r.offset === undefined).map((r) => r.index);
  if (unaligned.length)
    lines.push(
      "",
      `Runs ${unaligned.join(", ")} could not line the profile's clock up with the page's, so ` +
        "they count whole and their interactions and frames are left out below.",
    );
  const functions = aggregate(journeys).slice(0, limits.functions);
  lines.push(
    "",
    "### Functions by self time",
    "",
    ...table(
      ["function", "where (function starts at)", "self ms", "share", "total ms"],
      functions.map((f) => [
        `\`${f.name}\``,
        f.where || "–",
        per(f.self),
        share(f.self, all - sums.idle),
        per(f.total),
      ]),
    ),
  );

  const aligned = runs.filter((r) => r.offset !== undefined);
  type Slow = { run: CpuRun; title: string; start: number; duration: number };
  const interactions: Slow[] = aligned
    .flatMap((run) =>
      run.snapshot.interactions.map((i: Interaction) => ({
        run,
        title: `${i.type} ${i.target}`,
        start: i.start,
        duration: i.duration,
      })),
    )
    .toSorted((a, b) => b.duration - a.duration)
    .slice(0, limits.interactions);
  const frames: Slow[] = aligned
    .flatMap((run) =>
      run.snapshot.frames.long.map((f: LongFrame) => ({
        run,
        title: "frame",
        start: f.start,
        duration: f.duration,
      })),
    )
    .toSorted((a, b) => b.duration - a.duration)
    .slice(0, limits.frames);
  const describe = (heading: string, intro: string, list: Slow[]) => {
    if (!list.length) return;
    lines.push("", `### ${heading}`, "", intro);
    for (const slow of list) {
      const [from, to] = window(slow.run, slow.start, slow.duration);
      lines.push(
        "",
        `**${slow.title}**, run ${slow.run.index}, ${slow.duration.toFixed(1)} ms at ${slow.start.toFixed(0)} ms. ` +
          explain(slow.run, from, to, 3).join("\n"),
      );
    }
  };
  describe(
    "Slowest interactions",
    "From the input to the frame after it, as the Interactions table times them; each with its heaviest JavaScript stacks, innermost first.",
    interactions,
  );
  describe(
    "Longest frames",
    "Gaps between animation frames over 50 ms, with their heaviest JavaScript stacks.",
    frames,
  );
  return `${lines.join("\n")}\n`;
}
