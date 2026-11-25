// baum tile kit v1: the bootstrap. It talks to the page that framed the tile
// (the wire protocol below is internal: the page and this file always come
// from one build), reports what went wrong, and defines window.baum, the
// author API, which is frozen and additive only within v1.
//
// It runs in <head>, before any author script, so its listeners see every
// error. The page sends `init`, `update` and `theme` (which may carry colour
// `tokens`), and the render check's runner `measure` and `ping`; the
// bootstrap sends `hello`, `ready`, `resize`, `error`, `jump`, `ask`, `open`,
// `key` and `pong`.
//
// A sandboxed frame's errors reach the page only as "Script error.", so the
// bootstrap reports each one itself, with its message and its line in the
// author's HTML.

const KIT = 1;
const PROTOCOL = "baum.tile/1";
/** Lines of the frame document before the author's HTML, set at compose
 *  time, so errors are reported in the author's own line numbers. */
const AUTHOR_LINE = /*@AUTHOR_LINE@*/ 0;
const RENDER_TIMEOUT = 3000;
/** Errors reported per code, so a chatty one cannot hide another. */
const MAX_ERRORS = 10;
const MAX_TEXT = 500;
/** The most a message from the page may hold: data is at most 64 KiB and
 *  the excerpts 384 KiB, so these leave room without letting a bad message
 *  cost much to walk. */
const MAX_CHARS = 1500000;
const MAX_NODES = 200000;
const MAX_DEPTH = 64;
const KEYS = new Set(["Escape", "u", "j", "k", "ArrowLeft", "ArrowRight", "[", "]", "?"]);
const FACES = new Set(["glance", "detail"]);
const SIZES = new Set(["full", "half", "third"]);
const THEMES = new Set(["light", "dark"]);
/** The colour tokens a theme may set: the same list as themes.Tokens in
 *  internal/themes (a test keeps them equal). */
const THEME_TOKENS = new Set([
  "--color-ink", "--color-paper", "--color-shadow", "--brand-forest", "--brand-sand-100",
  "--brand-green-600", "--color-critical", "--color-blue", "--series-1", "--series-2",
  "--series-3", "--series-4", "--series-5", "--series-6", "--series-7", "--series-8",
  "--series-caramel", "--series-sage", "--series-espresso", "--surface-page", "--surface-card",
  "--surface-canvas", "--surface-popup", "--text-primary", "--text-secondary",
  "--text-inverse", "--border-default", "--border-focus", "--action-primary",
  "--action-primary-hover", "--nav-hover", "--nav-selected", "--status-positive",
  "--status-caution", "--status-critical",
]);
const MAX_TOKEN_VALUE = 64;
const LANG = /^[A-Za-z]{2,8}(?:-[A-Za-z0-9]{1,8}){0,4}$/;

const host = window.parent;
const framed = host !== window;
const consoleError = console.error;
let spec = null;
let started = false;
let helloSent = false;
let renderSeq = 0;
let readySent = false;
const errorsSent = table({});

function post(kind, fields) {
  if (!framed) return;
  try {
    host.postMessage(Object.assign({ protocol: PROTOCOL, kind }, fields), "*");
  } catch {
    // The page has gone; nothing to tell.
  }
}

const clip = (text) => (text.length > MAX_TEXT ? `${text.slice(0, MAX_TEXT - 1)}…` : text);

/** Words for any thrown or logged value; never throws itself. */
function describeError(value) {
  try {
    if (value instanceof Error) return `${value.name}: ${value.message}`;
    if (typeof value === "string") return value;
    return JSON.stringify(value) ?? String(value);
  } catch {
    try {
      return String(value);
    } catch {
      return "(a value that can't be shown)";
    }
  }
}

/** The first frame of a stack in the author's lines, author-relative. */
function stackPosition(error) {
  let stack = "";
  try {
    stack = error && typeof error.stack === "string" ? error.stack : "";
  } catch {
    // A stack that throws has no position.
  }
  for (const line of stack.split("\n")) {
    const match = /:(\d+):(\d+)\)?\s*$/.exec(line);
    if (match && Number(match[1]) > AUTHOR_LINE) {
      return { line: Number(match[1]) - AUTHOR_LINE, column: Number(match[2]) };
    }
  }
  return null;
}

function linePosition(line, column) {
  return Number.isInteger(line) && line > AUTHOR_LINE
    ? { line: line - AUTHOR_LINE, column: Number.isInteger(column) ? column : undefined }
    : null;
}

