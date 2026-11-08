import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const { checkStylesheet, width } = await import("../scripts/check-side-panel-standard.mjs");

test("panels take the shared width", () => {
  assert.match(checkStylesheet("components/x.css", ".ledger { grid-template-columns: 1fr 304px; }").join(""), /--vult-inspector-width/);
  assert.match(checkStylesheet("components/x.css", ".x > .collection-inspector-panel { width: 320px; }").join(""), /sizes a side panel/);
  assert.deepEqual(checkStylesheet("components/x.css", ".ledger { grid-template-columns: 1fr var(--vult-inspector-width); }"), []);
  assert.deepEqual(checkStylesheet("components/x.css", ".x > .collection-inspector-panel { flex: 1; min-height: 0; }"), []);
  assert.deepEqual(checkStylesheet("components/x.css", ".x { width: 1304px; }"), []);
});

test("the token keeps the documented width", async () => {
  const css = await readFile(new URL("../src/design-system/collection-inspector.css", import.meta.url), "utf8");
  assert.deepEqual(checkStylesheet("design-system/collection-inspector.css", css), []);
  assert.match(checkStylesheet("design-system/collection-inspector.css", css.replace("--vult-inspector-width: 304px", "--vult-inspector-width: 320px")).join(""), /304px/);
  const doc = await readFile(new URL("../../docs/design/side-panels.md", import.meta.url), "utf8");
  for (const value of [width, "1120px", "40px", "8px"]) assert.match(doc, new RegExp(value));
});
