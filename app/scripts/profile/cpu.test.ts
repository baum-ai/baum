import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it } from "node:test";
import type { Snapshot } from "../../src/perf-collector.ts";
import {
  aggregate,
  breakdown,
  CLOCK_FUNCTION,
  CLOCK_MIN_SAMPLES,
  clockOffset,
  cpuSection,
  decodeMappings,
  displayPath,
  packageOf,
  resolveSources,
  SourceMap,
  stackText,
  Symbolizer,
  timeline,
  topStacks,
  type CallFrame,
  type CpuProfile,
  type Segment,
} from "./cpu.ts";

const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

function vlq(value: number) {
  let v = value < 0 ? (-value << 1) | 1 : value << 1;
  let out = "";
  do {
    let digit = v & 31;
    v >>>= 5;
    if (v > 0) digit |= 32;
    out += BASE64[digit];
  } while (v > 0);
  return out;
}

/** Encodes absolute segments per line as a source map's mappings. */
function encode(lines: Segment[][]) {
  const last = [0, 0, 0, 0];
  return lines
    .map((line) => {
      let column = 0;
      return line
        .map((segment) => {
          const fields = [segment[0]! - column];
          column = segment[0]!;
          for (let i = 1; i < segment.length; i++) {
            fields.push(segment[i]! - last[i - 1]!);
            last[i - 1] = segment[i]!;
          }
          return fields.map(vlq).join("");
        })
        .join(",");
    })
    .join(";");
}

// A tiny bundle: line 0 `function Ke(a){return a}`, line 1
// `const Zt=(b)=>b;class Q{render(){}}`.
const CODE = "function Ke(a){return a}\nconst Zt=(b)=>b;class Q{render(){}};class P{render(){}}";
const SEGMENTS: Segment[][] = [
  [
    [0, 0, 9, 0],
    [9, 0, 9, 9, 0], // Ke -> highlight
    [11, 0, 9, 18],
  ],
  [
    [6, 0, 19, 6, 1], // Zt -> scrollTo
    [9, 0, 19, 15],
    [24, 1, 4, 2], // render, a kept name
    [30, 1, 4, 8],
    [44, 1, 40, 2], // another render in the same package
    [50, 1, 40, 8],
  ],
];
const RAW = {
  version: 3,
  sources: [
    "/repo/oss/app/src/review/diff.tsx",
    "/repo/oss/app/node_modules/.pnpm/react-dom@19.2.0/node_modules/react-dom/cjs/react-dom.js",
  ],
  names: ["highlight", "scrollTo"],
  mappings: encode(SEGMENTS),
};
const MAP = new SourceMap(RAW, CODE);

