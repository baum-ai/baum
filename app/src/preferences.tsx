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
      setPreferences(normalize(saved));
      setReady(true);
      setError("");
    } catch (cause) {
      if (!signal?.aborted && current === revision.current) setError(String(cause));
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
    if (!ready || busy.current) return;
    busy.current = true;
    const previous = preferences;
    setPreferences({ ...previous, ...patch });
    ++revision.current;
    setSaving(true);
    setError("");
    try {
      const saved = await request<Preferences>("api/settings/appearance", patch, "PUT");
      setPreferences(normalize(saved));
    } catch (cause) {
      setPreferences(previous);
      setError(String(cause));
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
