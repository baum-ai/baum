import { expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { menuKey, type Panel, TrayPanelView } from "./tray";

const now = Date.parse("2026-10-26T12:00:00Z");
const panel: Panel = {
  summary: "1 running",
  jobs: true,
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

it("opens the jobs list from the status only while reviews run or wait", () => {
  const act = vi.fn();
  const busy = TrayPanelView({ panel, now, act });
  const status = (busy.props as { children: { type: string; props: { onClick?: () => void } }[] })
    .children[0];
  expect(status?.type).toBe("button");
  status?.props.onClick?.();
  expect(act).toHaveBeenCalledWith("summary");
  const idle = renderToStaticMarkup(
    <TrayPanelView panel={{ ...panel, summary: "Idle", jobs: false }} now={now} act={act} />,
  );
  expect(idle).toContain('<div class="tray-status">');
  expect(idle).not.toContain('class="tray-item tray-status"');
});

it("offers each alert as an item that runs it, above the waiting sessions", () => {
  const act = vi.fn();
  const view = TrayPanelView({
    panel: {
      ...panel,
      attention: [
        {
          id: "alert:job:6e280a68",
          text: "Review failed: acme/widgets #7",
          action: "Open job",
        },
      ],
    },
    now,
    act,
  });
  const html = renderToStaticMarkup(view);
  expect(html).toContain("Review failed: acme/widgets #7");
  expect(html).toContain("Open job");
  expect(html).toContain('class="tray-dot attention"');
  expect(html.indexOf("Review failed")).toBeLessThan(html.indexOf("Claude needs your permission"));
  const children = (view.props as { children: unknown[] }).children;
  const alerts = children[1] as { props: { onClick: () => void } }[];
  alerts[0]?.props.onClick();
  expect(act).toHaveBeenCalledWith("alert:job:6e280a68");
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

it("moves through the menu's items with the arrows, wrapping, and closes with Escape", () => {
  const menu = document.createElement("div");
  menu.innerHTML = renderToStaticMarkup(
    <TrayPanelView panel={{ ...panel, automatic: null }} now={now} act={() => {}} />,
  );
  document.body.append(menu);
  const close = vi.fn();
  const press = (key: string) => menuKey(new KeyboardEvent("keydown", { key }), menu, close);
  const focused = () => document.activeElement?.textContent;
  // From nothing, down starts at the first item and up at the last.
  press("ArrowDown");
  expect(focused()).toBe("baum1 running");
  press("ArrowUp");
  expect(focused()).toBe("Quit and stop background reviews");
  press("ArrowUp");
  // Pause is disabled while the daemon's state is unknown, so it is skipped.
  expect(focused()).toBe("Open baum");
  press("ArrowDown");
  expect(focused()).toBe("Quit and stop background reviews");
  press("ArrowDown");
  expect(focused()).toBe("baum1 running");
  press("End");
  expect(focused()).toBe("Quit and stop background reviews");
  press("Home");
  expect(focused()).toBe("baum1 running");
  // The running status opens the jobs list, so it is an item; the waiting
  // session follows it.
  press("ArrowDown");
  expect(focused()).toContain("Claude needs your permission");
  expect(close).not.toHaveBeenCalled();
  press("Escape");
  expect(close).toHaveBeenCalledTimes(1);
  menu.remove();
});
