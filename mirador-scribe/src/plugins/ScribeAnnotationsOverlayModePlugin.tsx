import type { ComponentType } from 'react';
import PropTypes from 'prop-types';

interface OverlayWrapperProps extends Record<string, unknown> {
  TargetComponent: ComponentType<Record<string, unknown>>;
  targetProps: Record<string, unknown>;
}

// Always disable Mirador's built-in annotation drawing. Scribe renders its
// own overlays via ScribeTextOverlayPlugin so we never want the default
// purple/violet annotation shapes drawn on top of the canvas.
function ScribeAnnotationsOverlayModePlugin({
  TargetComponent,
  targetProps,
  ...props
}: OverlayWrapperProps) {
  return (
    <TargetComponent
      {...targetProps}
      {...props}
      drawAnnotations={false}
      drawSearchAnnotations={false}
    />
  );
}

ScribeAnnotationsOverlayModePlugin.propTypes = {
  TargetComponent: PropTypes.elementType.isRequired,
  targetProps: PropTypes.object.isRequired,
};

const scribeAnnotationsOverlayModePlugin = {
  component: ScribeAnnotationsOverlayModePlugin,
  mode: 'wrap',
  target: 'AnnotationsOverlay',
};

export default scribeAnnotationsOverlayModePlugin;
