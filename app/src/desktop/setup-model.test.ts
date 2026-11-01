import { expect, it } from "vitest";
import {
  describeCommand,
  detectedHarnesses,
  displayPath,
  repositoryPath,
  shownHarness,
  setupSave,
  shownFix,
  storedReviews,
  withModel,
  withEfforts,
} from "./setup-model";

const followsReviews = { fix_backend: "", fix_model: "", fix_effort: "" };

it("abbreviates only the current home and expands repository input for native actions", () => {
  expect(displayPath("Install /home/user/.local/bin/baum", "/home/user")).toBe(
    "Install ~/.local/bin/baum",
  );
  expect(displayPath("/tmp/home-other/repo", "/tmp/home")).toBe("/tmp/home-other/repo");
  expect(displayPath("/home/user/repo", null)).toBe("/home/user/repo");
  expect(displayPath("C:\\Users\\demo\\repo", "C:\\Users\\demo")).toBe("~\\repo");
  expect(repositoryPath("~/repo", "/home/user")).toBe("/home/user/repo");
  expect(repositoryPath("~-other/repo", "/home/user")).toBe("~-other/repo");
});

it("keeps a replacement's real path separate from its displayed text", () => {
  const found = { path: "/home/user/.local/bin/baum", resolved: "/old/baum", version: "1" };
  const command = describeCommand({
    installed: false,
    found,
    plan: { link: found.path, admin: false, on_path: true, replaces: found },
  });
  expect(displayPath(command.state, "/home/user")).toContain("~/.local/bin/baum");
  expect(command.replace).toBe(found.path);
});

it("keeps a chosen decision model out of onboarding saves", () => {
  const base = {
    backend: "codex",
    model: "gpt-6-sol",
    automatic: true,
    pull_requests: "both" as const,
  };
  expect(withModel({ ...base, decisions_model: "" }).decisions_model).toBe("gpt-6-luna");
  // Only the daemon says whether a model is used; the setup keeps it.
  expect(withModel({ ...base, decisions_model: "claude-haiku-5-5" }).decisions_model).toBe(
    "claude-haiku-5-5",
  );
  expect(storedReviews({ ...base, decisions_model: "gpt-6-luna" })).toEqual({
    ...base,
    ...followsReviews,
  });
  expect(
    storedReviews({
      ...base,
      decisions_model: "gpt-6-sol",
      decisions_effective_model: "gpt-6-sol",
      decisions_model_ignored: false,
    }),
  ).toEqual({ ...base, ...followsReviews });
});

it("saves fixes and gives extraction the review choice only when setup changes it", () => {
  const saved = {
    backend: "auto",
    model: "",
    effort: "",
    automatic: true,
    pull_requests: "both" as const,
    ...followsReviews,
    decisions_chosen_backend: "anthropic",
    decisions_model: "claude-sonnet-5-5",
    decisions_effort: "low",
  };
  const shown = withModel({ ...saved, backend: "codex" });
  // Luna is the harness's default decision model, so extraction follows it.
  expect(setupSave(shown, saved)).toMatchObject({
    backend: "codex",
    model: "gpt-6-luna",
    decisions_model: "",
    decisions_effort: "",
  });
  const chosen = { ...shown, model: "gpt-6.1-sol", effort: "high" };
  expect(setupSave(chosen, saved)).toMatchObject({
    decisions_model: "gpt-6.1-sol",
    decisions_effort: "high",
  });
  // Unchanged settings save nothing; a fix change alone leaves extraction be.
  expect(setupSave(chosen, chosen)).toBeNull();
  const fixes = withModel({ ...chosen, fix_backend: "claude", fix_model: "", fix_effort: "max" });
  expect(fixes).toMatchObject({ fix_model: "claude-haiku-5-5", fix_effort: "max" });
  const fixOnly = setupSave(fixes, chosen);
  expect(fixOnly).toMatchObject({
    fix_backend: "claude",
    fix_model: "claude-haiku-5-5",
    fix_effort: "max",
  });
  expect(fixOnly).not.toHaveProperty("decisions_model");
  // Following the reviews again clears the fixes' own model and effort.
  expect(storedReviews({ ...fixes, fix_backend: "" })).toMatchObject(followsReviews);
  expect(shownFix(fixes, ["codex"])).toMatchObject(followsReviews);
  expect(shownFix(fixes, ["claude"])).toBe(fixes);
});

