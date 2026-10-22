export function invoke<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  const bridge = (
    window as Window & {
      __TAURI_INTERNALS__?: {
        invoke<T>(command: string, args?: Record<string, unknown>): Promise<T>;
      };
    }
  ).__TAURI_INTERNALS__;
  if (!bridge) return Promise.reject(new Error("Open this screen in the baum desktop app."));
  return bridge.invoke<T>(command, args);
}
