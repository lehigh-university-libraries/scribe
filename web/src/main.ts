import "./styles.css";

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
const root = app;

const path = window.location.pathname;
async function renderRoute() {
  if (path.startsWith("/editor")) {
    const { renderEditor } = await import("./pages/editor");
    await renderEditor(root);
    return;
  }
  if (path.startsWith("/settings")) {
    const { renderShell } = await import("./pages/shell");
    await renderShell(root, "settings");
    return;
  }
  if (path.startsWith("/contexts")) {
    const { renderShell } = await import("./pages/shell");
    await renderShell(root, "contexts");
    return;
  }
  const { renderHome } = await import("./pages/home");
  await renderHome(root);
}

void renderRoute();
