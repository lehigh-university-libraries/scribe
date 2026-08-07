declare module "mirador" {
  export interface MiradorStore {
    dispatch(action: unknown): unknown;
  }

  export interface MiradorViewer {
    store: MiradorStore;
    unmount(): void;
  }

  export function updateWindow(
    id: string,
    payload: Record<string, unknown>,
  ): unknown;

  const Mirador: {
    viewer: (config: unknown, plugins?: unknown[]) => MiradorViewer;
  };

  export default Mirador;
}
