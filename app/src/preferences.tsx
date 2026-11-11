import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  useRef,
  type ReactNode,
} from "react";
import { request } from "./api";

/** The key held with 1 to 8 for the sidebar's pages; "" is the platform's
 *  command key (⌘ on a Mac, Ctrl elsewhere). */
export type PageModifier = "" | "meta" | "ctrl" | "alt";
type Preferences = {
  theme: "system" | "light" | "dark";
  key_hints: boolean;
  page_modifier: PageModifier;
};
const defaults: Preferences = { theme: "system", key_hints: true, page_modifier: "" };
const PreferencesContext = createContext({
  preferences: defaults,
  ready: false,
  saving: false,
  error: "",
  update: (_patch: Partial<Preferences>) => {},
  refresh: () => {},
});

function normalize(saved: Partial<Preferences>): Preferences {
  return {
    theme: saved.theme === "light" || saved.theme === "dark" ? saved.theme : "system",
    key_hints: saved.key_hints !== false,
    page_modifier:
      saved.page_modifier === "meta" ||
      saved.page_modifier === "ctrl" ||
      saved.page_modifier === "alt"
        ? saved.page_modifier
        : "",
  };
}

function samePreferences(a: Preferences, b: Preferences) {
  return a.theme === b.theme && a.key_hints === b.key_hints && a.page_modifier === b.page_modifier;
}

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const revision = useRef(0);
  const busy = useRef(false);
  const queued = useRef<Partial<Preferences> | null>(null);
  const confirmed = useRef<Preferences>(defaults);
  const [preferences, setPreferences] = useState(defaults);
  const [ready, setReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (busy.current) return;
    const current = ++revision.current;
    try {
      const saved = await request<Preferences>("api/settings/appearance", undefined, "GET", signal);
      if (signal?.aborted || current !== revision.current) return;
      const next = normalize(saved);
      confirmed.current = next;
      setPreferences((now) => (samePreferences(now, next) ? now : next));
      setReady(true);
      setError("");
    } catch (cause) {
      if (!signal?.aborted && current === revision.current)
        setError(cause instanceof Error ? cause.message : "Local request failed.");
    }
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    const focus = () => void refresh(controller.signal);
    window.addEventListener("focus", focus);
    return () => {
      controller.abort();
      window.removeEventListener("focus", focus);
    };
  }, [refresh]);
  useEffect(() => {
    const dark = window.matchMedia?.("(prefers-color-scheme: dark)");
    const apply = () => {
      document.documentElement.dataset.theme =
        preferences.theme === "system" ? (dark?.matches ? "dark" : "light") : preferences.theme;
    };
    apply();
    dark?.addEventListener("change", apply);
    return () => dark?.removeEventListener("change", apply);
  }, [preferences.theme]);
  const update = useCallback(
    async (patch: Partial<Preferences>) => {
      if (!ready) return;
      setPreferences((current) => ({ ...current, ...patch }));
      ++revision.current;
      if (busy.current) {
        // A save is in flight: coalesce this change and send it once that save finishes.
        queued.current = { ...queued.current, ...patch };
        return;
      }
      busy.current = true;
      setSaving(true);
      setError("");
      // Changes made while a save is in flight land in `queued`; read it through a function.
      const pending = (): Partial<Preferences> | null => queued.current;
      let next: Partial<Preferences> | null = patch;
      try {
        while (next) {
          queued.current = null;
          try {
            const saved = await request<Preferences>("api/settings/appearance", next, "PUT");
            confirmed.current = normalize(saved);
            setPreferences({ ...confirmed.current, ...pending() });
          } catch (cause) {
            setPreferences({ ...confirmed.current, ...pending() });
            setError(cause instanceof Error ? cause.message : "Local request failed.");
          }
          next = pending();
        }
      } finally {
        busy.current = false;
        setSaving(false);
      }
    },
    [ready],
  );
  const reload = useCallback(() => void refresh(), [refresh]);
  const value = useMemo(
    () => ({ preferences, ready, saving, error, update, refresh: reload }),
    [preferences, ready, saving, error, update, reload],
  );
  return <PreferencesContext.Provider value={value}>{children}</PreferencesContext.Provider>;
}

export function usePreferences() {
  return useContext(PreferencesContext);
}
