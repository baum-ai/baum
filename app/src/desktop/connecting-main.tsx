import { createRoot } from "react-dom/client";
import { Connecting } from "./connecting";
import { followSystemTheme } from "./system-theme";
import "./screen.css";
followSystemTheme();
createRoot(document.getElementById("root")!).render(<Connecting />);
