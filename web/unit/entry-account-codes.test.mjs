import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic Book",
  currency: "USD",
  scale: 2,
};
const currencies = [
  { code: "USD", scale: 2 },
  { code: "EUR", scale: 2 },
];
const accounts = [
  {
    code: "bank-reference",
    name: "Example checking",
    kind: "asset",
    gl_code: "0100",
  },
  {
    code: "reserve-reference",
    name: "Example reserve",
    kind: "asset",
    gl_code: "0101",
  },
  {
    code: "software-reference",
    name: "Software",
    kind: "expense",
    gl_code: "0634",
  },
  {
    code: "travel-reference",
    name: "Expenses:Travel",
    kind: "expense",
    gl_code: "0640",
  },
  {
    code: "payable-reference",
    name: "Accounts payable",
    kind: "liability",
    gl_code: "0200",
  },
];
const line = (id, reference, side, amount = "10000", fields = {}) => ({
  id,
  account_code: reference,
  account_name: accounts.find((account) => account.code === reference).name,
  side,
  amount_minor: amount,
  ...fields,
});
const entry = {
  id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  book_id: book.id,
  kind: "expense",
  description: "Synthetic subscription",
  counterparty: "Fixture supplier",
  category: "software-reference",
  amount_minor: "10000",
  date: "2026-10-01",
  currency: "USD",
  scale: 2,
  attachment_count: 0,
  fx: {
    original_currency: "EUR",
    original_amount_minor: "9000",
    cash_account: "bank-reference",
  },
  lines: [
    line("expense-line", "software-reference", "debit", "10000", {
      native_currency: "EUR",
      native_amount_minor: "9000",
      memo: "Synthetic line memo",
    }),
    line("bank-line", "bank-reference", "credit", "10000", {
      memo: "Synthetic subscription",
    }),
  ],
};
const state = {
  book,
  currencies,
  accounts,
  transactions: [entry],
  cash_accounts: accounts.slice(0, 2),
  account_balances: [{ code: "bank-reference", usage: "cash" }],
};
const csv = (data) =>
  [
    "book_id,currency,transaction_id,line_id,account_code,side,amount_minor",
    ...data.lines.map((posting) =>
      [
        book.id,
        book.currency,
        data.id,
        posting.id,
        posting.account_code,
        posting.side,
        posting.amount_minor,
      ].join(","),
    ),
  ].join("\r\n");
const settle = () =>
  React.act(async () => new Promise((done) => requestAnimationFrame(done)));
const click = (element) =>
  React.act(async () => {
    assert.ok(element, "expected an available control");
    element.click();
  });
function surfaces(
  ui,
  data = entry,
  snapshot = state,
  scope = { kind: "user", id: "synthetic-user-a" },
) {
  return React.createElement(
    ui.UserPreferencesProvider,
    { scope },
    React.createElement(
      React.Fragment,
      null,
      React.createElement(ui.TransactionsTable, {
        book,
        state: snapshot,
        detail: data.id,
        loading: false,
        resetKey: "account-codes",
        onOpen() {},
        emptyState: React.createElement("p", null, "No entries"),
      }),
      React.createElement(
        "aside",
        { className: "entry-preview-content" },
        React.createElement(ui.EntryOverview, {
          data,
          state: snapshot,
          book,
          currencies,
        }),
        React.createElement(ui.EntryAccountingDetails, {
          data,
          accounts: snapshot.accounts,
          book,
          currencies,
        }),
      ),
    ),
  );
}
const category = () => {
  const label = [...document.querySelectorAll(".entry-overview-facts dt")].find(
    (element) => element.textContent === "Category",
  );
  return label?.nextElementSibling?.textContent;
};
// Postings read as money moving: credited accounts first, then debited ones.
const postingNames = () =>
  [...document.querySelectorAll(".entry-posting-name")].map((name) => name.title);
const postings = () => document.querySelector('[aria-label="Postings"]');

