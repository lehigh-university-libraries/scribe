import assert from "node:assert/strict";
import test from "node:test";

import { waitForActionByValue } from "./deployed-readiness-dom.mjs";

function fakeRoot(values) {
  return {
    locator(selector) {
      assert.equal(selector, "[data-item-delete]");
      return {
        async count() {
          return values().length;
        },
        nth(index) {
          const candidate = { id: `candidate-${index}` };
          return {
            ...candidate,
            async getAttribute(attribute) {
              assert.equal(attribute, "data-item-delete");
              return values()[index];
            },
          };
        },
      };
    },
  };
}

test("waitForActionByValue waits for an asynchronously rendered exact action", async () => {
  let clock = 0;
  const waits = [];
  const root = fakeRoot(() => (clock === 0 ? [] : ["other", "item-42"]));

  const action = await waitForActionByValue(root, "data-item-delete", "item-42", {
    timeoutMs: 1_000,
    now: () => clock,
    wait: async (delayMs) => {
      waits.push(delayMs);
      clock += delayMs;
    },
  });

  assert.equal(action?.id, "candidate-1");
  assert.deepEqual(waits, [100]);
});

test("waitForActionByValue fails closed at its bounded deadline", async () => {
  let clock = 0;
  const waits = [];
  const root = fakeRoot(() => ["other"]);

  const action = await waitForActionByValue(root, "data-item-delete", "item-42", {
    timeoutMs: 150,
    now: () => clock,
    wait: async (delayMs) => {
      waits.push(delayMs);
      clock += delayMs;
    },
  });

  assert.equal(action, undefined);
  assert.deepEqual(waits, [100, 50]);
});
