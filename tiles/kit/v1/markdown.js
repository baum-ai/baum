// baum tile kit v1: baum.md, a small safe Markdown subset. v1 is frozen and
// additive only. Everything is built with createElement and text nodes, so
// raw HTML in the text stays text and a link is only words with its URL in
// the title: a tile cannot navigate.

const MD_LIMIT = 20000;
const MD_DEPTH = 8;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const HEADING = /^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$/;
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const QUOTE = /^ {0,3}> ?/;
const ITEM = /^( {0,3})([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;
const BLANK = /^[ \t]*$/;

const startsBlock = (line) =>
  FENCE.test(line) || HEADING.test(line) || RULE.test(line) || QUOTE.test(line) || ITEM.test(line);

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
      let end = text.indexOf(fence, i + run);
      while (end >= 0 && text[end + run] === "`") end = text.indexOf(fence, end + run + 1);
      if (end < 0) {
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
    if (c === "[" && !unclosed.has("](")) {
      const close = text.indexOf("](", i + 1);
      if (close < 0) unclosed.add("](");
      // The URL runs to the ")" that balances its own parentheses.
      let end = -1;
      for (let j = close + 2, depth = 0; close >= 0 && j < text.length && !/\s/.test(text[j]); j++) {
        if (text[j] === "(") depth++;
        else if (text[j] === ")" && depth-- === 0) {
          end = j;
          break;
        }
      }
      const label = close < 0 ? "" : text.slice(i + 1, close);
      const url = end < 0 ? "" : text.slice(close + 2, end);
      if (label && !label.includes("[") && url) {
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
        const next = lines.slice(i + 1).find((candidate) => !BLANK.test(candidate));
        if (next === undefined || !(/^\s{2,}/.test(next) || sameKind(ITEM.exec(next)))) break;
        body.push("");
        i++;
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
    while (i < lines.length && BLANK.test(lines[i])) {
      const next = lines.slice(i + 1).find((candidate) => !BLANK.test(candidate));
      if (next === undefined || !sameKind(ITEM.exec(next))) break;
      i++;
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
    } else if ((match = HEADING.exec(line))) {
      const level = match[1].length;
      const node = el(level <= 2 ? "p" : level === 3 ? "h3" : "h4", level <= 2 ? "kit-strong" : "");
      inline(node, match[2], depth);
      parent.append(node);
      i++;
    } else if (RULE.test(line)) {
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
    .filter((line) => !FENCE.test(line) && !RULE.test(line))
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
  const node = document.createElement("baum-md");
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
