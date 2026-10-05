export async function waitForSavedPage(page) {
  await page.getByRole("status").filter({ hasText: /^Saved page(?:\.|: .+\.)$/ }).waitFor({ state: "visible" });
}

export async function waitForActionByValue(root, attribute, value, options = {}) {
  const timeoutMs = Number(options.timeoutMs);
  const pollIntervalMs = Number(options.pollIntervalMs ?? 100);
  const now = options.now ?? Date.now;
  const wait = options.wait ?? ((delayMs) => new Promise((resolve) => {
    setTimeout(resolve, delayMs);
  }));
  if (
    !root
    || typeof root.locator !== "function"
    || !/^data-[a-z0-9-]+$/.test(String(attribute ?? ""))
    || typeof value !== "string"
    || !Number.isFinite(timeoutMs)
    || timeoutMs <= 0
    || !Number.isFinite(pollIntervalMs)
    || pollIntervalMs <= 0
    || typeof now !== "function"
    || typeof wait !== "function"
  ) {
    throw new Error("invalid action wait contract");
  }

  const deadline = now() + timeoutMs;
  while (true) {
    const candidates = root.locator(`[${attribute}]`);
    const count = await candidates.count();
    for (let index = 0; index < count; index += 1) {
      const candidate = candidates.nth(index);
      if (await candidate.getAttribute(attribute) === value) return candidate;
    }
    const remainingMs = deadline - now();
    if (remainingMs <= 0) return undefined;
    await wait(Math.min(pollIntervalMs, remainingMs));
  }
}

function bottomPaneHeightForViewport({ height, width }) {
  const viewportHeight = Number.isFinite(height) ? Math.max(0, Math.floor(height)) : 0;
  const viewportWidth = Number.isFinite(width) ? Math.max(0, Math.floor(width)) : 0;
  const shortViewport = viewportHeight < 420;
  const desiredPaneHeight = shortViewport ? 300 : viewportWidth <= 900 ? 420 : 320;
  const minimumCanvasHeight = shortViewport ? 72 : viewportWidth <= 480 ? 170 : 220;
  return Math.min(
    desiredPaneHeight,
    Math.max(0, viewportHeight - minimumCanvasHeight - 60),
  );
}

export async function assertResponsiveEditorGeometry(page, width, height, minimumImageHeight) {
  const deadline = Date.now() + 10_000;
  do {
    const geometry = await page.locator('[data-scribe-action-panel="true"]').evaluate((panel) => {
      const parent = panel.parentElement;
      const companion = parent?.parentElement;
      const viewer = document.getElementById("mirador-viewer");
      const osd = viewer?.querySelector(".openseadragon-canvas");
      const panelBounds = panel.getBoundingClientRect();
      const parentBounds = parent?.getBoundingClientRect();
      const viewerBounds = viewer?.getBoundingClientRect();
      const osdBounds = osd?.getBoundingClientRect();
      const osdCanvases = osd instanceof HTMLCanvasElement
        ? [osd]
        : Array.from(osd?.querySelectorAll("canvas") ?? []);
      const actionGroups = [
        panel.querySelector('[role="group"][aria-label="View and modes"]'),
        panel.querySelector('[role="group"][aria-label="Text and page actions"]'),
      ];
      const primaryActions = actionGroups.flatMap((group) => (
        Array.from(group?.querySelectorAll("button[aria-label]") ?? [])
      ));
      const primaryActionsVisible = primaryActions.every((button) => {
        const bounds = button.getBoundingClientRect();
        const style = getComputedStyle(button);
        return bounds.width > 0
          && bounds.height > 0
          && bounds.left >= Math.max(0, panelBounds.left) - 1
          && bounds.right <= Math.min(window.innerWidth, panelBounds.right) + 1
          && bounds.top >= Math.max(0, panelBounds.top) - 1
          && bounds.bottom <= Math.min(window.innerHeight, panelBounds.bottom) + 1
          && style.display !== "none"
          && style.visibility !== "hidden";
      });
      return {
        companionHeight: companion?.getBoundingClientRect().height ?? 0,
        osdHasPixels: osdCanvases.some((canvas) => canvas.width > 0 && canvas.height > 0),
        osdImageHeight: osdBounds?.height ?? 0,
        pageOverflow: document.documentElement.scrollWidth > window.innerWidth
          || document.documentElement.scrollHeight > window.innerHeight,
        panelClientHeight: panel.clientHeight,
        panelClientWidth: panel.clientWidth,
        panelScrollTop: panel.scrollTop,
        panelScrollWidth: panel.scrollWidth,
        parentClientHeight: parent?.clientHeight ?? 0,
        parentClientWidth: parent?.clientWidth ?? 0,
        parentScrollTop: parent?.scrollTop ?? 0,
        parentScrollWidth: parent?.scrollWidth ?? 0,
        panelWithinParent: Boolean(parentBounds)
          && panelBounds.top >= parentBounds.top - 1
          && panelBounds.bottom <= parentBounds.bottom + 1,
        panelWithinViewer: Boolean(viewerBounds)
          && panelBounds.top >= viewerBounds.top - 1
          && panelBounds.bottom <= viewerBounds.bottom + 1,
        primaryActionCount: primaryActions.length,
        primaryActionsVisible,
        viewerClientHeight: viewer?.clientHeight ?? 0,
        viewerClientWidth: viewer?.clientWidth ?? 0,
        viewportHeight: window.innerHeight,
        viewportWidth: window.innerWidth,
      };
    });
    const expectedPaneHeight = bottomPaneHeightForViewport({
      height: geometry.viewerClientHeight,
      width: geometry.viewerClientWidth,
    });
    if (
      !geometry.pageOverflow
      && geometry.viewportWidth === width
      && geometry.viewportHeight === height
      && geometry.viewerClientHeight >= Math.min(500, height - 180)
      && geometry.panelClientHeight > 0
      && geometry.parentClientHeight > 0
      && Math.abs(geometry.panelClientHeight - geometry.parentClientHeight) <= 2
      && geometry.panelClientWidth > 0
      && geometry.parentClientWidth > 0
      && geometry.panelScrollTop === 0
      && geometry.parentScrollTop === 0
      && geometry.panelScrollWidth <= geometry.panelClientWidth + 1
      && geometry.parentScrollWidth <= geometry.parentClientWidth + 1
      && geometry.panelWithinParent
      && geometry.panelWithinViewer
      && geometry.primaryActionCount === 18
      && geometry.primaryActionsVisible
      && geometry.osdHasPixels
      && geometry.osdImageHeight >= minimumImageHeight
      && Math.abs(geometry.companionHeight - expectedPaneHeight) <= 1
    ) return;
    await page.waitForTimeout(100);
  } while (Date.now() < deadline);
  throw new Error("editor action panel geometry failed");
}
