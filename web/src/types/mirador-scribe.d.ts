declare module "mirador" {
  const Mirador: {
    viewer: (config: unknown, plugins?: unknown[]) => unknown;
  };

  export default Mirador;
}

declare module "mirador-scribe" {
  const plugin: unknown[];

  export const annotationAdapters: {
    ScribeAnnotationAdapter: unknown;
  };

  export default plugin;
}
