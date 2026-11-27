import { useEffect, useState } from "react";
import { themeScope, type Theme } from "./theme";

/** Whether the operating system is in dark mode, which `system` follows. */
export function useSystemDark() {
  const [dark, setDark] = useState(
    () => window.matchMedia?.("(prefers-color-scheme: dark)").matches === true,
  );
  useEffect(() => {
    const query = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!query) return;
    const change = () => setDark(query.matches);
    change();
    query.addEventListener("change", change);
    return () => query.removeEventListener("change", change);
  }, []);
  return dark;
}

/** A sketch of baum's own layout in a theme: the sidebar on the canvas, and
 *  the page's panel with its header line and action, a few text bars and a
 *  diff with an added and a removed line. Only palette roles colour it,
 *  resolved on the element itself (`themeScope`), so it shows its own base
 *  whatever the page's mode is. */
export function ThemeMiniature({ theme }: { theme: Theme }) {
  return (
    <div className="theme-mini" aria-hidden="true" data-testid="theme-mini" {...themeScope(theme)}>
      <div className="theme-mini-rail">
        <span className="theme-mini-bar theme-mini-selected" />
        <span className="theme-mini-bar" />
        <span className="theme-mini-bar" />
      </div>
      <div className="theme-mini-panel">
        <div className="theme-mini-head">
          <span className="theme-mini-bar theme-mini-title" />
          <span className="theme-mini-action" />
        </div>
        <span className="theme-mini-bar" />
        <span className="theme-mini-bar theme-mini-short" />
        <div className="theme-mini-diff">
          <div className="theme-mini-line" data-tone="positive">
            <span className="theme-mini-mark" data-tone="positive" />
            <span className="theme-mini-bar" />
          </div>
          <div className="theme-mini-line" data-tone="critical">
            <span className="theme-mini-mark" data-tone="critical" />
            <span className="theme-mini-bar" />
          </div>
        </div>
      </div>
    </div>
  );
}
