import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import { accountReadResponse } from "./helpers/account-reads.mjs";

const book = {
  id: "book",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_today: "2026-10-31",
  accounting_timezone: "UTC",
};
const range = { from: "2026-10-01", to: "2026-10-31" };
const accounts = [
  {
    code: "1000",
    name: "Operating",
    kind: "asset",
    usage: "cash",
    currency: "USD",
    scale: 2,
    balance_minor: "300",
    native_minor: "300",
    posting_count: 3,
  },
  {
    code: "4000",
    name: "Income:Consulting",
    kind: "revenue",
    usage: "general",
    currency: "",
    scale: 2,
    balance_minor: "-300",
    native_minor: "",
    posting_count: 3,
  },
];
const csv = (count = 3) =>
  "book_id,currency,scale,transaction_id,line_id,account_code,side,amount_minor,economic_date,posting_date,native_currency,native_amount_minor\r\n" +
  Array.from(
    { length: count },
    (_, i) =>
      `book,USD,2,e${String(i).padStart(3, "0")},l${i}-a,1000,debit,100,2026-10-01,2026-10-02,USD,100\r\nbook,USD,2,e${String(i).padStart(3, "0")},l${i}-b,4000,credit,100,2026-10-01,2026-10-02,,\r\n`,
  ).join("");
const url = (offset = 0) =>
  `/api/v1/account-detail?codes=%5B%221000%22%5D&from=${range.from}&to=${range.to}&view=ledger&offset=${offset}`;

test("bounded account detail checks scope, complete totals, ordering and every running balance", async () => {
  const { parseAccountDetail } = await loadComponent(
    "src/lib/account-detail-read.ts",
  );
  const data = await accountReadResponse(url(), csv(), book, accounts).json();
  const result = parseAccountDetail(
    data,
    book,
    ["1000"],
    range,
    "balance",
    300n,
  );
  assert.equal(result.ledger.opening, 0n);
  assert.equal(result.ledger.closing, 300n);
  assert.deepEqual(
    result.ledger.lines.map((line) => [
      line.balance,
      line.date,
      line.postingDate,
    ]),
    [
      [100n, "2026-10-01", "2026-10-02"],
      [200n, "2026-10-01", "2026-10-02"],
      [300n, "2026-10-01", "2026-10-02"],
    ],
  );
  for (const change of [
    (v) => (v.book_id = "other"),
    (v) => (v.currency = "EUR"),
    (v) => (v.scale = 3),
    (v) => (v.account_codes = ["elsewhere"]),
    (v) => (v.from = "2026-09-01"),
    (v) => (v.total_minor = "999"),
    (v) => (v.ledger.lines[1].balance_minor = "900"),
    (v) => (v.ledger.lines[1].id = v.ledger.lines[0].id),
    (v) => (v.ledger.opening_minor = "100"),
    (v) => (v.count = 4),
    (v) => (v.ledger.lines[0].date = "2026-02-31"),
    (v) => (v.activity.groups[0].count = 99),
  ]) {
    const invalid = structuredClone(data);
    change(invalid);
    assert.throws(() =>
      parseAccountDetail(invalid, book, ["1000"], range, "balance", 300n),
    );
  }
  const many = await accountReadResponse(
    url(),
    csv(205),
    book,
    accounts,
  ).json();
  const first = parseAccountDetail(
    many,
    book,
    ["1000"],
    range,
    "balance",
    20500n,
  );
  const rawNext = await accountReadResponse(
    url(100),
    csv(205),
    book,
    accounts,
  ).json();
  const next = parseAccountDetail(
    rawNext,
    book,
    ["1000"],
    range,
    "balance",
    20500n,
    100,
    first.revision,
    first.ledger.lines.at(-1).balance,
  );
  assert.equal(next.ledger.lines.length, 100);
  assert.equal(next.ledger.lines[0].balance, 10100n);
  assert.throws(() =>
    parseAccountDetail(
      { ...rawNext, revision: "b".repeat(64) },
      book,
      ["1000"],
      range,
      "balance",
      20500n,
      100,
      first.revision,
      10000n,
    ),
  );
  assert.throws(() =>
    parseAccountDetail(
      rawNext,
      book,
      ["1000"],
      range,
      "balance",
      20500n,
      100,
      first.revision,
      0n,
    ),
  );
});

