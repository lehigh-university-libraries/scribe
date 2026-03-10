import { useEffect } from 'react';
import PropTypes from 'prop-types';
import { addOrUpdateCompanionWindow as addOrUpdateCompanionWindowAction } from 'mirador';
import { hasCompanionWindowContent } from '../utils/iiif';

function ScribeAutoOpenPlugin({
  hasScribeWindow,
  openScribeWindow,
}) {
  useEffect(() => {
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

const mapStateToProps = (state, { windowId }) => ({
  hasScribeWindow: hasCompanionWindowContent(state, windowId, 'scribeEditor'),
});

const mapDispatchToProps = (dispatch, { windowId }) => ({
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
