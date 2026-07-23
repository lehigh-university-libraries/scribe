import { describe, expect, it } from "vitest";

import { mapConcurrent } from "./async";

describe("mapConcurrent", () => {
  it("bounds active work and preserves result order", async () => {
    let active = 0;
    let peak = 0;
    const releases: Array<() => void> = [];
    const mapped = mapConcurrent([1, 2, 3, 4, 5], 2, async (value) => {
      active++;
      peak = Math.max(peak, active);
      await new Promise<void>((resolve) => releases.push(resolve));
      active--;
      return value * 10;
    });

    await waitFor(() => expect(releases).toHaveLength(2));
    releases.shift()?.();
    await waitFor(() => expect(releases).toHaveLength(2));
    releases.shift()?.();
    await waitFor(() => expect(releases).toHaveLength(2));
    while (releases.length > 0) {
      releases.shift()?.();
      await Promise.resolve();
    }

    expect(await mapped).toEqual([10, 20, 30, 40, 50]);
    expect(peak).toBe(2);
  });

  it("rejects invalid concurrency and accepts empty input", async () => {
    await expect(mapConcurrent([], 0, async () => 1)).rejects.toThrow(RangeError);
    await expect(mapConcurrent([], 2, async () => 1)).resolves.toEqual([]);
  });
});

async function waitFor(assertion: () => void): Promise<void> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      assertion();
      return;
    } catch (error) {
      lastError = error;
      await Promise.resolve();
    }
  }
  throw lastError;
}
