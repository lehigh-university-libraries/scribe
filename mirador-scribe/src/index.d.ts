import type {
  ScribeActiveCanvasEventDetail,
  ScribeAnnotationAdapterConstructor,
  ScribeAnnotationAdapterInstance,
  ScribeAnnotationMutationEventDetail,
  ScribeDirtyStateEventDetail,
  ScribeCreateAnnotationEventDetail,
  ScribeCreateLineAtViewportCenterEventDetail,
  ScribeFocusResizeHandleEventDetail,
  ScribePublishRequestEventDetail,
  ScribePublishResultEventDetail,
  ScribeReloadAnnotationsEventDetail,
  ScribeReloadAnnotationsResultEventDetail,
  ScribeRemoteRebaseReadyEventDetail,
  ScribeSaveRequestEventDetail,
  ScribeSaveResultEventDetail,
  ScribeTranscriptionEventDetail,
  ScribeTranscriptionJobStateEventDetail,
  ScribeTranscriptionOverlayStateEventDetail,
} from './types/scribe';

export type {
  AnnotationChange,
  AnnotationMutation,
  AnnotationPageSnapshot,
  AnnotationResource,
  CanonicalIIIFAnnotationPage,
  DraftMutationResponse,
  EditorRow,
  EditorSession,
  EditorSessionAction,
  EditorSessionCache,
  EditorSessionStatus,
  IdentifiedIIIFAnnotation,
  IIIFAnnotation,
  IIIFAnnotationPage,
  IIIFSelector,
  IIIFTarget,
  IIIFTextualBody,
  ImageBBox,
  MiradorState,
  PagePatch,
  Point2D,
  RawIIIFProperties,
  ScribeActiveCanvasEventDetail,
  ScribeAdapterFactory,
  ScribeAdapterLike,
  ScribeAdapterRuntime,
  ScribeAnnotationAdapterConstructor,
  ScribeAnnotationAdapterInstance,
  ScribeAnnotationClient,
  ScribeAnnotationMutationEventDetail,
  ScribeDirtyStateEventDetail,
  ScribeCreateAnnotationEventDetail,
  ScribeCreateLineAtViewportCenterEventDetail,
  ScribeFocusResizeHandleEventDetail,
  ScribePublishRequestEventDetail,
  ScribePublishResultEventDetail,
  ScribeReloadAnnotationsEventDetail,
  ScribeReloadAnnotationsResultEventDetail,
  ScribeRemoteRebaseReadyEventDetail,
  ScribeSaveRequestEventDetail,
  ScribeSaveResultEventDetail,
  ScribeTranscriptionEventDetail,
  ScribeTranscriptionJobStateEventDetail,
  ScribeTranscriptionOverlayStateEventDetail,
} from './types/scribe';

declare global {
  interface DocumentEventMap {
    'scribe:active-canvas': CustomEvent<ScribeActiveCanvasEventDetail>;
    'scribe:annotation-mutation': CustomEvent<ScribeAnnotationMutationEventDetail>;
    'scribe:dirty-state': CustomEvent<ScribeDirtyStateEventDetail>;
    'scribe:create-annotation': CustomEvent<ScribeCreateAnnotationEventDetail>;
    'scribe:create-line-at-viewport-center': CustomEvent<ScribeCreateLineAtViewportCenterEventDetail>;
    'scribe:focus-resize-handle': CustomEvent<ScribeFocusResizeHandleEventDetail>;
    'scribe:publish-result': CustomEvent<ScribePublishResultEventDetail>;
    'scribe:reload-annotations': CustomEvent<ScribeReloadAnnotationsEventDetail>;
    'scribe:reload-annotations-result': CustomEvent<ScribeReloadAnnotationsResultEventDetail>;
    'scribe:remote-rebase-ready': CustomEvent<ScribeRemoteRebaseReadyEventDetail>;
    'scribe:request-publish': CustomEvent<ScribePublishRequestEventDetail>;
    'scribe:request-save': CustomEvent<ScribeSaveRequestEventDetail>;
    'scribe:save-result': CustomEvent<ScribeSaveResultEventDetail>;
    'scribe:transcription-job-state': CustomEvent<ScribeTranscriptionJobStateEventDetail>;
    'scribe:transcription-overlay-state': CustomEvent<ScribeTranscriptionOverlayStateEventDetail>;
    'scribe:transcription-result': CustomEvent<ScribeTranscriptionEventDetail>;
    'scribe:transcription-segment': CustomEvent<ScribeTranscriptionEventDetail>;
  }
}

export type ScribeAnnotationAdapter = ScribeAnnotationAdapterInstance;
export const ScribeAnnotationAdapter: ScribeAnnotationAdapterConstructor;

export const annotationAdapters: {
  ScribeAnnotationAdapter: typeof ScribeAnnotationAdapter;
};

export interface MiradorPluginDescriptor extends Record<string, unknown> {
  component: unknown;
  target: string;
}

declare const scribeMiradorPlugin: readonly MiradorPluginDescriptor[];
export default scribeMiradorPlugin;