test("account detail loads more scoped rows without CSV reads or resetting totals and pauses after unmount", async () => {
  await withDOM(async ({ render }) => {
    const { AccountGroupActivity } = await loadComponent(
      "src/components/workspaces/account-group-activity.tsx",
    );
    const original = globalThis.fetch,
      calls = [];
    globalThis.fetch = async (url, init) => {
      calls.push({ url: String(url), signal: init.signal });
      return accountReadResponse(url, csv(205), book, accounts);
    };
    const node = {
      key: "cash",
      name: "Operating",
      kind: "asset",
      codes: ["1000"],
      own: [accounts[0]],
      children: [],
    };
    try {
      const props = {
        book,
        state: {},
        accounts,
        node,
        name: "Operating",
        range,
        amount: 20500n,
        returnTo: document.body,
        onClose() {},
        embedded: true,
      };
      await render(React.createElement(AccountGroupActivity, props));
      await React.act(async () => {});
      assert.equal(calls.length, 1);
      assert.match(calls[0].url, /account-detail/);
      assert.ok(document.body.textContent.includes("$205.00"));
      await React.act(async () =>
        [...document.querySelectorAll("button")]
          .find((b) => b.textContent === "Show more")
          .click(),
      );
      assert.equal(
        new URL(calls[1].url, "http://localhost").searchParams.get("offset"),
        "100",
      );
      assert.ok(
        new URL(calls[1].url, "http://localhost").searchParams.get("revision"),
      );
      await React.act(async () =>
        [...document.querySelectorAll("button")]
          .find((b) => b.textContent === "Show more")
          .click(),
      );
      assert.equal(calls.length, 3);
      assert.equal(
        new URL(calls[2].url, "http://localhost").searchParams.get("offset"),
        "200",
      );
      assert.ok(
        ![...document.querySelectorAll("button")].some(
          (b) => b.textContent === "Show more",
        ),
      );
      assert.ok(document.body.textContent.includes("$205.00"));
      await render(
        React.createElement(AccountGroupActivity, {
          ...props,
          state: { unrelated: "identity changed" },
        }),
      );
      assert.equal(calls.length, 3);
      await render(null);
      assert.ok(calls[0].signal.aborted);
      assert.ok(calls.every((c) => !c.url.includes("exports")));
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("Accounts reuses its captured snapshot and the engine decides the matching accounts", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", "/accounts?from=2026-10-01&to=2026-10-31");
    const { V0Accounts } = await loadComponent("src/components/v0/v0-accounts.tsx");
    const unused = { ...accounts[0], code: "1010", name: "Savings", balance_minor: "0", native_minor: "0", posting_count: 0 };
    const chart = [...accounts, unused].map(a => ({ ...a, period_change_minor: a.balance_minor }));
    const account_activity = await accountReadResponse(`/api/v1/account-hierarchy-activity?as_of=${range.to}`, csv(), book, chart).json();
    const state = {
      book, accounts: chart, account_balances: chart, transactions: [], read_view: "accounts",
      period: range, account_activity, revision: "example",
      position: { assets_minor: "300", liabilities_minor: "0", equity_accounts_minor: "0", accumulated_result_minor: "300", net_assets_minor: "300" },
      summary: { income_minor: "300", expenses_minor: "0", net_minor: "300" },
    };
    const added = { ...unused, code: "1020", name: "Reserve", period_change_minor: "0" };
    const updatedChart = [...chart, added];
    const updated = {
      ...state, account_balances: updatedChart, revision: "after-account-added",
      account_activity: await accountReadResponse(`/api/v1/account-hierarchy-activity?as_of=${range.to}`, csv(), book, updatedChart).json(),
    };
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url) => {
      calls.push(String(url));
      const parsed = new URL(url, window.location.href);
      if (parsed.pathname.endsWith("/state")) return Response.json(updated);
      if (parsed.pathname.endsWith("/search")) {
        assert.equal(parsed.searchParams.get("q"), "savngs");
        assert.equal(parsed.searchParams.get("account_ids"), "1");
        return Response.json({ account_ids: ["1010"], accounts: { total: 1, items: [] } });
      }
      const response = accountReadResponse(url, csv(), book, updatedChart);
      assert.ok(response, String(url));
      return response;
    };
    const props = { displayBook: book, display: state, accountSearch: "", onShowTransactions() {} };
    try {
      await render(React.createElement(V0Accounts, { ...props, active: false }));
      assert.equal(calls.length, 0);
      await render(React.createElement(V0Accounts, props));
      assert.equal(calls.length, 0, `initial state already has the amounts and history: ${calls.join(", ")}`);
      await render(React.createElement(V0Accounts, { ...props, accountSearch: "savngs" }));
      const root = document.getElementById("root");
      assert.ok(root.querySelector('[aria-label="1 account"]'));
      assert.equal(root.querySelector('input[type="search"]'), null, "Accounts uses the header finder");
      assert.match(root.textContent, /Savings/);
      await render(React.createElement(V0Accounts, props));
      assert.ok(root.querySelector('[aria-label="2 accounts"]'));
      assert.ok(calls.every((url) => !url.includes("/dashboard/") && !url.includes("account-hierarchy-activity?as_of=2026-10-31&book_id=book")));
      const count = calls.length;
      await render(React.createElement(V0Accounts, { ...props, accountSelection: { bookId: book.id, id: "1010" } }));
      assert.match(document.body.textContent, /Savings/);
      assert.ok(calls.slice(count).some((url) => url.includes("/account-detail")));
      await render(React.createElement(V0Accounts, { ...props, accountSelection: { bookId: book.id, id: "1020" } }));
      assert.match(document.body.textContent, /Reserve/);
      assert.equal(calls.filter((url) => url.includes("/api/v1/state")).length, 1, "a newly found account needs one fresh snapshot");
      await render(null);
      calls.length = 0;
      await render(React.createElement(V0Accounts, { ...props, accountSelection: { bookId: book.id, id: "1020" } }));
      assert.match(document.body.textContent, /Reserve/, "an initial account selection survives adopting the startup snapshot");
      assert.equal(calls.filter((url) => url.includes("/api/v1/state")).length, 1);
    } finally {
      globalThis.fetch = original;
    }
  });
});
