import { renderShell } from "./shell";

export async function renderHome(app: HTMLElement): Promise<void> {
  await renderShell(app, "library");
}
