import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "ledger-book",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  start_date: "2025-01-01",
  accounting_today: "2026-10-28",
  accounting_timezone: "UTC",
};
const account = {
  code: "bank",
  name: "Operating",
  kind: "asset",
  usage: "cash",
  currency: "USD",
  scale: 2,
  balance_minor: "0",
  native_minor: "",
  posting_count: 1,
};
const range = { from: "2026-02-01", to: "2026-02-28" };
const row = (line_id, date, side, amount, extra = {}) => ({
  book_id: book.id,
  currency: "USD",
  scale: "2",
  account_code: "bank",
  economic_date: date,
  posting_date: date,
  transaction_id: line_id,
  journal_id: `journal-${line_id}`,
  line_id,
  side,
  amount_minor: String(amount),
  line_memo: "",
  ...extra,
});
const journal = [
  row("opening", "2025-12-01", "debit", 10000),
  row("same-entry-debit", "2026-02-01", "debit", 3000, {
    transaction_id: "split",
    posting_date: "2026-03-03",
  }),
  row("same-entry-credit", "2026-02-01", "credit", 500, {
    transaction_id: "split",
    posting_date: "2026-03-03",
  }),
  row("debit", "2026-02-10", "credit", 2000),
  row("reversal", "2026-02-20", "debit", 2000, { reversal_of: "debit" }),
  row("future", "2026-03-01", "debit", 99999),
];
const metadata = [
  {
    transaction_id: "split",
    book_id: book.id,
    description: "Supplier refund and fee",
    counterparty: "Supplier",
    kind: "journal",
    reversed_by: "",
  },
];

test("opening + period debit/credit movements reconcile every journal-line running balance and closing balance", async () => {
  const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
  const original = JSON.stringify(journal);
  const ledger = accountLedger(book, account, journal, range, metadata);
  assert.equal(ledger.opening, 10000n);
  assert.equal(ledger.debits, 5000n);
  assert.equal(ledger.credits, 2500n);
  assert.equal(ledger.periodChange, 2500n);
  assert.equal(ledger.closing, 12500n);
  assert.deepEqual(
    ledger.lines.map((l) => l.id),
    ["same-entry-debit", "same-entry-credit", "debit", "reversal"],
  );
  assert.deepEqual(
    ledger.lines.map((l) => l.balance),
    [13000n, 12500n, 10500n, 12500n],
  );
  assert.equal(
    ledger.lines[0].postingDate,
    "2026-03-03",
    "scope uses accounting date, not posting date",
  );
  assert.equal(ledger.lines[0].description, "Supplier refund and fee");
  assert.equal(
    ledger.lines[1].transactionId,
    "split",
    "split lines retain their common evidence destination",
  );
  assert.equal(JSON.stringify(journal), original);
});

test("liabilities and income retain credit balances positive; income is not reset by the selected period", async () => {
  const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
  const creditRows = [
    row("old", "2025-12-01", "credit", 10000),
    row("new", "2026-02-01", "credit", 3000),
    row("paid", "2026-02-02", "debit", 2000),
  ];
  for (const kind of ["liability", "equity", "revenue"]) {
    const l = accountLedger(book, { ...account, kind }, creditRows, range);
    assert.equal(l.positiveSide, "credit");
    assert.equal(l.opening, 10000n);
    assert.equal(l.debits, 2000n);
    assert.equal(l.credits, 3000n);
    assert.equal(l.closing, 11000n);
    assert.equal(l.periodChange, 1000n);
  }
  const expense = accountLedger(
    book,
    { ...account, kind: "expense" },
    journal,
    range,
  );
  assert.equal(expense.opening, 10000n);
  assert.equal(expense.periodChange, 2500n);
  assert.equal(expense.closing, 12500n);
});

test("no period postings keep the recorded opening/closing position; empty and zero lines are exact", async () => {
  const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
  const noRows = accountLedger(book, account, [journal[0]], range);
  assert.equal(noRows.opening, 10000n);
  assert.equal(noRows.closing, 10000n);
  assert.equal(noRows.lines.length, 0);
  const empty = accountLedger(book, account, [], range);
  assert.equal(empty.opening, 0n);
  assert.equal(empty.closing, 0n);
  const zero = accountLedger(
    book,
    account,
    [row("zero", "2026-02-01", "debit", 0)],
    range,
  );
  assert.equal(zero.lines.length, 1);
  assert.equal(zero.lines[0].balance, 0n);
  const large = accountLedger(
    book,
    account,
    [
      row("huge", "2025-12-01", "debit", "900719925474099312345"),
      row("cent", "2026-02-01", "credit", 1),
    ],
    range,
  );
  assert.equal(large.closing, 900719925474099312344n);
});

