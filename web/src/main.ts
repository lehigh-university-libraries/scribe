import "./styles.css";
import { renderHome } from "./pages/home";
import { renderEditor } from "./pages/editor";
import { renderShell } from "./pages/shell";

const storedTheme = (() => {
  try {
    return window.localStorage.getItem("scribe.shell.theme") === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
})();
document.documentElement.dataset.theme = storedTheme;

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

const path = window.location.pathname;
if (path.startsWith("/editor")) {
  void renderEditor(app);
} else if (path.startsWith("/settings")) {
  void renderShell(app, "settings");
} else if (path.startsWith("/contexts")) {
  void renderShell(app, "contexts");
} else {
  void renderHome(app);
}
