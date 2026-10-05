import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { applySavedTheme } from "./lib/theme";
import { readEarly } from "./lib/search";
import "./styles.css";
import "./alerts.css";
import "./dashboard.css";
import "./admin.css";

applySavedTheme();
// Every page reads these first: start them alongside React's first render.
readEarly(["/demo/session", "/demo/feed", "/demo/alerts", "/v0/connectors"]);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
