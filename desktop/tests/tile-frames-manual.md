# Custom tile frames in the desktop app: manual check

The browser suites check custom tile frames in Chromium and WebKit
(`just local_app::e2e`, `just local_app::e2e-webkit`). They cannot run the
desktop shells' own webviews (WKWebView on macOS, WebKitGTK on Linux,
WebView2 on Windows), their navigation handlers or their command bridge.
Run this checklist on each platform before a release that changes the
shell, its webview, the frame route or the tile host, and before a shell
starts declaring `window.__BAUM_DESKTOP__ = { tileFrames: true }`.

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
4. Run a listener on 127.0.0.1:4170 (any HTTP server that logs requests).
   The escape probes aim at it; it must log nothing.

## Checklist

Write the platform, the shell's version and the webview's version, then
tick each line.

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
      `parent bridge` threw and `webkit handlers` is empty. With the web
      inspector, in the probe's frame, run
      `window.__TAURI_INTERNALS__`, `window.ipc`, `window.chrome?.webview`
      and `window.webkit?.messageHandlers`: each is undefined or empty, and
      `window.chrome?.webview?.postMessage("x")` (WebView2) does nothing the
      shell logs.
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
- [ ] "Escape probe: busy" freezes the window for about a second and a half
      on engines that run the frame on the page's thread, then the app
      answers again (the documented limit: a busy loop is not interrupted).
- [ ] The listener on 127.0.0.1:4170 logged nothing during the whole check.
- [ ] Residual, recorded rather than ticked: with `resolvectl monitor`
      (Linux) or the platform's DNS log, whether opening the probe resolves
      `outside.invalid` (its `<base>` and prefetch hints).
