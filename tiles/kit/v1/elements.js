// baum tile kit v1: baum.html, citations and the baum-cite and baum-diff
// elements. v1 is frozen and additive only: never remove, rename or change
// the documented behaviour of anything in manifest.json. The three scripts
// run in one function scope, in the order manifest.json lists them.

/** What the page sent in `init` and `update`, and what this render found. */
const state = {
  face: "",
  size: "",
  theme: "light",
  lang: "en",
  data: null,
  refs: new Map(),
  check: null,
  emptyUsed: false,
  failed: false,
  unresolved: new Set(),
};

/** Marks an element the kit built, so the elements can tell a bare tag. */
const BUILT = Symbol("baum.built");

// The document's methods and accessors, taken from the prototypes now: a
// named element (<img name="createElement">) shadows them on `document`.
const doc = document;
const DOC = Document.prototype;
const docCall = (name, ...args) => DOC[name].apply(doc, args);
const docGet = (name) => {
  const descriptor = Object.getOwnPropertyDescriptor(DOC, name);
  return descriptor ? descriptor.get.call(doc) : undefined;
};
const listen = (target, type, listener) =>
  EventTarget.prototype.addEventListener.call(target, type, listener);
const create = (tag) => docCall("createElement", tag);

function el(tag, className, text) {
  const node = create(tag);
  if (className) node.className = className;
  if (text !== undefined && text !== null) node.textContent = String(text);
  return node;
}

const isRecord = (value) =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const str = (value) => (typeof value === "string" ? value : "");
const int = (value) => (Number.isInteger(value) ? value : null);
/** A lookup table that answers only for its own keys, never Object's. */
const table = (entries) => Object.assign(Object.create(null), entries);

// ---------------------------------------------------------------------------
// baum.html: an escaping tagged template. The static parts are the author's
// markup; every interpolated value becomes text, a node, an attribute value
// set with setAttribute, or a refusal. Nothing interpolated is parsed.

const HOLE = `baum${Math.random().toString(36).slice(2, 10)}`;
const ANY_HOLE = new RegExp(`\\{\\{${HOLE}-(\\d+)\\}\\}`, "g");
const HAS_HOLE = new RegExp(`\\{\\{${HOLE}-\\d+\\}\\}`);
const ONLY_HOLE = new RegExp(`^\\{\\{${HOLE}-(\\d+)\\}\\}$`);
const REFUSED = new Set([
  "script", "iframe", "frame", "frameset", "object", "embed", "base", "meta", "link", "form",
  "portal",
]);
/** Elements whose text is code or raw text, in any namespace: no values. */
const RAW = new Set(["style", "script", "title", "textarea", "xmp", "noscript", "noembed", "noframes"]);
/** Attributes no value may be interpolated into. */
const FIXED = new Set(["srcset", "ping", "action", "formaction", "attributename"]);
/** Attributes that load a resource: a data: image or a blob: URL only. */
const RESOURCES = new Set([
  "src", "poster", "background", "data", "cite", "longdesc", "lowsrc", "dynsrc", "manifest",
  "codebase", "icon", "archive", "srcset", "ping", "action", "formaction",
]);
/** Elements whose href navigates or references: a #fragment only. */
const LINKS = new Set(["a", "area", "use"]);
const templates = new WeakMap();

