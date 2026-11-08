import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { validateTypography } from "../scripts/check-typography.mjs";
const theme = await readFile(
  new URL("../src/design-system/styles/theme.css", import.meta.url),
  "utf8",
);
const product = await readFile(
  new URL(
    "../src/design-system/styles/product-typography.css",
    import.meta.url,
  ),
  "utf8",
);
const check = (
  source,
  legacy = {},
  file = "src/components/example.css",
  extension = product,
) =>
  validateTypography({
    theme,
    product: extension,
    files: [{ file, source }],
    legacy,
  });
test("semantic typography roles and inherited styles pass without legacy exemptions", () => {
  check(
    ".value { font-size: var(--text-vult-dashboard-value); } .label { font-size: var(--text-vult-label) } button { font: inherit }",
  );
});
test("new custom, fluid and shorthand sizes fail even when the raw size exists in the scale", () => {
  for (const size of [
    "28px",
    "24px",
    "clamp(24px, 2vw, 36px)",
    "var(--custom-size)",
  ])
    assert.throws(
      () => check(`.value {font-size:${size}}`),
      /semantic typography role/,
    );
  assert.throws(
    () => check(".value {font: 28px/32px sans-serif}"),
    /semantic typography role/,
  );
});
test("the migration inventory preserves old declarations without allowing new copies", () => {
  const legacy = { "src/components/old.css": { "font-size:13px": 1 } };
  const before = JSON.stringify(legacy);
  check(".old {font-size:13px}", legacy, "src/components/old.css");
  assert.throws(
    () =>
      check(
        ".old {font-size:13px} .extra {font-size:13px}",
        legacy,
        "src/components/old.css",
      ),
    /semantic typography role/,
  );
  assert.throws(
    () => check(".new {font-size:13px}", legacy),
    /semantic typography role/,
  );
  check(
    ".old {font-size:var(--text-vult-ui)}",
    legacy,
    "src/components/old.css",
  );
  assert.equal(JSON.stringify(legacy), before);
});
test("dashboard size drift fails and contributor documentation exposes the approved policy", async () => {
  assert.throws(
    () =>
      check(
        "",
        {},
        "src/components/example.css",
        product.replace("24px", "28px"),
      ),
    /dashboard-value must be 24px/,
  );
  const doc = await readFile(
    new URL("../../docs/design/typography.md", import.meta.url),
    "utf8",
  );
  for (const size of [12, 14, 16, 20, 24, 30])
    assert.match(doc, new RegExp(`\\| ${size} / [0-9]+px \\|`));
  const contributing = await readFile(
    new URL("../../CONTRIBUTING.md", import.meta.url),
    "utf8",
  );
  assert.match(contributing, /docs\/design\/typography\.md/);
});
