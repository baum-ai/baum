import { useEffect, useRef, useState } from "react";
import {
  capital,
  leftOf,
  providerName,
  readablePlans,
  span,
  tone,
  type PlanUsageEntry,
} from "../plan-usage";
import { invoke } from "./native";

/** What the tray panel draws: the tray's last view, from `tray_panel`. */
export type Panel = {
  summary: string;
  waiting: string | null;
  attention: string[];
  needs: { key: string; title: string; detail: string }[];
  automatic: boolean | null;
  pause_label: string;
  plans: PlanUsageEntry[];
};

/** How often the open panel asks again; the tray polls the daemon itself. */
const REFRESH_MS = 2000;

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
  return (
    <div className="tray-menu" role="menu" aria-label="baum">
      <div className="tray-status">
        <strong>baum</strong>
        <span className={`tray-dot${paused ? " paused" : ""}`} aria-hidden="true" />
        <span>{paused ? "Automatic reviews paused" : panel.summary}</span>
      </div>
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
      {panel.attention.map((line) => (
        <p key={line} className="tray-attention">
          {line}
        </p>
      ))}
      {plans.length > 0 ? (
        <>
          <hr />
          <section className="tray-usage" aria-label="Plan usage left">
            <div className="tray-usage-head">Plan usage left</div>
            {plans.flatMap((plan) =>
              plan.windows.map((win) => {
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
            )}
          </section>
        </>
      ) : null}
      <hr />
      <button type="button" role="menuitem" className="tray-item" onClick={() => act("open")}>
        Open baum
      </button>
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
    void load();
    const timer = window.setInterval(load, REFRESH_MS);
    window.addEventListener("focus", load);
    return () => {
      live = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
    };
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
          <div className="tray-status">
            <strong>baum</strong>
            <span>Connecting to the baum daemon</span>
          </div>
        </div>
      )}
    </div>
  );
}
