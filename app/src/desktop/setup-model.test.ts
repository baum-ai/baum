import { expect, it } from "vitest";
import {
  describeCommand,
  displayPath,
  repositoryPath,
  shownHarness,
  storedReviews,
  withModel,
} from "./setup-model";

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

it("keeps a chosen decision model and saves the harness's default as none", () => {
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
  expect(storedReviews({ ...base, decisions_model: "gpt-6-luna" }).decisions_model).toBe("");
  expect(
    storedReviews({
      ...base,
      decisions_model: "gpt-6-sol",
      decisions_effective_model: "gpt-6-sol",
      decisions_model_ignored: false,
    }),
  ).toEqual({ ...base, decisions_model: "gpt-6-sol" });
});

it("shows auto as the harness the daemon resolved, else the screen's guess", () => {
  const auto = { backend: "auto", model: "", automatic: true, pull_requests: "both" as const };
  expect(shownHarness({ ...auto, decisions_backend: "codex" }, "claude")).toBe("codex");
  expect(shownHarness({ ...auto, decisions_backend: "" }, "claude")).toBe("claude");
  expect(
    shownHarness({ ...auto, backend: "anthropic", decisions_backend: "codex" }, "claude"),
  ).toBe("anthropic");
  // Data extraction's own harness says nothing about the reviews'.
  expect(
    shownHarness(
      { ...auto, decisions_chosen_backend: "codex", decisions_backend: "codex" },
      "claude",
    ),
  ).toBe("claude");
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
    decisions_model: "",
  });
  expect(storedReviews({ ...own, decisions_model: "gpt-6.1-sol" }).decisions_model).toBe(
    "gpt-6.1-sol",
  );
});
