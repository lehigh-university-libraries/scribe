import { useEffect, useState } from 'react';
import PropTypes from 'prop-types';

function ScribeAnnotationsOverlayModePlugin({
  TargetComponent,
  targetProps,
  ...props
}) {
  const [displayMode, setDisplayMode] = useState('segments');

  useEffect(() => {
    const handleModeChange = (event) => {
      if (event?.detail?.windowId !== props.windowId) return;
      setDisplayMode(event.detail.mode || 'segments');
    };
    document.addEventListener('scribe:display-mode-change', handleModeChange);
    return () => document.removeEventListener('scribe:display-mode-change', handleModeChange);
  }, [props.windowId]);

  return (
    <TargetComponent
      {...targetProps}
      {...props}
      drawAnnotations={displayMode === 'segments'}
      drawSearchAnnotations={displayMode === 'segments'}
    />
  );
}

ScribeAnnotationsOverlayModePlugin.propTypes = {
  TargetComponent: PropTypes.elementType.isRequired,
  targetProps: PropTypes.object.isRequired,
  windowId: PropTypes.string.isRequired,
};

const scribeAnnotationsOverlayModePlugin = {
  component: ScribeAnnotationsOverlayModePlugin,
  mode: 'wrap',
  target: 'AnnotationsOverlay',
};

export default scribeAnnotationsOverlayModePlugin;