const FRAGMENT = /^#/;
const IMAGE = /^data:image\/[a-z0-9.+-]+[;,]/i;
const BLOB = /^blob:/i;
/** What a style value may not hold: it may set a value, never add a rule. */
const STYLE = /[;:{}()\\"'<>]|\/\*|url/i;

function checkURL(tag, name, value) {
  const lower = name.toLowerCase();
  const link = lower.endsWith("href");
  if (!link && !RESOURCES.has(lower)) return;
  const ok = link && LINKS.has(tag)
    ? FRAGMENT.test(value)
    : FRAGMENT.test(value) && link || IMAGE.test(value) || BLOB.test(value);
  if (!ok) {
    throw new TypeError(
      link && LINKS.has(tag)
        ? `baum.html: <${tag} ${name}> may only be a #fragment.`
        : `baum.html: ${name} may only be a data:image or blob: URL.`,
    );
  }
}

function checkAttribute(element, name, value) {
  const lower = name.toLowerCase();
  if (lower.startsWith("on")) {
    throw new TypeError(`baum.html: no ${name}; use addEventListener.`);
  }
  const tag = element.localName.toLowerCase();
  checkURL(tag, name, value);
  if ((tag === "animate" || tag === "set") && lower === "attributename" && /^(on|.*href$)/i.test(value)) {
    throw new TypeError(`baum.html: <${tag}> may not change ${value}.`);
  }
}

/** Whether each hole stands in an attribute ("attr") or between nodes
 *  ("node"), from a small scan of the static parts. */
function holeKinds(strings) {
  const kinds = [];
  let mode = "text";
  let quote = "";
  for (let i = 0; i < strings.length; i++) {
    const s = strings[i];
    for (let j = 0; j < s.length; j++) {
      const c = s[j];
      if (mode === "text") {
        if (c === "<" && s.startsWith("!--", j + 1)) {
          mode = "comment";
          j += 3;
        } else if (c === "<" && /[a-zA-Z/!?]/.test(s[j + 1] || "")) mode = "tag";
      } else if (mode === "comment") {
        if (s.startsWith("-->", j)) {
          mode = "text";
          j += 2;
        }
      } else if (mode === "tag") {
        if (c === '"' || c === "'") {
          mode = "quote";
          quote = c;
        } else if (c === ">") mode = "text";
      } else if (c === quote) mode = "tag";
    }
    if (i === strings.length - 1) break;
    if (mode === "comment" || (mode === "text" && s.endsWith("<"))) {
      throw new TypeError("baum.html: no values in comments or tag names.");
    }
    kinds.push(mode === "text" ? "node" : "attr");
  }
  return kinds;
}

function compile(strings) {
  const kinds = holeKinds(strings);
  let source = strings[0];
  for (let i = 1; i < strings.length; i++) {
    const marker = `{{${HOLE}-${i - 1}}}`;
    // A comment marks a place between nodes, which the parser keeps where it
    // stands even inside a table. Spelled in two parts so the inlined script
    // never holds the sequence that opens an HTML comment.
    source += (kinds[i - 1] === "node" ? `<${"!--"}${marker}-->` : marker) + strings[i];
  }
  const template = create("template");
  // The author's own static markup, never data: values are only ever
  // placed into the parsed copy below.
  template.innerHTML = source;
  const parts = [];
  const found = new Set();
  const walker = docCall("createTreeWalker", template.content, 0x85);
  let index = -1;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    index++;
    const parent = node.parentNode && node.parentNode.localName;
    if (node.nodeType !== 1) {
      const match = node.nodeType === 8 && ONLY_HOLE.exec(node.data);
      if (match && !RAW.has(String(parent).toLowerCase())) {
        parts.push({ index, node: true, hole: Number(match[1]) });
        found.add(Number(match[1]));
      } else if (HAS_HOLE.test(node.data)) {
        throw new TypeError(`baum.html: no values in <${parent}>.`);
      }
      continue;
    }
    const tag = node.localName.toLowerCase();
    if (REFUSED.has(tag)) throw new TypeError(`baum.html: no <${tag}> in a tile.`);
    if (tag === "template" && HAS_HOLE.test(node.innerHTML)) {
      throw new TypeError("baum.html: no values in a <template>; make its copies with baum.html.");
    }
    for (const attribute of Array.from(node.attributes)) {
      const { name, value } = attribute;
      if (HAS_HOLE.test(name)) throw new TypeError("baum.html: no values as attribute names.");
      if (!HAS_HOLE.test(value)) {
        checkAttribute(node, name, value);
        continue;
      }
      if (name.toLowerCase().startsWith("on")) checkAttribute(node, name, "");
      if (FIXED.has(name.toLowerCase())) throw new TypeError(`baum.html: ${name} takes no values.`);
      const statics = [];
      const holes = [];
      let last = 0;
      for (const match of value.matchAll(ANY_HOLE)) {
        statics.push(value.slice(last, match.index));
        holes.push(Number(match[1]));
        found.add(Number(match[1]));
        last = match.index + match[0].length;
      }
      statics.push(value.slice(last));
      parts.push({ index, name, ns: attribute.namespaceURI, local: attribute.localName, statics, holes });
    }
  }
  if (found.size !== strings.length - 1) {
    throw new TypeError("baum.html: a value is out of place.");
  }
  return { template, parts };
}

