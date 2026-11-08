import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

const book = { id: "personal", currency: "USD", scale: 2 };
const state = {
  accounts: [
    { code: "card", name: "Card", kind: "liability" },
    { code: "cost", name: "Established expenses", kind: "expense" },
    { code: "pending", name: "Unresolved counterparts", kind: "unknown" },
  ],
};
const period = { from: "2026-01-01", to: "2026-01-31" };
function movement(id, date, debit, credit, amount) {
  return [[debit, "debit"], [credit, "credit"]].map(([account_code, side], i) => ({
    book_id: book.id,
    currency: book.currency,
    scale: "2",
    line_id: `${id}-${i}`,
    economic_date: date,
    account_code,
    side,
    amount_minor: amount,
  }));
}

test("a recorded card payment keeps the known card balance visible without inventing income or a funding asset", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const journal = [
    ...movement("purchase", "2026-01-02", "cost", "card", "10000"),
    ...movement("payment", "2026-01-03", "card", "pending", "5000"),
  ];
  const before = structuredClone(journal);
  const result = financialSnapshot(book, state, journal, period);
  assert.equal(result.assets, "0");
  assert.equal(result.liabilities, "5000");
  assert.equal(result.equity, "-5000");
  assert.equal(result.income, "0");
  assert.equal(result.expenses, "10000");
  assert.deepEqual(result.rows.find(row => row.code === "pending"), {
    code: "pending", name: "Unresolved counterparts", kind: "unknown", amount: "-5000",
  });
  assert.deepEqual(journal, before);
});

test("offsetting unresolved movements retain their separate-account disclosure and obey the reporting cutoff", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const journal = [
    ...movement("interest", "2026-01-03", "pending", "card", "10000"),
    ...movement("payment", "2026-01-04", "card", "pending", "10000"),
  ];
  const result = financialSnapshot(book, state, journal, period);
  assert.equal(result.liabilities, "0");
  assert.equal(result.expenses, "0");
  assert.equal(result.income, "0");
  assert.equal(result.rows.find(row => row.code === "pending")?.amount, "0");
  const earlier = financialSnapshot(book, state, journal, { from: "2026-01-01", to: "2026-01-02" });
  assert.equal(earlier.rows.some(row => row.code === "pending"), false);
});

test("unclassified support still rejects unbalanced evidence and unrecognized account types", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const journal = movement("observed", "2026-01-03", "pending", "card", "10000");
  assert.throws(() => financialSnapshot(book, state, [journal[0]], period), /do not balance/);
  const unsupported = { accounts: state.accounts.map(account => account.code === "pending" ? { ...account, kind: "unrecognized" } : account) };
  assert.throws(() => financialSnapshot(book, unsupported, journal, period), /accounting category/);
});
