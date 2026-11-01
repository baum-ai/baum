import { createRoot } from "react-dom/client";
import { Setup } from "./setup";
import { followSystemTheme } from "./system-theme";
import "./screen.css";
import "./setup.css";
followSystemTheme();
createRoot(document.getElementById("root")!).render(<Setup />);