function describe(value) {
  if (value === null) return "null";
  if (Array.isArray(value)) return "an array";
  return typeof value === "object" ? "an object" : `a ${typeof value}`;
}

/** Puts a value before `before` in `parent`: text, a node, or the items of
 *  an array; null, undefined and booleans put nothing. */
function insert(parent, before, value, depth) {
  if (value === null || value === undefined || typeof value === "boolean") return;
  if (typeof value === "string" || typeof value === "number" || typeof value === "bigint") {
    parent.insertBefore(docCall("createTextNode", String(value)), before);
  } else if (value instanceof Node && value.nodeType !== 9) {
    parent.insertBefore(value, before);
  } else if (typeof value === "object" && typeof value[Symbol.iterator] === "function") {
    if (depth > 32) throw new TypeError("baum.html: arrays nest too deep.");
    for (const item of value) insert(parent, before, item, depth + 1);
  } else {
    throw new TypeError(`baum.html can't show ${describe(value)}: pass text, numbers or nodes.`);
  }
}

function attributeText(value, style) {
  if (value === null || value === undefined || typeof value === "boolean") return "";
  if (typeof value === "string" || typeof value === "number" || typeof value === "bigint") {
    const text = String(value);
    if (style && STYLE.test(text)) {
      throw new TypeError("baum.html: a style value may only be a word or a number, as in style=\"--value:${n}\".");
    }
    return text;
  }
  if (Array.isArray(value)) return value.map((item) => attributeText(item, style)).filter(Boolean).join(" ");
  throw new TypeError(`baum.html can't put ${describe(value)} in an attribute.`);
}

function html(strings, ...values) {
  if (!Array.isArray(strings) || !Array.isArray(strings.raw)) {
    throw new TypeError("baum.html is a tag: baum.html`<p>${text}</p>`.");
  }
  let compiled = templates.get(strings);
  if (!compiled) {
    compiled = compile(strings);
    templates.set(strings, compiled);
  }
  const fragment = docCall("importNode", compiled.template.content, true);
  if (compiled.parts.length === 0) return fragment;
  const nodes = [];
  const wanted = new Set(compiled.parts.map((part) => part.index));
  const walker = docCall("createTreeWalker", fragment, 0x85);
  let index = -1;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    index++;
    if (wanted.has(index)) nodes[index] = node;
  }
  for (const part of compiled.parts) {
    const node = nodes[part.index];
    if (part.node) {
      insert(node.parentNode, node, values[part.hole], 0);
      node.remove();
      continue;
    }
    const { name, ns, local, statics, holes } = part;
    const whole = statics.length === 2 && statics[0] === "" && statics[1] === "";
    const only = values[holes[0]];
    if (whole && (only === null || only === undefined || only === false)) {
      if (ns) node.removeAttributeNS(ns, local);
      else node.removeAttribute(name);
      continue;
    }
    const style = name.toLowerCase() === "style";
    let text = statics[0];
    for (let i = 0; i < holes.length; i++) text += attributeText(values[holes[i]], style) + statics[i + 1];
    checkAttribute(node, name, text);
    if (ns) node.setAttributeNS(ns, name, text);
    else node.setAttribute(name, text);
  }
  return fragment;
}

// ---------------------------------------------------------------------------
// Citations. A citation is a review anchor or {type: risk|evidence|change|
// tile, id}; the page resolves each one the tile's data holds, under the key
// keyOf gives it, and sends the payloads in `init`.

/** The fields a key holds after the type, in order; "?" marks an optional
 *  one, which is null when absent. */
