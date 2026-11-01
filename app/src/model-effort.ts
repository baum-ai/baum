const standard = ["low", "medium", "high", "xhigh", "max"];
const delegated = [...standard, "ultra"];

export const effortLabels: Record<string, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
  ultra: "Ultra",
};

/** CLI capabilities, rather than API-only reasoning settings. See the model
 *  and effort references in the desktop README. */
export function modelEfforts(backend: string, model: string): readonly string[] {
  if (
    (backend === "claude" || backend === "anthropic") &&
    /^claude-(haiku|sonnet|opus)-5-5(?:$|-)/.test(model)
  )
    return standard;
  if (backend === "codex") {
    if (model === "gpt-6-luna") return standard;
    if (["gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra"].includes(model)) return delegated;
  }
  return [];
}

export function supportedEffort(backend: string, model: string, effort = "") {
  return modelEfforts(backend, model).includes(effort) ? effort : "";
}
