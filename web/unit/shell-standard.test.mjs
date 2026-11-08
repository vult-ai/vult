import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const { checkStylesheet, checkSource, checkTokens, tokens } = await import("../scripts/check-shell-standard.mjs");

test("pages cannot restyle the rail, the page bar or the canvas", () => {
  for (const css of [
    ".shell-scroll-region:has(.ledger) { padding: 0; }",
    ".vult-shell-header { height: 64px; }",
    "@media (min-width: 48rem) { .ledger .vult-shell-rail { width: 300px; } }",
    ".vult-app-shell[data-wide] .shell-canvas { max-width: none; }",
  ])
    assert.match(checkStylesheet("components/book/ledger.css", css).join("\n"), /styles the app frame/, css);
  assert.deepEqual(checkStylesheet("components/book/ledger.css", ".ledger-shell-note { padding: 0; }"), []);
  assert.deepEqual(checkStylesheet("components/book/ledger.css", ".vult-shell-headerless { padding: 0; }"), []);
  assert.deepEqual(checkStylesheet("components/shell/app-shell.css", ".vult-shell-header { gap: 4px; }"), []);
});

test("print styles may hide the frame", () => {
  assert.deepEqual(checkStylesheet("components/statements/statements.css", 'html[data-vult-report-print="true"] .vult-shell-header { display: none; }'), []);
  assert.deepEqual(checkStylesheet("components/book/ledger.css", "@media print { .vult-shell-rail { display: none; } }"), []);
  assert.match(checkStylesheet("components/book/ledger.css", "@media print { .a { color: red; } } .vult-shell-rail { display: none; }").join(""), /app frame/);
});

test("the frame has no deployment modes", () => {
  assert.match(checkSource('<div data-vult-deployment="production">').join(""), /one mode/);
  assert.match(checkSource('className="vult-app-shell vult-shell-preview"').join(""), /one mode/);
  assert.match(checkStylesheet("design-system/styles/globals.css", "html:not([data-vult-deployment]) .x { top: 2rem; }").join(""), /one mode/);
  assert.deepEqual(checkSource('<div className="vult-app-shell" data-vult-shell-body>'), []);
});

test("the shell tokens keep the documented values", async () => {
  const css = await readFile(new URL("../src/design-system/styles/shell-rail.css", import.meta.url), "utf8");
  assert.deepEqual(checkTokens(css), []);
  assert.match(checkTokens(css.replace("--vult-shell-header-height: 56px", "--vult-shell-header-height: 60px")).join(""), /56px/);
  const doc = await readFile(new URL("../../docs/design/shell.md", import.meta.url), "utf8");
  for (const value of ["256px", "64px", "56px", "32px", "4px", "8px", "16px", "24px", "180px", "148px", "1440px", "320px", "768px"])
    assert.match(doc, new RegExp(value), `the document states ${value}`);
  assert.equal(Object.keys(tokens).length, 12);
});
