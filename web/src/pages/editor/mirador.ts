export type AnnotationAdapterConstructor<TClient> = new (
  endpointURL: string,
  iiifPresentationVersion: 3,
  canvasID: string,
  user: string,
  client: TClient,
) => unknown;

export const hiddenPanels = {
  info: false,
  attribution: false,
  canvas: false,
  annotations: false,
  search: false,
  layers: false,
};

export function commonViewerOptions<TClient>(
  annotationBase: string,
  Adapter: AnnotationAdapterConstructor<TClient>,
  client: TClient,
  osdConfig: { crossOriginPolicy: string; ajaxWithCredentials: boolean },
) {
  return {
    id: "mirador-viewer",
    osdConfig,
    annotation: {
      adapter: (canvasID: string) => new Adapter(annotationBase, 3, canvasID, "Scribe User", client),
      readonly: false,
    },
    annotations: { htmlSanitizationRuleSet: "liberal" },
    thumbnailNavigation: { defaultPosition: "off", displaySettings: false },
  };
}
