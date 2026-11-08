import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("leading category ranking conserves every bucket in Other and colors survive rank changes", async () => {
  const { chartCategories, categoryColor, categoryShare } = await loadComponent(
    "src/lib/movement-categories.ts",
  );
  const points = [
    { date: "2026-10-01", money_in_minor: "0", money_out_minor: "210" },
  ];
  const cats = Array.from({ length: 6 }, (_, i) => ({
    id: "cat" + i,
    label: "Category " + i,
    direction: "out",
    total_minor: String((i + 1) * 10),
    series: [String((i + 1) * 10)],
  }));
  const ranked = chartCategories(cats, points, "out");
  assert.equal(ranked.length, 5);
  assert.equal(ranked[0].id, "cat5");
  assert.equal(ranked.at(-1).total_minor, "30");
  assert.equal(
    ranked.reduce((n, c) => n + BigInt(c.series[0]), 0n),
    210n,
  );
  assert.equal(
    chartCategories([...cats].reverse(), points, "out").find(
      (c) => c.id === "cat4",
    ).color,
    categoryColor("cat4"),
  );
  assert.equal(categoryShare("60", "210"), "28.6%");
  assert.equal(categoryShare("0", "0"), "0%");
  assert.equal(categoryShare("1", "10000"), "<0.1%");
});

test("whole-day density buckets conserve every amount across months, years, custom and short windows", async () => {
  const { uniformMovementBuckets } = await loadComponent(
    "src/lib/movement-categories.ts",
  );
  for (const days of [1, 2, 7, 12, 17, 28, 29, 31, 304, 365, 366, 750]) {
    const first = Date.parse("2024-01-01T00:00:00Z");
    const date = (i) =>
      new Date(first + i * 86400000).toISOString().slice(0, 10);
    const points = Array.from({ length: days }, (_, i) => ({
      date: date(i),
      money_in_minor: (9007199254740993n + BigInt(i)).toString(),
      money_out_minor: String(i * 3),
    }));
    const categories = [
      {
        id: "offer",
        label: "Product A",
        direction: "in",
        total_minor: points
          .reduce((n, p) => n + BigInt(p.money_in_minor), 0n)
          .toString(),
        series: points.map((p) => p.money_in_minor),
      },
    ];
    const result = uniformMovementBuckets(points, categories, {
      from: date(0),
      to: date(days - 1),
    });
    assert.equal(result.points.length, Math.min(days, 60));
    for (const key of ["money_in_minor", "money_out_minor"])
      assert.equal(
        result.points.reduce((n, p) => n + BigInt(p[key]), 0n),
        points.reduce((n, p) => n + BigInt(p[key]), 0n),
      );
    assert.equal(
      result.categories[0].series
        .reduce((n, v) => n + BigInt(v), 0n)
        .toString(),
      categories[0].total_minor,
    );
    assert.equal(
      result.points.filter((p) => !p.empty).length,
      Math.min(days, 60),
    );
    for (const point of points)
      assert.equal(
        result.points.filter(
          (p) => !p.empty && point.date >= p.date && point.date <= p.end,
        ).length,
        1,
        "each dated fact belongs to one bucket",
      );
  }
  assert.throws(
    () =>
      uniformMovementBuckets(
        [{ date: "2026-10-02", money_in_minor: "1", money_out_minor: "0" }],
        [],
        { from: "2026-10-01", to: "2026-10-01" },
      ),
    /outside/,
  );
});

test("density follows actual plot width while short windows retain their real date precision", async () => {
  const { movementChartDensity, uniformMovementBuckets } = await loadComponent(
    "src/lib/movement-categories.ts",
  );
  for (const [width, slots, maxBarWidth] of [
    [280, 24, 12],
    [600, 36, 18],
    [720, 40, 18],
    [1100, 60, 28],
    [1400, 64, 28],
  ]) {
    assert.deepEqual(movementChartDensity(width), { slots, maxBarWidth });
    for (const days of [26, 31, 304, 365]) {
      const start = Date.parse("2026-01-01T00:00:00Z");
      const end = new Date(start + (days - 1) * 86400000)
        .toISOString()
        .slice(0, 10);
      const result = uniformMovementBuckets(
        [],
        [],
        { from: "2026-01-01", to: end },
        slots,
      );
      assert.equal(result.points.length, Math.min(slots, days));
      assert.ok(
        result.points.every(
          (p) =>
            !p.empty && p.money_in_minor === "0" && p.money_out_minor === "0",
        ),
      );
    }
  }
});
