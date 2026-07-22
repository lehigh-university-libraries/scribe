declare module "mirador" {
  const Mirador: {
    viewer: (config: unknown, plugins?: unknown[]) => unknown;
  };

  export default Mirador;
}
