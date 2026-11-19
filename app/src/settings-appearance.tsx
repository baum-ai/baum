import { useEffect, useId, useState, type ReactNode } from "react";
import { CheckIcon } from "@baum/viewer/components/icons";
import { Alert, Button, SegmentedControl } from "@baum/viewer/components/ui";
import { MenuButton, useContextMenu } from "./context-menu";
import { ConfirmDialog } from "./group-dialogs";
import { usePreferences } from "./preferences";
import { SectionHead, SettingRow } from "./settings";
import { activeSlot, slotTheme, themeScope, type Theme, type ThemeBase } from "./theme";

/** Whether the operating system is in dark mode, which `system` follows. */
function useSystemDark() {
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
function ThemeMiniature({ theme }: { theme: Theme }) {
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

/** What the menu on a custom theme's thumbnail opens: renaming is typed in a
 *  row below the strips, and a delete asks first. */
function useThemeActions() {
  const { renameTheme, deleteTheme } = usePreferences();
  const menu = useContextMenu();
  const [renaming, setRenaming] = useState<{ id: string; name: string } | null>(null);
  const [deleting, setDeleting] = useState<{ id: string; name: string } | null>(null);
  const [pending, setPending] = useState(false);
  const [problem, setProblem] = useState("");
  const run = async (action: () => Promise<void>) => {
    setPending(true);
    setProblem("");
    try {
      await action();
      return true;
    } catch (cause) {
      setProblem(cause instanceof Error ? cause.message : "Local request failed.");
      return false;
    } finally {
      setPending(false);
    }
  };
  const items = (theme: Theme) => () => [
    { label: "Rename", onSelect: () => setRenaming({ id: theme.id, name: theme.name }) },
    {
      label: "Delete",
      danger: true,
      onSelect: () => setDeleting({ id: theme.id, name: theme.name }),
    },
  ];
  const dialogs = (
    <>
      {renaming ? (
        <form
          className="theme-rename"
          onSubmit={(event) => {
            event.preventDefault();
            const name = renaming.name.trim();
            if (!name) return;
            void run(() => renameTheme(renaming.id, name)).then((ok) => {
              if (ok) setRenaming(null);
            });
          }}
        >
          <input
            type="text"
            className="local-field"
            aria-label={`Name of ${renaming.name}`}
            autoFocus
            maxLength={60}
            value={renaming.name}
            onChange={(event) => setRenaming({ id: renaming.id, name: event.target.value })}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.stopPropagation();
                setRenaming(null);
              }
            }}
          />
          <Button type="submit" size="compact" disabled={pending || !renaming.name.trim()}>
            Rename
          </Button>
          <Button type="button" variant="ghost" size="compact" onClick={() => setRenaming(null)}>
            Cancel
          </Button>
        </form>
      ) : null}
      {problem ? <Alert>{problem}</Alert> : null}
      {menu.menu}
      {deleting ? (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          action="Delete"
          danger
          pending={pending}
          close={() => setDeleting(null)}
          confirm={() =>
            void run(() => deleteTheme(deleting.id)).then((ok) => {
              if (ok) setDeleting(null);
            })
          }
        >
          A slot using this theme goes back to the default.
        </ConfirmDialog>
      ) : null}
    </>
  );
  return { menu, items, dialogs };
}

/** One slot's row: its heading (marked "in use" when it is the slot in use),
 *  when it is used, and a strip of thumbnails, one per theme, which are also
 *  the choice: visually hidden native radios, so the arrow keys move within
 *  the row. A custom theme's thumbnail has its menu in its corner, outside the
 *  radio's label so it does not choose the theme. */
