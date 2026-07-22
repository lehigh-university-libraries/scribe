declare module 'mirador' {
  import type { ComponentType } from 'react';

  export const ConnectedCompanionWindow: ComponentType<Record<string, unknown>>;

  export function addOrUpdateCompanionWindow(
    windowId: string,
    companionWindow: Record<string, unknown>,
  ): Record<string, unknown>;

  export function receiveAnnotation(
    canvasId: string,
    annotationId: string,
    annotation: unknown,
  ): Record<string, unknown>;

  export function selectAnnotation(
    windowId: string,
    annotationId: string,
  ): Record<string, unknown>;
}
