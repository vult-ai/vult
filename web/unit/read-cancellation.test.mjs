import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";
import { activityResponse } from "./helpers/activity.mjs";

const book = {
  id: "11111111-1111-4111-8111-111111111111",
  profile: "business",
  name: "Synthetic cancellation",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_today: "2026-10-25",
  accounting_timezone: "UTC",
};
const state = {
  book,
  currencies: [{ code: "USD", scale: 2 }],
  summary: {},
  cash_accounts: [],
  payables: [],
  accounts: [],
  transactions: [],
  fx_gain_minor: "0",
  fx_loss_minor: "0",
  read_view: "transactions",
  transaction_counts: { "": 0 },
  coverage: { next_offset: 0, limit: 25, has_more: false, complete: true },
};
const wait = async () =>
  React.act(async () => new Promise((r) => setTimeout(r, 210)));
async function fill(window, input, value) {
  await React.act(async () => {
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(input, value);
    input.dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}

test("superseded Book searches and unmounted views abort their actual requests", async () => {
  await withDOM(async ({ render, window, unmount }) => {
    const { App, Theme } = await loadApplication();
    const saved = globalThis.fetch;
    const reads = [];
    // Only Activity's reads are counted; other screens read on their own.
    globalThis.fetch = (url, init) =>
      new Promise((resolve) => {
        if (/\/state\?/.test(String(url)) && /[?&]view=transactions/.test(String(url)))
          reads.push({ url, signal: init?.signal, resolve });
      });
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            initial: {
              result: {
                catalog: { books: [book], currencies: state.currencies },
                state,
              },
            },
          }),
        ),
      );
      // Activity's search comes from the finder or the address (?q=); Back and
      // Forward change it the same way.
      const visit = (q) => React.act(async () => {
        window.history.pushState(null, "", `?q=${q}`);
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      await visit("first");
      await wait();
      assert.equal(reads.length, 1);
      assert.equal(reads[0].signal.aborted, false);
      await visit("second");
      assert.equal(reads[0].signal.aborted, true);
      await wait();
      assert.equal(reads.length, 2);
      assert.match(reads[1].url, /[?&]q=second/);
      await unmount();
      assert.equal(reads[1].signal.aborted, true);
    } finally {
      globalThis.fetch = saved;
    }
  });
});

test("Accounts does not refetch activity when only the visible transaction page changes", async () => {
  await withDOM(async ({ render }) => {
    const { Accounts } = await loadComponent(
      "src/components/workspaces/accounts.tsx",
    );
    const saved = globalThis.fetch;
    const calls = [];
    globalThis.fetch = async (url) => {
      calls.push(String(url));
      assert.match(String(url), /account-activity/);
      return activityResponse(book);
    };
    const display = {
      ...state,
      account_balances: [
        {
          code: "1000",
          name: "Cash",
          kind: "asset",
          usage: "cash",
          currency: "USD",
          scale: 2,
          balance_minor: "0",
          native_minor: "0",
          posting_count: 0,
        },
      ],
    };
    const view = (revision, rows, postings = 0) =>
      React.createElement(Accounts, {
        display: {
          ...display,
          transactions: rows,
          account_balances: display.account_balances.map((a) => ({
            ...a,
            posting_count: postings,
          })),
        },
        displayBook: book,
        refreshKey: revision,
        onAccount() {},
      });
    try {
      await render(view(0, []));
      assert.equal(calls.length, 1);
      await render(view(0, [{ id: "different-visible-page" }]));
      assert.equal(calls.length, 1);
      await render(view(0, [], 2));
      assert.equal(
        calls.length,
        2,
        "an agent posting outside this browser must refresh activity too",
      );
      await render(view(1, [], 2));
      assert.equal(
        calls.length,
        3,
        "the existing explicit save invalidation remains supported",
      );
    } finally {
      globalThis.fetch = saved;
    }
  });
});
