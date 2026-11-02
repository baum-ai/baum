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
 *  and effort references in the desktop README. Models baum does not know
 *  get the standard levels; the agent handles one a model lacks. */
export function modelEfforts(backend: string, model: string): readonly string[] {
  if (!model.trim() || (backend !== "claude" && backend !== "codex")) return [];
  if (backend === "codex" && ["gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra"].includes(model))
    return delegated;
  return standard;
}

export function supportedEffort(backend: string, model: string, effort = "") {
  return modelEfforts(backend, model).includes(effort) ? effort : "";
}
