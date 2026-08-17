export async function waitForActionByValue(root, attribute, value, options = {}) {
  const timeoutMs = Number(options.timeoutMs);
  const pollIntervalMs = Number(options.pollIntervalMs ?? 100);
  const now = options.now ?? Date.now;
  const wait = options.wait ?? ((delayMs) => new Promise((resolve) => {
    setTimeout(resolve, delayMs);
  }));
  if (
    !root
    || typeof root.locator !== "function"
    || !/^data-[a-z0-9-]+$/.test(String(attribute ?? ""))
    || typeof value !== "string"
    || !Number.isFinite(timeoutMs)
    || timeoutMs <= 0
    || !Number.isFinite(pollIntervalMs)
    || pollIntervalMs <= 0
    || typeof now !== "function"
    || typeof wait !== "function"
  ) {
    throw new Error("invalid action wait contract");
  }

  const deadline = now() + timeoutMs;
  while (true) {
    const candidates = root.locator(`[${attribute}]`);
    const count = await candidates.count();
    for (let index = 0; index < count; index += 1) {
      const candidate = candidates.nth(index);
      if (await candidate.getAttribute(attribute) === value) return candidate;
    }
    const remainingMs = deadline - now();
    if (remainingMs <= 0) return undefined;
    await wait(Math.min(pollIntervalMs, remainingMs));
  }
}