/** Reports an error; `message` and `where` may be functions, which are
 *  only called while the code has reports left. */
function report(code, message, where) {
  if ((errorsSent[code] || 0) >= MAX_ERRORS) return;
  errorsSent[code] = (errorsSent[code] || 0) + 1;
  if (typeof message === "function") message = message();
  if (typeof where === "function") where = where();
  const fields = { code, message: clip(String(message || code)) };
  if (where) {
    if (where.line !== undefined) fields.line = where.line;
    if (where.column !== undefined) fields.column = where.column;
    if (where.blocked !== undefined) fields.blocked = where.blocked;
  }
  post("error", fields);
}

listen(window, "error", (event) => {
  // Resource errors reach window too, without an ErrorEvent's message.
  if (!(event instanceof ErrorEvent)) return;
  report(
    "script_error",
    () => event.message || describeError(event.error),
    () => stackPosition(event.error) || linePosition(event.lineno, event.colno),
  );
});

listen(window, "unhandledrejection", (event) => {
  report("unhandled_rejection", () => describeError(event.reason), () => stackPosition(event.reason));
});

listen(doc, "securitypolicyviolation", (event) => {
  let blocked = String(event.blockedURI || "");
  try {
    const url = new URL(blocked);
    blocked = url.host ? `${url.protocol}//${url.host}` : url.protocol;
  } catch {
    // "inline", "eval" and the like name no URL.
  }
  const directive = event.effectiveDirective || event.violatedDirective || "policy";
  const where = linePosition(event.lineNumber, event.columnNumber) || {};
  where.blocked = blocked;
  report("csp_violation", `The frame's ${directive} policy blocked ${blocked || "a request"}.`, where);
});

console.error = function error(...args) {
  report("console_error", () => args.map(describeError).join(" "));
  return consoleError.apply(console, args);
};

// ---------------------------------------------------------------------------
// Messages from the page. Only the frame's parent is heard, and only
// well-formed messages of plain JSON-shaped values within the limits.

function plainJSON(value) {
  const stack = [[value, 0]];
  let nodes = 0;
  let chars = 0;
  while (stack.length > 0) {
    const [item, depth] = stack.pop();
    if (++nodes > MAX_NODES || depth > MAX_DEPTH) return false;
    if (item === null || typeof item === "boolean") continue;
    if (typeof item === "number") {
      if (!Number.isFinite(item)) return false;
    } else if (typeof item === "string") {
      chars += item.length;
      if (chars > MAX_CHARS) return false;
    } else if (Array.isArray(item)) {
      for (const child of item) stack.push([child, depth + 1]);
    } else if (typeof item === "object") {
      const proto = Object.getPrototypeOf(item);
      if (proto !== Object.prototype && proto !== null) return false;
      for (const key of Object.keys(item)) {
        chars += key.length;
        stack.push([item[key], depth + 1]);
      }
    } else return false;
  }
  return true;
}

/** The resolved citations, keyed as keyOf keys them. A payload that is not
 *  an object with a status and a label is dropped. */
function readRefs(refs) {
  const map = new Map();
  for (const [key, payload] of Object.entries(refs)) {
    if (
      isRecord(payload) &&
      (payload.status === "ok" || payload.status === "unavailable") &&
      typeof payload.label === "string"
    ) {
      map.set(key, payload);
    }
  }
  return map;
}

// ---------------------------------------------------------------------------
// Theme colours: a port of ParseColor and ValidateToken in internal/themes
// (parse.go), with the same grammar and regular expressions; a shared test
// file keeps the two agreeing. Whitespace is ASCII only.

const CSS_NUMBER = /^[+-]?(\d+(\.\d+)?|\.\d+)([eE][+-]?\d+)?$/;
const COLOR_FUNCTION = /^(rgba?|hsla?|oklch)\(([^()]*)\)$/;
const ASCII_SPACE = /[ \t\n\r\f]+/;
const trimASCII = (text) => text.replace(/^[ \t\n\r\f]+|[ \t\n\r\f]+$/g, "");
const fieldsASCII = (text) => text.split(ASCII_SPACE).filter((part) => part !== "");

/** A CSS number: no hex, underscores, inf or nan, and finite. */
function cssFloat(text) {
  if (!CSS_NUMBER.test(text)) return null;
  const value = Number(text);
  return Number.isFinite(value) ? value : null;
}

