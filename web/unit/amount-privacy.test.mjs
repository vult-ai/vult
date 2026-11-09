import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

/* Dashboard amounts start hidden; one click shows them, and the choice is
   remembered in this browser. Charts are drawn from the real values either way. */
const { PrivacyExample, hiddenAmount } = await loadComponent("unit/helpers/amount-privacy.tsx");

test("amounts start hidden and one click shows them", async () => {
  await withDOM(async ({ render }) => {
    await render(React.createElement(PrivacyExample));
    const toggle = document.querySelector(".amount-privacy-toggle");
    assert.equal(document.querySelector("output").textContent, hiddenAmount);
    assert.equal(toggle.textContent, "Show amounts");
    assert.equal(toggle.getAttribute("aria-pressed"), "false");
    await React.act(async () => toggle.click());
    assert.equal(document.querySelector("output").textContent, "$1,250.00");
    assert.equal(toggle.textContent, "Hide amounts");
    assert.equal(localStorage.getItem("vult:amounts-hidden"), "shown");
  }, { amountsHidden: true });
});
