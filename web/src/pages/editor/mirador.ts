import type {
  ScribeAdapterRuntime,
  ScribeAnnotationAdapterConstructor,
  ScribeAnnotationClient,
} from "mirador-scribe";

export type CanvasAdapterRuntime = Required<
  Pick<ScribeAdapterRuntime, "contextId" | "itemImageId" | "windowId">
> & Pick<ScribeAdapterRuntime, "resolveContextId">;

export const hiddenPanels = {
  info: false,
  attribution: false,
  canvas: false,
  annotations: false,
  search: false,
  layers: false,
};

export interface EditorViewport {
  height: number;
  width: number;
}

// Mirador's navigation chrome is inside the viewer but outside the
// OpenSeadragon canvas. Reserve it separately so the canvas minimum below is
// an actual image area rather than a window-plus-toolbar allowance.
const miradorWindowChromeHeight = 60;

interface CompanionWindowOwnerState {
  defaultSidebarPanelHeight?: number;
  position?: string;
}

export function bottomPaneHeightForViewport({
  height,
  width,
}: EditorViewport): number {
  const viewportHeight = Number.isFinite(height) ? Math.max(0, Math.floor(height)) : 0;
  const viewportWidth = Number.isFinite(width) ? Math.max(0, Math.floor(width)) : 0;
  const shortViewport = viewportHeight < 420;
  const desiredPaneHeight = shortViewport
    ? 300
    : viewportWidth <= 900
      ? 420
      : 320;
  const minimumCanvasHeight = shortViewport
    ? 72
    : viewportWidth <= 480
      ? 170
      : 220;
  const availablePaneHeight = Math.max(
    0,
    viewportHeight - minimumCanvasHeight - miradorWindowChromeHeight,
  );
  return Math.min(desiredPaneHeight, availablePaneHeight);
}

export function observeResponsiveBottomPane(
  viewerElement: HTMLElement,
  updateHeight: (height: number) => void,
): () => void {
  let stopped = false;
  let lastHeight = -1;
  const applyCurrentHeight = () => {
    if (stopped) return;
    const height = bottomPaneHeightForViewport({
      height: viewerElement.clientHeight,
      width: viewerElement.clientWidth,
    });
    if (height === lastHeight) return;
    lastHeight = height;
    updateHeight(height);
  };
  const resizeObserver = new ResizeObserver(applyCurrentHeight);
  resizeObserver.observe(viewerElement);
  applyCurrentHeight();

  return () => {
    if (stopped) return;
    stopped = true;
    resizeObserver.disconnect();
  };
}

export function commonViewerOptions(
  annotationBase: string,
  Adapter: ScribeAnnotationAdapterConstructor,
  client: ScribeAnnotationClient,
  runtimeForCanvas: (canvasID: string) => CanvasAdapterRuntime,
  osdConfig: { crossOriginPolicy: string; ajaxWithCredentials: boolean },
  bottomPaneHeight = 320,
) {
  const normalizedBottomPaneHeight = Number.isFinite(bottomPaneHeight)
    ? Math.max(50, Math.floor(bottomPaneHeight))
    : 320;
  return {
    id: "mirador-viewer",
    osdConfig,
    theme: {
      components: {
        CompanionWindow: {
          styleOverrides: {
            contents: {
              display: "flex",
              flex: "1 1 auto",
              flexDirection: "column",
              minHeight: 0,
              overflowY: "auto",
            },
            resize: ({ ownerState }: { ownerState?: CompanionWindowOwnerState }) => {
              const position = ownerState?.position ?? "";
              const paneHeight = ownerState?.defaultSidebarPanelHeight;
              const isBottom = position === "bottom" || position === "far-bottom";
              return {
                display: "flex",
                flexDirection: "column",
                minHeight: 50,
                minWidth: position === "left" ? 235 : 100,
                position: "relative",
                ...(isBottom && typeof paneHeight === "number" && Number.isFinite(paneHeight)
                  ? { height: `${Math.max(50, Math.floor(paneHeight))}px !important` }
                  : {}),
              };
            },
          },
        },
      },
    },
    annotation: {
      adapter: (canvasID: string) =>
        new Adapter(annotationBase, 3, canvasID, "Scribe User", {
          client,
          ...runtimeForCanvas(canvasID),
        }),
      readonly: false,
    },
    annotations: { htmlSanitizationRuleSet: "liberal" },
    thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
    window: {
      defaultSidebarPanelHeight: normalizedBottomPaneHeight,
    },
  };
}
