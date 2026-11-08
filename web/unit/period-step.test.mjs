import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

test("the period arrows move whole months by months and any other range by its own days", async () => {
  const { stepRange, wholeMonths, wholePeriodName, monthRange } = await loadComponent("src/lib/period-step.ts");
  assert.deepEqual(stepRange({ from: "2026-07-01", to: "2026-07-31" }, -1), { from: "2026-06-01", to: "2026-06-30" });
  assert.deepEqual(stepRange({ from: "2026-03-01", to: "2026-03-31" }, -1), { from: "2026-02-01", to: "2026-02-28" }, "month lengths follow the month");
  assert.deepEqual(stepRange({ from: "2026-01-01", to: "2026-01-31" }, -1), { from: "2025-12-01", to: "2025-12-31" }, "across the year");
  assert.deepEqual(stepRange({ from: "2026-07-01", to: "2026-09-30" }, 1), { from: "2026-10-01", to: "2026-12-31" }, "a quarter to the next");
  assert.deepEqual(stepRange({ from: "2025-01-01", to: "2025-12-31" }, -1), { from: "2024-01-01", to: "2024-12-31" });
  assert.deepEqual(stepRange({ from: "2026-10-08", to: "2026-11-06" }, -1), { from: "2026-09-08", to: "2026-10-07" }, "30 days to the 30 before");
  assert.equal(wholeMonths({ from: "2026-02-01", to: "2026-02-28" }), 1);
  assert.equal(wholeMonths({ from: "2026-02-02", to: "2026-02-28" }), 0);
  assert.equal(wholePeriodName({ from: "2026-07-01", to: "2026-09-30" }), "Q3 2026");
  assert.equal(wholePeriodName({ from: "2025-01-01", to: "2025-12-31" }), "2025");
  assert.match(wholePeriodName({ from: "2026-10-01", to: "2026-10-31" }), /October 2026/);
  assert.equal(wholePeriodName({ from: "2026-10-08", to: "2026-11-06" }), undefined);
  assert.deepEqual(monthRange(2024, 2), { from: "2024-02-01", to: "2024-02-29" }, "a leap February");
});