test("source dates/identities/currency must be valid, and unknown posting dates never come from recorded_at", async () => {
  const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
  const fail = (rows) =>
    assert.throws(() => accountLedger(book, account, rows, range));
  fail([...journal, journal[0]]);
  fail([row("bad", "2026-02-30", "debit", 10)]);
  fail([row("bad", "2026-02-01", "debit", 10, { book_id: "other" })]);
  fail([row("bad", "2026-02-01", "debit", 10, { currency: "EUR" })]);
  fail([row("bad", "2026-02-01", "debit", 10, { line_id: "" })]);
  fail([row("bad", "2026-02-01", "debit", "1.2")]);
  assert.throws(() =>
    accountLedger(book, account, journal, { from: range.to, to: range.from }),
  );
  assert.throws(() =>
    accountLedger(book, account, journal, range, [
      { ...metadata[0], book_id: "other" },
    ]),
  );
  const unknown = accountLedger(
    book,
    account,
    [
      row("unknown", "2026-02-01", "debit", 10, {
        posting_date: "",
        recorded_at: "2026-04-01T12:00:00Z",
      }),
    ],
    range,
  );
  assert.equal(unknown.lines[0].postingDate, null);
  const earlier = accountLedger(
    book,
    account,
    [journal[3], journal[0], journal[1]],
    range,
  );
  assert.deepEqual(
    earlier.lines.map((l) => l.id),
    ["same-entry-debit", "debit"],
  );
});

test("the ledger renders compact totals, true dates, split rows and direct entry/evidence actions", async () => {
  await withDOM(async ({ render }) => {
    const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
    const { AccountLedgerView } = await loadComponent(
      "src/components/workspaces/account-ledger.tsx",
    );
    const opened = [];
    const errors = [];
    window.addEventListener("error", (event) => errors.push(event.error));
    const ledger = accountLedger(book, account, journal, range, metadata);
    await render(
      React.createElement(AccountLedgerView, {
        ledger,
        book,
        name: account.name,
        onInspect: (id) => opened.push(id),
      }),
    );
    assert.deepEqual(
      [...document.querySelectorAll(".account-ledger-summary dt")].map(
        (e) => e.textContent,
      ),
      ["Balance before February 1, 2026", "Debits", "Credits", "Balance on February 28, 2026"],
    );
    assert.deepEqual(
      [...document.querySelectorAll(".account-ledger-summary dd")].map(
        (e) => e.textContent,
      ),
      ["$100.00", "$50.00", "$25.00", "$125.00"],
    );
    assert.match(document.querySelector("thead")?.textContent ?? "", /Balance/);
    assert.equal(document.querySelectorAll("tbody tr").length, 4);
    assert.equal(
      document.querySelector("time").getAttribute("datetime"),
      "2026-02-01",
    );
    assert.ok(document.querySelector('time[datetime="2026-03-03"]'));
    const button = [...document.querySelectorAll("button")].find((e) =>
      e
        .getAttribute("aria-label")
        ?.includes("View entry and source evidence: Supplier"),
    );
    assert.ok(button);
    await React.act(async () => button.click());
    assert.deepEqual(
      opened,
      ["split"],
      "one explicit action opens the existing source-aware inspector once",
    );
    await React.act(async () =>
      document.querySelectorAll("tbody tr")[1].click(),
    );
    assert.deepEqual(opened, ["split", "split"]);
    assert.deepEqual(
      errors,
      [],
      "evidence activation must not throw browser errors",
    );
  });
});

test("pagination never restarts the running balance or changes the full-period totals", async () => {
  await withDOM(async ({ render }) => {
    const { accountLedger } = await loadComponent("src/lib/account-ledger.ts");
    const { AccountLedgerView } = await loadComponent(
      "src/components/workspaces/account-ledger.tsx",
    );
    const many = [
      journal[0],
      ...Array.from({ length: 45 }, (_, index) =>
        row(`posting-${index + 1}`, "2026-02-01", "debit", 1),
      ),
    ];
    const ledger = accountLedger(book, account, many, range);
    await render(
      React.createElement(AccountLedgerView, {
        ledger,
        book,
        name: account.name,
        periodTotalShown: true,
      }),
    );
    assert.match(
      document.body.textContent,
      /Opening and closing include earlier recorded postings/,
    );
    assert.equal(document.querySelectorAll("tbody tr").length, 25);
    assert.equal(
      [...document.querySelectorAll("tbody tr")].at(-1).lastElementChild
        .textContent,
      "$100.25",
    );
    const next = document.querySelector('button[aria-label="Next page"]');
    assert.ok(next);
    await React.act(async () => next.click());
    assert.equal(document.querySelectorAll("tbody tr").length, 20);
    assert.equal(
      document.querySelector("tbody tr").lastElementChild.textContent,
      "$100.26",
    );
    assert.equal(
      [...document.querySelectorAll(".account-ledger-summary dd")].at(-1)
        .textContent,
      "$100.45",
    );
  });
});