test("Assigned codes always appear in Activity, Category and journal postings regardless of retired visibility preferences", () =>
  withDOM(async ({ render, window }) => {
    const ui = await loadComponent("unit/helpers/activity-account-codes.tsx");
    const previous = globalThis.fetch,
      reads = [],
      original = JSON.stringify({ state, entry });
    globalThis.fetch = async (url, options) => {
      reads.push({ url: String(url), method: options?.method || "GET" });
      assert.match(String(url), /exports\/journal-lines\.csv/);
      return new Response(csv(entry), {
        headers: { "Content-Type": "text/csv" },
      });
    };
    try {
      window.localStorage.setItem(
        "vult.account-code-visibility.v1:user:synthetic-user-a",
        "false",
      );
      await render(surfaces(ui));
      await settle();
      assert.equal(category(), "0634 · Software");
      const codeField = document.querySelector(".transaction-category .vult-name-link");
      assert.equal(codeField.querySelector(".vult-code").textContent, "0634 · ");
      assert.equal(codeField.querySelector(".vult-name-link-label").textContent, "Software");
      assert.deepEqual(postingNames(), [
        "0100 · Example checking",
        "0634 · Software",
      ]);
      assert.deepEqual(
        [...document.querySelectorAll('[role="columnheader"]')].map(
          (element) => element.textContent,
        ),
        ["Date", "To/From", "Amount", "Category"],
      );
      assert.deepEqual(
        [...document.querySelectorAll(".entry-posting")].map((row) => row.dataset.side),
        ["credit", "debit"],
      );
      assert.equal(category(), "0634 · Software");
      assert.match(
        document.querySelector('[data-testid="book-transactions"]').textContent,
        /0634 · Software/,
      );
      assert.deepEqual(postingNames(), [
        "0100 · Example checking",
        "0634 · Software",
      ]);
      assert.deepEqual(
        [...document.querySelectorAll(".entry-posting-amount")].map(
          (element) => element.textContent,
        ),
        ["$100.00", "$100.00"],
      );
      assert.match(
        postings().textContent,
        /€90\.00 original/,
      );
      assert.match(
        postings().textContent,
        /Synthetic line memo/,
      );
      assert.deepEqual(
        postingNames(),
        ["0100 · Example checking", "0634 · Software"],
        "full account paths remain available alongside compact names",
      );
      assert.doesNotMatch(
        postings().textContent,
        /Synthetic subscription/,
        "the entry description is not repeated in posting rows",
      );
      assert.doesNotMatch(
        postings().outerHTML,
        /software-reference|bank-reference/,
      );
      assert.doesNotMatch(
        document.querySelector(".entry-hero").textContent,
        /0100|0634/,
        "the summary names the party, never account codes",
      );
      const readsBeforeMetadataChange = reads.length;
      await render(
        surfaces(ui, entry, {
          ...state,
          accounts: accounts.map((account) =>
            account.code === "software-reference"
              ? { ...account, gl_code: "0700" }
              : account,
          ),
        }),
      );
      assert.equal(category(), "0700 · Software");
      assert.equal(postingNames()[1], "0700 · Software");
      assert.match(
        document.querySelector('[data-testid="book-transactions"]').textContent,
        /0700 · Software/,
      );
      assert.equal(
        reads.length,
        readsBeforeMetadataChange,
        "renumbering presentation does not refetch or rewrite the retained journal",
      );
      await render(
        surfaces(ui, entry, {
          ...state,
          accounts: accounts.map((account) =>
            account.code === "software-reference"
              ? { ...account, gl_code: "" }
              : account,
          ),
        }),
      );
      assert.equal(category(), "Software");
      assert.equal(postingNames()[1], "Software");
      await render(
        surfaces(ui, entry, state, { kind: "user", id: "synthetic-user-b" }),
      );
      assert.equal(category(), "0634 · Software");
      assert.equal(JSON.stringify({ state, entry }), original);
      assert.ok(reads.length > 0);
      assert.ok(reads.every((read) => read.method === "GET"));
    } finally {
      globalThis.fetch = previous;
    }
  }));

test("Standard code labels preserve split, payable settlement and transfer meanings and every exact journal line", () =>
  withDOM(async ({ render, window }) => {
    const ui = await loadComponent("unit/helpers/activity-account-codes.tsx");
    const cases = [
      {
        ...entry,
        lines: [
          line("s", "software-reference", "debit", "6000"),
          line("t", "travel-reference", "debit", "4000"),
          line("b", "bank-reference", "credit"),
        ],
        expected: "Split (2)",
      },
      {
        ...entry,
        kind: "payable_payment",
        category: "payable-reference",
        fx: {
          original_currency: "USD",
          original_amount_minor: "10000",
          cash_account: "bank-reference",
        },
        lines: [
          line("p", "payable-reference", "debit"),
          line("b", "bank-reference", "credit"),
        ],
        expected: "0200 · Accounts payable",
      },
      {
        ...entry,
        kind: "transfer",
        fx: {
          original_currency: "USD",
          original_amount_minor: "10000",
          cash_account: "bank-reference",
          destination_account: "reserve-reference",
        },
        lines: [
          line("r", "reserve-reference", "debit"),
          line("b", "bank-reference", "credit"),
        ],
        expected: undefined,
      },
    ];
    for (const data of cases) {
      const snapshot = { ...state, transactions: [data] },
        original = JSON.stringify(data);
      await render(
        React.createElement(
          ui.UserPreferencesProvider,
          { scope: { kind: "user", id: "synthetic-user-a" } },
          React.createElement(
            "div",
            null,
            React.createElement(ui.EntryOverview, {
              data,
              state: snapshot,
              book,
              currencies,
            }),
            React.createElement(ui.EntryAccountingDetails, {
              data,
              accounts,
              book,
              currencies,
            }),
          ),
        ),
      );
      assert.equal(category(), data.expected);
      assert.equal(
        document.querySelectorAll(".entry-posting").length,
        data.lines.length,
      );
      const amount = (posting) =>
        posting.amount_minor === "10000" ? "$100.00" : posting.amount_minor === "6000" ? "$60.00" : "$40.00";
      assert.deepEqual(
        [...document.querySelectorAll(".entry-posting")].map(
          (row) => `${row.dataset.side} ${row.querySelector(".entry-posting-amount").textContent}`,
        ),
        [...data.lines.filter((p) => p.side === "credit"), ...data.lines.filter((p) => p.side === "debit")].map(
          (posting) => `${posting.side} ${posting.side === "credit" ? "$100.00" : amount(posting)}`,
        ),
      );
      assert.equal(JSON.stringify(data), original);
      if (data.kind === "transfer") {
        assert.match(postings().textContent, /From.*Example checking.*To.*Example reserve/);
        assert.deepEqual(postingNames(), [
          "0100 · Example checking",
          "0101 · Example reserve",
        ]);
      }
    }
  }));

