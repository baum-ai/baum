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
import { invoke } from "./desktop/native";
import {
  activeTheme,
  allowedTokens,
  applyTheme,
  builtinThemes,
  slotTheme,
  type Theme,
  type ThemeBase,
} from "./theme";

/** The key held with 1 to 8 for the sidebar's pages; "" is the platform's
 *  command key (⌘ on a Mac, Ctrl elsewhere). */
export type PageModifier = "" | "meta" | "ctrl" | "alt";
type Preferences = {
  theme: "system" | "light" | "dark";
  key_hints: boolean;
  page_modifier: PageModifier;
  /** Whether the sidebar stays open beside the page or folds to its glyphs,
   *  opening over the page under the pointer. */
  sidebar: SidebarChoice;
  /** The theme of each slot, by id: the `theme` setting picks the slot in use. */
  light_theme: string;
  dark_theme: string;
  /** The built-in themes and the person's own. */
  themes: Theme[];
};
/** A save waiting its turn: changed settings, or a theme rename or delete. */
type QueuedSave =
  | { patch: Partial<Preferences> }
  | {
      path: string;
      body: unknown;
      method: string;
      resolve: () => void;
      reject: (cause: unknown) => void;
    };
/** The settings changed but not yet saved, laid over each answer. */
function unsaved(queue: QueuedSave[]): Partial<Preferences> {
  return queue.reduce<Partial<Preferences>>(
    (patch, save) => ("patch" in save ? { ...patch, ...save.patch } : patch),
    {},
  );
}
export type SidebarChoice = "pinned" | "folded";
const defaults: Preferences = {
  theme: "system",
  key_hints: true,
  page_modifier: "",
  sidebar: "pinned",
  light_theme: "default-light",
  dark_theme: "default-dark",
  themes: builtinThemes,
};
const PreferencesContext = createContext({
  preferences: defaults,
  ready: false,
  saving: false,
  error: "",
  update: (_patch: Partial<Preferences>) => {},
  refresh: (): void | Promise<void> => {},
  /** A theme shown in place of the saved one until it is set to null. */
  preview: null as Theme | null,
  setPreview: (_theme: Theme | null) => {},
  renameTheme: async (_id: string, _name: string): Promise<void> => {},
  deleteTheme: async (_id: string): Promise<void> => {},
});

