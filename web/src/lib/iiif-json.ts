const JSON_NUMBER_PATTERN = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/;

type ParseContext = { source?: string };

function canonicalNumberToken(source: string): string | null {
  const match = JSON_NUMBER_PATTERN.exec(source);
  if (match === null) return null;

  const unsigned = source.startsWith("-") ? source.slice(1) : source;
  const exponentIndex = unsigned.search(/[eE]/);
  const significand = exponentIndex < 0 ? unsigned : unsigned.slice(0, exponentIndex);
  const rawExponent = exponentIndex < 0 ? "0" : unsigned.slice(exponentIndex + 1);
  const [integerPart, fractionalPart = ""] = significand.split(".");
  let digits = `${integerPart}${fractionalPart}`.replace(/^0+/, "");
  if (digits === "") return "0";

  let significantEnd = digits.length;
  while (digits.charCodeAt(significantEnd - 1) === 48) significantEnd -= 1;
  const trailingZeroes = digits.length - significantEnd;
  digits = digits.slice(0, significantEnd);

  // A finite JavaScript number can only serialize with a small decimal
  // exponent. Avoid constructing an attacker-controlled, enormous BigInt for
  // a token which necessarily needs the lossless wrapper anyway.
  const exponentDigits = rawExponent.replace(/^[+-]/, "").replace(/^0+/, "");
  if (exponentDigits.length > 6) return null;
  const exponent = BigInt(rawExponent)
    - BigInt(fractionalPart.length)
    + BigInt(trailingZeroes);
  const sign = source.startsWith("-") ? "-" : "";
  return `${sign}${digits}e${exponent.toString()}`;
}

function nativeNumberPreservesToken(source: string, value: number): boolean {
  if (!Number.isFinite(value)) return false;
  const serialized = JSON.stringify(value);
  const canonicalSource = canonicalNumberToken(source);
  return serialized !== undefined
    && canonicalSource !== null
    && canonicalSource === canonicalNumberToken(serialized);
}

let hasSourceTextAccess: boolean | undefined;

function supportsSourceTextAccess(): boolean {
  if (hasSourceTextAccess !== undefined) return hasSourceTextAccess;
  let observed = "";
  JSON.parse("9007199254740993", (_key: string, value: unknown, context?: ParseContext) => {
    observed = context?.source ?? "";
    return value;
  });
  hasSourceTextAccess = observed === "9007199254740993";
  return hasSourceTextAccess;
}

/**
 * Parses extensible IIIF JSON without silently rounding unknown numeric
 * values. Numbers that JSON.stringify can reproduce exactly remain native
 * numbers. Other valid number tokens use a boxed string: structuredClone
 * preserves that marker and mirador-scribe's serializer restores the original
 * unquoted JSON number.
 */
export function parseIIIFJSON(raw: string): unknown {
  if (!supportsSourceTextAccess()) {
    throw new Error("this browser cannot preserve extension numbers in IIIF JSON");
  }
  return JSON.parse(raw, (_key: string, value: unknown, context?: ParseContext) => {
    if (typeof value !== "number" || typeof context?.source !== "string") return value;
    if (nativeNumberPreservesToken(context.source, value)) return value;
    return new String(context.source);
  });
}
