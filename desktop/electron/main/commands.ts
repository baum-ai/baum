// The commands pages may call through the preload bridge, and which pages may
// call which. This table replaces Tauri's capability files: a call reaches a
// handler only from the main frame of one of the app's own windows, from an
// origin and role the guard (bridge.ts) resolved, and only for a command the
// role lists here. Handlers never see the raw IPC event.

export type Role = "connecting-screen" | "setup-screen" | "daemon-pages" | "tray-panel";

export const COMMANDS = {
  connection: ["connecting-screen"],
  reconnect: ["connecting-screen"],
  open_check_window: ["daemon-pages"],
  close_check_window: ["daemon-pages"],
  setup_state: ["setup-screen", "daemon-pages"],
  install_command: ["setup-screen", "daemon-pages"],
  uninstall_command: ["setup-screen", "daemon-pages"],
  install_hooks: ["setup-screen", "daemon-pages"],
  uninstall_hooks: ["setup-screen", "daemon-pages"],
  update_hooks: ["setup-screen", "daemon-pages"],
  choose_repository: ["setup-screen", "daemon-pages"],
  add_repository: ["setup-screen"],
  review_settings: ["setup-screen"],
  save_review_settings: ["setup-screen"],
  github_connection: ["setup-screen"],
  repositories: ["setup-screen"],
  finish_setup: ["setup-screen"],
  tray_panel: ["tray-panel"],
  tray_panel_height: ["tray-panel"],
  tray_action: ["tray-panel"],
  zoom_state: ["daemon-pages"],
  set_zoom: ["daemon-pages"],
  step_zoom: ["connecting-screen", "setup-screen", "daemon-pages"],
  theme_state: ["connecting-screen", "setup-screen", "tray-panel"],
  save_theme: ["daemon-pages"],
  update_state: ["daemon-pages"],
  check_for_updates: ["daemon-pages"],
  install_update: ["daemon-pages"],
  cancel_update: ["daemon-pages"],
  dismiss_update_notice: ["daemon-pages"],
  save_tile_type: ["daemon-pages"],
  open_link: ["setup-screen", "daemon-pages"],
} as const satisfies Record<string, readonly Role[]>;

export type CommandName = keyof typeof COMMANDS;

/** What a handler learns about its caller: the window it came from (a
 *  BrowserWindow in the app; tests pass their own), its role and origin. */
export interface CommandContext {
  window: unknown;
  role: Role;
  origin: string;
}

export type Handler = (args: Record<string, unknown>, context: CommandContext) => unknown;

const handlers = new Map<CommandName, Handler>();

export function registerCommand(name: CommandName, handler: Handler): void {
  if (handlers.has(name)) throw new Error(`command ${name} registered twice`);
  handlers.set(name, handler);
}

export function isCommand(name: unknown): name is CommandName {
  return typeof name === "string" && Object.hasOwn(COMMANDS, name);
}

export function allows(role: Role, name: CommandName): boolean {
  return (COMMANDS[name] as readonly Role[]).includes(role);
}

/** Runs a command the guard has let through. Errors reach the page as a
 *  rejected promise with the error's message, as Tauri's did. */
export async function dispatch(
  name: CommandName,
  args: Record<string, unknown>,
  context: CommandContext,
): Promise<unknown> {
  if (!allows(context.role, name)) throw new Error(`command ${name} not allowed`);
  const handler = handlers.get(name);
  if (!handler) throw new Error(`command ${name} is not available`);
  return handler(args, context);
}

/** Tests only. */
export function clearCommands(): void {
  handlers.clear();
}
