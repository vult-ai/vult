import { accountReadResponse } from "./helpers/account-reads.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "meaning-book",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_today: "2026-10-28",
  accounting_timezone: "UTC",
};
const range = { from: "2026-01-01", to: "2026-10-28" };
const position = (code, name, kind, currency = "") => ({
  code,
  name,
  kind,
  currency,
  scale: 2,
  usage: "general",
  balance_minor: "0",
  native_minor: "",
  posting_count: 1,
});
const accounts = [
  position("wise", "Wise PLN", "asset", "PLN"),
  position("bank", "Bank USD", "asset", "USD"),
  position("loan", "Borrowings", "liability"),
  position("fx", "Recorded FX gain", "revenue", "PLN"),
];
const state = {
  accounts: accounts.map(({ code, name, kind }) => ({ code, name, kind })),
  account_balances: accounts,
  transactions: [],
};
const line = (
  transaction_id,
  account_code,
  side,
  amount_minor,
  native_currency = "",
  native_amount_minor = "",
  economic_date = "2026-10-01",
) => ({
  book_id: book.id,
  transaction_id,
  line_id: `${transaction_id}-${account_code}`,
  account_code,
  currency: "USD",
  scale: "2",
  economic_date,
  side,
  amount_minor,
  native_currency,
  native_amount_minor,
});
const journal = [
  line("opening", "wise", "debit", "25000", "PLN", "100000"),
  line("opening", "loan", "credit", "25000"),
  line("transfer", "wise", "credit", "10000", "PLN", "40000"),
  line("transfer", "bank", "debit", "10000", "USD", "10000"),
  line("fx-change", "wise", "debit", "1000"),
  line("fx-change", "fx", "credit", "1000"),
  line("future", "wise", "debit", "90000", "PLN", "900000", "2026-12-01"),
  line("future", "loan", "credit", "90000", "", "", "2026-12-01"),
];
const csv = (rows) => {
  const keys = Object.keys(rows[0]);
  return [
    keys.join(","),
    ...rows.map((row) => keys.map((key) => row[key]).join(",")),
  ].join("\r\n");
};
const leaf = (account) => ({
  key: account.code,
  name: account.name,
  kind: account.kind,
  own: [account],
  children: [],
  codes: [account.code],
});
const settle = () =>
  React.act(async () => new Promise((resolve) => setTimeout(resolve, 60)));

async function details(render, node, amount, overrides = {}) {
  const { AccountGroupActivity } = await loadComponent(
    "src/components/workspaces/account-group-activity.tsx",
  );
  await render(
    React.createElement(AccountGroupActivity, {
      book,
      state,
      accounts,
      node,
      name: node.name,
      range,
      amount,
      returnTo: document.body,
      onClose() {},
      onShowLedger() {},
      embedded: true,
      ...overrides,
    }),
  );
  await settle();
  return document.querySelector('[data-testid="account-details"]');
}

async function withJournal(run, rows = journal) {
  await withDOM(async ({ render }) => {
    const original = globalThis.fetch;
    globalThis.fetch = async (url) =>
      accountReadResponse(url, csv(rows), book, accounts) || new Response(
        String(url).includes("transactions.csv")
          ? "transaction_id,book_id,kind,reversed_by\r\n"
          : csv(rows),
        { headers: { "Content-Type": "text/csv" } },
      );
    try {
      await run(render);
    } finally {
      globalThis.fetch = original;
    }
  });
}

test("group net changes retain interactive zero-effect transfers and never present them as the transaction amount", async () => {
  await withJournal(async (render) => {
    const inspected = [];
    const node = {
      key: "assets",
      name: "Assets",
      kind: "asset",
      own: [],
      children: accounts.slice(0, 2).map(leaf),
      codes: ["wise", "bank"],
    };
    const detail = await details(render, node, 26000n, {
      onInspect: (id) => inspected.push(id),
    });
    assert.equal(
      detail.querySelector(".account-group-activity-total").textContent,
      "$260.00",
    );
    assert.match(detail.textContent, /USD Book currency/);
    assert.match(detail.textContent, /Group net change/);
    assert.ok(
      detail.querySelector(
        ".account-detail-header .account-group-activity-total",
      ),
    );
    assert.match(
      detail.querySelector(".account-detail-header").textContent,
      /Balance as of October 28, 2026/,
    );
    assert.doesNotMatch(detail.textContent, /Offsetting postings/);
    await React.act(async () =>
      detail.querySelector('[aria-label="About group net changes"]').click(),
    );
    const help = document.querySelector('[role="tooltip"]');
    assert.ok(help);
    assert.match(help.textContent, /Offsetting postings can show \$0\.00/);
    await React.act(async () =>
      detail
        .querySelector('[aria-label="About group net changes"]')
        .dispatchEvent(
          new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
        ),
    );
    assert.equal(
      document.querySelector('[role="tooltip"]'),
      null,
      "Escape closes only the contextual explanation",
    );
    const row = [...detail.querySelectorAll("tbody tr")].find((row) =>
      row.textContent.includes("$0.00"),
    );
    assert.ok(row, "zero group net changes remain in the table");
    await React.act(async () => row.click());
    assert.deepEqual(
      inspected,
      ["transfer"],
      "zero-effect rows still inspect their original entry",
    );
    assert.doesNotMatch(detail.textContent, /View account (activity|ledger)/);
  });
});

