// Whether the main window shows the first-run setup screen. This is the part
// of onboarding.rs the window and the guard need; the onboarding port (3/7)
// adds the setup commands, closes the screen when setup finishes
// (`setup.open = false`, then attaches again) and reopens it from the menu.
import { loadSection } from "./settings.ts";

/** The first page of the main window: the setup screen on first run, else
 *  the connecting screen. An app that cannot run baum skips setup and shows
 *  why on the connecting screen. */
export function initialPage(onboarded: boolean, misplaced: boolean): "/index.html" | "/setup.html" {
  return onboarded || misplaced ? "/index.html" : "/setup.html";
}

/** Whether desktop.json records that the user finished first-run setup. */
export function onboarded(desktopJson: string): boolean {
  return loadSection(desktopJson, "setup").onboarded === true;
}

export class SetupScreen {
  /** The setup screen shows, so the window must stay on it. */
  open = false;
}