it("shows the resolved harness only when installed and limits setup to Claude Code and Codex", () => {
  const auto = { backend: "auto", model: "", automatic: true, pull_requests: "both" as const };
  expect(shownHarness({ ...auto, decisions_backend: "codex" }, ["claude", "codex"])).toBe("codex");
  expect(shownHarness({ ...auto, decisions_backend: "" }, ["claude"])).toBe("claude");
  expect(
    shownHarness({ ...auto, backend: "anthropic", decisions_backend: "codex" }, ["claude"]),
  ).toBe("claude");
  expect(shownHarness({ ...auto, backend: "cursor-agent" }, ["codex"])).toBe("codex");
  expect(shownHarness({ ...auto, decisions_backend: "anthropic" }, ["claude"])).toBe("claude");
  // Data extraction's own harness says nothing about the reviews'.
  expect(
    shownHarness({ ...auto, decisions_chosen_backend: "codex", decisions_backend: "codex" }, [
      "claude",
      "codex",
    ]),
  ).toBe("claude");
});

it("replaces a missing saved agent with an installed one and leaves neither selected when none are installed", () => {
  const saved = {
    backend: "claude",
    model: "claude-opus-5-5",
    automatic: true,
    pull_requests: "both" as const,
  };
  expect(shownHarness(saved, ["codex"])).toBe("codex");
  expect(shownHarness({ ...saved, backend: "codex" }, ["claude"])).toBe("claude");
  expect(shownHarness(saved, ["claude", "codex"])).toBe("claude");
  expect(shownHarness(saved, [])).toBe("");
  expect(shownHarness({ ...saved, backend: "auto", decisions_backend: "claude" }, ["codex"])).toBe(
    "codex",
  );
  expect(shownHarness({ ...saved, backend: "auto" }, [])).toBe("");
});

it("detects agent CLIs independently of whether their baum hooks are connected", () => {
  const base = { installed: false, enabled: false, plugin: "baum@baum" };
  expect(
    detectedHarnesses([
      { ...base, agent: "codex", name: "Codex", detected: true },
      { ...base, agent: "claude-code", name: "Claude Code", detected: false, installed: true },
    ]),
  ).toEqual(["codex"]);
  expect(detectedHarnesses([])).toEqual([]);
});

it("keeps the decision model of an extraction harness Settings chose", () => {
  const own = {
    backend: "claude",
    model: "claude-opus-5-5",
    automatic: true,
    pull_requests: "both" as const,
    decisions_chosen_backend: "codex",
  };
  expect(withModel({ ...own, decisions_model: "" }).decisions_model).toBe("gpt-6-luna");
  expect(storedReviews({ ...own, decisions_model: "gpt-6-luna" })).toEqual({
    backend: "claude",
    model: "claude-opus-5-5",
    automatic: true,
    pull_requests: "both",
    ...followsReviews,
  });
  expect(storedReviews({ ...own, decisions_model: "gpt-6.1-sol" })).not.toHaveProperty(
    "decisions_model",
  );
});

it("resets an effort unsupported by a new model without filling an empty custom model", () => {
  const base = {
    backend: "codex",
    model: "gpt-6.1-sol",
    effort: "ultra",
    automatic: true,
    pull_requests: "both" as const,
    decisions_model: "gpt-6-luna",
    decisions_effort: "xhigh",
  };
  expect(withEfforts(base)).toEqual(base);
  expect(withEfforts({ ...base, model: "gpt-6-luna" }).effort).toBe("");
  expect(withEfforts({ ...base, model: "" })).toMatchObject({ model: "", effort: "" });
  expect(storedReviews(base)).toEqual({
    backend: "codex",
    model: "gpt-6.1-sol",
    effort: "ultra",
    automatic: true,
    pull_requests: "both",
    ...followsReviews,
  });
});

it("uses the effective decision model's effort options when the saved model is ignored", () => {
  const base = {
    backend: "claude",
    model: "claude-sonnet-5-5",
    automatic: true,
    pull_requests: "both" as const,
    decisions_model: "gpt-6-luna",
    decisions_effort: "max",
    decisions_model_ignored: true,
    decisions_effective_model: "claude-haiku-5-5",
  };
  expect(withModel(base).decisions_effort).toBe("max");
  expect(withEfforts(base, "claude-haiku-5-5").decisions_effort).toBe("max");
});

it("only calls Codex connected when its hooks are enabled and trusted", async () => {
  const { agentConnected, describeAgent } = await import("./setup-model");
  const codex = {
    agent: "codex",
    name: "Codex",
    detected: true,
    installed: true,
    enabled: true,
    plugin: "baum@baum",
  };
  expect(agentConnected(codex)).toBe(false);
  expect(describeAgent(codex)).toBe("Hook approval could not be verified");
  expect(describeAgent({ ...codex, trust: { state: "untrusted" } })).toBe(
    "Needs approval in Codex",
  );
  expect(agentConnected({ ...codex, trust: { state: "trusted" } })).toBe(true);
  expect(agentConnected({ ...codex, enabled: false, trust: { state: "trusted" } })).toBe(false);
  expect(agentConnected({ ...codex, agent: "claude-code" })).toBe(true);
});
