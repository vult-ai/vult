import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("an amount's fraction is the decimal separator and its digits, in every regional format", async () => {
  const { amountFraction, decimalSeparator } = await loadComponent("src/lib/amount-fraction.ts");
  const { money } = await loadComponent("src/lib/api.ts");
  const part = (text, decimal) => {
    const at = amountFraction(text, decimal);
    return at && text.slice(at[0], at[1]);
  };
  const usd = { currency: "USD", scale: 2 };
  const prefs = (regional_format, negative_style = "minus") => ({ regional_format, digit_grouping: true, negative_style });
  assert.equal(part(money("8591200", usd, prefs("en-US")), "."), ".00");
  assert.equal(part(money("-1234", usd, prefs("en-US")), "."), ".34", "a minus sign stays with the whole part");
  assert.equal(part(money("-123456", usd, prefs("en-US", "parentheses")), "."), ".56", "a closing parenthesis is not part of the fraction");
  const pln = money("123456", { currency: "PLN", scale: 2 }, prefs("pl-PL"));
  assert.equal(part(pln, decimalSeparator("pl-PL")), ",56", "a comma decimal splits there, and the symbol after it stays full size");
  assert.equal(part(money("1234", { currency: "JPY", scale: 0 }, prefs("en-US")), "."), null, "a currency without minor units has no fraction");
  assert.equal(part("—", "."), null);
  assert.equal(part("Not available", "."), null);
});

test("DisplayAmount draws the fraction smaller and keeps the exact string to copy and hear", async () => {
  await withDOM(async ({ render }) => {
    const { DisplayAmount } = await loadComponent("src/components/ui/display-amount.tsx");
    await render(React.createElement(DisplayAmount, { value: "$85,912.00" }));
    const root = document.getElementById("root");
    assert.equal(root.querySelector(".rolling-number-text").textContent, "$85,912.00");
    // The separator and both cent wheels; the dollars and symbol stay full size.
    const fraction = [...root.querySelectorAll(".rolling-number-visual .rolling-fraction")];
    assert.deepEqual(fraction.map((e) => e.className), ["rolling-char rolling-fraction", "rolling-digit rolling-fraction", "rolling-digit rolling-fraction"]);
    assert.equal(fraction[0].textContent, ".");
    assert.equal(root.querySelectorAll(".rolling-number-visual > span").length, 10);
    assert.equal(root.querySelector(".rolling-number-text .rolling-fraction").textContent, ".00");
    assert.equal(root.querySelector(".rolling-number-visual").getAttribute("aria-hidden"), "true");
  });
});