const KEY_FIELDS = table({
  hunk: ["id", "lines?"],
  file: ["path"],
  commit: ["sha"],
  pr: ["number"],
  pr_body: ["section?"],
  pr_comment: ["id"],
  review_thread: ["id"],
  prompt: ["session", "index"],
  session_event: ["session", "event"],
  check: ["id", "test?"],
  bot_report: ["id"],
  issue: ["system", "key"],
  spec: ["page", "assertion?"],
  external_url: ["url"],
  attachment: ["id?", "url?"],
  risk: ["id"],
  evidence: ["id"],
  change: ["id"],
  tile: ["id"],
});

/** A citation's key: JSON.stringify([type, ...fields]), as ARCHITECTURE.md
 *  ("Citation keys") defines it, or null for something that is not one. */
function keyOf(c) {
  if (!isRecord(c) || typeof c.type !== "string") return null;
  const fields = KEY_FIELDS[c.type];
  if (!fields) return null;
  const key = [c.type];
  for (const field of fields) {
    const optional = field.endsWith("?");
    let value = c[optional ? field.slice(0, -1) : field];
    if (field === "lines?") {
      value = Array.isArray(value) && value.length === 2 && value.every(Number.isInteger) ? [value[0], value[1]] : null;
    } else if (typeof value !== "string" && !Number.isFinite(value)) {
      if (!optional || (value !== undefined && value !== null)) return null;
      value = null;
    }
    key.push(value);
  }
  if (c.type === "attachment" && key[1] === null && key[2] === null) return null;
  return JSON.stringify(key);
}

function resolve(citation) {
  const key = keyOf(citation);
  const payload = key === null ? undefined : state.refs.get(key);
  return { key, payload: isRecord(payload) ? payload : null };
}

const REASONS = table({
  not_in_review: "not in this review",
  comparing: "comparing versions",
  too_many: "too many citations",
  load_failed: "couldn't be loaded",
});
const reason = (payload) => REASONS[str(payload && payload.why)] || "not available";

const baseName = (path) => path.slice(path.lastIndexOf("/") + 1);

/** A hunk's cited lines, or the hunk's range: "a–b", or "a" for one line. */
function rangeText(citation, payload) {
  const range = Array.isArray(citation.lines) ? citation.lines : payload.range;
  if (!Array.isArray(range) || int(range[0]) === null) return "";
  return int(range[1]) === null || range[1] === range[0] ? `${range[0]}` : `${range[0]}–${range[1]}`;
}

const TIERS = new Set(["critical", "high", "medium", "low"]);
const EVIDENCE_TONES = table({
  verified: "positive",
  observed: "info",
  failed: "critical",
  missing: "caution",
});
const CHECK_TONES = table({
  success: "positive",
  failure: "critical",
  timed_out: "critical",
  action_required: "caution",
});

/** The words a chip shows for a resolved citation, and its longer title. */
function citeText(citation, payload) {
  const label = str(payload.label);
  switch (citation.type) {
    case "hunk": {
      const path = str(payload.path);
      if (!path) return { text: label, title: label };
      const range = rangeText(citation, payload);
      return { text: range ? `${baseName(path)}:${range}` : baseName(path), title: range ? `${path}:${range}` : path };
    }
    case "file": {
      const path = str(payload.path) || str(citation.path);
      return { text: baseName(path) || label, title: path || label };
    }
    case "commit": {
      const sha = str(payload.sha) || str(citation.sha);
      return { text: sha.slice(0, 7) || label, title: str(payload.subject) || label };
    }
    case "risk":
      return { text: label, title: str(payload.text) || label };
    case "evidence":
      return { text: label, title: str(payload.claim) || label };
    default:
      return { text: label, title: label };
  }
}

function citeMark(kind, payload) {
  const mark = el("span", "baum-mark");
  mark.setAttribute("aria-hidden", "true");
  if (kind === "risk" && TIERS.has(payload.tier)) mark.dataset.tier = payload.tier;
  else if (kind === "evidence" && EVIDENCE_TONES[payload.evidence_status]) {
    mark.dataset.tone = EVIDENCE_TONES[payload.evidence_status];
  } else if (kind === "check" && CHECK_TONES[payload.conclusion]) {
    mark.dataset.tone = CHECK_TONES[payload.conclusion];
  } else return null;
  return mark;
}