test("Assigned codes render without a preference provider or browser storage", () =>
  withDOM(async ({ render, window }) => {
    const ui = await loadComponent("unit/helpers/activity-account-codes.tsx");
    await render(
      React.createElement(
        React.Fragment,
        null,
        React.createElement(ui.EntryOverview, {
          data: entry,
          state,
          book,
          currencies,
        }),
        React.createElement(ui.EntryAccountingDetails, {
          data: entry,
          accounts,
          book,
          currencies,
        }),
      ),
    );
    assert.equal(category(), "0634 · Software");
    assert.deepEqual(postingNames(), [
      "0100 · Example checking",
      "0634 · Software",
    ]);
    assert.equal(window.localStorage.length, 0);
  }));

test("GL code is visible and directly editable without opening a transaction or changing its classification", () =>
  withDOM(async ({ render, window }) => {
    const ui = await loadComponent("unit/helpers/activity-account-codes.tsx");
    const previous = globalThis.fetch,
      writes = [],
      changes = [];
    const original = JSON.stringify(entry);
    const account = { ...accounts[2], gl_code: "" };
    const changed = (event) => changes.push(event.detail);
    window.addEventListener("vult-account-code-changed", changed);
    globalThis.fetch = async (url, options) => {
      if (String(url).endsWith("/session"))
        return new Response(JSON.stringify({ csrf: "synthetic-csrf" }));
      assert.match(String(url), /account-codes\?/);
      if (options?.method === "POST") {
        writes.push({ url: String(url), body: JSON.parse(options.body) });
        return new Response(
          JSON.stringify({
            account_reference: account.code,
            gl_code: "0634",
            revision: 6,
          }),
        );
      }
      return new Response(
        JSON.stringify({
          account_reference: account.code,
          gl_code: "",
          revision: 5,
          history: [],
        }),
      );
    };
    try {
      await render(
        React.createElement(ui.GLCodeField, {
          book: book.id,
          account,
          value: "Software",
        }),
      );
      assert.match(document.body.textContent, /Software/);
      assert.doesNotMatch(document.body.textContent, /No code/);
      await click(
        document.querySelector(
          'button[aria-label="Set GL code for Software"]',
        ),
      );
      await settle();
      await settle();
      const dialog = document.querySelector('[role="dialog"]');
      assert.ok(dialog, "the controlled editor must stay open after mounting");
      assert.match(dialog.textContent, /GL code/);
      const input = dialog.querySelector('input[name="gl_code"]');
      assert.ok(input && !input.disabled);
      assert.equal(writes.length, 0, "opening a row field never saves a code");
      await React.act(async () => {
        Object.getOwnPropertyDescriptor(
          window.HTMLInputElement.prototype,
          "value",
        ).set.call(input, "0634");
        input.dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      await React.act(async () =>
        dialog
          .querySelector("form")
          .dispatchEvent(
            new window.Event("submit", { bubbles: true, cancelable: true }),
          ),
      );
      await settle();
      assert.equal(writes.length, 1);
      assert.equal(writes[0].body.account_reference, account.code);
      assert.equal(writes[0].body.gl_code, "0634");
      assert.equal(writes[0].body.revision, 5);
      assert.equal(writes[0].body.book_id, book.id);
      assert.ok(writes[0].body.idempotency_key);
      assert.deepEqual(changes, [book.id]);
      assert.equal(
        JSON.stringify(entry),
        original,
        "the posted entry remains unchanged",
      );
      await render(
        React.createElement(ui.GLCodeField, {
          book: book.id,
          account: { ...account, gl_code: "0634" },
          value: "0634 · Software",
        }),
      );
      assert.match(
        document.querySelector("button").textContent,
        /0634 · Software/,
      );
      assert.doesNotMatch(
        document.querySelector("button").textContent,
        /No code/,
      );
    } finally {
      globalThis.fetch = previous;
      window.removeEventListener("vult-account-code-changed", changed);
    }
  }));
