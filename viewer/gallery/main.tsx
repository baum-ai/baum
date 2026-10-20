import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { Gallery } from "./gallery";
import "./gallery.css";

// Follow the system colour scheme, as the standalone app does.
const dark = window.matchMedia("(prefers-color-scheme: dark)");
const applyTheme = () => {
  if (dark.matches) document.documentElement.dataset.theme = "dark";
  else delete document.documentElement.dataset.theme;
};
applyTheme();
dark.addEventListener("change", applyTheme);

const root = createRoot(document.getElementById("root")!);
const render = (Page: typeof Gallery) =>
  root.render(
    <StrictMode>
      <Page />
    </StrictMode>,
  );
render(Gallery);

// Editing a tile component (or anything under it) replaces the modules in
// place and draws the page again with them, without a reload: the filters
// live in the URL, so the page comes back as it was and keeps its scroll.
if (import.meta.hot) {
  import.meta.hot.accept("./gallery", (next) => {
    if (next) render(next.Gallery as typeof Gallery);
  });
}
