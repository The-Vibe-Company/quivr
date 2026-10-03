import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { applySavedTheme } from "./lib/theme";
import "./styles.css";
import "./alerts.css";
import "./dashboard.css";
import "./admin.css";

applySavedTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
