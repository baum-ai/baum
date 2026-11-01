import { createRoot } from "react-dom/client";
import { TrayPanel } from "./tray";
import { followSystemTheme } from "./system-theme";
import "./tray.css";
followSystemTheme();
createRoot(document.getElementById("root")!).render(<TrayPanel />);