/** A number or a percentage, a percentage being a share of `full`. */
function cssNumberOrPercent(text, full) {
  if (!text.endsWith("%")) return cssFloat(text);
  const value = cssFloat(text.slice(0, -1));
  if (value === null) return null;
  const scaled = (value / 100) * full;
  return Number.isFinite(scaled) ? scaled : null;
}

function cssPercent(text) {
  if (!text.endsWith("%")) return null;
  const value = cssFloat(text.slice(0, -1));
  if (value === null) return null;
  const scaled = value / 100;
  return Number.isFinite(scaled) ? scaled : null;
}

function cssHue(text) {
  let scale = 1;
  if (text.endsWith("deg")) text = text.slice(0, -3);
  else if (text.endsWith("grad")) (text = text.slice(0, -4)), (scale = 0.9);
  else if (text.endsWith("turn")) (text = text.slice(0, -4)), (scale = 360);
  else if (text.endsWith("rad")) (text = text.slice(0, -3)), (scale = 180 / Math.PI);
  const value = cssFloat(text);
  return value !== null && Number.isFinite(value * scale);
}

/** Whether `text` is a colour ParseColor accepts: a hex colour or an rgb(),
 *  rgba(), hsl(), hsla() or oklch() function, nothing else. */
function isColor(text) {
  text = trimASCII(text).toLowerCase();
  if (text.startsWith("#")) {
    return /^(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/.test(text.slice(1));
  }
  const match = COLOR_FUNCTION.exec(text);
  if (!match) return false;
  const name = match[1];
  const body = trimASCII(match[2]);
  let fields;
  let alpha = "";
  const legacy = body.includes(",");
  if (legacy) {
    if (body.includes("/") || name === "oklch") return false;
    fields = [];
    for (let part of body.split(",")) {
      part = trimASCII(part);
      if (part === "" || fieldsASCII(part).length !== 1) return false;
      fields.push(part);
    }
    if (fields.length === 4) alpha = fields.pop();
  } else {
    let colour = body;
    const slash = body.indexOf("/");
    if (slash >= 0) {
      colour = body.slice(0, slash);
      alpha = trimASCII(body.slice(slash + 1));
      if (alpha === "" || fieldsASCII(alpha).length !== 1) return false;
    }
    fields = fieldsASCII(colour);
  }
  if (fields.length !== 3) return false;
  if (alpha !== "" && cssNumberOrPercent(alpha, 1) === null) return false;
  if (name === "rgb" || name === "rgba") {
    let percents = 0;
    for (const field of fields) {
      if (field.endsWith("%")) percents++;
      if (cssNumberOrPercent(field, 255) === null) return false;
    }
    return !(legacy && percents !== 0 && percents !== 3);
  }
  if (name === "hsl" || name === "hsla") {
    return cssHue(fields[0]) && cssPercent(fields[1]) !== null && cssPercent(fields[2]) !== null;
  }
  return (
    cssNumberOrPercent(fields[0], 1) !== null &&
    cssNumberOrPercent(fields[1], 0.4) !== null &&
    cssHue(fields[2])
  );
}

/** The tokens of a theme message as an array of [token, value], or null when
 *  they are not a plain object of allowlisted tokens with colour values. A
 *  value over 64 bytes is refused; a colour is ASCII, so a longer string in
 *  characters is refused as well as one longer in bytes. */
function readTokens(tokens) {
  if (!isRecord(tokens)) return null;
  const entries = [];
  for (const token of Object.keys(tokens)) {
    const value = tokens[token];
    if (!THEME_TOKENS.has(token) || typeof value !== "string") return null;
    if (value.length > MAX_TOKEN_VALUE || !isColor(value)) return null;
    entries.push([token, value]);
  }
  return entries;
}

/** The tokens the last theme message set, so the next can remove the ones it
 *  leaves out. */
let themeTokens = [];

function applyTheme(theme, tokens) {
  state.theme = theme;
  const root = docGet("documentElement");
  root.dataset.theme = theme;
  root.style.colorScheme = theme;
  const next = table({});
  for (const [token, value] of tokens) {
    root.style.setProperty(token, value);
    next[token] = true;
  }
  for (const token of themeTokens) {
    if (!next[token]) root.style.removeProperty(token);
  }
  themeTokens = tokens.map(([token]) => token);
}

function onInit(message) {
  const { face, size, theme, lang, data, refs, check } = message;
  const tokens = message.tokens === undefined ? [] : readTokens(message.tokens);
  if (tokens === null) return;
  if (started || !FACES.has(face) || !SIZES.has(size) || !THEMES.has(theme)) return;
  if (typeof lang !== "string" || !LANG.test(lang) || !("data" in message) || !isRecord(refs)) return;
  if (check !== undefined && !(isRecord(check) && typeof check.id === "string")) return;
  started = true;
  Object.assign(state, { face, size, lang, data, refs: readRefs(refs), check: check || null });
  const root = docGet("documentElement");
  root.lang = lang;
  root.dataset.face = face;
  root.dataset.size = size;
  applyTheme(theme, tokens);
  if (face === "detail") watchHeight();
  if (spec) render();
}

function onTheme(message) {
  if (!started || !THEMES.has(message.theme)) return;
  const tokens = message.tokens === undefined ? [] : readTokens(message.tokens);
  if (tokens !== null) applyTheme(message.theme, tokens);
}

function onUpdate(message) {
  if (!started || !("data" in message) || !isRecord(message.refs)) return;
  state.data = message.data;
  state.refs = readRefs(message.refs);
  if (spec) render();
}

listen(window, "message", (event) => {
  if (!framed || event.source !== host) return;
  const message = event.data;
  if (!isRecord(message) || message.protocol !== PROTOCOL || !plainJSON(message)) return;
  if (message.kind === "init") onInit(message);
  else if (message.kind === "update") onUpdate(message);
  else if (message.kind === "theme") onTheme(message);
  else if (message.kind === "measure" && state.check && readySent) post("resize", { height: contentHeight() });
  // The render check's runner asks a frame that has not said hello whether
  // it still runs: one whose script never yields cannot answer.
  else if (message.kind === "ping") post("pong", {});
});

// ---------------------------------------------------------------------------
// Rendering.

const TIMEOUT = Symbol("timeout");

function frames(count) {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, 100);
    const step = (left) =>
      left === 0
        ? (clearTimeout(timer), resolve())
        : requestAnimationFrame(() => step(left - 1));
    step(count);
  });
}

