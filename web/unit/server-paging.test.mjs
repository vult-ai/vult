import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic paged",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  profile: "business",
};
const base = {
  book,
  currencies: [{ code: "USD", scale: 2 }],
  summary: {},
  accounts: [],
  cash_accounts: [],
  payables: [],
  fx_gain_minor: "0",
  fx_loss_minor: "0",
};
const records = Array.from({ length: 61 }, (_, i) => ({
  id: `entry-${i}`,
  attachment_count: 0,
  book_id: book.id,
  kind: i % 2 ? "expense" : "income",
  date: "2026-01-01",
  description: `Synthetic ${i}`,
  amount_minor: "1",
  payer: "business",
  fx: { original_currency: "USD", original_amount_minor: "1" },
}));
const rows = () =>
  [...document.querySelectorAll("tr[data-record-id]")].map(
    (r) => r.dataset.recordId,
  );
const click = async (selector) =>
  React.act(async () => document.querySelector(selector).click());

test("transaction pages fetch bounded rows, reset on kind changes, and retain complete filter counts", async () => {
  await withDOM(async ({ render }) => {
    const { App, Theme } = await loadApplication();
    const saved = globalThis.fetch;
    const calls = [];
    globalThis.fetch = async (url) => {
      const q = new URL(url, "http://localhost");
      let data;
      if (q.pathname.endsWith("/session"))
        data = { mode: "local", csrf: "fixture" };
      else if (q.pathname.endsWith("/books"))
        data = { books: [book], currencies: base.currencies };
      else if (q.pathname === "/api/v1/state") {
        calls.push(q.searchParams);
        assert.equal(q.searchParams.get("view"), "transactions");
        const offset = Number(q.searchParams.get("offset")),
          limit = Number(q.searchParams.get("limit"));
        const matched = records.filter(
          (r) =>
            !q.searchParams.get("kind") ||
            r.kind === q.searchParams.get("kind"),
        );
        data = {
          ...base,
          read_view: "transactions",
          transaction_counts: { "": 61, expense: 30, income: 31 },
          transactions: matched.slice(offset, offset + limit),
          coverage: {
            has_more: offset + limit < matched.length,
            next_offset: offset + limit,
            limit,
            complete: false,
          },
        };
      }
      return { ok: true, json: async () => data };
    };
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App),
        ),
      );
      assert.equal(rows().length, 25);
      assert.equal(rows()[0], "entry-0");
      const table = document.querySelector("table");
      await click('[aria-label="Transactions pages"] [aria-label="Next page"]');
      assert.equal(rows().length, 25);
      assert.equal(rows()[0], "entry-25");
      assert.equal(calls.at(-1).get("offset"), "25");
      await React.act(async () =>
        [
          ...document.querySelectorAll(
            '[aria-label="Transaction type filters, matching records"] button',
          ),
        ]
          .find((x) => x.textContent.startsWith("Expenses"))
          .click(),
      );
      assert.equal(calls.at(-1).get("offset"), "0");
      assert.equal(calls.at(-1).get("kind"), "expense");
      assert.equal(rows()[0], "entry-1");
      assert.match(
        document.querySelector(
          '[aria-label="Transaction type filters, matching records"]',
        ).textContent,
        /All61Income31Expenses30/,
      );
      await click('[aria-label="Transactions pages"] [aria-label="Next page"]');
      assert.equal(rows().length, 5);
      assert.equal(rows()[0], "entry-51");
      assert.equal(document.querySelector("table"), table);
      assert.ok(
        document.querySelector(
          '[aria-label="Transactions pages"] [aria-label="Next page"]',
        ).disabled,
      );
      await React.act(async () =>
        [
          ...document.querySelectorAll(
            '[aria-label="Transaction type filters, matching records"] button',
          ),
        ]
          .find((x) => x.textContent.startsWith("All"))
          .click(),
      );
      assert.equal(
        calls.at(-1).get("offset"),
        "0",
        "returning to an earlier filter must reset its page too",
      );
      assert.equal(rows()[0], "entry-0");
    } finally {
      globalThis.fetch = saved;
    }
  });
});

// The current Sources workspace owns complete-index filtering, sorting and
// document inspection; documents-preview.test.mjs covers that UI contract.
// Backend bounded document paging remains covered by the PostgreSQL HTTP suite.
