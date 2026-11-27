# Custom tile frames in the desktop app: manual check

The browser suites check custom tile frames in Chromium and WebKit
(`just local_app::e2e`, `just local_app::e2e-webkit`). They cannot run a
desktop shell's navigation guard or its command bridge. The Electron shell's
harness (`tests/shell`, in CI; see "Checking navigation" in the README) runs
the shell itself on Linux. Run this checklist on macOS and Windows (and on
Linux when the harness cannot show something) before a release that changes
the shell, its Electron version, the frame route or the tile host, and
before a shell starts declaring `window.__BAUM_DESKTOP__ = { tileFrames: true }`.
The Tauri shell (shipped until the bridge release) runs WKWebView on macOS,
WebKitGTK on Linux and WebView2 on Windows; check it with the Web Inspector
where this list says DevTools.

## Set up

1. Build the browser fixture and the app: `just local_app::build`, then
   `CGO_ENABLED=0 go test -c ./internal/uiserver -o app/e2e/fixture` in
   `oss/` (in Docker on Linux; natively on macOS and Windows, which is the
   point).
2. Start it with the synthetic custom tiles:
   `BAUM_APP_CUSTOM_TILES=1 BAUM_APP_BROWSER_FIXTURE=1 BAUM_APP_SCHEMA_DIR=../schema BAUM_APP_STATE_DIR=<dir> ./e2e/fixture -test.run '^TestLocalAppBrowserFixture$' -test.timeout=0`
   from `oss/app`.
3. Point a desktop build at the fixture's credential in `<dir>` and open
   acme/demo #7 as tiles (`?repository=acme%2Fdemo&pr=7&view=tiles&more=1`).
   For the Electron shell, build it (`pnpm build` in `oss/desktop`) and start
   it unpackaged with `--remote-debugging-port=9222`; open DevTools for its
   pages and frames from `chrome://inspect` in Chrome or Edge.
4. Run a listener on 127.0.0.1:4170 (any HTTP server that logs requests).
   The escape probes aim at it; it must log nothing.

## Checklist

Write the platform, the shell's version and its Electron (or webview)
version, then tick each line.

- [ ] A shell without the declaration (or with `tileFrames` not `true`)
      mounts no frame: every custom tile says "Custom tiles aren't available
      in this version of the desktop app yet".
- [ ] With the declaration, Widget health draws in its card and its detail,
      in light and dark, and switching the theme in Settings updates it at
      once.
- [ ] The frame's document request carries `Sec-Fetch-Dest: iframe`,
      `Sec-Fetch-Mode: navigate` and `Sec-Fetch-Site: same-origin` (the
      daemon logs a frame request without them at debug level); otherwise
      every tile says it couldn't load.
- [ ] The fonts in the frame are the app's (Instrument Sans, Manrope).
- [ ] Escape probe's detail lists `origin` as `null`, and every read of the
      page, cookies, storage and the bridge as `threw`.
- [ ] **A frame cannot call an app command.** In the probe's detail,
      `parent bridge` and `parent desktop bridge` threw, `desktop bridge`
      and `desktop declaration` are `undefined` and `webkit handlers` is
      empty. With DevTools, in the probe's frame (pick it in the console's
      context menu), run `window.baumDesktop`, `window.__BAUM_DESKTOP__`,
      `window.require`, `window.process`, `window.__TAURI_INTERNALS__`,
      `window.ipc`, `window.chrome?.webview` and
      `window.webkit?.messageHandlers`: each is undefined or empty, and
      `parent.postMessage({ name: "open_link", args: { url: "https://example.com/" } }, "*")`
      opens nothing and the app logs no command (an unpackaged Electron
      shell started with `BAUM_DESKTOP_LOG_COMMANDS=1` logs each command it
      runs). In the Tauri shell on Windows,
      `window.chrome?.webview?.postMessage("x")` does nothing the shell logs.
- [ ] **A frame navigating itself opens nothing.** Open "Escape probe: leave
      outside" and "Escape probe: leave daemon": no browser window or tab
      opens, the listener logs nothing, the app's page stays where it was,
      and each says "This tile couldn't load" (or stays blank where the
      webview reports no load; note which).
- [ ] `window.open` in the probe opened no window, and no dialog appeared
      for alert, confirm or prompt.
- [ ] "Escape probe: flood" says it couldn't load ("too many messages"); the
      other probes still draw.
- [ ] Typing in the session panel's composer with the pointer resting on
      "Escape probe: focus" keeps every key in the composer.
- [ ] Tab reaches Widget health's detail frame, its citation chips open the
      cited code, and Escape inside it goes back to the summary.
- [ ] "Escape probe: busy" leaves the window answering in the Electron
      shell, which runs tile frames apart from the page (other tiles may
      pause for about a second and a half). In the Tauri shell it freezes
      the window for about that long, then the app answers again (the
      documented limit: a busy loop is not interrupted).
- [ ] The listener on 127.0.0.1:4170 logged nothing during the whole check.
- [ ] Residual, recorded rather than ticked: with `resolvectl monitor`
      (Linux) or the platform's DNS log, whether opening the probe resolves
      `outside.invalid` (its `<base>` and prefetch hints).