function ThemeRow({
  slot,
  inUse,
  ready,
  choose,
  actions,
}: {
  slot: ThemeBase;
  inUse: boolean;
  ready: boolean;
  choose: (id: string) => void;
  actions: ReturnType<typeof useThemeActions>;
}) {
  const { preferences } = usePreferences();
  const id = useId();
  const chosen = slotTheme(preferences, slot);
  const title = slot === "dark" ? "Dark theme" : "Light theme";
  return (
    <section className="theme-row" data-slot={slot} data-in-use={inUse ? "" : undefined}>
      <h3 className="theme-row-title">
        {title}
        {inUse ? <span className="theme-row-use"> · in use</span> : null}
      </h3>
      <p className="theme-row-hint">
        {slot === "dark"
          ? "Used when the theme mode is Dark, or System on a dark desktop."
          : "Used when the theme mode is Light, or System on a light desktop."}
      </p>
      <div role="radiogroup" aria-label={title} className="theme-strip">
        {preferences.themes.map((theme) => (
          <div
            key={theme.id}
            className="theme-option"
            // The ⋯ shows by the row-menu rule. A custom theme's menu opens
            // from a right click too, and from ⌘. on its radio (the host
            // marks where `useActionsKey` sends the event); a built-in has
            // none, so the browser's own menu stays.
            data-list-row=""
            data-actions-target={theme.builtin ? undefined : ""}
            onContextMenu={
              theme.builtin
                ? undefined
                : (event) =>
                    actions.menu.open(
                      event,
                      actions.items(theme)(),
                      `${theme.name} in ${title.toLowerCase()}`,
                    )
            }
          >
            <label className="theme-option-label" title={theme.name}>
              <input
                type="radio"
                className="sr-only"
                name={`${id}-${slot}`}
                value={theme.id}
                aria-label={theme.name}
                checked={theme.id === chosen.id}
                disabled={!ready}
                onChange={() => choose(theme.id)}
              />
              <span className="theme-mini-frame">
                <ThemeMiniature theme={theme} />
              </span>
              <span className="theme-option-name">
                <span className="theme-option-text">{theme.name}</span>
                <CheckIcon size={14} className="theme-option-check" />
              </span>
            </label>
            {theme.builtin ? null : (
              <span className="theme-option-menu">
                <MenuButton
                  label={`${theme.name} in ${title.toLowerCase()}`}
                  menu={actions.menu}
                  items={actions.items(theme)}
                />
              </span>
            )}
          </div>
        ))}
      </div>
    </section>
  );
}

/** Settings › Appearance: the theme mode, then a row for each of the light and
 *  dark slots to choose its theme from by thumbnail. Choices apply at once
 *  and save like any other preference. */
export function AppearanceSection(): ReactNode {
  const { preferences, update, ready, error, refresh } = usePreferences();
  const systemDark = useSystemDark();
  const inUse = activeSlot(preferences, systemDark);
  const actions = useThemeActions();
  return (
    <section aria-labelledby="settings-appearance-heading" className="settings-section">
      <SectionHead
        id="settings-appearance-heading"
        title="Appearance"
        detail="The theme baum uses in light and in dark mode, on this device."
      />
      {error ? (
        <Alert>
          {error}
          <Button variant="secondary" size="compact" onClick={refresh}>
            Try again
          </Button>
        </Alert>
      ) : null}
      <div className="local-panel settings-list">
        <SettingRow
          label="Theme mode"
          detail="System follows your computer's light or dark mode; Light and Dark pick the theme below that is used."
        >
          <SegmentedControl
            label="Theme mode"
            value={preferences.theme}
            items={[
              { value: "system", label: "System", disabled: !ready },
              { value: "light", label: "Light", disabled: !ready },
              { value: "dark", label: "Dark", disabled: !ready },
            ]}
            onChange={(theme) => update({ theme })}
          />
        </SettingRow>
        <ThemeRow
          slot="light"
          inUse={inUse === "light"}
          ready={ready}
          choose={(id) => update({ light_theme: id })}
          actions={actions}
        />
        <ThemeRow
          slot="dark"
          inUse={inUse === "dark"}
          ready={ready}
          choose={(id) => update({ dark_theme: id })}
          actions={actions}
        />
      </div>
      {actions.dialogs}
    </section>
  );
}
