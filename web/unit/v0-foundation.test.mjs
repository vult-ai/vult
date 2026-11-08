import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";
const book = {
  id: "book-a",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
};
const accounts = [
  ["cash", "asset"],
  ["loan", "liability"],
  ["card", "liability"],
  ["equipment", "asset"],
  ["capital", "equity"],
  ["sales", "revenue"],
  ["cost", "expense"],
  ["fxgain", "other_income"],
  ["fxloss", "other_expense"],
].map(([code, kind]) => ({ code, kind, name: code }));
const state = { accounts };
const period = { from: "2026-01-01", to: "2026-12-31" };
function pair(debit, credit, amount, date = "2026-01-15", prefix = "p") {
  return [
    [debit, "debit"],
    [credit, "credit"],
  ].map(([account_code, side], i) => ({
    book_id: book.id,
    currency: "USD",
    scale: "2",
    line_id: prefix + i,
    economic_date: date,
    account_code,
    side,
    amount_minor: String(amount),
  }));
}
test("v0 foundation handles equity funding, borrowing, card expenses and debt-financed equipment distinctly", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  for (const [lines, expected] of [
    [
      pair("cash", "capital", "50000000"),
      ["50000000", "0", "50000000", "0", "0"],
    ],
    [pair("cash", "loan", "20000000"), ["20000000", "20000000", "0", "0", "0"]],
    [
      pair("cost", "card", "1000000"),
      ["0", "1000000", "-1000000", "0", "1000000"],
    ],
    [
      pair("equipment", "loan", "10000000"),
      ["10000000", "10000000", "0", "0", "0"],
    ],
  ]) {
    const r = financialSnapshot(book, state, lines, period);
    assert.deepEqual(
      [r.assets, r.liabilities, r.equity, r.income, r.expenses],
      expected,
    );
    assert.equal(BigInt(r.assets), BigInt(r.liabilities) + BigInt(r.equity));
  }
});
test("v0 stock totals include accumulated results without adding cash twice or rounding large amounts", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const r = financialSnapshot(
    book,
    state,
    [
      ...pair("cash", "capital", "9007199254740993", "2026-01-01", "a"),
      ...pair("cash", "sales", "1", "2026-01-02", "b"),
    ],
    period,
  );
  assert.equal(r.assets, "9007199254740994");
  assert.equal(r.equity, r.assets);
  assert.equal(r.accumulatedResult, "1");
});
test("v0 reports cut off by economic date and preserve original and reversal entries", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const journal = [
    ...pair("cost", "cash", "10000", "2026-01-10", "a"),
    ...pair("cash", "cost", "10000", "2026-02-05", "b"),
  ];
  const jan = financialSnapshot(book, state, journal, {
      from: "2026-01-01",
      to: "2026-01-31",
    }),
    feb = financialSnapshot(book, state, journal, {
      from: "2026-02-01",
      to: "2026-02-28",
    });
  assert.equal(jan.assets, "-10000");
  assert.equal(jan.expenses, "10000");
  assert.equal(feb.assets, "0");
  assert.equal(feb.expenses, "-10000");
  assert.equal(feb.equity, "0");
  assert.equal(journal.length, 4);
});
test("v0 performance retains realized exchange gains and losses after obligations settle", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const r = financialSnapshot(
    book,
    state,
    [
      ...pair("cash", "fxgain", "500", "2026-01-15", "a"),
      ...pair("fxloss", "cash", "300", "2026-01-16", "b"),
    ],
    period,
  );
  assert.equal(r.liabilities, "0");
  assert.equal(r.income, "500");
  assert.equal(r.expenses, "300");
  assert.equal(r.result, "200");
});
test("v0 rejects duplicate, foreign-book, incomplete and unbalanced journal evidence", async () => {
  const { financialSnapshot } = await loadComponent("src/lib/v0-ledger.ts");
  const lines = pair("cash", "loan", "10000");
  assert.throws(
    () => financialSnapshot(book, state, [...lines, lines[0]], period),
    /Duplicate/,
  );
  assert.throws(
    () =>
      financialSnapshot(
        book,
        state,
        [{ ...lines[0], book_id: "other" }, lines[1]],
        period,
      ),
    /different Book/,
  );
  assert.throws(
    () => financialSnapshot(book, state, [lines[0]], period),
    /do not balance/,
  );
  assert.throws(
    () =>
      financialSnapshot(
        book,
        state,
        [{ ...lines[0], account_code: "missing" }, lines[1]],
        period,
      ),
    /missing/,
  );
  assert.throws(() =>
    financialSnapshot(
      book,
      state,
      [{ ...lines[0], economic_date: "2026-02-30" }, lines[1]],
      period,
    ),
  );
});
test("v0 Accounts groups include all five categories and keep recorded amounts unchanged", async () => {
  const { inAccountGroup } = await loadComponent("src/lib/v0-ledger.ts");
  assert.ok(inAccountGroup({ kind: "other_income" }, "income"));
  assert.ok(inAccountGroup({ kind: "other_expense" }, "expense"));
  const a = { kind: "liability", balance_minor: "-10000" };
  assert.ok(inAccountGroup(a, "liability"));
  assert.equal(a.balance_minor, "-10000");
});
