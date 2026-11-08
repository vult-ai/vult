import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const book = {
  id: "book-a",
  name: "Synthetic Book",
  currency: "USD",
  scale: 2,
  start_date: "2026-01-01",
  accounting_timezone: "UTC",
  accounting_today: "2026-01-31",
};
const range = { from: "2026-01-01", to: "2026-01-31" };
const summary = (extra = {}) => ({
  book_id: book.id,
  currency: book.currency,
  scale: 2,
  period: range,
  through: "2026-01-31",
  revision: "a".repeat(64),
  cash_minor: "0",
  cash_accounts: [],
  money_in_minor: "12345",
  money_out_minor: "345",
  movement_count: 4,
  ...extra,
});
const response = (value) =>
  new Response(JSON.stringify(value), {
    headers: { "Content-Type": "application/json" },
  });
const { loadStatementCashMovement } = await loadComponent(
  "src/lib/statement-cash-movement.ts",
);
const { StatementsWorkspace } = await loadComponent(
  "src/components/statements/statements-workspace.tsx",
);

test("Cash movement displays exact canonical receipt/payment/net totals with one read and unchanged Activity action", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(
      {},
      "",
      "/statements?report=cash_movement&from=2026-01-01&to=2026-01-31",
    );
    const original = globalThis.fetch,
      reads = [],
      navigated = [];
    globalThis.fetch = async (url, options) => {
      const q = new URL(url, "http://localhost");
      reads.push({ q, options });
      assert.equal(q.pathname, "/api/v1/dashboard/cash-summary");
      assert.equal(q.searchParams.get("book_id"), book.id);
      assert.equal(q.searchParams.get("from"), range.from);
      assert.equal(q.searchParams.get("to"), range.to);
      return response(summary());
    };
    try {
      await render(
        React.createElement(StatementsWorkspace, {
          book,
          state: { book, source_import: { import_id: "retained-source" } },
          onShowTransactions: (value) => navigated.push(value),
          onInspect() {},
        }),
      );
      const section = document.querySelector(".statement-cash-movement");
      assert.ok(section);
      assert.deepEqual(
        [...section.querySelectorAll("dd")].map((n) => n.textContent),
        ["$123.45", "$3.45", "$120.00"],
      );
      assert.equal(reads.length, 1);
      assert.equal(reads[0].options.method || "GET", "GET");
      await React.act(async () =>
        [...section.querySelectorAll("button")]
          .find((n) => n.textContent.includes("Open period activity"))
          .click(),
      );
      assert.deepEqual(navigated, [range]);
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("cash position uncertainty does not discard known period movements; unknown movements never become zero", async () => {
  await withDOM(async () => {
    const original = globalThis.fetch;
    try {
      globalThis.fetch = async () =>
        response(
          summary({
            position_error: "Position unavailable",
            cash_minor: undefined,
          }),
        );
      const value = await loadStatementCashMovement(book, range);
      assert.equal(value.money_in_minor, "12345");
      assert.equal(value.revision, "a".repeat(64));
      for (const partial of [
        { movement_error: "Evidence unavailable", money_in_minor: undefined },
        { money_in_minor: "-1" },
        { movement_count: -1 },
        { movement_count: 1.5 },
        { book_id: "other-book" },
        { period: { ...range, to: "2026-01-30" } },
      ]) {
        globalThis.fetch = async () => response(summary(partial));
        await assert.rejects(loadStatementCashMovement(book, range));
      }
      globalThis.fetch = async () =>
        response(
          summary({
            money_in_minor: "0",
            money_out_minor: "0",
            movement_count: 0,
          }),
        );
      assert.equal(
        (await loadStatementCashMovement(book, range)).money_out_minor,
        "0",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("canonical source-integrity failures retain their verified critical treatment and requests remain cancellable", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(
      {},
      "",
      "/statements?report=cash_movement&from=2026-01-01&to=2026-01-31",
    );
    const original = globalThis.fetch,
      controller = new AbortController();
    let signal;
    globalThis.fetch = async (_url, options) => {
      signal = options.signal;
      return response(summary({ integrity_failure: true }));
    };
    try {
      await assert.rejects(
        loadStatementCashMovement(book, range, controller.signal),
        /source bytes could not be verified/,
      );
      assert.equal(signal, controller.signal);
      await render(
        React.createElement(StatementsWorkspace, {
          book,
          state: { book },
          onShowTransactions() {},
          onInspect() {},
        }),
      );
      assert.ok(
        document.querySelector('[data-severity="critical"]'),
        "the actual statement preserves the canonical validator's private critical authority",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("changing periods aborts the old cash read and a late old response cannot replace current totals", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(
      {},
      "",
      "/statements?report=cash_movement&from=2026-01-01&to=2026-01-31",
    );
    const original = globalThis.fetch;
    let finish,
      firstSignal,
      calls = 0;
    globalThis.fetch = async (_url, options) => {
      if (++calls === 1) {
        firstSignal = options.signal;
        return new Promise((resolve) => {
          finish = resolve;
        });
      }
      return response(
        summary({
          period: { from: "2026-01-15", to: "2026-01-20" },
          money_in_minor: "88800",
        }),
      );
    };
    try {
      await render(
        React.createElement(StatementsWorkspace, {
          book,
          state: { book },
          onShowTransactions() {},
          onInspect() {},
        }),
      );
      await React.act(async () => {
        window.history.replaceState(
          {},
          "",
          "/statements?report=cash_movement&from=2026-01-15&to=2026-01-20",
        );
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(firstSignal.aborted, true);
      assert.equal(
        document.querySelector(".statement-cash-movement dd").textContent,
        "$888.00",
      );
      await React.act(async () => finish(response(summary())));
      assert.equal(
        document.querySelector(".statement-cash-movement dd").textContent,
        "$888.00",
      );
      assert.equal(calls, 2);
    } finally {
      globalThis.fetch = original;
    }
  });
});
