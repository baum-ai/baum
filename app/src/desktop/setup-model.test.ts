import { expect, it } from "vitest";
import { displayPath, repositoryPath, describeCommand } from "./setup-model";

it("abbreviates only the current home and expands repository input for native actions", () => {
  expect(displayPath("Install /home/user/.local/bin/baum", "/home/user")).toBe(
    "Install $HOME/.local/bin/baum",
  );
  expect(displayPath("/tmp/home-other/repo", "/tmp/home")).toBe("/tmp/home-other/repo");
  expect(displayPath("/home/user/repo", null)).toBe("/home/user/repo");
  expect(displayPath("C:\\Users\\demo\\repo", "C:\\Users\\demo")).toBe("$HOME\\repo");
  expect(repositoryPath("$HOME/repo", "/home/user")).toBe("/home/user/repo");
  expect(repositoryPath("$HOME-other/repo", "/home/user")).toBe("$HOME-other/repo");
});

it("keeps a replacement's real path separate from its displayed text", () => {
  const found = { path: "/home/user/.local/bin/baum", resolved: "/old/baum", version: "1" };
  const command = describeCommand({
    installed: false,
    found,
    plan: { link: found.path, admin: false, on_path: true, replaces: found },
  });
  expect(displayPath(command.state, "/home/user")).toContain("$HOME/.local/bin/baum");
  expect(command.replace).toBe(found.path);
});
