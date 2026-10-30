import { useEffect, useState } from "react";
type Preferences = { theme: "system" | "light" | "dark"; keyHints: boolean };
const key = "baum-appearance";
function read(): Preferences {
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? "{}");
    return {
      theme: saved?.theme === "light" || saved?.theme === "dark" ? saved.theme : "system",
      keyHints: saved?.keyHints !== false,
    };
  } catch {
    return { theme: "system", keyHints: true };
  }
}
export function usePreferences() {
  const [preferences, setPreferences] = useState(read);
  useEffect(() => {
    const refresh = (event: Event) =>
      setPreferences(event instanceof CustomEvent ? (event.detail as Preferences) : read());
    window.addEventListener("storage", refresh);
    window.addEventListener("baum-preferences", refresh);
    return () => {
      window.removeEventListener("storage", refresh);
      window.removeEventListener("baum-preferences", refresh);
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
  const update = (patch: Partial<Preferences>) => {
    const next = { ...preferences, ...patch };
    try {
      localStorage.setItem(key, JSON.stringify(next));
    } catch {
      /* Storage may be unavailable. */
    }
    setPreferences(next);
    window.dispatchEvent(new CustomEvent("baum-preferences", { detail: next }));
  };
  return [preferences, update] as const;
}
