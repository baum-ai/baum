export type PromptSegment = { kind: "text" | "code"; value: string };

/** A prompt split into the text a person typed and the code fences they
 *  pasted. Only the fences are worth rendering: the rest is shown as typed,
 *  since a traceback's `__init__`, a glob's `**` or a shell `# comment` would
 *  come out of markdown as bold or a heading. A fence keeps its own markers,
 *  so the code block knows its language; one left open runs to the end, as
 *  markdown would run it. */
export function promptSegments(text: string): PromptSegment[] {
  const segments: PromptSegment[] = [];
  let prose: string[] = [];
  let code: string[] | null = null;
  let closer: RegExp | null = null;
  const flushProse = () => {
    const value = prose.join("\n").replace(/^\n+|\n+$/g, "");
    if (value.trim()) segments.push({ kind: "text", value });
    prose = [];
  };
  for (const line of text.replace(/\r\n?/g, "\n").split("\n")) {
    if (code && closer) {
      code.push(line);
      if (closer.test(line)) {
        segments.push({ kind: "code", value: code.join("\n") });
        code = null;
        closer = null;
      }
      continue;
    }
    // A backtick fence's info string holds no backtick, so a line such as
    // "```npm i``` then build" is inline code, not an opening fence.
    const open = /^ {0,3}(?:(`{3,})[^`]*$|(~{3,}))/.exec(line);
    if (open) {
      flushProse();
      const fence = (open[1] ?? open[2])!;
      closer = new RegExp(`^ {0,3}${fence[0] === "`" ? "`" : "~"}{${fence.length},}\\s*$`);
      code = [line];
      continue;
    }
    prose.push(line);
  }
  if (code) segments.push({ kind: "code", value: code.join("\n") });
  flushProse();
  return segments;
}
