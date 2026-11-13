import { useEffect, useRef, useState } from "react";
import {
  capital,
  leftOf,
  needsSignIn,
  providerName,
  readablePlans,
  span,
  tone,
  type PlanUsageEntry,
} from "../plan-usage";
import { useVisibleInterval } from "../visible-interval";
import { invoke } from "./native";

/** What the tray panel draws: the tray's last view, from `tray_panel`. */
export type Panel = {
  summary: string;
  /** Reviews run or wait, so the summary opens the jobs list. */
  jobs: boolean;
  waiting: string | null;
  /** What the icon is red for, each going where to look (`alert:…`). */
  attention: { id: string; text: string; action: string }[];
  needs: { key: string; title: string; detail: string }[];
  automatic: boolean | null;
  pause_label: string;
  plans: PlanUsageEntry[];
};

/** How often the visible panel asks again; the tray reads the daemon itself. */
const REFRESH_MS = 2000;

/** The menu's keys, as a native menu's: ↓ and ↑ move through its items,
 *  wrapping, from the first or the last when none has the focus yet; Home
 *  and End go to the first and last, and Escape closes the panel. Enter and
 *  Space press the focused item, as buttons do. */
export function menuKey(event: KeyboardEvent, menu: HTMLElement, close: () => void) {
  const items = Array.from(
    menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'),
  );
  const at = items.indexOf(document.activeElement as HTMLButtonElement);
  const last = items.length - 1;
  const target =
    event.key === "ArrowDown"
      ? items[at < 0 || at === last ? 0 : at + 1]
      : event.key === "ArrowUp"
        ? items[at <= 0 ? last : at - 1]
        : event.key === "Home"
          ? items[0]
          : event.key === "End"
            ? items[last]
            : null;
  if (target) {
    event.preventDefault();
    target.focus();
  } else if (event.key === "Escape") {
    event.preventDefault();
    close();
  }
}

/** The tray panel's content. Pure so it renders without the app. */
export function TrayPanelView({
  panel,
  now,
  act,
}: {
  panel: Panel;
  now: number;
  act: (id: string) => void;
}) {
  const plans = readablePlans(panel.plans);
  const paused = panel.automatic === false;
  // What the daemon is doing, after a dot of the icon's state: as the menu
  // bar's own items do, the panel names no app, since its icon already does.
  const summary = (
    <>
      <span
        className={`tray-dot${panel.attention.length > 0 ? " attention" : paused ? " paused" : ""}`}
        aria-hidden="true"
      />
      <span>{paused ? "Automatic reviews paused" : panel.summary}</span>
    </>
  );
  return (
    <div className="tray-menu" role="menu" aria-label="baum">
      {/* While reviews run or wait, the summary opens the jobs list. The
          panel has no Open item: the Inbox's row below, shown even when
          it is empty, opens baum. */}
      {panel.jobs ? (
        <button
          type="button"
          role="menuitem"
          className="tray-item tray-summary"
          onClick={() => act("summary")}
        >
          {summary}
        </button>
      ) : (
        <p className="tray-summary">{summary}</p>
      )}
      {panel.attention.map((alert) => (
        <button
          key={alert.id}
          type="button"
          role="menuitem"
          className="tray-item tray-alert"
          onClick={() => act(alert.id)}
        >
          <strong>{alert.text}</strong>
          <span>{alert.action}</span>
        </button>
      ))}
      {panel.needs.map((needs) => (
        <button
          key={needs.key}
          type="button"
          role="menuitem"
          className="tray-item tray-needs"
          onClick={() => act(`session:${needs.key}`)}
        >
          <strong>{needs.title}</strong>
          <span>{needs.detail} · click to answer in baum</span>
        </button>
      ))}
      {panel.waiting ? (
        <button type="button" role="menuitem" className="tray-item" onClick={() => act("open")}>
          {panel.waiting}
        </button>
      ) : null}
      {plans.length > 0 ? (
        <>
          <hr />
          <section className="tray-usage" aria-label="Plan usage left">
            <div className="tray-usage-head">Plan usage left</div>
            {plans.flatMap((plan) => [
              needsSignIn(plan) ? (
                <div key={plan.provider} className="tray-meter">
                  <div className="tray-meter-line">
                    <strong>{providerName[plan.provider] ?? plan.provider}</strong>
                    <span className="tray-muted">{plan.reason}</span>
                  </div>
                </div>
              ) : null,
              ...plan.windows.map((win) => {
                const left = typeof win.used_percent === "number" ? leftOf(win.used_percent) : null;
                const resets = win.resets_at ? Date.parse(win.resets_at) : Number.NaN;
                return (
                  <div key={`${plan.provider}:${win.name}`} className="tray-meter">
                    <div className="tray-meter-line">
                      <span>
                        <strong>{providerName[plan.provider] ?? plan.provider}</strong> ·{" "}
                        {capital(win.name)}
                      </span>
                      <span className="tray-muted">
                        {left === null
                          ? "reset since baum last saw it"
                          : `${left}% left${Number.isNaN(resets) ? "" : ` · resets in ${span(resets - now)}`}`}
                      </span>
                    </div>
                    <div
                      className="tray-track"
                      role="meter"
                      aria-label={`${providerName[plan.provider] ?? plan.provider} ${win.name} left`}
                      aria-valuemin={0}
                      aria-valuemax={100}
                      aria-valuenow={left ?? undefined}
                    >
                      {left === null ? null : (
                        <div style={{ width: `${left}%`, background: tone(left) }} />
                      )}
                    </div>
                  </div>
                );
              }),
            ])}
          </section>
        </>
      ) : null}
      <hr />
      <button
        type="button"
        role="menuitem"
        className="tray-item"
        disabled={panel.automatic === null}
        onClick={() => act("pause")}
      >
        {panel.pause_label}
      </button>
      <hr />
      <button type="button" role="menuitem" className="tray-item" onClick={() => act("quit-stop")}>
        Quit and stop background reviews
      </button>
    </div>
  );
}

/** The panel page: asks the tray for its view while open and fits the
 *  window to its height. */
export function TrayPanel() {
  const [panel, setPanel] = useState<Panel | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const root = useRef<HTMLDivElement>(null);
  const reload = useRef<(() => Promise<void>) | null>(null);
  useEffect(() => {
    let live = true;
    const load = () =>
      invoke<Panel | null>("tray_panel").then(
        (next) => {
          if (!live) return;
          setNow(Date.now());
          setPanel(next);
        },
        () => undefined,
      );
    reload.current = load;
    void load();
    window.addEventListener("focus", load);
    return () => {
      live = false;
      reload.current = null;
      window.removeEventListener("focus", load);
    };
  }, []);
  // A closed panel asks for nothing; opening it refreshes once.
  useVisibleInterval(() => void reload.current?.(), REFRESH_MS);
  // The window takes the keys, so the first arrow reaches the menu before
  // any item has the focus.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (root.current) menuKey(event, root.current, () => act("close"));
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    const fit = () =>
      void invoke("tray_panel_height", { height: Math.ceil(element.scrollHeight) }).catch(
        () => undefined,
      );
    const observer = new ResizeObserver(fit);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const act = (id: string) => void invoke("tray_action", { id }).catch(() => undefined);
  return (
    <div ref={root}>
      {panel ? (
        <TrayPanelView panel={panel} now={now} act={act} />
      ) : (
        <div className="tray-menu">
          <p className="tray-summary">Connecting to baum…</p>
        </div>
      )}
    </div>
  );
}
