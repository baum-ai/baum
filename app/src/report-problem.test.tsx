import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { IssueParts } from "./api";
import { issueLink, ReportProblemDialog } from "./report-problem";

const parts: IssueParts = {
  version: "1.2.3",
  os: "darwin/arm64",
  versions: "- claude: 2.1.7\n- codex: 0.41.0",
  diagnostics: "Overall: ok\n- claude: ok, version 2.1.7, ~/bin/claude\n- codex: ok",
  max_url: 6500,
  truncation_note: "(truncated)",
};
const query = (url: string) => Object.fromEntries(new URL(url).searchParams);

describe("issueLink", () => {
  it("attaches nothing, the versions or the whole summary", () => {
    expect(query(issueLink("It broke", "none", parts)!.url)).toEqual({
      template: "bug_report.yml",
      description: "It broke",
    });
    expect(query(issueLink("It broke", "versions", parts)!.url)).toEqual({
      template: "bug_report.yml",
      description: "It broke",
      version: "1.2.3",
      os: "darwin/arm64",
      diagnostics: parts.versions,
    });
    const all = issueLink("It broke", "all", parts)!;
    expect(all.url.startsWith("https://github.com/baum-ai/baum/issues/new?")).toBe(true);
    expect(query(all.url).diagnostics).toBe(parts.diagnostics);
    expect(all.diagnostics).toBe(parts.diagnostics);
  });
  it("drops diagnostics lines from the end, with the note, to fit the link", () => {
    const long = {
      ...parts,
      diagnostics: Array.from({ length: 1000 }, (_, i) => `- line ${i}`).join("\n"),
    };
    const link = issueLink("It broke", "all", long)!;
    expect(link.url.length).toBeLessThanOrEqual(6500);
    expect(link.diagnostics.startsWith("- line 0\n")).toBe(true);
    expect(link.diagnostics.endsWith("\n(truncated)")).toBe(true);
    expect(query(link.url).diagnostics).toBe(link.diagnostics);
  });
  it("gives no link when what was written alone is too long", () => {
    expect(issueLink("x".repeat(7000), "none", parts)).toBeNull();
    expect(issueLink("x".repeat(7000), "all", parts)).toBeNull();
  });
});

let node: HTMLDivElement;
let root: Root;
let open: ReturnType<typeof vi.fn>;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  open = vi.fn();
  vi.stubGlobal("open", open);
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
const button = (text: string) =>
  [...node.querySelectorAll("button")].find((b) => b.textContent?.startsWith(text))!;
const write = (text: string) =>
  act(async () => {
    const box = node.querySelector("textarea")!;
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
const preview = () => node.querySelector("[aria-label='What is attached']")?.textContent;

describe("ReportProblemDialog", () => {
  it("previews each attachment and opens the issue with what was written", async () => {
    const close = vi.fn();
    await act(async () => root.render(<ReportProblemDialog parts={parts} close={close} />));
    expect(node.textContent).toContain("This will open an issue in baum-ai/baum on GitHub");
    expect(button("Open issue").disabled).toBe(true);
    // Versions only is chosen first.
    expect(button("Versions only").getAttribute("aria-pressed")).toBe("true");
    expect(preview()).toBe(
      "baum version: 1.2.3\nOperating system: darwin/arm64\n\n- claude: 2.1.7\n- codex: 0.41.0",
    );
    await act(async () => button("All diagnostics").click());
    expect(preview()).toContain("~/bin/claude");
    await act(async () => button("Nothing").click());
    expect(preview()).toBeUndefined();
    expect(node.textContent).toContain("Only what you write is sent.");
    await write("The inbox is empty");
    await act(async () => button("Open issue").click());
    expect(open).toHaveBeenCalledTimes(1);
    expect(query(open.mock.calls[0]![0] as string)).toEqual({
      template: "bug_report.yml",
      description: "The inbox is empty",
    });
    expect(open.mock.calls[0]!.slice(1)).toEqual(["_blank", "noopener,noreferrer"]);
    expect(close).toHaveBeenCalled();
  });
  it("asks the daemon for the parts, and can still send nothing when it cannot answer", async () => {
    let answer: () => Promise<Response> = async () =>
      Response.json({ local: true, report: null, report_issue: parts });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => answer()),
    );
    await act(async () => root.render(<ReportProblemDialog close={() => {}} />));
    expect(preview()).toContain("- codex: 0.41.0");
    await act(async () => root.unmount());
    root = createRoot(node);
    answer = async () => Response.json({ error: "down" }, { status: 500 });
    await act(async () => root.render(<ReportProblemDialog close={() => {}} />));
    await write("Broken");
    expect(node.textContent).toContain("You can still send what you write with Nothing attached.");
    expect(button("Open issue").disabled).toBe(true);
    await act(async () => button("Nothing").click());
    expect(button("Open issue").disabled).toBe(false);
  });
});