function kindOf(citation) {
  return isRecord(citation) && /^[a-z_]{1,24}$/.test(citation.type) ? citation.type : "unknown";
}

function cite(citation, label) {
  const node = create("baum-cite");
  node[BUILT] = true;
  const kind = kindOf(citation);
  node.dataset.kind = kind;
  const { key, payload } = resolve(citation);
  const own = typeof label === "string" && label.trim() ? label : null;
  if (!payload || payload.status !== "ok") {
    if (!payload) state.unresolved.add(key ?? "invalid citation");
    node.dataset.state = "unavailable";
    const chip = el("span", "baum-chip");
    const text = payload ? own || str(payload.label) || "Citation unavailable" : "Citation unavailable";
    chip.append(el("span", "baum-label", text));
    chip.title = payload ? `Unavailable: ${reason(payload)}` : "This citation isn't in the review.";
    node.append(chip);
    return node;
  }
  node.dataset.state = "ok";
  const { text, title } = citeText(citation, payload);
  const interactive = state.face === "detail" && payload.navigable === true;
  const chip = el(interactive ? "button" : "span", "baum-chip");
  if (interactive) {
    chip.type = "button";
    chip.addEventListener("click", () => post("jump", { key }));
  }
  const mark = citeMark(kind, payload);
  if (mark) chip.append(mark);
  chip.append(el("span", "baum-label", own || text || "Citation"));
  if (title && title !== (own || text)) chip.title = title;
  node.append(chip);
  return node;
}

// ---------------------------------------------------------------------------
// baum.diff: one cited hunk as the review's code excerpts draw it, from the
// lines the page sent (the detail face only). A glance draws the file and
// range on one line.

const MARKS = table({ add: "+", remove: "−", context: " " });
const SYNTAX = new Set([
  "keyword", "string", "string-expression", "comment", "constant", "function", "parameter",
  "punctuation", "link", "inserted", "deleted", "changed",
]);

function codeCell(line) {
  const cell = el("td", "baum-code");
  const text = str(line.text);
  const tokens = Array.isArray(line.tokens) ? line.tokens : null;
  const fits =
    tokens &&
    tokens.every((token) => Array.isArray(token) && typeof token[0] === "string") &&
    tokens.map((token) => token[0]).join("") === text;
  if (!fits) {
    cell.textContent = text;
    return cell;
  }
  for (const [content, kind] of tokens) {
    if (SYNTAX.has(kind)) {
      const span = el("span", "", content);
      span.style.color = `var(--syntax-token-${kind})`;
      cell.append(span);
    } else cell.append(content);
  }
  return cell;
}

function lineRow(line) {
  const kind = line.kind === "add" || line.kind === "remove" ? line.kind : "context";
  const row = el("tr");
  row.dataset.kind = kind;
  row.append(
    el("td", "baum-n", kind === "add" ? "" : int(line.old) ?? ""),
    el("td", "baum-n", kind === "remove" ? "" : int(line.new) ?? ""),
    el("td", "baum-m", MARKS[kind]),
    codeCell(line),
  );
  return row;
}

function noteRow(className, text) {
  const row = el("tr", className);
  const cell = el("td", "", text);
  cell.colSpan = 4;
  row.append(cell);
  return row;
}

const plural = (count, one) => `${count} ${one}${count === 1 ? "" : "s"}`;

function diffHead(citation, payload, key) {
  const head = el("div", "baum-diff-head");
  const path = str(payload && payload.path) || str(citation.path);
  const range = payload && citation.type === "hunk" ? rangeText(citation, payload) : "";
  const navigable = state.face === "detail" && payload && payload.status === "ok" && payload.navigable === true;
  const name = el(navigable ? "button" : "span", "baum-diff-path");
  if (navigable) {
    name.type = "button";
    name.addEventListener("click", () => post("jump", { key }));
  }
  const slash = path.lastIndexOf("/") + 1;
  name.append(el("span", "", path.slice(0, slash)), el("span", "", path.slice(slash) + (range ? `:${range}` : "")));
  if (!path) name.lastChild.textContent = str(payload && payload.label) || "Code";
  name.title = path + (range ? `:${range}` : "");
  head.append(name);
  if (payload && payload.status === "ok" && int(payload.additions) !== null && int(payload.deletions) !== null) {
    const stat = el("span", "baum-diff-stat");
    stat.append(el("span", "", `+${payload.additions}`), " ", el("span", "", `−${payload.deletions}`));
    head.append(stat);
  }
  return head;
}

