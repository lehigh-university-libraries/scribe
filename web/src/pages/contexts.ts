import { renderShell } from "./shell";

export async function renderContexts(app: HTMLElement): Promise<void> {
  await renderShell(app, "contexts");
}
