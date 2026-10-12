import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app";
import "./app.css";

// Follow the system colour scheme: `viewer.css` applies its dark tokens under
// `:root[data-theme="dark"]`.
const dark = window.matchMedia("(prefers-color-scheme: dark)");
const applyTheme = () => {
  if (dark.matches) document.documentElement.dataset.theme = "dark";
  else delete document.documentElement.dataset.theme;
};
applyTheme();
dark.addEventListener("change", applyTheme);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
