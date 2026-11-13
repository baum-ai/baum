/** The palette utilities `palette-utilities.css` declares, as a rewrite from
 *  the arbitrary value they replace: `text-(--text-secondary)` is
 *  `text-secondary`, `border-b-(--border-strong)` is `border-b-strong`,
 *  `bg-(--surface-card)` is `bg-surface-card`; and the radius steps:
 *  `rounded-(--radius-sm)` is `rounded-sm`. The local app's palette lint
 *  and the hosted app's tests use it to reject an arbitrary value written
 *  where a utility exists. Pure, so it runs in Node and in Vitest alike. */

const borderUtilities = [
  "border",
  "border-x",
  "border-y",
  "border-t",
  "border-r",
  "border-b",
  "border-l",
  "border-s",
  "border-e",
  "divide",
];
const colorUtilities = [
  ...borderUtilities,
  "text",
  "bg",
  "outline",
  "ring",
  "ring-offset",
  "fill",
  "stroke",
  "decoration",
  "accent",
  "caret",
  "from",
  "via",
  "to",
];

/** Corner utilities. Tailwind's `rounded-<step>` reads `--radius-<step>`,
 *  and the palette sets those four steps over Tailwind's defaults, so
 *  `rounded-sm` already is `rounded-(--radius-sm)` (`palette-utilities.test.ts`
 *  holds that). `rounded-full` is not among them: Tailwind's is a fixed
 *  value, not `--radius-full`. */
const radiusUtilities = [
  "rounded",
  ...["s", "e", "t", "r", "b", "l", "ss", "se", "ee", "es", "tl", "tr", "br", "bl"].map(
    (side) => `rounded-${side}`,
  ),
];
export const radiusSteps = ["xs", "sm", "md", "lg"];

/** A function from `<utility>`, `<token>` (without `--`) to the palette
 *  utility, or null when the token has none for that utility. */
export function paletteUtilityNames(css: string) {
  const block = css.match(/@theme inline reference \{([\s\S]*?)\n\}/)?.[1] ?? "";
  const text = new Set<string>();
  const border = new Set<string>();
  const color = new Set<string>();
  const shadow = new Set<string>();
  for (const [, key = "", token = ""] of block.matchAll(/--([\w-]+):\s*var\(--([\w-]+)\);/g)) {
    if (key.startsWith("text-color-")) text.add(token);
    else if (key.startsWith("border-color-")) border.add(token);
    else if (key.startsWith("color-")) color.add(token);
    else if (key.startsWith("shadow-")) shadow.add(token);
  }
  return (utility: string, token: string): string | null => {
    if (utility === "text" && text.has(token)) return `text-${token.slice("text-".length)}`;
    if (borderUtilities.includes(utility) && border.has(token))
      return `${utility}-${token.slice("border-".length)}`;
    if (colorUtilities.includes(utility) && color.has(token)) return `${utility}-${token}`;
    if (utility === "shadow" && shadow.has(token)) return `shadow-${token.slice("shadow-".length)}`;
    const step = token.slice("radius-".length);
    if (
      radiusUtilities.includes(utility) &&
      token.startsWith("radius-") &&
      radiusSteps.includes(step)
    )
      return `${utility}-${step}`;
    return null;
  };
}

const utilities = [...new Set([...colorUtilities, ...radiusUtilities, "shadow"])].toSorted(
  (a, b) => b.length - a.length,
);
const arbitrary = new RegExp(
  `(?<=^|[\\s"'\`:{(])!?(${utilities.join("|")})-\\(--([\\w-]+)\\)(?![\\w(-])`,
  "g",
);

/** Each line of `source` that writes an arbitrary palette value a utility
 *  replaces, with the class and its replacement. */
export function arbitraryPaletteValues(
  source: string,
  names: ReturnType<typeof paletteUtilityNames>,
) {
  return source.split("\n").flatMap((text, index) =>
    [...text.matchAll(arbitrary)].flatMap(([match, utility = "", token = ""]) => {
      const replacement = names(utility, token);
      return replacement ? [{ line: index + 1, match, replacement }] : [];
    }),
  );
}
