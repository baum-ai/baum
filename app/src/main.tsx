import { createRoot } from "react-dom/client";
import { App } from "./app";
import "./style.css";
import { blockBrowserMenu } from "./native-menu";
import { trackHidden } from "./visible-interval";
blockBrowserMenu();
trackHidden();
createRoot(document.getElementById("root")!).render(<App />);
