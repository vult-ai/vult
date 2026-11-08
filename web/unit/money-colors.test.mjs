import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const css = (path) => readFile(new URL(`../src/${path}`, import.meta.url), "utf8");

// Money in and out are categories, not good or bad news: expenses are never drawn in red.
test("money in and out use their own colors, never the direction colors", async () => {
  const palette = await css("design-system/styles/chart-palette.css");
  assert.match(palette, /--color-money-in: var\(--color-text-primary\)/);
  assert.match(palette, /--color-money-out: var\(--color-fg-quaternary\)/);
  assert.doesNotMatch(palette, /data-direction-colors/, "no unused reversed convention");
  for (const path of ["components/book/activity-summary.css", "design-system/headline.css"]) {
    const sheet = await css(path);
    assert.doesNotMatch(sheet, /data-series="(income|expenses)"\][^{]*\{[^}]*--color-direction/, `${path}: a category uses a direction color`);
    assert.match(sheet, /--color-money-out/);
  }
});