describe("source maps", () => {
  it("decodes Base64 VLQ mappings, empty lines included", () => {
    const lines: Segment[][] = [
      [
        [0, 0, 0, 0],
        [5, 1, 3, 2, 0],
      ],
      [],
      [
        [2, 1, 4, 0],
        [40, 0, 120, 7, 1],
      ],
    ];
    assert.deepEqual(decodeMappings(encode(lines)), lines);
    assert.deepEqual(decodeMappings(""), [[]]);
    assert.equal(vlq(-17), "jB");
    assert.deepEqual(decodeMappings("jB"), [[[-17]]]);
  });

  it("finds the original position at or before a column", () => {
    assert.deepEqual(MAP.original(0, 11), {
      source: "/repo/oss/app/src/review/diff.tsx",
      line: 10,
      column: 18,
    });
    assert.equal(MAP.original(0, 14)?.line, 10);
    assert.equal(MAP.original(1, 3), undefined);
    assert.equal(MAP.original(5, 0), undefined);
  });

  it("names a function by the mapping of its minified name", () => {
    // V8 places a function at its parameters, after the name.
    assert.equal(MAP.functionName(0, 11, "Ke"), "highlight");
    assert.equal(MAP.functionName(1, 9, "Zt"), "scrollTo");
    // A method name the minifier kept has no name of its own.
    assert.equal(MAP.functionName(1, 30, "render"), "render");
    assert.equal(MAP.functionName(1, 30, "Q.render"), "Q.render");
    // Not found near the position: unresolved.
    assert.equal(MAP.functionName(0, 11, "Xy"), undefined);
  });

  it("resolves sources against the map and its sourceRoot", () => {
    assert.deepEqual(
      resolveSources(
        { version: 3, sources: ["../../../../app/src/a.ts", "\0vite/preload"], mappings: "" },
        "/oss/internal/localapp/dist/assets/app.js.map",
      ),
      ["/oss/app/src/a.ts", "\0vite/preload"],
    );
    assert.deepEqual(
      resolveSources(
        { version: 3, sources: ["a.ts"], sourceRoot: "src/", mappings: "" },
        "/x/m.map",
      ),
      ["/x/src/a.ts"],
    );
    assert.equal(
      packageOf("/a/node_modules/.pnpm/react-dom@19/node_modules/react-dom/cjs/x.js"),
      "react-dom",
    );
    assert.equal(
      packageOf("/a/node_modules/@tanstack/react-virtual/dist/x.js"),
      "@tanstack/react-virtual",
    );
    assert.equal(packageOf("/oss/app/src/a.ts"), undefined);
    assert.equal(displayPath("/home/dev/repo/oss/app/src/a.ts"), "oss/app/src/a.ts");
  });

  it("finds the build's own maps", () => {
    // The real profiling build, when one is there; nothing to check otherwise.
    const dist = join(import.meta.dirname, "../../../internal/localapp/dist");
    let html = "";
    try {
      html = readFileSync(join(dist, "index.html"), "utf8");
    } catch {
      return;
    }
    const entry = html.match(/src="\.\/assets\/([^"]+\.js)"/)?.[1];
    if (!entry) return;
    const frame = Symbolizer.forDist(dist).frame({
      functionName: "",
      url: `http://127.0.0.1:1/s/x/assets/${entry}`,
      lineNumber: 0,
      columnNumber: 0,
    });
    assert.equal(frame.category, "js");
  });
});

const call = (functionName: string, url = "", lineNumber = 0, columnNumber = 0): CallFrame => ({
  functionName,
  url,
  lineNumber,
  columnNumber,
});
const BUNDLE = "http://127.0.0.1:4000/s/ns/assets/app-x.js";
const symbolizer = new Symbolizer((file) => (file === "app-x.js" ? MAP : null));

describe("naming frames", () => {
  it("maps a bundle's frames to source, packages by name", () => {
    assert.deepEqual(symbolizer.frame(call("Ke", BUNDLE, 0, 11)), {
      name: "highlight",
      where: "oss/app/src/review/diff.tsx:10",
      category: "js",
    });
    assert.deepEqual(symbolizer.frame(call("render", BUNDLE, 1, 30)), {
      name: "render",
      where: "react-dom",
      package: "react-dom",
      category: "js",
      key: "oss/app/node_modules/.pnpm/react-dom@19.2.0/node_modules/react-dom/cjs/react-dom.js:5\nrender",
    });
  });

  it("keeps a package's functions of the same name apart", () => {
    const line = timeline(
      profile([[call("render", BUNDLE, 1, 30)], [call("render", BUNDLE, 1, 50)]]),
      symbolizer,
    );
    const renders = aggregate([{ line }]).filter((f) => f.name === "render");
    assert.equal(renders.length, 2);
    assert.ok(renders.every((f) => f.where === "react-dom"));
  });

  it("shows the bundle's place for a name it cannot resolve", () => {
    assert.deepEqual(symbolizer.frame(call("Xy", BUNDLE, 0, 11)), {
      name: "Xy",
      where: "oss/app/src/review/diff.tsx:10, /assets/app-x.js:1:12",
      category: "js",
    });
  });

  it("keeps what it cannot map, and V8's own entries", () => {
    assert.equal(
      symbolizer.frame(call("", "http://127.0.0.1:4000/s/ns/assets/other.js", 3, 4)).where,
      "/assets/other.js:4:5",
    );
    assert.deepEqual(symbolizer.frame(call("(program)")), {
      name: "(program)",
      where: "",
      category: "program",
    });
    assert.equal(symbolizer.frame(call("(garbage collector)")).category, "gc");
    assert.equal(symbolizer.frame(call("parse")).where, "browser");
    assert.equal(symbolizer.frame(call("")).name, "(anonymous)");
  });
});

