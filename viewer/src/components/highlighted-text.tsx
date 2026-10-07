import type { CSSProperties } from "react";

import type { HighlightToken } from "../highlight";

const FONT_ITALIC = 1;
const FONT_BOLD = 2;
const FONT_UNDERLINE = 4;
const FONT_STRIKETHROUGH = 8;

function tokenStyle({ color, fontStyle = 0 }: HighlightToken): CSSProperties {
  const decoration = [
    fontStyle & FONT_UNDERLINE ? "underline" : "",
    fontStyle & FONT_STRIKETHROUGH ? "line-through" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return {
    color,
    fontStyle: fontStyle & FONT_ITALIC ? "italic" : undefined,
    fontWeight: fontStyle & FONT_BOLD ? 700 : undefined,
    textDecoration: decoration || undefined,
  };
}

/** A line of code, coloured when tokens are available. Without tokens it is
 *  the bare text; with them, one inline span per token and the same text, so a
 *  cell that holds it keeps its layout, selection and copied text. */
export function HighlightedText({
  text,
  tokens,
}: {
  text: string;
  tokens?: readonly HighlightToken[] | null;
}) {
  if (!tokens) return text;
  return tokens.map((token, index) => (
    <span key={index} style={tokenStyle(token)}>
      {token.content}
    </span>
  ));
}
