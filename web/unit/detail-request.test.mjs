import assert from "node:assert/strict";
import test from "node:test";
import { loadApplication } from "./helpers/application.mjs";
import { React, withDOM } from "./helpers/dom.mjs";

test("a slower previous detail request cannot replace the currently selected record", async () => {
  await withDOM(async ({ render }) => {
    const { EntryDetail } = await loadApplication();
    const savedFetch = globalThis.fetch;
    const pending = new Map();
    globalThis.fetch = (url) =>
      new Promise((done) => {
        const parsed = new URL(url, "http://localhost");
        assert.equal(parsed.searchParams.get("book_id"), "book-a");
        pending.set(parsed.pathname.split("/").at(-1), done);
      });
    const record = (id, amount) => ({
      id,
      description: `Record ${id}`,
      amount_minor: amount,
      kind: "income",
      date: "2026-10-01",
      recorded_at: "2026-10-01T12:00:00Z",
      note: "",
      counterparty: "",
      fx: {
        original_amount_minor: amount,
        original_currency: "USD",
        paid_amount_minor: amount,
        paid_currency: "USD",
        selection: "same-currency",
        quotes: [],
      },
      lines: [],
      allocations: [],
    });
    const view = (id) =>
      React.createElement(EntryDetail, {
        id,
        book: { id: "book-a", currency: "USD", scale: 2 },
        currencies: [{ code: "USD", scale: 2 }],
        state: { accounts: [], cash_accounts: [], transactions: [] },
        onSaved() {},
        onReplace() {},
        onNavigate() {},
      });
    try {
      await render(view("a"));
      await render(view("b"));
      await React.act(async () =>
        pending.get("b")({ ok: true, json: async () => record("b", "200") }),
      );
      assert.equal(
        document.querySelector(".entry-overview h3").textContent,
        "Record b",
      );
      await React.act(async () =>
        pending.get("a")({ ok: true, json: async () => record("a", "100") }),
      );
      assert.equal(
        document.querySelector(".entry-overview h3").textContent,
        "Record b",
      );
    } finally {
      globalThis.fetch = savedFetch;
    }
  });
});
