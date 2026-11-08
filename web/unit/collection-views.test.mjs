import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";
import { openFilters } from "./helpers/book-controls.mjs";

test("counted views share the type filter, retain the table, and use complete counts with bounded date-matched reads", async (t) => {
  t.mock.timers.enable({ apis: ["Date"], now: new Date("2026-10-24T12:00:00Z") });
  await withDOM(async ({ render, window }) => {
    const { App, Theme } = await loadApplication();
    const book = {
      id: "11111111-1111-4111-8111-111111111111",
      name: "Synthetic",
      founder: "Alex",
      currency: "USD",
      scale: 2,
      start_date: "2026-01-01",
    };
    const currencies = [
      { code: "USD", scale: 2 },
      { code: "EUR", scale: 2 },
    ];
    const records = [
      ["expense-a", "expense", "2026-10-23"],
      ["expense-b", "expense", "2026-10-23"],
      ["income", "income", "2026-10-24"],
      ["loan", "loan", "2026-10-22"],
      ["transfer", "transfer", "2026-10-21"],
    ].map(([id, kind, date]) =>
      Object.freeze({
        attachment_count: 0,
        id,
        kind,
        date,
        book_id: book.id,
        description: id,
        counterparty: "",
        category: "",
        payer: "business",
        amount_minor: "11000",
        fx: Object.freeze({
          original_currency: "EUR",
          original_amount_minor: "10000",
        }),
      }),
    );
    const base = {
      book,
      currencies,
      summary: {},
      accounts: [],
      cash_accounts: [],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
    };
    const previousFetch = globalThis.fetch;
    const reads = [];
    globalThis.fetch = async (url) => {
      if (String(url).endsWith("/books"))
        return { ok: true, json: async () => ({ books: [book], currencies }) };
      const endpoint = new URL(url, "http://localhost");
      if (endpoint.pathname !== "/api/v1/state") return { ok: false, status: 404 };
      const query = endpoint.searchParams;
      reads.push(query);
      assert.equal(query.get("book_id"), book.id);
      assert.equal(query.get("view"), "transactions");
      assert.equal(query.get("limit"), "25");
      const matched = records.filter(
        (row) => !query.get("from") || row.date >= query.get("from"),
      );
      const counts = { "": matched.length };
      for (const row of matched) counts[row.kind] = (counts[row.kind] || 0) + 1;
      return {
        ok: true,
        json: async () => ({
          ...base,
          read_view: "transactions",
          transaction_counts: counts,
          coverage: {
            has_more: false,
            next_offset: 5,
            limit: 25,
            complete: true,
          },
          transactions: matched.filter(
            (row) => !query.get("kind") || row.kind === query.get("kind"),
          ),
        }),
      };
    };
    const group = () =>
      document.querySelector(
        '[aria-label="Transaction type filters, matching records"]',
      );
    const button = (label) =>
      [...group().querySelectorAll("button")].find((node) =>
        node.textContent.startsWith(label),
      );
    const rowIds = () =>
      [...document.querySelectorAll("tr[data-record-id]")].map((row) =>
        row.getAttribute("data-record-id"),
      );
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App),
        ),
      );
      const table = document.querySelector("table");
      assert.equal(reads.length, 1);
      assert.deepEqual(
        rowIds(),
        records.map((row) => row.id),
      );
      assert.deepEqual(
        [...group().querySelectorAll("button")].map((node) => node.textContent),
        ["All5", "Income1", "Expenses2"],
      );
      await React.act(async () => button("Expenses").click());
      assert.deepEqual(rowIds(), ["expense-a", "expense-b"]);
      assert.equal(button("Expenses").getAttribute("aria-pressed"), "true");
      assert.equal(
        button("Income").textContent,
        "Income1",
        "inactive counts cannot collapse to zero when another type is selected",
      );
      await openFilters();
      const select = document.querySelector(
        '[aria-label="Filter transaction type"]',
      );
      assert.equal(select.value, "expense");
      await React.act(async () => {
        select.value = "loan";
        select.dispatchEvent(new window.Event("change", { bubbles: true }));
      });
      assert.deepEqual(rowIds(), ["loan"]);
      assert.equal(
        [...group().querySelectorAll('button[aria-pressed="true"]')].length,
        1,
        "a deliberately selected advanced type has a visible active filter",
      );
      await React.act(async () =>
        document
          .querySelector('[aria-label="Close transaction filters"]')
          .click(),
      );
      await React.act(async () => button("All").click());
      assert.deepEqual(
        rowIds(),
        records.map((row) => row.id),
        "All retains funding and transfers as well as income and expenses",
      );
      assert.equal(
        reads.length,
        4,
        "each selected kind requests bounded rows with a complete type census",
      );
      assert.equal(document.querySelector("table"), table);

      await React.act(async () => button("Expenses").click());
      await React.act(async () => document.querySelector(".dashboard-period").click());
      const today = [...document.querySelectorAll(".period-preset")].find(button => button.textContent.trim() === "Today");
      assert.ok(today);
      await React.act(async () => today.click());
      assert.equal(reads.length, 6);
      assert.equal(reads[5].get("from"), "2026-10-24");
      assert.deepEqual(rowIds(), []);
      assert.deepEqual(
        [...group().querySelectorAll("button")].map((node) => node.textContent),
        ["All1", "Income1", "Expenses0"],
      );
      await React.act(async () => button("Income").click());
      assert.deepEqual(rowIds(), ["income"]);
      assert.equal(reads.length, 7);
      assert.equal(document.querySelector("table"), table);
      assert.match(table.textContent, /\$110\.00€100\.00/);
      assert.equal(table.querySelector("time").dateTime, "2026-10-24");
      assert.equal(table.querySelector("time .sr-only").textContent, "October 24, 2026");
    } finally {
      globalThis.fetch = previousFetch;
    }
  });
});
