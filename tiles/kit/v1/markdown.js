// baum tile kit v1: baum.md, a small safe Markdown subset. v1 is frozen and
// additive only. Everything is built with createElement and text nodes, so
// raw HTML in the text stays text and a link is only words with its URL in
// the title: a tile cannot navigate.

const MD_LIMIT = 20000;
const MD_DEPTH = 8;
/** The longest link URL looked for, so a run of brackets stays linear. */
const MD_URL = 1000;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const QUOTE = /^ {0,3}> ?/;
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;
const BLANK = /^[ \t]*$/;
const SPACE = (c) => c === " " || c === "\t";

/** A heading's level and words, parsed by hand: a regular expression for
 *  its optional closing hashes backtracks over long runs of spaces. */
function heading(line) {
  const match = /^ {0,3}(#{1,6})(?:[ \t]|$)/.exec(line);
  if (!match) return null;
  const start = match[0].length;
  let end = line.length;
  while (end > start && SPACE(line[end - 1])) end--;
  let close = end;
  while (close > start && line[close - 1] === "#") close--;
  if (close < end && (close === start || SPACE(line[close - 1]))) end = close;
  return { level: match[1].length, text: line.slice(start, end).trim() };
}

function isRule(line) {
  if (!/^ {0,3}[-*_]/.test(line)) return false;
  const marks = line.replace(/[ \t]/g, "");
  return marks.length >= 3 && [...marks].every((mark) => mark === marks[0]);
}

const startsBlock = (line) =>
  FENCE.test(line) || heading(line) !== null || isRule(line) || QUOTE.test(line) || ITEM.test(line);

/** The index of the next line that is not blank, from `from`, or -1. */
function nextFilled(lines, from) {
  for (let i = from; i < lines.length; i++) if (!BLANK.test(lines[i])) return i;
  return -1;
}

/** Inline Markdown into `parent`: code spans, strong, emphasis,
 *  strikethrough, links (as text) and backslash escapes. */
function inline(parent, text, depth) {
  let buffer = "";
  const flush = () => {
    if (buffer) parent.append(buffer);
    buffer = "";
  };
  // A delimiter with no closer after one position has none after any later
  // one, so each is searched for at most once without success.
  const unclosed = new Set();
  const closer = (delimiter, from) => {
    if (unclosed.has(delimiter)) return -1;
    for (let at = text.indexOf(delimiter, from); at >= 0; at = text.indexOf(delimiter, at + 1)) {
      if (!/\s/.test(text[at - 1])) return at;
    }
    unclosed.add(delimiter);
    return -1;
  };
  // The next "](" and "[" after the current position, each searched for
  // again only once passed, so every scan of the text moves forward.
  let closeAt = -2;
  let openAt = -2;
  let parenAt = -2;
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === "\\" && /[\\`*_{}[\]()#+\-.!~<>|]/.test(text[i + 1] || "")) {
      buffer += text[i + 1];
      i += 2;
      continue;
    }
    if (c === "`") {
      let run = 1;
      while (text[i + run] === "`") run++;
      const fence = "`".repeat(run);
      let end = unclosed.has(fence) ? -1 : text.indexOf(fence, i + run);
      while (end >= 0 && text[end + run] === "`") end = text.indexOf(fence, end + run + 1);
      if (end < 0) {
        unclosed.add(fence);
        buffer += fence;
        i += run;
        continue;
      }
      let code = text.slice(i + run, end);
      if (code.length > 2 && code.startsWith(" ") && code.endsWith(" ")) code = code.slice(1, -1);
      flush();
      parent.append(el("code", "", code));
      i = end + run;
      continue;
    }
    const pair = text.slice(i, i + 2);
    const open = (delimiter, tag) => {
      const start = i + delimiter.length;
      if (/\s/.test(text[start] || " ")) return false;
      if (delimiter[0] === "_" && /[\p{L}\p{N}]/u.test(text[i - 1] || "")) return false;
      const end = closer(delimiter, start + 1);
      if (end < 0) return false;
      flush();
      const node = el(tag);
      if (depth < MD_DEPTH) inline(node, text.slice(start, end), depth + 1);
      else node.textContent = text.slice(start, end);
      parent.append(node);
      i = end + delimiter.length;
      return true;
    };
    if ((pair === "**" || pair === "__") && open(pair, "strong")) continue;
    if (pair === "~~" && open(pair, "del")) continue;
    if ((c === "*" || c === "_") && pair !== "**" && pair !== "__" && open(c, "em")) continue;
    if (c === "[") {
      if (closeAt === -2 || (closeAt >= 0 && closeAt <= i)) closeAt = text.indexOf("](", i + 1);
      if (openAt === -2 || (openAt >= 0 && openAt <= i)) openAt = text.indexOf("[", i + 1);
      // Only the last "[" before a "](" can open a link to it, so each "]("
      // is looked past once. The URL runs to the ")" that balances its own
      // parentheses (at most 32 deep), within MD_URL characters, and is only
      // scanned when a ")" follows at all.
      let end = -1;
      const close = closeAt >= 0 && (openAt < 0 || openAt > closeAt) ? closeAt : -1;
      if (close > i && (parenAt === -2 || (parenAt >= 0 && parenAt < close + 2))) {
        parenAt = text.indexOf(")", close + 2);
      }
      const limit = parenAt < 0 ? -1 : Math.min(text.length, close + 2 + MD_URL);
      for (let j = close + 2, depth = 0; close > i && j < limit && depth <= 32 && text.charCodeAt(j) > 32; j++) {
        if (text[j] === "(") depth++;
        else if (text[j] === ")" && depth-- === 0) {
          end = j;
          break;
        }
      }
      const label = end < 0 ? "" : text.slice(i + 1, close);
      const url = end < 0 ? "" : text.slice(close + 2, end);
      if (label && url) {
        flush();
        const link = el("span", "baum-link");
        link.title = url;
        if (depth < MD_DEPTH) inline(link, label, depth + 1);
        else link.textContent = label;
        parent.append(link);
        i = end + 1;
        continue;
      }
    }
    buffer += c;
    i++;
  }
  flush();
}

