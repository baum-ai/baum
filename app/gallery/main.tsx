import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { Gallery } from "./gallery";
import "./gallery.css";

const root = createRoot(document.getElementById("root")!);
const render = (Page: typeof Gallery) =>
  root.render(
    <StrictMode>
      <Page />
    </StrictMode>,
  );
render(Gallery);

// Editing a diagram component, flow.css or the samples replaces the modules
// in place and draws the page again with them, without a reload: the
// controls live in the address, so the page comes back as it was.
if (import.meta.hot) {
  import.meta.hot.accept("./gallery", (next) => {
    if (next) render(next.Gallery as typeof Gallery);
  });
}