/** A profile from `stacks`, each sample's stack outermost first, one
 *  sample every `interval` µs from `start`. */
function profile(stacks: CallFrame[][], interval = 250, start = 1_000_000): CpuProfile {
  const nodes: CpuProfile["nodes"] = [{ id: 1, callFrame: call("(root)"), children: [] }];
  const samples: number[] = [];
  for (const stack of stacks) {
    let parent = nodes[0]!;
    for (const frame of stack) {
      let child = (parent.children ?? [])
        .map((id) => nodes.find((n) => n.id === id)!)
        .find((n) => JSON.stringify(n.callFrame) === JSON.stringify(frame));
      if (!child) {
        child = { id: nodes.length + 1, callFrame: frame, children: [] };
        nodes.push(child);
        parent.children!.push(child.id);
      }
      parent = child;
    }
    samples.push(parent.id);
  }
  return {
    nodes,
    startTime: start,
    endTime: start + stacks.length * interval,
    samples,
    timeDeltas: samples.map((_, i) => (i === 0 ? 0 : interval)),
  };
}

const A = call("Ke", BUNDLE, 0, 11); // highlight
const B = call("Zt", BUNDLE, 1, 9); // scrollTo
const R = call("render", BUNDLE, 1, 30); // react-dom's
const PROGRAM = call("(program)");
const GC = call("(garbage collector)");
const IDLE = call("(idle)");

describe("adding up", () => {
  it("gives self time to the innermost frame and total time once per stack", () => {
    const line = timeline(
      profile([[A], [A, B], [A, B, A], [A, B, A], [PROGRAM], [GC], [IDLE], [IDLE]]),
      symbolizer,
    );
    assert.equal(line.interval, 250);
    const byName = Object.fromEntries(aggregate([{ line }]).map((f) => [f.name, f]));
    // highlight: 3 samples innermost; on 4 stacks, counted once each.
    assert.equal(byName.highlight!.self, 750);
    assert.equal(byName.highlight!.total, 1000);
    assert.equal(byName.scrollTo!.self, 250);
    assert.equal(byName.scrollTo!.total, 750);
    assert.equal(byName["(program)"]!.self, 250);
    assert.equal(byName["(idle)"], undefined);
    assert.equal(aggregate([{ line }])[0]!.name, "highlight");
    assert.deepEqual(breakdown(line), { js: 1000, program: 250, gc: 250, idle: 500 });
  });

  it("counts only the samples in a window, across profiles", () => {
    const line = timeline(profile([[A], [A], [B], [B]]), symbolizer);
    // Samples at 1 000 000, …250, …500, …750 µs.
    const inWindow = aggregate([{ line, from: 1_000_250, to: 1_000_750 }]);
    assert.deepEqual(
      inWindow.map((f) => [f.name, f.self]),
      [
        ["highlight", 250],
        ["scrollTo", 250],
      ],
    );
    const twice = aggregate([{ line }, { line }]);
    assert.equal(twice.find((f) => f.name === "scrollTo")!.self, 1000);
  });

  it("weighs each sample until the next, as DevTools does", () => {
    const p = profile([[A], [B], [A]]);
    p.timeDeltas = [0, 1000, 250];
    const line = timeline(p, symbolizer);
    const byName = Object.fromEntries(aggregate([{ line }]).map((f) => [f.name, f.self]));
    // A: 1000 until B, then the last sample the median interval.
    assert.equal(byName.highlight, 1000 + 1000);
    assert.equal(byName.scrollTo, 250);
  });

  it("writes stacks innermost first, a package's frames run together", () => {
    const line = timeline(profile([[A, B, R, R, R, A]]), symbolizer);
    const leaf = line.nodes[0]!;
    assert.equal(
      stackText(line, leaf),
      "`highlight` (oss/app/src/review/diff.tsx:10) ← `render` (react-dom) ← [react-dom ×2] ← " +
        "`scrollTo` (oss/app/src/review/diff.tsx:20) ← `highlight` (oss/app/src/review/diff.tsx:10)",
    );
    assert.equal(stackText(line, leaf, 2).split(" ← ").length, 2);
    // One frame on its own is written out.
    const single = timeline(profile([[B, R, R, A]]), symbolizer);
    assert.equal(
      stackText(single, single.nodes[0]!),
      "`highlight` (oss/app/src/review/diff.tsx:10) ← `render` (react-dom) ← `render` (react-dom) ← " +
        "`scrollTo` (oss/app/src/review/diff.tsx:20)",
    );
  });
});