/** A turn of the event loop that is neither a timer nor an animation frame:
 *  a hidden window runs no animation frames and slows its timers to about
 *  one a second, but still delivers messages. */
function turn() {
  if (typeof MessageChannel !== "function") return Promise.resolve();
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    channel.port1.onmessage = () => {
      channel.port1.close();
      resolve();
    };
    channel.port2.postMessage(null);
  });
}

/** Two frames after the render, once the fonts it uses have loaded. Under the
 *  render check, which may run in a hidden window, it waits for the fonts
 *  and a turn instead, and the measures read the layout directly. */
async function settle() {
  if (state.check) {
    const fonts = docGet("fonts");
    if (fonts && fonts.status === "loading") await fonts.ready;
    await turn();
    return;
  }
  await frames(2);
  const fonts = docGet("fonts");
  if (fonts && fonts.status === "loading") {
    await Promise.race([fonts.ready, new Promise((resolve) => setTimeout(resolve, 500))]);
    await frames(1);
  }
}

function visibleText(root) {
  let count = 0;
  const walker = docCall("createTreeWalker", root, 4);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const parent = node.parentNode && node.parentNode.nodeName;
    if (parent !== "SCRIPT" && parent !== "STYLE" && parent !== "TEMPLATE") {
      count += node.data.trim().length;
    }
  }
  return count;
}

function contentHeight() {
  return Math.ceil(docGet("documentElement").getBoundingClientRect().height);
}

function measure(root) {
  const html = docGet("documentElement");
  let media = 0;
  for (const node of root.querySelectorAll("img, svg, canvas, video")) {
    const box = node.getBoundingClientRect();
    if (box.width >= 16 && box.height >= 16) media++;
  }
  return {
    height: state.face === "glance" ? Math.ceil(root.scrollHeight) : contentHeight(),
    text_chars: visibleText(root),
    elements: root.getElementsByTagName("*").length,
    media,
    empty_state: state.emptyUsed,
    failed: state.failed,
    unresolved: state.unresolved.size,
    overflow_x: html.scrollWidth > html.clientWidth + 2,
    overflow_y: state.face === "glance" && root.scrollHeight > root.clientHeight + 2,
    render_ms: state.renderMS,
  };
}

