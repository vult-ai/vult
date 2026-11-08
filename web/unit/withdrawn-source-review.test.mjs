import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { fixture, trigger, click } from "./helpers/sources.mjs";

test("Activity keeps attachment indicators and ignores retained backend finding counts", async () => {
  await withDOM(async ({ render, unmount }) => {
    const { default: Transactions } = await loadComponent(
      "src/components/book/transactions-table.tsx",
    );
    const row = {
      id: "entry",
      description: "Existing payment",
      date: "2026-04-12",
      kind: "expense",
      counterparty: "Synthetic supplier",
      payer: "business",
      category: "5000",
      amount_minor: "3500",
      attachment_count: 2,
      review_finding_count: 12,
      fx: { original_currency: "USD", original_amount_minor: "3500" },
    };
    const retained = JSON.stringify(row);
    const old = globalThis.fetch;
    const reads = [];
    globalThis.fetch = async (url) => {
      reads.push(String(url));
      return new Response(JSON.stringify({ links: [], has_more: false }), {
        headers: { "Content-Type": "application/json" },
      });
    };
    try {
      await render(
        React.createElement(Transactions, {
          state: {
            transactions: [row],
            currencies: [],
            accounts: [],
            cash_accounts: [],
            account_balances: [],
          },
          book: {
            id: "11111111-1111-4111-8111-111111111111",
            currency: "USD",
            scale: 2,
          },
          detail: "",
          loading: false,
          resetKey: "withdrawn",
          emptyState: "Empty",
          onOpen: () => {},
        }),
      );
      const indicator = document.querySelector("#source-indicator-entry");
      assert.ok(indicator);
      assert.match(indicator.textContent, /2 attached sources/);
      assert.equal(
        document.querySelector('[id^="review-indicator-"],.source-findings'),
        null,
      );
      assert.doesNotMatch(
        document.body.textContent,
        /needs review|Agent alerts|Dismiss/i,
      );
      assert.equal(
        document
          .querySelector('button[aria-label="View Existing payment details"]')
          .getAttribute("aria-describedby"),
        "source-indicator-entry",
      );
      assert.ok(reads.every((url) => !url.includes("source-findings")));
      assert.equal(JSON.stringify(row), retained);
    } finally {
      await unmount();
      globalThis.fetch = old;
    }
  });
});
