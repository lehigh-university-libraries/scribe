import "./styles.css";
import { renderHome } from "./pages/home";
import { renderEditor } from "./pages/editor";

const app = document.getElementById("app");
if (!app) {
  throw new Error("missing #app element");
}

if (window.location.pathname.startsWith("/editor")) {
  void renderEditor(app);
} else {
  void renderHome(app);
}
