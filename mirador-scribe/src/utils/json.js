const JSON_NUMBER_PATTERN = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/;

/** @returns {((source: string) => object) | null} */
function rawJSONFactory() {
  const candidate = /** @type {JSON & { rawJSON?: (source: string) => object }} */ (JSON).rawJSON;
  return typeof candidate === 'function' ? candidate.bind(JSON) : null;
}

/**
 * Serializes an extensible IIIF resource while restoring lossless number
 * wrappers produced by the web API boundary. BigInt is accepted for external
 * editor integrations that use it for the same purpose.
 *
 * @param {unknown} value
 * @returns {string}
 */
export function stringifyIIIFJSON(value) {
  const rawJSON = rawJSONFactory();
  if (!rawJSON) {
    throw new Error('this browser cannot preserve extension numbers in IIIF JSON');
  }
  const encoded = JSON.stringify(value, (_key, candidate) => {
    if (typeof candidate === 'bigint') return rawJSON(candidate.toString());
    if (candidate instanceof String) {
      const source = candidate.valueOf();
      if (JSON_NUMBER_PATTERN.test(source)) return rawJSON(source);
    }
    return candidate;
  });
  if (encoded === undefined) throw new Error('IIIF JSON value is not serializable');
  return encoded;
}
