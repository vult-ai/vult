import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, React, withDOM } from "./helpers/dom.mjs";
import { scopedPostingResponse } from "./helpers/scoped-postings.mjs";

const book = { id: "book", currency: "USD", scale: 2 };
const accounts = [
  ["1000", "Operating", "asset"],
  ["1001", "Reserve", "asset"],
  ["5000", "Software", "expense"],
  ["5200", "Travel", "expense"],
  ["5100", "Exchange loss", "expense"],
  ["4000", "Sales", "revenue"],
  ["2000", "Accounts payable", "liability"],
].map(([code, name, kind]) => ({ code, name, kind }));
const state = { accounts, cash_accounts: [{ code: "1000" }, { code: "1001" }] };
const transaction = (id, extra = {}) => ({
  id,
  kind: "journal",
  category: "",
  ...extra,
});
const header =
  "book_id,currency,transaction_id,line_id,account_code,side,amount_minor,line_memo\r\n";
const csv = (rows) =>
  header +
  rows
    .map(
      ([id, line, code, side, amount, memo = ""]) =>
        `book,USD,${id},${line},${code},${side},${amount},"${memo.replaceAll('"', '""')}"\r\n`,
    )
    .join("");

test("posted journal categories and effects retain splits, refunds, transfers and noncash expenses", async () => {
  const { parseTransactionJournal, transactionFacts } = await loadComponent(
    "src/lib/transaction-journal.ts",
  );
  const journal = parseTransactionJournal(
    csv([
      [
        "expense",
        "e1",
        "5000",
        "debit",
        "20000",
        'A comma, newline\nand "quote"',
      ],
      ["expense", "e2", "1000", "credit", "20000"],
      ["income", "i1", "1000", "debit", "70000"],
      ["income", "i2", "4000", "credit", "70000"],
      ["refund", "r1", "1000", "debit", "2000"],
      ["refund", "r2", "5000", "credit", "2000"],
      ["customer-refund", "cr1", "4000", "debit", "3000"],
      ["customer-refund", "cr2", "1000", "credit", "3000"],
      ["split", "s1", "5000", "debit", "10000"],
      ["split", "s2", "5200", "debit", "5000"],
      ["split", "s3", "1000", "credit", "15000"],
      ["transfer", "t1", "1001", "debit", "10000"],
      ["transfer", "t2", "1000", "credit", "11000"],
      ["transfer", "t3", "5100", "debit", "1000"],
      ["bill", "b1", "5000", "debit", "9900"],
      ["bill", "b2", "2000", "credit", "9900"],
      ["foreign-expense", "f1", "5000", "debit", "10000"],
      ["foreign-expense", "f2", "5100", "debit", "300"],
      ["foreign-expense", "f3", "1000", "credit", "10300"],
      ["cancelled", "c1", "5000", "debit", "100"],
      ["cancelled", "c2", "5000", "credit", "100"],
      ["cancelled", "c3", "5200", "debit", "300"],
      ["cancelled", "c4", "1000", "credit", "300"],
    ]),
    book,
  );
  const present = transactionFacts(state, journal);
  const facts = (id, extra) => present(transaction(id, extra));
  assert.deepEqual(facts("expense", { view_kind: "expense" }), {
    category: "Software",
    categoryTitle: "Software",
    categoryAccount: state.accounts.find((account) => account.code === "5000"),
    direction: "out",
    directionLabel: "Expense",
  });
  assert.equal(facts("income").direction, "in");
  assert.equal(facts("income").category, "Sales");
  assert.equal(facts("refund").direction, "in");
  assert.equal(facts("refund").category, "Software");
  assert.deepEqual(
    [facts("refund", { view_kind: "expense" }).direction, facts("refund", { view_kind: "expense" }).directionLabel],
    ["in", "Refund"],
    "money coming back on an expense is a refund, not an expense",
  );
  assert.deepEqual(
    [facts("customer-refund", { view_kind: "income" }).direction, facts("customer-refund", { view_kind: "income" }).directionLabel],
    ["out", "Refund"],
    "money paid back to a customer is a refund, not income",
  );
  assert.equal(facts("split").category, "Split (2)");
  assert.match(facts("split").categoryTitle, /Software · Travel/);
  assert.equal(
    facts("transfer").direction,
    "transfer",
    "an FX residual must not turn an internal transfer into an expense",
  );
  assert.equal(facts("transfer").category, "Operating → Reserve");
  assert.equal(
    facts("bill").directionLabel,
    "Expense",
    "a noncash bill describes an expense without claiming a bank payment",
  );
  assert.equal(
    facts("cancelled").category,
    "Travel",
    "cancelled same-account legs do not create a false split",
  );
  assert.equal(
    facts("foreign-expense").category,
    "Software",
    "a realized FX valuation leg does not create a false category split",
  );
  assert.equal(facts("expense", { kind: "reversal" }).direction, "reversal");
  assert.equal(facts("missing").category, "—");
});

