import { describe, expect, it } from "vitest";
import {
  remainingRequestBudgetMs,
  upstreamTimeoutForBudgetMs,
} from "./request-budget.mjs";

describe("frontend request budget", () => {
  it("caps upstream inactivity by the remaining end-to-end request budget", () => {
    expect(upstreamTimeoutForBudgetMs(1_000, 1_000, 285_000, 270_000))
      .toBe(270_000);
    expect(upstreamTimeoutForBudgetMs(1_000, 31_000, 285_000, 270_000))
      .toBe(255_000);
    expect(upstreamTimeoutForBudgetMs(1_000, 286_000, 285_000, 270_000))
      .toBe(0);
  });

  it("does not charge a negative clock delta against the request", () => {
    expect(remainingRequestBudgetMs(2_000, 1_000, 285_000))
      .toBe(285_000);
  });

  it("rejects invalid budgets and upstream timeouts", () => {
    expect(() => remainingRequestBudgetMs(0, 0, 0)).toThrow(TypeError);
    expect(() => upstreamTimeoutForBudgetMs(0, 0, 1, 0)).toThrow(TypeError);
  });
});
