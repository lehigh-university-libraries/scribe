export function remainingRequestBudgetMs(startedAt, now, totalBudgetMs) {
  if (
    !Number.isFinite(startedAt)
    || !Number.isFinite(now)
    || !Number.isSafeInteger(totalBudgetMs)
    || totalBudgetMs <= 0
  ) {
    throw new TypeError("invalid request budget");
  }
  return Math.max(0, Math.floor(totalBudgetMs - Math.max(0, now - startedAt)));
}

export function upstreamTimeoutForBudgetMs(
  startedAt,
  now,
  totalBudgetMs,
  upstreamTimeoutMs,
) {
  if (!Number.isSafeInteger(upstreamTimeoutMs) || upstreamTimeoutMs <= 0) {
    throw new TypeError("invalid upstream timeout");
  }
  return Math.min(
    upstreamTimeoutMs,
    remainingRequestBudgetMs(startedAt, now, totalBudgetMs),
  );
}
