import "./styles.css";
import { renderHome } from "./pages/home";
import { renderEditor } from "./pages/editor";
import { renderContexts } from "./pages/contexts";

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

const path = window.location.pathname;
if (path.startsWith("/editor")) {
  void renderEditor(app);
} else if (path.startsWith("/contexts")) {
  void renderContexts(app);
} else {
  void renderHome(app);
}
