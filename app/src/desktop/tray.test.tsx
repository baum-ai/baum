import { expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { type Panel, TrayPanelView } from "./tray";

const now = Date.parse("2026-10-26T12:00:00Z");
const panel: Panel = {
  summary: "1 running",
  waiting: null,
  attention: [],
  needs: [
    {
      key: "claude-code:a",
      title: "Claude needs your permission to use Bash",
      detail: "Tidy the app's buttons",
    },
  ],
  automatic: true,
  pause_label: "Pause automatic reviews",
  plans: [
    {
      provider: "claude",
      available: true,
      windows: [
        { name: "5-hour", used_percent: 38, resets_at: "2026-10-26T15:00:00Z" },
        { name: "weekly", used_percent: 74, resets_at: "2026-10-28T12:00:00Z" },
      ],
      fetched_at: "2026-10-26T11:40:00Z",
    },
    {
      provider: "codex",
      available: true,
      windows: [{ name: "weekly", used_percent: 93, resets_at: "2026-10-27T18:00:00Z" }],
      fetched_at: "2026-10-26T11:50:00Z",
    },
  ],
};

it("draws the status, a waiting session, each plan window and the actions", () => {
  const html = renderToStaticMarkup(<TrayPanelView panel={panel} now={now} act={() => {}} />);
  expect(html).toContain("1 running");
  expect(html).toContain("Claude needs your permission to use Bash");
  expect(html).toContain("Tidy the app&#x27;s buttons · click to answer in baum");
  expect(html).toContain("Plan usage left");
  expect(html).not.toContain(" ago");
  expect(html).toContain("62% left · resets in 3 h");
  expect(html).toContain("26% left · resets in 2 d");
  expect(html).toContain("7% left · resets in 30 h");
  expect(html).toContain("var(--status-critical)");
  expect(html).toContain("Open baum");
  expect(html).toContain("Pause automatic reviews");
  expect(html).toContain("Quit and stop background reviews");
});

it("leaves plan usage out when no agent could be read, and says when reviews are paused", () => {
  const html = renderToStaticMarkup(
    <TrayPanelView
      panel={{
        ...panel,
        needs: [],
        automatic: false,
        pause_label: "Resume automatic reviews",
        plans: [
          {
            provider: "claude",
            available: false,
            reason: "Claude Code is not signed in on this computer.",
            windows: [],
            fetched_at: "",
          },
        ],
      }}
      now={now}
      act={() => {}}
    />,
  );
  expect(html).not.toContain("Plan usage left");
  expect(html).not.toContain("Claude Code");
  expect(html).toContain("Automatic reviews paused");
  expect(html).toContain("Resume automatic reviews");
});
