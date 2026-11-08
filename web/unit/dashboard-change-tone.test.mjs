import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent } from "./helpers/dom.mjs";

// Colour follows meaning: falling costs read as good news, rising costs as bad.
test("summary changes are coloured by whether they are good news", async () => {
  const { renderToStaticMarkup } = await import("react-dom/server");
  const { SummaryPanel } = await loadComponent("src/components/v0/dashboard-panels.tsx");
  const book = { id: "b1", name: "Example", currency: "USD", reporting_currency: "USD", profile: "business", timezone: "UTC" };
  const snapshot = (income, expenses) => ({
    period: { from: "2026-01-01", to: "2026-06-30" },
    financial: { income: income[0], expenses: expenses[0], result: String(BigInt(income[0]) - BigInt(expenses[0])) },
    prior_year: { period: { from: "2025-01-01", to: "2025-06-30" }, income: income[1], expenses: expenses[1], result: String(BigInt(income[1]) - BigInt(expenses[1])) },
    cash_minor: "100000", cash_status: "complete", trends: { current: [], previous: [] },
  });
  const tones = (s) => [...renderToStaticMarkup(React.createElement(SummaryPanel, { book, snapshot: s })).matchAll(/class="dash-change" data-tone="(\w+)"/g)].map((m) => m[1]);
  // Income, expenses, net result.
  assert.deepEqual(tones(snapshot(["900", "1000"], ["500", "800"])), ["down", "up", "up"], "less income is bad; lower costs are good");
  assert.deepEqual(tones(snapshot(["1200", "1000"], ["900", "600"])), ["up", "down", "down"], "more income is good; higher costs are bad");
});
