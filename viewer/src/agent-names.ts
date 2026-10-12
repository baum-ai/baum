/** Product names for the coding-agent harnesses a session can name, by the
 *  vendor slug the document records. */
const HARNESS_NAMES: Record<string, string> = {
  claude_code: "Claude Code",
  codex: "Codex",
  cursor: "Cursor",
  gemini_cli: "Gemini CLI",
  pi: "Pi",
};

/** A vendor slug as the product it names. Anything not known here is shown
 *  as recorded, with its first letter raised. */
export function harnessName(vendor: string): string {
  const known = Object.hasOwn(HARNESS_NAMES, vendor) ? HARNESS_NAMES[vendor] : undefined;
  if (known) return known;
  if (!vendor) return "Unknown";
  return vendor.charAt(0).toUpperCase() + vendor.slice(1).replaceAll("_", " ");
}