async function render() {
  const seq = ++renderSeq;
  const began = performance.now();
  Object.assign(state, { emptyUsed: false, failed: false, unresolved: new Set() });
  const root = el("div");
  root.id = "baum-root";
  const old = docCall("getElementById", "baum-root");
  if (old) old.replaceWith(root);
  else docGet("body").prepend(root);
  const draw = spec[state.face];
  const view = Object.freeze({
    data: state.data,
    face: state.face,
    size: state.size,
    theme: state.theme,
    lang: state.lang,
    root,
  });
  try {
    let result = draw(view);
    if (result && typeof result.then === "function") {
      let timer;
      const late = new Promise((resolve) => {
        timer = setTimeout(() => resolve(TIMEOUT), RENDER_TIMEOUT);
      });
      result = await Promise.race([result, late]);
      clearTimeout(timer);
      if (result === TIMEOUT) {
        if (seq === renderSeq) report("timeout", "The render took over 3 seconds.");
        return;
      }
    }
    if (seq !== renderSeq) return;
    if (result !== root && !(result instanceof Node && result.parentNode === root)) {
      insert(root, null, result, 0);
    }
    state.renderMS = Math.round(performance.now() - began);
  } catch (error) {
    if (seq === renderSeq) report("script_error", describeError(error), stackPosition(error));
    return;
  }
  await settle();
  if (seq !== renderSeq) return;
  post("ready", { metrics: measure(root) });
  readySent = true;
  if (state.face === "detail") reportHeight();
}

// ---------------------------------------------------------------------------
// Height, for a detail: reported as it changes, once per frame. A height that
// grows ten times in a second by the same amount is a feedback loop with the
// frame's own size (vh units, say): it stops being reported.

let lastHeight = -1;
let heightQueued = false;
let heightFrozen = false;
let growth = { delta: 0, since: 0, count: 0 };
let observer = null;

function reportHeight() {
  if (heightFrozen || !readySent) return;
  const height = contentHeight();
  if (height === lastHeight) return;
  if (lastHeight >= 0 && height > lastHeight) {
    const delta = height - lastHeight;
    const now = performance.now();
    if (delta === growth.delta && now - growth.since <= 1000) growth.count++;
    else growth = { delta, since: now, count: 1 };
    if (growth.count >= 10) {
      heightFrozen = true;
      if (observer) observer.disconnect();
      report("layout_loop", `The height kept growing by ${delta}px with the frame; stopped at ${lastHeight}px.`);
      return;
    }
  } else growth = { delta: 0, since: 0, count: 0 };
  lastHeight = height;
  post("resize", { height });
}

function watchHeight() {
  if (typeof ResizeObserver !== "function") return;
  observer = new ResizeObserver(() => {
    if (heightQueued) return;
    heightQueued = true;
    requestAnimationFrame(() => {
      heightQueued = false;
      reportHeight();
    });
  });
  observer.observe(docGet("documentElement"));
}

// ---------------------------------------------------------------------------
// Keys the page acts on, from a detail the reader is in: only when the tile
// did not handle the key and it was not typed into a field.

listen(window, "keydown", (event) => {
  if (state.face !== "detail" || event.defaultPrevented || event.isComposing) return;
  if (event.ctrlKey || event.metaKey || event.altKey || !KEYS.has(event.key)) return;
  const target = event.target;
  if (
    target instanceof Element &&
    target.closest("input, textarea, select, [contenteditable]:not([contenteditable='false'])")
  ) {
    return;
  }
  post("key", { key: event.key, shift: event.shiftKey });
});

// ---------------------------------------------------------------------------
// The author API.

function tile(definition) {
  if (spec) throw new Error("baum.tile() was already called: a tile registers once.");
  if (!isRecord(definition) || typeof definition.glance !== "function" || typeof definition.detail !== "function") {
    throw new TypeError("baum.tile() takes {glance(view), detail(view)}: two functions.");
  }
  spec = { glance: definition.glance, detail: definition.detail };
  if (started) render();
}

function jump(citation) {
  const { key, payload } = resolve(citation);
  if (!payload || payload.status !== "ok" || payload.navigable !== true) return false;
  post("jump", { key });
  return true;
}

Object.defineProperty(window, "baum", {
  enumerable: true,
  value: Object.freeze({
    tile,
    html: Object.freeze(html),
    cite,
    diff,
    md,
    empty,
    fail,
    refs: Object.freeze({
      get: (citation) => resolve(citation).payload,
    }),
    jump,
    ask: () => post("ask", {}),
    openDetail: () => post("open", {}),
  }),
});

defineElement("baum-cite", "cite");
defineElement("baum-diff", "diff");
defineElement("baum-md", "md");

function hello() {
  if (helloSent) return;
  helloSent = true;
  post("hello", { kit: KIT, registered: spec !== null });
}
if (docGet("readyState") === "loading") listen(doc, "DOMContentLoaded", hello);
else queueMicrotask(hello);