describe("the clocks", () => {
  it("lines the profile up with the page from the clock function's samples", () => {
    const clock = call(CLOCK_FUNCTION, "", 1, 2);
    // Page time 100 ms is profile time 5 000 000 µs: offset 4 900 000 µs.
    // The clock function runs from page 100 to 105 ms; sampled every
    // 250 µs from 5 000 100 µs.
    const stacks = [[A], ...Array.from({ length: 20 }, () => [clock]), [IDLE]];
    const p = profile(stacks, 250, 5_000_100 - 250);
    const line = timeline(p, symbolizer);
    const offset = clockOffset(line, 100, 105)!;
    assert.ok(Math.abs(offset - 4_900_000) <= 125, `offset ${offset}`);
    assert.equal(clockOffset(timeline(profile([[A]]), symbolizer), 1, 2), undefined);
  });

  it("leaves the clocks unaligned on too few samples", () => {
    const clock = call(CLOCK_FUNCTION, "", 1, 2);
    // One sample, then nine: neither is enough for 30 ms.
    for (const length of [1, CLOCK_MIN_SAMPLES - 1]) {
      const p = profile(
        Array.from({ length }, () => [clock]),
        3000,
        5_000_000,
      );
      assert.equal(clockOffset(timeline(p, symbolizer), 0, 30), undefined, `${length} samples`);
    }
  });

  it("leaves the clocks unaligned when the sampler starved for part of the spin", () => {
    const clock = call(CLOCK_FUNCTION, "", 1, 2);
    // 30 ms of spin, but the samples cover only its first 10 ms.
    const p = profile(
      Array.from({ length: 41 }, () => [clock]),
      250,
      5_000_000,
    );
    assert.equal(clockOffset(timeline(p, symbolizer), 0, 30), undefined);
    // Covering 26 of its 30 ms, with a gap in the middle, is enough.
    const gappy = profile(
      Array.from({ length: 40 }, () => [clock]),
      250,
      5_000_000,
    );
    gappy.timeDeltas![20] = 16_000;
    assert.notEqual(clockOffset(timeline(gappy, symbolizer), 0, 30), undefined);
  });

  it("keeps samples in order across a negative time delta", () => {
    const p = profile([[A], [B], [A]]);
    p.timeDeltas = [0, -500, 250];
    const line = timeline(p, symbolizer);
    assert.deepEqual([...line.times], [1_000_000, 1_000_000, 1_000_250]);
    assert.ok([...line.weights].every((w) => w >= 0));
  });
});

function snapshot(change: Partial<Snapshot> = {}): Snapshot {
  return {
    version: 1,
    userAgent: "Chrome",
    since: 0,
    now: 10,
    navigations: [],
    interactions: [],
    frames: { count: 0, buckets: { "≤17": 0, "≤33": 0, "≤50": 0, "≤100": 0, ">100": 0 }, long: [] },
    renders: {},
    commits: [],
    requests: [],
    marks: [],
    dom: 1,
    heap: null,
    dropped: {},
    ...change,
  };
}

