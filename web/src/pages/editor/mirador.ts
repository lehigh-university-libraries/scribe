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

export function commonViewerOptions(
  annotationBase: string,
  Adapter: ScribeAnnotationAdapterConstructor,
  client: ScribeAnnotationClient,
  runtimeForCanvas: (canvasID: string) => CanvasAdapterRuntime,
  osdConfig: { crossOriginPolicy: string; ajaxWithCredentials: boolean },
) {
  return {
    id: "mirador-viewer",
    osdConfig,
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
  };
}