test("journal projection rejects cross-Book, incomplete and conflicting evidence while retaining exact integers", async () => {
  const { parseTransactionJournal } = await loadComponent(
    "src/lib/transaction-journal.ts",
  );
  const rows = [
    ["large", "l1", "1000", "debit", "9007199254740993"],
    ["large", "l2", "4000", "credit", "9007199254740993"],
  ];
  const source = csv(rows);
  const journal = parseTransactionJournal("\ufeff" + source, book);
  assert.equal(journal.get("large")[0].amount, 9007199254740993n);
  assert.equal(
    parseTransactionJournal(csv([...rows, rows[0]]), book).get("large").length,
    2,
  );
  assert.throws(
    () =>
      parseTransactionJournal(source.replace("book,USD", "other,USD"), book),
    /another Book/,
  );
  assert.throws(
    () => parseTransactionJournal(source.replace("book,USD", "book,EUR"), book),
    /another Book/,
  );
  assert.throws(
    () => parseTransactionJournal(csv([rows[0]]), book),
    /unbalanced/,
  );
  assert.throws(
    () =>
      parseTransactionJournal(
        csv([...rows, ["large", "l1", "1000", "debit", "1"]]),
        book,
      ),
    /conflicting/,
  );
  assert.throws(
    () =>
      parseTransactionJournal(
        header + 'book,USD,x,l1,1000,debit,10,"unfinished',
        book,
      ),
    /truncated/,
  );
  assert.throws(
    () =>
      parseTransactionJournal(
        source.replace("debit,9007199254740993", "debit,1.2"),
        book,
      ),
    /invalid posting/,
  );
});

test("retained journal reads cache across table changes, invalidate after saves, and discard a late Book response", async () => {
  await withDOM(async ({ render, window }) => {
    const { useTransactionJournal } = await loadComponent(
      "src/lib/use-transaction-journal.ts",
    );
    const previous = globalThis.fetch;
    const pending = [];
    const calls = [];
    globalThis.fetch = async (url, options) => {
      calls.push({ url, signal: options.signal });
      return new Promise((resolve) => pending.push(resolve));
    };
    let read;
    function View({ currentBook = book, revision = 0, active = true }) {
      read = useTransactionJournal(currentBook, revision, active, ["a"]);
      return React.createElement(
        "div",
        null,
        read.journal
          ? [...read.journal.keys()].join(",")
          : read.error || "Loading",
      );
    }
    const result = scopedPostingResponse;
    const source = csv([
      ["a", "a1", "1000", "debit", "10"],
      ["a", "a2", "4000", "credit", "10"],
    ]);
    try {
      await render(React.createElement(View));
      assert.equal(calls.length, 1);
      assert.equal(
        new URL(calls[0].url, window.location.href).searchParams.get("book_id"),
        "book",
      );
      await React.act(async () => pending.shift()(result(source)));
      assert.ok(read.journal.has("a"));
      await render(React.createElement(View));
      await render(React.createElement(View, { active: false }));
      await render(React.createElement(View));
      assert.equal(
        calls.length,
        1,
        "filter, page and workspace return reuse the snapshot",
      );
      await render(React.createElement(View, { revision: 1 }));
      assert.equal(calls.length, 2);
      assert.equal(
        read.journal,
        null,
        "old categories cannot masquerade as a new save revision",
      );
      await render(
        React.createElement(View, {
          currentBook: { ...book, id: "other" },
          revision: 1,
        }),
      );
      assert.equal(calls[1].signal.aborted, true);
      await React.act(async () => pending.shift()(result(source)));
      assert.equal(read.journal, null);
      await React.act(async () =>
        pending.shift()(result(source.replaceAll("book,USD", "other,USD"))),
      );
      assert.ok(read.journal.has("a"));
      await render(
        React.createElement(View, {
          currentBook: { ...book, id: "other" },
          revision: 2,
        }),
      );
      await React.act(async () => pending.shift()({ ok: false }));
      assert.match(read.error, /could not be loaded/);
      assert.equal(read.journal, null);
      await React.act(async () => read.retry());
      assert.equal(
        calls.length,
        5,
        "a failed snapshot can retry without changing Book or revision",
      );
      await React.act(async () =>
        pending.shift()(result(source.replaceAll("book,USD", "other,USD"))),
      );
      assert.ok(read.journal.has("a"));
      assert.equal(read.error, "");
    } finally {
      globalThis.fetch = previous;
    }
  });
});

