import { createRoot } from "react-dom/client";
import { Connecting } from "./connecting";
import { followSystemTheme } from "./system-theme";
import "./screen.css";
import { blockBrowserMenu } from "../native-menu";
followSystemTheme();
blockBrowserMenu();
createRoot(document.getElementById("root")!).render(<Connecting />);
