import { createRoot } from "react-dom/client";
import { Setup } from "./setup";
import { followSystemTheme } from "./system-theme";
import "./screen.css";
import "./setup.css";
import { blockBrowserMenu } from "../native-menu";
followSystemTheme();
blockBrowserMenu();
createRoot(document.getElementById("root")!).render(<Setup />);