describe("the slowest interactions and frames", () => {
  // Offset 0: page milliseconds are profile milliseconds. From 0 to 2 ms
  // the page highlights, from 2 to 3 ms it scrolls, then lays out.
  const stacks = [
    ...Array.from({ length: 8 }, () => [B, A]),
    ...Array.from({ length: 4 }, () => [B]),
    ...Array.from({ length: 4 }, () => [PROGRAM]),
  ];
  const line = timeline(profile(stacks, 250, 0), symbolizer);

  it("finds the stacks in an interaction's window", () => {
    const stacksIn = topStacks(line, 0, 2000);
    assert.deepEqual(stacksIn, [
      {
        text: "`highlight` (oss/app/src/review/diff.tsx:10) ← `scrollTo` (oss/app/src/review/diff.tsx:20)",
        time: 2000,
      },
    ]);
    assert.equal(
      topStacks(line, 2000, 4000)[0]!.text,
      "`scrollTo` (oss/app/src/review/diff.tsx:20)",
    );
    // Layout and the like have no JavaScript stack.
    assert.deepEqual(topStacks(line, 3000, 4000), []);
  });

  it("attributes each to its window in the summary", () => {
    const text = cpuSection(
      [
        {
          index: 1,
          line,
          offset: 0,
          file: "run-1.cpuprofile",
          snapshot: snapshot({
            since: 0,
            now: 4,
            interactions: [
              { type: "keydown", target: "j", start: 0, delay: 0, duration: 2 },
              { type: "keydown", target: "k", start: 2, delay: 0, duration: 1 },
            ],
            frames: {
              count: 2,
              buckets: { "≤17": 0, "≤33": 0, "≤50": 0, "≤100": 0, ">100": 0 },
              long: [{ start: 3, duration: 1 }],
            },
          }),
        },
      ],
      250,
    );
    assert.match(text, /## CPU \(Chromium\)/);
    assert.match(
      text,
      /\| `highlight` \| oss\/app\/src\/review\/diff\.tsx:10 \| 2\.0 \| 50% \| 2\.0 \|/,
    );
    assert.match(
      text,
      /\| `scrollTo` \| oss\/app\/src\/review\/diff\.tsx:20 \| 1\.0 \| 25% \| 3\.0 \|/,
    );
    const j = text.slice(text.indexOf("**keydown j**"), text.indexOf("**keydown k**"));
    assert.match(j, /JavaScript 2\.0 ms, outside it .* 0\.0 ms/);
    assert.match(j, /- 2\.0 ms: `highlight` .* ← `scrollTo`/);
    const k = text.slice(text.indexOf("**keydown k**"), text.indexOf("### Longest frames"));
    assert.match(k, /- 1\.0 ms: `scrollTo`/);
    assert.doesNotMatch(k, /highlight/);
    const frame = text.slice(text.indexOf("### Longest frames"));
    assert.match(frame, /outside it \(style, layout, paint, the browser\) 1\.0 ms/);
    // The slowest interaction comes first.
    assert.ok(text.indexOf("**keydown j**") < text.indexOf("**keydown k**"));
  });

  it("counts a run whose clocks it could not line up whole, without its interactions", () => {
    const text = cpuSection(
      [
        {
          index: 2,
          line,
          file: "run-2.cpuprofile",
          snapshot: snapshot({
            interactions: [{ type: "keydown", target: "j", start: 0, delay: 0, duration: 2 }],
          }),
        },
      ],
      250,
    );
    assert.match(text, /Runs 2 could not line/);
    assert.doesNotMatch(text, /Slowest interactions/);
    assert.match(text, /\| `highlight` \|/);
  });

  it("counts from the collector's reset, or from the page's start after a launch", () => {
    const run = {
      index: 1,
      line,
      offset: 0,
      file: "run-1.cpuprofile",
      snapshot: snapshot({ since: 2, now: 4 }),
    };
    // From the reset at 2 ms: scrolling and layout, no highlighting.
    assert.doesNotMatch(cpuSection([run], 250), /`highlight`/);
    assert.match(cpuSection([{ ...run, from: 0 }], 250), /\| `highlight` \|/);
  });

  it("writes nothing without profiles", () => {
    assert.equal(cpuSection([], 250), "");
  });
});
