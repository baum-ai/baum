import { createRoot } from "react-dom/client";
import { App } from "./app";
import "./style.css";
import { blockBrowserMenu } from "./native-menu";
blockBrowserMenu();
createRoot(document.getElementById("root")!).render(<App />);