test("journal enrichment fills the real grid without replacing its rows, selection or original amounts", async () => {
  await withDOM(async ({ render }) => {
    const { default: Transactions } = await loadComponent(
      "src/components/book/transactions-table.tsx",
    );
    const previous = globalThis.fetch;
    let finish;
    let reads = 0;
    globalThis.fetch = async () => {
      reads++;
      return new Promise((resolve) => {
        finish = resolve;
      });
    };
    const row = {
      id: "expense",
      kind: "journal",
      view_kind: "expense",
      date: "2026-10-12",
      description: "Source entry",
      attachment_count: 0,
      counterparty: "Example merchant",
      category: "",
      amount_minor: "20000",
      fx: { original_currency: "USD", original_amount_minor: "20000" },
    };
    const current = {
      ...state,
      account_balances: accounts.map((a) => ({
        ...a,
        usage: a.code.startsWith("100") ? "cash" : "general",
      })),
      currencies: [{ code: "USD", scale: 2 }],
      transactions: [row],
    };
    const view = () =>
      React.createElement(Transactions, {
        book,
        state: current,
        detail: "",
        loading: false,
        resetKey: "same",
        emptyState: "Empty",
        onOpen() {},
      });
    try {
      await render(view());
      const table = document.querySelector("table");
      const tr = table.querySelector('tr[data-record-id="expense"]');
      const selector = tr.querySelector('input[type="checkbox"]');
      await React.act(async () => selector.click());
      await React.act(async () =>
        finish(scopedPostingResponse(
            csv([
              ["expense", "e1", "5000", "debit", "20000"],
              ["expense", "e2", "1000", "credit", "20000"],
            ]),
        )),
      );
      assert.ok(document.querySelector("table") === table);
      assert.ok(table.querySelector('tr[data-record-id="expense"]') === tr);
      assert.equal(selector.checked, true);
      assert.equal(tr.cells[3].textContent, "Software");
      assert.equal(
        tr.querySelector(".transaction-ledger").textContent,
        "$200.00",
      );
      assert.equal(
        tr.querySelector(".transaction-money").getAttribute("aria-label"),
        "Expense: $200.00",
      );
      await render(view());
      assert.equal(reads, 1);
      current.transactions = [
        row,
        { ...row, id: "new", description: "New source entry" },
      ];
      await render(view());
      assert.equal(
        reads,
        2,
        "a newly observed entry refreshes the cached journal",
      );
      await React.act(async () =>
        finish(scopedPostingResponse(
            csv([
              ["expense", "e1", "5000", "debit", "20000"],
              ["expense", "e2", "1000", "credit", "20000"],
            ]),
        )),
      );
      const readNotice = document.querySelector(
        '.error-notice-copy[role="status"]',
      );
      assert.ok(
        readNotice,
        "optional journal enrichment must expose a recoverable read failure",
      );
      assert.doesNotMatch(
        readNotice.textContent,
        /does not cover|journal|stack/i,
      );
      assert.equal(
        tr.cells[3].textContent,
        "Software",
        "the verified snapshot survives a supplemental read failure",
      );
      assert.equal(selector.checked, true);
      assert.equal(
        tr.querySelector(".transaction-ledger").textContent,
        "$200.00",
      );
      const retry = [...document.querySelectorAll("button")].find(
        (button) => button.textContent === "Try again",
      );
      await React.act(async () => retry.click());
      assert.equal(reads, 3);
      await React.act(async () =>
        finish(scopedPostingResponse(
            csv([
              ["expense", "e1", "5000", "debit", "20000"],
              ["expense", "e2", "1000", "credit", "20000"],
              ["new", "n1", "5200", "debit", "20000"],
              ["new", "n2", "1000", "credit", "20000"],
            ]),
        )),
      );
      assert.equal(document.querySelector('[role="alert"]'), null);
      assert.equal(
        table.querySelector('tr[data-record-id="new"]').cells[3].textContent,
        "Travel",
      );
    } finally {
      globalThis.fetch = previous;
    }
  });
});
