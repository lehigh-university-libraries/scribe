import { describe, expect, it } from 'vitest';

import { stringifyIIIFJSON } from './json';

describe('stringifyIIIFJSON', () => {
  it('restores clone-safe lossless number wrappers as JSON numbers', () => {
    const value = structuredClone({
      largeInteger: new String('9007199254740993'),
      preciseDecimal: new String('0.123456789012345678901'),
      width: 1200,
    });

    expect(stringifyIIIFJSON(value)).toBe(
      '{"largeInteger":9007199254740993,"preciseDecimal":0.123456789012345678901,"width":1200}',
    );
  });

  it('serializes BigInt integrations without converting the value to a string', () => {
    expect(stringifyIIIFJSON({ counter: 9007199254740993n })).toBe('{"counter":9007199254740993}');
  });
});
