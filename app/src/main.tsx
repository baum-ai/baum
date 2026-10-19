import { createRoot } from "react-dom/client";
import { App } from "./app";
import "./style.css";
const dark = matchMedia("(prefers-color-scheme: dark)");
const theme = () => {
  document.documentElement.dataset.theme = dark.matches ? "dark" : "light";
};
theme();
dark.addEventListener("change", theme);
createRoot(document.getElementById("root")!).render(<App />);
