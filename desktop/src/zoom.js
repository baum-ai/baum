// Injected into every page of the window (see zoom.rs).
(() => {
  if (window.__baumZoom) return;
  window.__baumZoom = true;
  const mac = navigator.platform.startsWith("Mac");
  const directions = { "=": 1, "+": 1, "-": -1, _: -1, 0: 0 };
  // Physical keys too, for layouts whose unshifted key gives another
  // character (AZERTY's 0 key is "à").
  const codes = { Equal: 1, NumpadAdd: 1, Minus: -1, NumpadSubtract: -1, Digit0: 0, Numpad0: 0 };
  window.addEventListener(
    "keydown",
    (event) => {
      if (event.altKey || (mac ? !event.metaKey || event.ctrlKey : !event.ctrlKey || event.metaKey)) {
        return;
      }
      const direction = directions[event.key] ?? codes[event.code];
      const bridge = window.__TAURI_INTERNALS__;
      if (direction === undefined || !bridge) return;
      event.preventDefault();
      bridge.invoke("step_zoom", { direction }).then(
        (level) => window.dispatchEvent(new CustomEvent("baum:zoom", { detail: level })),
        (error) => console.error("baum: cannot zoom", error),
      );
    },
    true,
  );
})();