test("native and Book balances share a cutoff while Book-only FX adjustments change only the Book amount", async () => {
  await withJournal(async (render) => {
    const detail = await details(render, leaf(accounts[0]), 16000n);
    assert.equal(
      detail.querySelector(".account-group-activity-total").textContent,
      "$160.00",
    );
    assert.match(detail.textContent, /Native balance \(PLN\):.*600\.00/);
    assert.match(detail.textContent, /USD Book currency/);
    assert.match(detail.textContent, /not a live currency conversion/);
    await React.act(async () =>
      detail.querySelector('[aria-label="About account ledger"]').click(),
    );
    const context = document.querySelector('[role="tooltip"]');
    assert.match(context.textContent, /USD Book currency/);
    assert.match(context.textContent, /Accounting dates/);
    assert.match(context.textContent, /Debit balances positive/);
    await React.act(async () =>
      document.activeElement.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    assert.equal(
      detail
        .querySelector('[aria-label="About account ledger"]')
        .getAttribute("aria-expanded"),
      "false",
    );
    assert.ok(detail.querySelector('[aria-label="Account actions"]'));
    await React.act(async () =>
      detail.querySelector('[aria-label="Account actions"]').click(),
    );
    assert.match(
      document.querySelector('[role="menu"][aria-label="Account actions"]')
        .textContent,
      /View account activity/,
    );
    await React.act(async () =>
      document.activeElement.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      ),
    );
    assert.doesNotMatch(detail.textContent, /View account ledger/);
    assert.doesNotMatch(detail.textContent, /Group net change/);
  });
  await withJournal(
    async (render) => {
      const detail = await details(render, leaf(accounts[0]), 16000n);
      assert.match(detail.textContent, /Native balance \(PLN\): Unavailable/);
      assert.equal(
        detail.querySelector(".account-group-activity-total").textContent,
        "$160.00",
      );
    },
    journal.map((row) =>
      row.native_currency === "PLN" ? { ...row, native_currency: "EUR" } : row,
    ),
  );
});

test("period totals retain their meaning and one-member groups remain groups", async () => {
  await withJournal(async (render) => {
    const detail = await details(render, leaf(accounts[3]), 1000n);
    assert.match(
      detail.querySelector(".account-detail-header").textContent,
      /Period total.*January 1.*October 28.*USD Book currency/,
    );
    assert.doesNotMatch(detail.textContent, /Native balance/);
  });
  await withJournal(async (render) => {
    const node = {
      key: "one-member-assets",
      name: "Assets",
      kind: "asset",
      own: [],
      children: [leaf(accounts[0])],
      codes: ["wise"],
    };
    const detail = await details(render, node, 16000n);
    assert.match(detail.textContent, /Group net change/);
    assert.doesNotMatch(
      detail.textContent,
      /View account activity|Native balance/,
    );
  });
});

test("missing native amounts cannot become zero and mixed native currencies cannot be added together", async () => {
  const { activityFromJournal } = await loadComponent(
    "src/lib/account-hierarchy-activity.ts",
  );
  const before = JSON.stringify(journal);
  const result = activityFromJournal(book, journal, range.to);
  assert.equal(result.nativeBalances.wise, "60000");
  assert.equal(result.nativeCurrencies.wise, "PLN");
  assert.throws(
    () =>
      activityFromJournal(
        book,
        journal.map((row, index) =>
          index === 0 ? { ...row, native_amount_minor: "" } : row,
        ),
        range.to,
      ),
    /native balances/,
  );
  assert.throws(
    () =>
      activityFromJournal(
        book,
        journal.map((row, index) =>
          index === 2 ? { ...row, native_currency: "EUR" } : row,
        ),
        range.to,
      ),
    /native balances/,
  );
  assert.equal(JSON.stringify(journal), before);
});
