import { createContext, useContext, useEffect, useState, useRef, type ReactNode } from "react";
import { request } from "./api";

type Preferences = { theme: "system" | "light" | "dark"; key_hints: boolean };
const defaults: Preferences = { theme: "system", key_hints: true };
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
  };
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
  const refresh = async (signal?: AbortSignal) => {
    if (busy.current) return;
    const current = ++revision.current;
    try {
      const saved = await request<Preferences>("api/settings/appearance", undefined, "GET", signal);
      if (signal?.aborted || current !== revision.current) return;
      confirmed.current = normalize(saved);
      setPreferences(confirmed.current);
      setReady(true);
      setError("");
    } catch (cause) {
      if (!signal?.aborted && current === revision.current)
        setError(cause instanceof Error ? cause.message : "Local request failed.");
    }
  };
  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    const focus = () => void refresh(controller.signal);
    window.addEventListener("focus", focus);
    return () => {
      controller.abort();
      window.removeEventListener("focus", focus);
    };
  }, []);
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
  const update = async (patch: Partial<Preferences>) => {
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
  };
  return (
    <PreferencesContext.Provider
      value={{ preferences, ready, saving, error, update, refresh: () => void refresh() }}
    >
      {children}
    </PreferencesContext.Provider>
  );
}

export function usePreferences() {
  return useContext(PreferencesContext);
}
