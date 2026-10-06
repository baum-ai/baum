/** Which product mark a harness or a model wears.
 *
 *  Harnesses arrive as the vendor slugs documents record (`claude_code`, `codex`),
 *  though older documents and fixtures name them loosely (`claude`, `Claude
 *  Code`), so both are normalised before matching. Models are matched by the
 *  family in their name, since a model id carries its maker only by
 *  convention: `cursor-grok-4.5-high-fast` is a Grok model served by Cursor,
 *  and wears Grok's mark, as does a gateway id such as `openai/gpt-5.5`. Anything unrecognised gets no mark rather than a
 *  guess. */
export type Brand = "claude" | "cursor" | "deepseek" | "gemini" | "grok" | "openai" | "pi";

const HARNESS_BRANDS: Record<string, Brand> = {
  claudecode: "claude",
  claude: "claude",
  // Codex wears OpenAI's mark: its own, a solid cloud with a cut-out
  // prompt, reads as a blot at the sizes these rows draw it.
  codex: "openai",
  cursor: "cursor",
  pi: "pi",
  geminicli: "gemini",
  gemini: "gemini",
};

export function harnessBrand(vendor: string | null | undefined): Brand | null {
  if (!vendor) return null;
  return HARNESS_BRANDS[vendor.toLowerCase().replaceAll(/[^a-z0-9]/g, "")] ?? null;
}

/** First match wins, so a family named anywhere in the id beats the
 *  reseller that served it: `cursor-grok-4.5` is Grok. */
const MODEL_FAMILIES: [RegExp, Brand][] = [
  [/grok/, "grok"],
  [/claude|opus|sonnet|haiku|fable/, "claude"],
  [/deepseek/, "deepseek"],
  [/gemini/, "gemini"],
  [/^(gpt|chatgpt|o\d)|codex/, "openai"],
];

/** Cursor's own models, and its ids that name no family. */
const CURSOR_MODEL = /^(cursor|composer)/;

export function modelBrand(model: string | null | undefined): Brand | null {
  if (!model) return null;
  const name = model.toLowerCase();
  // A gateway (`openai/gpt-5.5`) or reseller (`cursor-gpt-5`) prefix hides
  // the family from the anchored patterns, so match on the bare id.
  const bare = name.replace(/^[a-z0-9_-]+\//, "").replace(/^cursor-/, "");
  const family = MODEL_FAMILIES.find(([pattern]) => pattern.test(bare))?.[1];
  if (family) return family;
  return CURSOR_MODEL.test(name) || CURSOR_MODEL.test(bare) ? "cursor" : null;
}
