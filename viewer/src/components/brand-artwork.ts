import type { Brand } from "../brands";

/** Vendor artwork is fixed identity, independent of the application's theme.
 *  Monochrome marks inherit the surrounding text color for contrast. */
export const BRAND_ARTWORK = {
  claude: "#d97757",
  deepseek: "#4d6bfe",
  gemini: "#3186ff",
  cursor: "currentColor",
  grok: "currentColor",
  openai: "currentColor",
  pi: "currentColor",
} as const satisfies Record<Brand, string>;