function diff(citation, options) {
  const max = isRecord(options) && int(options.maxLines) !== null ? options.maxLines : 40;
  const maxLines = Math.max(1, Math.min(max, 1000));
  const node = create("baum-diff");
  node[BUILT] = true;
  node.dataset.face = state.face === "detail" ? "detail" : "glance";
  const box = el("div", "baum-diff");
  node.append(box);
  const { key, payload } = resolve(citation);
  const unavailable = (why) => {
    node.dataset.state = "unavailable";
    box.append(el("div", "baum-diff-note", `Diff unavailable (${why})`));
    return node;
  };
  if (!payload) {
    state.unresolved.add(key ?? "invalid citation");
    return unavailable("the citation isn't in the review");
  }
  box.append(diffHead(citation, payload, key));
  if (payload.status !== "ok") return unavailable(reason(payload));
  if (citation.type !== "hunk" && citation.type !== "file") return unavailable("not code");
  node.dataset.state = "ok";
  if (node.dataset.face === "glance" || citation.type === "file") return node;
  const excerpt = payload.excerpt;
  if (!isRecord(excerpt) || !Array.isArray(excerpt.lines)) return unavailable("no excerpt");
  const body = el("div", "baum-diff-body");
  const table = el("table");
  const cols = el("colgroup");
  cols.append(el("col", "baum-n"), el("col", "baum-n"), el("col", "baum-m"), el("col"));
  const shown = el("tbody");
  if (str(excerpt.header)) shown.append(noteRow("baum-hunk", excerpt.header));
  const before = int(excerpt.omitted_before);
  if (before) shown.append(noteRow("baum-gap", `⋯ ${plural(before, "line")} above`));
  const lines = excerpt.lines.filter(isRecord);
  for (const line of lines.slice(0, maxLines)) shown.append(lineRow(line));
  table.append(cols, shown);
  const after = int(excerpt.omitted_after);
  const tail = el("tbody");
  for (const line of lines.slice(maxLines)) tail.append(lineRow(line));
  if (after) tail.append(noteRow("baum-gap", `⋯ ${plural(after, "line")} below`));
  if (tail.firstChild) table.append(tail);
  body.append(table);
  box.append(body);
  const rest = lines.length - maxLines;
  if (rest > 0) {
    tail.hidden = true;
    const more = el("button", "baum-more", `Show ${plural(rest, "more line")}`);
    more.type = "button";
    more.setAttribute("aria-expanded", "false");
    more.addEventListener("click", () => {
      tail.hidden = !tail.hidden;
      more.setAttribute("aria-expanded", String(!tail.hidden));
      more.textContent = tail.hidden ? `Show ${plural(rest, "more line")}` : "Show fewer lines";
    });
    box.append(more);
  }
  return node;
}

// ---------------------------------------------------------------------------
// States, and the elements' tags.

function empty(text) {
  state.emptyUsed = true;
  const node = el("p", "kit-empty", typeof text === "string" && text ? text : "Nothing to show.");
  return node;
}

function fail(message) {
  const text = (typeof message === "string" && message) || "This tile couldn't be drawn.";
  state.failed = true;
  report("render_failed", text);
  const node = el("p", "kit-fail", text);
  node.setAttribute("role", "alert");
  return node;
}

/** A tag written by hand rather than made by its factory has no data to
 *  draw: it says so, visibly and in the console. */
function defineElement(tag, factory) {
  if (!window.customElements || customElements.get(tag)) return;
  customElements.define(
    tag,
    class extends HTMLElement {
      connectedCallback() {
        if (this[BUILT]) return;
        this[BUILT] = true;
        this.dataset.state = "unavailable";
        this.textContent = `Use baum.${factory}() to make <${tag}>.`;
        console.error(this.textContent);
      }
    },
  );
}