/** A paragraph's lines, each break kept as a <br> as GitHub shows them. */
function paragraph(parent, lines, depth) {
  const node = el("p");
  lines.forEach((line, index) => {
    if (index > 0) node.append(el("br"));
    inline(node, line.trim(), depth);
  });
  parent.append(node);
}

function list(parent, lines, start, depth) {
  const first = ITEM.exec(lines[start]);
  const ordered = /\d/.test(first[2]);
  const node = el(ordered ? "ol" : "ul");
  const number = parseInt(first[2], 10);
  if (ordered && number !== 1) node.start = number;
  const sameKind = (match) => match && /\d/.test(match[2]) === ordered && match[1].length <= first[1].length + 1;
  let i = start;
  while (i < lines.length) {
    const match = ITEM.exec(lines[i]);
    if (!sameKind(match)) break;
    const indent = match[1].length + match[2].length + 1;
    const body = [match[3]];
    i++;
    while (i < lines.length) {
      const line = lines[i];
      if (BLANK.test(line)) {
        const next = nextFilled(lines, i);
        if (next < 0 || !(/^\s{2,}/.test(lines[next]) || sameKind(ITEM.exec(lines[next])))) break;
        while (i < next) {
          body.push("");
          i++;
        }
        continue;
      }
      if (sameKind(ITEM.exec(line))) break;
      if (!/^\s/.test(line) && startsBlock(line)) break;
      body.push(line.replace(new RegExp(`^ {0,${indent}}`), ""));
      i++;
    }
    const item = el("li");
    blocks(item, body, depth + 1);
    // A tight item holds its words, not a paragraph.
    if (item.childNodes.length === 1 && item.firstChild.nodeName === "P") {
      item.replaceChildren(...item.firstChild.childNodes);
    }
    node.append(item);
    if (i < lines.length && BLANK.test(lines[i])) {
      const next = nextFilled(lines, i);
      if (next >= 0 && sameKind(ITEM.exec(lines[next]))) i = next;
    }
  }
  parent.append(node);
  return i;
}

function blocks(parent, lines, depth) {
  if (depth > MD_DEPTH) {
    paragraph(parent, lines.filter((line) => !BLANK.test(line)), depth);
    return;
  }
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let match;
    if (BLANK.test(line)) {
      i++;
    } else if ((match = FENCE.exec(line))) {
      const marker = match[1];
      const body = [];
      i++;
      while (i < lines.length && !new RegExp(`^ {0,3}${marker[0]}{${marker.length},}[ \\t]*$`).test(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      i++;
      const pre = el("pre");
      pre.append(el("code", "", body.join("\n")));
      parent.append(pre);
    } else if ((match = heading(line))) {
      const { level } = match;
      const node = el(level <= 2 ? "p" : level === 3 ? "h3" : "h4", level <= 2 ? "kit-strong" : "");
      inline(node, match.text, depth);
      parent.append(node);
      i++;
    } else if (isRule(line)) {
      parent.append(el("hr"));
      i++;
    } else if (QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && QUOTE.test(lines[i])) {
        body.push(lines[i].replace(QUOTE, ""));
        i++;
      }
      const quote = el("blockquote");
      blocks(quote, body, depth + 1);
      parent.append(quote);
    } else if (ITEM.test(line)) {
      i = list(parent, lines, i, depth);
    } else {
      const body = [line];
      i++;
      while (i < lines.length && !BLANK.test(lines[i]) && !startsBlock(lines[i])) {
        body.push(lines[i]);
        i++;
      }
      paragraph(parent, body, depth);
    }
  }
}

/** One line of words: block marks dropped, lines run together. */
function flatten(source) {
  return source
    .split("\n")
    .filter((line) => !FENCE.test(line) && !isRule(line))
    .map((line) =>
      line
        .replace(/^ {0,3}(?:> ?)+/, "")
        .replace(/^ {0,3}#{1,6}[ \t]+/, "")
        .replace(/^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+/, "")
        .trim(),
    )
    .filter(Boolean)
    .join(" ");
}

function md(text, options) {
  const node = create("baum-md");
  node[BUILT] = true;
  const source = (typeof text === "string" ? text : text == null ? "" : String(text))
    .slice(0, MD_LIMIT)
    .replace(/\r\n?/g, "\n");
  if (isRecord(options) && options.inline) {
    node.dataset.inline = "";
    inline(node, flatten(source), 0);
  } else {
    blocks(node, source.split("\n"), 0);
  }
  return node;
}
