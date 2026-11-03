import { createRoot } from "react-dom/client";
import { TrayPanel } from "./tray";
import { followSystemTheme } from "./system-theme";
import "./tray.css";
import { blockBrowserMenu } from "../native-menu";
followSystemTheme();
blockBrowserMenu();
createRoot(document.getElementById("root")!).render(<TrayPanel />);
