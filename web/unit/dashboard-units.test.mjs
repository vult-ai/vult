import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("the chart draws a day per bar up to a month, then weeks, months, quarters or years", async () => {
  const { calendarUnit } = await loadComponent("src/lib/dashboard-projection.ts");
  assert.equal(calendarUnit("2026-11-01", "2026-11-07"), "day", "a week");
  assert.equal(calendarUnit("2026-10-08", "2026-11-06"), "day", "the last 30 days");
  assert.equal(calendarUnit("2026-10-01", "2026-10-31"), "day", "a whole month");
  assert.equal(calendarUnit("2026-08-09", "2026-11-06"), "week", "the last 90 days");
  assert.equal(calendarUnit("2026-01-01", "2026-12-31"), "month", "a year, one bar per month");
  assert.equal(calendarUnit("2020-01-01", "2026-12-31"), "year");
});

test("a month of days draws two days a pair, counted back from the last day, adding amounts only", async () => {
  const { groupPerformance } = await loadComponent("src/components/v0/dashboard-panels.tsx");
  const days = (from, n) => Array.from({ length: n }, (_, i) => {
    const d = new Date(`${from}T00:00:00Z`); d.setUTCDate(d.getUTCDate() + i);
    const s = d.toISOString().slice(0, 10); return { from: s, to: s };
  });
  const data = (buckets) => ({
    period: { from: buckets[0].from, to: buckets.at(-1).to }, grain: "day", buckets, domain: { min: "0", max: "0" },
    income: { total: String(buckets.length * 100), values: buckets.map(() => "100"), categories: [{ id: "sales", name: "Sales", values: buckets.map(() => "100") }] },
    expenses: { total: "0", values: buckets.map(() => "0"), categories: [{ id: "rent", name: "Rent", values: buckets.map(() => "0") }] },
  });
  const thirty = groupPerformance(data(days("2026-10-08", 30)));
  assert.equal(thirty.groups.length, 15, "30 days: 15 pairs, as full as a year's 12");
  assert.deepEqual([thirty.groups[0].from, thirty.groups[0].to], ["2026-10-08", "2026-10-09"]);
  assert.equal(thirty.groups.reduce((s, g) => s + g.income[0].value, 0n), 3000n, "nothing is split or lost");
  const month = groupPerformance(data(days("2026-10-01", 31)));
  assert.equal(month.groups.length, 16);
  assert.deepEqual([month.groups[0].from, month.groups[0].to], ["2026-10-01", "2026-10-01"], "the odd day is the first, so the latest pair is whole");
  assert.deepEqual([month.groups.at(-1).from, month.groups.at(-1).to], ["2026-10-30", "2026-10-31"]);
  assert.equal(groupPerformance(data(days("2026-11-01", 7))).groups.length, 7, "a week stays a day a pair");
});

test("the chart ends just above its tallest column, gridlines at round steps inside", async () => {
  const { chartScale } = await loadComponent("src/components/v0/dashboard-panels.tsx");
  const near = (a, b) => Math.abs(a - b) < 1e-6;
  for (const [highest, ticks] of [
    [213_000, [100_000, 200_000]],
    [1_084_000, [500_000, 1_000_000]],
    [28_000, [10_000, 20_000]],
    [39_000, [10_000, 20_000, 30_000]],
    [100_000, [50_000, 100_000]],
  ]) {
    const scale = chartScale(highest, 0);
    assert.ok(near(highest / scale.top, 0.96), `${highest} fills the chart`);
    assert.deepEqual(scale.ticks, ticks, `${highest} gridlines`);
    assert.equal(scale.low, 0);
  }
  assert.equal(chartScale(28_000, 3.63).low, 3.63, "a few dollars keep a sliver, no label");
  assert.equal(chartScale(28_000, 3.63).floor, undefined);
  assert.deepEqual(chartScale(28_000, 12_000).below, [10_000], "a reduction past a step gets the same gridlines");
  assert.equal(chartScale(1_084_000, 200_000).floor, 200_000, "short of a step, a round labelled floor");
});

test("Activity can choose dates when All time has no start date", async () => {
  const { DashboardPeriodPicker } = await loadComponent("src/components/workspaces/dashboard-period-picker.tsx");
  await withDOM(async ({ render }) => {
    await render(React.createElement(DashboardPeriodPicker, {
      context: "Activity",
      range: { from: "", to: "2026-11-07" },
      period: "all",
      onPreset() {},
      onRange() {},
    }));
    assert.equal(document.querySelector(".dashboard-period-full")?.textContent, "All time");
    assert.ok([...document.querySelectorAll(".period-step")].every((button) => button.disabled));
  });
});

test("Owed to you lists receivables by their recorded role, never by name", async () => {
  const { OwedPanel } = await loadComponent("src/components/v0/dashboard-panels.tsx");
  await withDOM(async ({ render }) => {
    const rows = [
      { code: "ar", name: "Accounts Receivable", kind: "asset", amount: "1250000", role: "receivable" },
      { code: "ar-name-only", name: "Receivables (named, no role)", kind: "asset", amount: "50000" },
      { code: "bank", name: "Checking", kind: "asset", amount: "9000000", role: "cash" },
      { code: "card", name: "Card", kind: "liability", amount: "120000" },
    ];
    const book = { id: "b", name: "Book", currency: "USD", scale: 2, profile: "business" };
    await render(React.createElement(OwedPanel, { book, position: { through: "2026-12-31", rows }, range: { from: "2026-01-01", to: "2026-12-31" }, onOpen() {}, onViewAccounts() {} }));
    const button = [...document.querySelectorAll(".dash-mode-option")].find((b) => b.textContent === "Owed to you");
    await React.act(async () => button.click());
    const text = document.body.textContent;
    assert.match(text, /Accounts Receivable/);
    assert.doesNotMatch(text, /named, no role/, "a name alone never makes a receivable");
    assert.doesNotMatch(text, /Checking/);
  });
});