function isDesktopApp() {
  return Boolean((window as Window & { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__);
}

function normalizeThemes(saved: unknown): Theme[] {
  const list: Theme[] = [];
  if (Array.isArray(saved))
    for (const item of saved as Partial<Theme>[]) {
      if (!item || typeof item.id !== "string" || typeof item.name !== "string") continue;
      const base: ThemeBase = item.base === "dark" ? "dark" : "light";
      const tokens: Record<string, string> = {};
      if (item.tokens && typeof item.tokens === "object")
        for (const [token, value] of Object.entries(item.tokens))
          if (typeof value === "string") tokens[token] = value;
      list.push({ id: item.id, name: item.name, base, tokens, builtin: item.builtin === true });
    }
  // The built-ins are always there, first, whatever an older daemon answers.
  return [
    ...builtinThemes,
    ...list.filter((theme) => !builtinThemes.some((builtin) => builtin.id === theme.id)),
  ];
}

function normalize(saved: Partial<Preferences>): Preferences {
  const themes = normalizeThemes(saved.themes);
  // A slot naming a theme that is gone shows the slot's built-in.
  const slotId = (id: string | undefined, slot: ThemeBase) =>
    id && themes.some((theme) => theme.id === id) ? id : `default-${slot}`;
  return {
    theme: saved.theme === "light" || saved.theme === "dark" ? saved.theme : "system",
    key_hints: saved.key_hints !== false,
    page_modifier:
      saved.page_modifier === "meta" ||
      saved.page_modifier === "ctrl" ||
      saved.page_modifier === "alt"
        ? saved.page_modifier
        : "",
    sidebar: saved.sidebar === "folded" ? "folded" : "pinned",
    light_theme: slotId(saved.light_theme, "light"),
    dark_theme: slotId(saved.dark_theme, "dark"),
    themes,
  };
}

function samePreferences(a: Preferences, b: Preferences) {
  return (
    a.theme === b.theme &&
    a.key_hints === b.key_hints &&
    a.page_modifier === b.page_modifier &&
    a.sidebar === b.sidebar &&
    a.light_theme === b.light_theme &&
    a.dark_theme === b.dark_theme &&
    JSON.stringify(a.themes) === JSON.stringify(b.themes)
  );
}

export function PreferencesProvider({ children }: { children: ReactNode }) {
  const revision = useRef(0);
  const busy = useRef(false);
  const queue = useRef<QueuedSave[]>([]);
  const confirmed = useRef<Preferences>(defaults);
  const [preferences, setPreferences] = useState(defaults);
  const [ready, setReady] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Theme | null>(null);
  // Refreshes asked while a save is in flight, resolved after the fetch that
  // follows the save.
  const afterSave = useRef<Array<() => void>>([]);
  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (busy.current) {
      if (signal?.aborted) return;
      await new Promise<void>((resolve) => afterSave.current.push(resolve));
      return;
    }
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
    // Until the saved preferences arrive the defaults would strip what the
    // desktop app's init script already painted, so wait, unless previewing.
    if (!ready && !preview) return;
    const dark = window.matchMedia?.("(prefers-color-scheme: dark)");
    const apply = () =>
      applyTheme(
        document.documentElement,
        activeTheme(preferences, dark?.matches === true, preview),
      );
    apply();
    dark?.addEventListener("change", apply);
    return () => dark?.removeEventListener("change", apply);
  }, [preferences, preview, ready]);
  // The desktop app keeps what the saved theme resolves to for its own
  // screens, which cannot ask the daemon. A preview is never sent.
  const sentTheme = useRef("");
  useEffect(() => {
    if (!ready || !isDesktopApp()) return;
    const palette = (slot: ThemeBase) => {
      const theme = slotTheme(preferences, slot);
      return { base: theme.base, tokens: allowedTokens(theme) };
    };
    const args = { mode: preferences.theme, light: palette("light"), dark: palette("dark") };
    const key = JSON.stringify(args);
    if (key === sentTheme.current) return;
    sentTheme.current = key;
    void (async () => {
      try {
        await invoke("save_theme", args);
      } catch {
        // Retry the next time the theme changes or the page loads.
        if (sentTheme.current === key) sentTheme.current = "";
      }
    })();
  }, [preferences, ready]);
  // Saves run one at a time, in order: a PUT of changed settings, or a theme
  // rename or delete. Each answers the whole preferences, so running them in
  // turn means no older answer lands after a newer one. Settings changed while
  // a PUT waits join it.
  const drain = useCallback(async () => {
    busy.current = true;
    setSaving(true);
    setError("");
    try {
      for (let save = queue.current.shift(); save; save = queue.current.shift()) {
        if ("patch" in save) {
          try {
            const saved = await request<Preferences>("api/settings/appearance", save.patch, "PUT");
            confirmed.current = normalize(saved);
            setPreferences({ ...confirmed.current, ...unsaved(queue.current) });
          } catch (cause) {
            setPreferences({ ...confirmed.current, ...unsaved(queue.current) });
            setError(cause instanceof Error ? cause.message : "Local request failed.");
          }
        } else {
          try {
            const saved = await request<Preferences>(save.path, save.body, save.method);
            ++revision.current;
            confirmed.current = normalize(saved);
            setPreferences({ ...confirmed.current, ...unsaved(queue.current) });
            save.resolve();
          } catch (cause) {
            save.reject(cause);
          }
        }
      }
    } finally {
      busy.current = false;
      setSaving(false);
      const waiting = afterSave.current;
      afterSave.current = [];
      if (waiting.length) void refresh().finally(() => waiting.forEach((resolve) => resolve()));
    }
  }, [refresh]);
  const update = useCallback(
    async (patch: Partial<Preferences>) => {
      if (!ready) return;
      setPreferences((current) => ({ ...current, ...patch }));
      ++revision.current;
      const last = queue.current.at(-1);
      if (last && "patch" in last) last.patch = { ...last.patch, ...patch };
      else queue.current.push({ patch });
      if (!busy.current) await drain();
    },
    [ready, drain],
  );
  const reload = useCallback(() => refresh(), [refresh]);
  // Renaming and deleting a theme answer the preferences as they now are.
  // They wait behind any save in flight, so its answer cannot undo them.
  const changeTheme = useCallback(
    (path: string, body: unknown, method: string) =>
      new Promise<void>((resolve, reject) => {
        ++revision.current;
        queue.current.push({ path, body, method, resolve, reject });
        if (!busy.current) void drain();
      }),
    [drain],
  );
  const renameTheme = useCallback(
    (id: string, name: string) =>
      changeTheme(`api/settings/appearance/themes/${encodeURIComponent(id)}`, { name }, "PATCH"),
    [changeTheme],
  );
  const deleteTheme = useCallback(
    (id: string) =>
      // The daemon takes only JSON requests, so a delete carries an empty body.
      changeTheme(`api/settings/appearance/themes/${encodeURIComponent(id)}`, {}, "DELETE"),
    [changeTheme],
  );
  const value = useMemo(
    () => ({
      preferences,
      ready,
      saving,
      error,
      update,
      refresh: reload,
      preview,
      setPreview,
      renameTheme,
      deleteTheme,
    }),
    [preferences, ready, saving, error, update, reload, preview, renameTheme, deleteTheme],
  );
  return <PreferencesContext.Provider value={value}>{children}</PreferencesContext.Provider>;
}

export function usePreferences() {
  return useContext(PreferencesContext);
}
