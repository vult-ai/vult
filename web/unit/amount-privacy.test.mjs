import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

/* Dashboard amounts show by default; one click hides them, and the choice is
   remembered in this browser. Charts are drawn from the real values either way.
   Each test loads its own copy of the setting. `amountsHidden: true` leaves
   the browser with no stored choice. */
const fresh = () => loadComponent("unit/helpers/amount-privacy.tsx");

test("with no stored choice, amounts are shown and one click hides them", async () => {
  const { PrivacyExample, hiddenAmount } = await fresh();
  await withDOM(async ({ render }) => {
    assert.equal(localStorage.getItem("vult:amounts-hidden"), null);
    await render(React.createElement(PrivacyExample));
    const toggle = document.querySelector(".amount-privacy-toggle");
    assert.equal(document.querySelector("output").textContent, "$1,250.00");
    assert.equal(toggle.textContent, "Hide amounts");
    assert.equal(toggle.getAttribute("aria-pressed"), "true");
    await React.act(async () => toggle.click());
    assert.equal(document.querySelector("output").textContent, hiddenAmount);
    assert.equal(localStorage.getItem("vult:amounts-hidden"), "hidden");
  }, { amountsHidden: true });
});

test("a browser that chose to hide amounts keeps them hidden", async () => {
  const { PrivacyExample, hiddenAmount } = await fresh();
  await withDOM(async ({ render }) => {
    localStorage.setItem("vult:amounts-hidden", "hidden");
    await render(React.createElement(PrivacyExample));
    const toggle = document.querySelector(".amount-privacy-toggle");
    assert.equal(document.querySelector("output").textContent, hiddenAmount);
    assert.equal(toggle.textContent, "Show amounts");
    await React.act(async () => toggle.click());
    assert.equal(document.querySelector("output").textContent, "$1,250.00");
    assert.equal(localStorage.getItem("vult:amounts-hidden"), "shown");
  }, { amountsHidden: true });
});

test("the invisible copy under a rolling amount never shows its cents", async () => {
  const css = await readFile(new URL("../src/components/ui/rolling-number.css", import.meta.url), "utf8");
  const rule = css.match(/\.rolling-number-text \.rolling-fraction\s*\{([^}]*)\}/);
  assert.ok(rule, "the copy's fraction has its own rule");
  // Two classes outrank the fraction's single-class colour wherever it sits.
  assert.match(rule[1], /^\s*color:\s*transparent;\s*$/);
});
