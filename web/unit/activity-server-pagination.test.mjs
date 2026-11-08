import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

test("Activity jumps across server pages without repaging loaded rows or losing collection counts", async () => {
  await withDOM(async ({ render, window }) => {
    const { App, Theme } = await loadApplication();
    const book = {
      id: "11111111-1111-4111-8111-111111111111",
      profile: "business", name: "Synthetic paging", currency: "USD", scale: 2,
      start_date: "2026-01-01", accounting_today: "2026-10-28",
      accounting_timezone: "UTC",
    };
    const currencies = [{ code: "USD", scale: 2 }];
    const records = Array.from({ length: 549 }, (_, index) => ({
      id: `synthetic-${index}`, attachment_count: 0, kind: "expense",
      date: "2026-10-01", book_id: book.id, description: `Payment ${index}`,
      counterparty: `Supplier ${index}`, payer: "business", category: "",
      amount_minor: "1000",
      fx: { original_currency: "USD", original_amount_minor: "1000" },
    }));
    const page = (offset) => ({
      book, currencies, summary: {}, accounts: [], cash_accounts: [], payables: [],
      transactions: records.slice(offset, offset + 25),
      fx_gain_minor: "0", fx_loss_minor: "0", read_view: "transactions",
      transaction_counts: { "": 549, expense: 549 },
      coverage: {
        limit: 25, next_offset: Math.min(offset + 25, records.length),
        has_more: offset + 25 < records.length, complete: offset + 25 >= records.length,
      },
    });
    const prior = globalThis.fetch;
    const reads = [];
    globalThis.fetch = async (url) => {
      const query = new URL(String(url), "http://localhost");
      assert.equal(query.pathname, "/api/v1/state");
      assert.equal(query.searchParams.get("book_id"), book.id);
      assert.equal(query.searchParams.get("view"), "transactions");
      assert.equal(query.searchParams.get("limit"), "25");
      reads.push(query.searchParams);
      return { ok: true, json: async () => page(Number(query.searchParams.get("offset"))) };
    };
    const rowIds = () => [...document.querySelectorAll("tr[data-record-id]")]
      .map((row) => row.getAttribute("data-record-id"));
    const jump = () => document.querySelector('input[aria-label="Go to page"]');
    try {
      await render(React.createElement(Theme, { initialTheme: "light", initialPreference: "light" },
        React.createElement(App, { initial: { result: { catalog: { books: [book], currencies }, state: page(0) } } })));
      const table = document.querySelector("table");
      const scroll = document.querySelector('[data-vult-view-scroll$=":body"]');
      assert.equal(rowIds().length, 25);
      assert.equal(jump().value, "1");
      assert.match(jump().title, /22/);
      await React.act(async () => table.querySelector('tbody input[type="checkbox"]').click());
      assert.equal(table.querySelector('tbody input[type="checkbox"]').checked, true);
      scroll.scrollTop = 360;
      await React.act(async () => {
        Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")
          .set.call(jump(), "22");
        jump().dispatchEvent(new window.Event("input", { bubbles: true }));
      });
      assert.equal(reads.length, 0, "typing multi-digit page numbers must not navigate early");
      await React.act(async () => jump().dispatchEvent(
        new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
      assert.equal(reads.at(-1).get("offset"), "525");
      assert.equal(document.querySelector("table"), table, "the collection frame remains mounted");
      assert.deepEqual(rowIds(), records.slice(525).map((row) => row.id));
      assert.equal(jump().value, "22");
      assert.equal(scroll.scrollTop, 0, "a new page starts at its first record");
      assert.equal(table.querySelectorAll('tbody input:checked').length, 0);
      const next = document.querySelector('[aria-label="Next page"]');
      assert.equal(next.disabled, true);
      const filters = document.querySelector('[aria-label="Transaction type filters, matching records"]');
      assert.match(filters.textContent, /All549/);
      await React.act(async () => [...filters.querySelectorAll("button")]
        .find((button) => button.textContent.startsWith("Expenses")).click());
      assert.equal(reads.at(-1).get("offset"), "0", "changing a filter resets the server page");
      assert.equal(reads.at(-1).get("kind"), "expense");
      assert.equal(jump().value, "1");
      assert.deepEqual(rowIds(), records.slice(0, 25).map((row) => row.id));
    } finally {
      globalThis.fetch = prior;
    }
  });
});
