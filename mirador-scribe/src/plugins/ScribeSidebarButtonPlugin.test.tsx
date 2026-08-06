import { describe, expect, it } from 'vitest';
import scribeSidebarButtonPlugin from './ScribeSidebarButtonPlugin';

describe('Scribe sidebar translations', () => {
  it('labels repeat provider work as retranscription', () => {
    expect(scribeSidebarButtonPlugin.config.translations.en.scribeEditorTranscribe)
      .toBe('Retranscribe');
  });
});
