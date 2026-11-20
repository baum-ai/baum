// A minimal W3C WebDriver client over plain HTTP, for WebKitWebDriver driving
// the desktop app's WebKitGTK webview (just desktop::profile), or chromedriver
// driving Chromium (--engine chromium, chromium.ts).

/** The JSON wire's key for an element reference. */
const ELEMENT = "element-6066-11e4-a52e-4f735466cecc";

export type ElementRef = { [ELEMENT]: string };

export class WebDriverError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(`${code}: ${message}`);
    this.code = code;
  }
}

async function call(base: string, method: string, path: string, body?: unknown): Promise<unknown> {
  const response = await fetch(base + path, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  const parsed = text ? (JSON.parse(text) as { value?: unknown }) : {};
  const value = parsed.value;
  if (!response.ok) {
    const error = (value ?? {}) as { error?: string; message?: string };
    throw new WebDriverError(error.error ?? String(response.status), error.message ?? text);
  }
  return value;
}

export type Session = Awaited<ReturnType<typeof createSession>>;

/** Starts a session on the driver at `base` (http://127.0.0.1:port), which
 *  launches `binary` with `args`. */
export function newSession(base: string, binary: string, args: string[] = []) {
  return createSession(base, { "webkitgtk:browserOptions": { binary, args } });
}

/** Starts a session with `capabilities` (alwaysMatch). */
export async function createSession(base: string, capabilities: Record<string, unknown>) {
  const created = (await call(base, "POST", "/session", {
    capabilities: { alwaysMatch: capabilities },
  })) as { sessionId: string };
  const prefix = `/session/${created.sessionId}`;
  const send = (method: string, path: string, body?: unknown) =>
    call(base, method, prefix + path, body);

  const session = {
    id: created.sessionId,
    async close() {
      await send("DELETE", "");
    },
    async url() {
      return (await send("GET", "/url")) as string;
    },
    async navigate(url: string) {
      await send("POST", "/url", { url });
    },
    /** A PNG of the window. */
    async screenshot() {
      return Buffer.from((await send("GET", "/screenshot")) as string, "base64");
    },
    async windows() {
      return (await send("GET", "/window/handles")) as string[];
    },
    async switchTo(handle: string) {
      await send("POST", "/window", { handle });
    },
    /** Runs `script` (a function body) in the page with `args`. */
    async execute<T>(script: string, args: unknown[] = []): Promise<T> {
      return (await send("POST", "/execute/sync", { script, args })) as T;
    },
    /** As `execute`, finishing when the script calls its last argument. */
    async executeAsync<T>(script: string, args: unknown[] = []): Promise<T> {
      return (await send("POST", "/execute/async", { script, args })) as T;
    },
    /** A Chrome DevTools Protocol command on the page (chromedriver only). */
    async cdp(cmd: string, params: Record<string, unknown> = {}) {
      return send("POST", "/goog/cdp/execute", { cmd, params });
    },
    async setTimeouts(timeouts: { script?: number; pageLoad?: number; implicit?: number }) {
      await send("POST", "/timeouts", timeouts);
    },
    async find(selector: string): Promise<ElementRef | null> {
      const found = (await send("POST", "/elements", {
        using: "css selector",
        value: selector,
      })) as ElementRef[];
      return found[0] ?? null;
    },
    async click(element: ElementRef) {
      await send("POST", `/element/${element[ELEMENT]}/click`, {});
    },
    async sendKeys(element: ElementRef, text: string) {
      await send("POST", `/element/${element[ELEMENT]}/value`, { text });
    },
    /** Presses each key in turn (W3C key values, e.g. "j" or "" for
     *  Enter), holding `modifiers` ("" Shift, "" Control) for each. */
    async press(keys: string[], modifiers: string[] = []) {
      // A person presses a modifier a moment before the key.
      const actions = [
        ...modifiers.map((value) => ({ type: "keyDown", value })),
        ...(modifiers.length ? [{ type: "pause", duration: 50 }] : []),
        ...keys.flatMap((value) => [
          { type: "keyDown", value },
          { type: "keyUp", value },
        ]),
        ...modifiers.toReversed().map((value) => ({ type: "keyUp", value })),
      ];
      await send("POST", "/actions", { actions: [{ type: "key", id: "keyboard", actions }] });
      await send("DELETE", "/actions");
    },
    /** Scrolls the wheel by `deltaY` over the middle of `element`. */
    async wheel(element: ElementRef, deltaY: number) {
      await send("POST", "/actions", {
        actions: [
          {
            type: "wheel",
            id: "wheel",
            actions: [
              { type: "scroll", x: 0, y: 0, deltaX: 0, deltaY, origin: element, duration: 100 },
            ],
          },
        ],
      });
    },
  };
  return session;
}

export const KEY = {
  enter: "",
  escape: "",
  shift: "",
  control: "",
  backspace: "",
};

/** Waits until the driver at `base` answers its status endpoint. */
export async function waitForDriver(base: string, timeout = 20_000) {
  const end = Date.now() + timeout;
  for (;;) {
    try {
      const status = (await call(base, "GET", "/status")) as { ready?: boolean };
      if (status.ready !== false) return;
    } catch (error) {
      if (Date.now() > end) throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
}
