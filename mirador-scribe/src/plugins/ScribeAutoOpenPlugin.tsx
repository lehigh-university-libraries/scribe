import { useEffect, useRef } from 'react';
import PropTypes from 'prop-types';
import { addOrUpdateCompanionWindow as addOrUpdateCompanionWindowAction } from 'mirador';
import { hasCompanionWindowContent } from '../utils/iiif';
import type { MiradorState } from '../types/scribe';

interface ScribeAutoOpenProps {
  hasScribeWindow: boolean;
  openScribeWindow(): void;
}

interface WindowOwnProps {
  windowId: string;
}

type MiradorDispatch = (action: Record<string, unknown>) => unknown;

export function ScribeAutoOpenPlugin({
  hasScribeWindow,
  openScribeWindow,
}: ScribeAutoOpenProps) {
  const didAutoOpen = useRef(false);
  useEffect(() => {
    if (didAutoOpen.current) return;
    didAutoOpen.current = true;
    if (!hasScribeWindow) {
      openScribeWindow();
    }
  }, [hasScribeWindow, openScribeWindow]);

  return null;
}

ScribeAutoOpenPlugin.propTypes = {
  hasScribeWindow: PropTypes.bool.isRequired,
  openScribeWindow: PropTypes.func.isRequired,
};

const mapStateToProps = (state: MiradorState, { windowId }: WindowOwnProps) => ({
  hasScribeWindow: hasCompanionWindowContent(state, windowId, 'scribeEditor'),
});

const mapDispatchToProps = (dispatch: MiradorDispatch, { windowId }: WindowOwnProps) => ({
  openScribeWindow: () => dispatch(
    addOrUpdateCompanionWindowAction(windowId, { content: 'scribeEditor', position: 'bottom' }),
  ),
});

const scribeAutoOpenPlugin = {
  component: ScribeAutoOpenPlugin,
  mapDispatchToProps,
  mapStateToProps,
  mode: 'add',
  target: 'Window',
};

export default scribeAutoOpenPlugin;
