import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const { loadCashSummary, CashPanel } = await loadComponent("unit/helpers/cash-status.tsx");
const book = { id: "11111111-1111-4111-8111-111111111111", currency: "USD", scale: 2, name: "Northwind Studio", founder: "", start_date: "" };
const period = { from: "2025-01-01", to: "2025-03-31" };
const base = { schema_version: 1, book_id: book.id, currency: "USD", scale: 2, period, through: "2025-03-31", revision: "a".repeat(64), cash_accounts: [], findings: [], limitations: [] };
async function read(body) {
  const previous = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  try { return await loadCashSummary(book, period); } finally { globalThis.fetch = previous; }
}

test("explicit cash status: partial and unavailable reads are accepted, and missing figures stay missing", async () => {
  const partial = await read({ ...base, cash_status: "partial", cash_minor: "120000", money_in_minor: "5000", money_out_minor: "2000", movement_count: 3 });
  assert.equal(partial.cash_minor, "120000");
  const unavailable = await read({ ...base, cash_status: "unavailable" });
  assert.equal(unavailable.cash_minor, undefined, "no cash figure is invented");
  const withheld = await read({ ...base, cash_status: "partial", cash_minor: "120000" });
  assert.equal(withheld.money_in_minor, undefined, "withheld money totals are not zero");
  await assert.rejects(read({ ...base, cash_status: "unavailable", cash_minor: "0" }));
  await assert.rejects(read({ ...base, cash_status: "partial" }));
  await assert.rejects(read({ ...base, cash_status: "complete-ish", cash_minor: "1" }));
  await assert.rejects(read({ ...base, cash_status: "partial", cash_minor: "1", money_in_minor: "5" }), undefined, "money in without money out");
});

test("the cash card says unavailable instead of zero, and marks a partial total", async () => {
  await withDOM(async ({ render }) => {
    const props = { book, range: period, onOpen() {}, onViewAccounts() {} };
    await render(React.createElement(CashPanel, { ...props, status: "unavailable" }));
    let text = document.getElementById("root").textContent;
    assert.match(text, /Not available/);
    assert.ok(!/\$0\.00/.test(text));
    await render(React.createElement(CashPanel, { ...props, status: "partial", total: "120000", accounts: [] }));
    text = document.getElementById("root").textContent;
    assert.match(text, /\$1,200\.00/);
    assert.match(text, /cash · partial/);
    await render(React.createElement(CashPanel, { ...props }));
    assert.ok(!/Not available/.test(document.getElementById("root").textContent), "loading is not unavailable");
  });
});
