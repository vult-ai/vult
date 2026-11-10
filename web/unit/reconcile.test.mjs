import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", currency: "USD", scale: 2, name: "Example Studio" };
const statement = {
  id: "example-statement", account_id: "checking", account_name: "Checking", kind: "cash", currency: "USD", scale: 2,
  document_id: "statement-document", period_start: "2026-03-01", period_end: "2026-03-31",
  opening_balance_minor: "10000", closing_balance_minor: "9500", evidence_state: "complete", row_count: 1, status: "reconciled",
};
function payload() {
  return { reconciliation: {
    statement,
    summary: {
      opening_balance_minor: "10000", closing_balance_minor: "9500", matched_minor: "0", adjusted_minor: "0", duplicate_minor: "-500",
      on_statement_not_in_book_minor: "0", on_statement_not_in_book_count: 0, outstanding_in_book_minor: "0", outstanding_in_book_count: 0,
      unresolved_lines: 0, duplicates: 1, pending_rows: 0, proposed_rows: 0, statement_arithmetic_difference_minor: "0", remaining_difference_minor: "0", fully_explained: true,
    },
    rows: [{ id: "repeated-row", row_number: 1, date: "2026-03-02", amount_minor: "-500", description: "Repeated bank fee", lifecycle: "settled", state: "duplicate", matched_minor: "0", remaining_minor: "0", match_ids: [], duplicate_of: "canonical-row" }],
    outstanding: [], matches: [],
  }, suggestions: [] };
}

test("reconciliation reads require an exact repeated-movement amount", async () => {
  const { readReconciliation } = await loadComponent("src/lib/statements.ts");
  const previous = globalThis.fetch;
  try {
    const value = payload();
    globalThis.fetch = async () => new Response(JSON.stringify(value), { status: 200 });
    assert.equal((await readReconciliation(book.id, statement.id)).reconciliation.summary.duplicate_minor, "-500");
    delete value.reconciliation.summary.duplicate_minor;
    await assert.rejects(readReconciliation(book.id, statement.id), /could not be confirmed/);
  } finally { globalThis.fetch = previous; }
});

test("repeated movements remain in the statement bridge without appearing as another matched allocation", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/?statement=${statement.id}`);
    const previous = globalThis.fetch;
    globalThis.fetch = async (url) => new Response(JSON.stringify(
      new URL(url, window.location.href).searchParams.has("statement_id") ? payload() : { statements: [statement] },
    ), { status: 200 });
    try {
      const { Reconcile } = await loadComponent("src/components/reconcile/reconcile.tsx");
      await render(React.createElement(Reconcile, { book, active: true, onInspect() {} }));
      const root = document.getElementById("root");
      assert.match(root.textContent, /matched, adjusted and repeated rows equals the closing/);
      const repeated = [...root.querySelectorAll(".reconcile-summary > div")].find((el) => el.querySelector("dt").textContent === "Repeated from another statement");
      assert.ok(repeated);
      assert.match(repeated.querySelector("dd").textContent, /5\.00/);
      const matched = [...root.querySelectorAll(".reconcile-summary > div")].find((el) => el.querySelector("dt").textContent === "Matched");
      assert.match(matched.querySelector("dd").textContent, /0\.00/);
      assert.doesNotMatch(root.textContent, /Not counted/);
    } finally { globalThis.fetch = previous; }
  });
});

test("unknown clearing allocation withholds the outstanding total without calling a known amount unavailable", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/?statement=${statement.id}`);
    const previous = globalThis.fetch;
    const value = payload();
    value.reconciliation.summary.unresolved_lines = 1;
    value.reconciliation.outstanding = [{ line_id: "timing-line", entry_id: "payment", date: "2026-03-12", description: "Payment across statement cutoffs", amount_minor: "-1000", unresolved: true }];
    globalThis.fetch = async (url) => new Response(JSON.stringify(
      new URL(url, window.location.href).searchParams.has("statement_id") ? value : { statements: [statement] },
    ), { status: 200 });
    try {
      const { Reconcile } = await loadComponent("src/components/reconcile/reconcile.tsx");
      await render(React.createElement(Reconcile, { book, active: true, onInspect() {} }));
      const root = document.getElementById("root");
      const outstanding = [...root.querySelectorAll(".reconcile-summary > div")].find((el) => el.querySelector("dt").textContent === "In the Book, not on a statement yet");
      assert.match(outstanding.querySelector("dd").textContent, /—.*1 unresolved/);
      assert.doesNotMatch(outstanding.querySelector("dd").textContent, /0\.00/);
      assert.match(root.textContent, /clearing allocation at this cutoff is unresolved/);
      assert.doesNotMatch(root.textContent, /no amount in USD/);
    } finally { globalThis.fetch = previous; }
  });
});

test("a selected statement reloads on activation and writes without fetching an unused list", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/?statement=${statement.id}`);
    const previous = globalThis.fetch;
    let details = 0, lists = 0, signal;
    globalThis.fetch = async (url, options) => {
      signal = options?.signal;
      if (new URL(url, window.location.href).searchParams.has("statement_id")) {
        details++;
        return new Response(JSON.stringify(payload()), { status: 200 });
      }
      lists++;
      return new Response(JSON.stringify({ statements: [statement] }), { status: 200 });
    };
    try {
      const { Reconcile } = await loadComponent("src/components/reconcile/reconcile.tsx");
      const props = { book, active: true, refreshKey: 0, onInspect() {} };
      await render(React.createElement(Reconcile, props));
      assert.equal(details, 1);
      assert.equal(lists, 0);
      await render(React.createElement(Reconcile, { ...props, active: false, refreshKey: 1 }));
      assert.equal(signal.aborted, true);
      assert.equal(details, 1);
      await render(React.createElement(Reconcile, { ...props, refreshKey: 1 }));
      assert.equal(details, 2);
      await render(React.createElement(Reconcile, { ...props, refreshKey: 2 }));
      assert.equal(details, 3);
      assert.equal(lists, 0);
      await React.act(async () => [...document.querySelectorAll("button")].find((button) => button.textContent === "All statements").click());
      assert.equal(lists, 1);
      assert.equal(details, 3);
    } finally { globalThis.fetch = previous; }
  });
});

test("a failed readback keeps the old comparison visible but blocks changes until a successful retry", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/?statement=${statement.id}`);
    const previous = globalThis.fetch;
    let details = 0, lists = 0, writes = 0, completeRetry;
    const matched = payload();
    matched.reconciliation.summary.duplicates = 0;
    matched.reconciliation.summary.duplicate_minor = "0";
    matched.reconciliation.summary.matched_minor = "-500";
    matched.reconciliation.rows[0] = { ...matched.reconciliation.rows[0], state: "matched", matched_minor: "-500", match_ids: ["example-match"] };
    delete matched.reconciliation.rows[0].duplicate_of;
    globalThis.fetch = async (url) => {
      const path = new URL(url, window.location.href);
      if (path.pathname === "/auth/session") return new Response(JSON.stringify({ csrf: "synthetic-csrf" }));
      if (path.pathname === "/api/v1/account-changes") {
        writes++;
        return new Response(JSON.stringify({}));
      }
      if (!path.searchParams.has("statement_id")) {
        lists++;
        return new Response(JSON.stringify({ statements: [statement] }));
      }
      details++;
      if (details === 1) return new Response(JSON.stringify(matched));
      if (details === 2) throw new TypeError("Failed to fetch");
      return new Promise((resolve) => { completeRetry = resolve; });
    };
    try {
      const { Reconcile } = await loadComponent("src/components/reconcile/reconcile.tsx");
      await render(React.createElement(Reconcile, { book, active: true, onInspect() {} }));
      const button = (label) => [...document.querySelectorAll("button")].find((el) => el.textContent === label);
      assert.equal(button("Mark reconciled").disabled, false);
      await React.act(async () => button("Undo match").click());
      assert.equal(writes, 1);
      assert.equal(details, 2);
      assert.equal(lists, 0);
      assert.match(document.querySelector(".reconcile-status").textContent, /Last loaded/);
      assert.match(document.getElementById("root").textContent, /These are the last loaded figures/);
      assert.ok(document.querySelector(".error-notice"));
      for (const label of ["Needs work", "Mark reconciled", "Undo match"]) assert.equal(button(label).disabled, true);
      await React.act(async () => button("Undo match").click());
      assert.equal(writes, 1);
      await React.act(async () => button("Try again").click());
      assert.equal(document.querySelector('section[aria-label="Checking statement"]').getAttribute("aria-busy"), "true");
      assert.equal(button("Needs work").disabled, true);
      const fresh = payload();
      fresh.reconciliation.statement = { ...statement, status: "needs_work" };
      await React.act(async () => completeRetry(new Response(JSON.stringify(fresh))));
      assert.equal(button("Needs work").disabled, false);
      assert.equal(button("Mark reconciled").disabled, false);
      assert.equal(document.querySelector(".error-notice"), null);
      assert.equal(document.querySelector(".reconcile-status").textContent, "Needs work");
      assert.doesNotMatch(document.getElementById("root").textContent, /These are the last loaded figures/);
    } finally { globalThis.fetch = previous; }
  });
});

test("a detail response aborted by leaving the page cannot replace its next comparison", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", `/?statement=${statement.id}`);
    const previous = globalThis.fetch;
    let finishOld, oldSignal, reads = 0;
    globalThis.fetch = async (_url, options) => {
      reads++;
      if (reads === 1) {
        oldSignal = options.signal;
        return new Promise((resolve) => { finishOld = resolve; });
      }
      const fresh = payload();
      fresh.reconciliation.statement = { ...statement, status: "needs_work" };
      return new Response(JSON.stringify(fresh));
    };
    try {
      const { Reconcile } = await loadComponent("src/components/reconcile/reconcile.tsx");
      const props = { book, active: true, onInspect() {} };
      await render(React.createElement(Reconcile, props));
      await render(React.createElement(Reconcile, { ...props, active: false }));
      assert.equal(oldSignal.aborted, true);
      await React.act(async () => finishOld(new Response(JSON.stringify(payload()))));
      assert.equal(document.querySelector(".reconcile-summary"), null);
      await render(React.createElement(Reconcile, props));
      assert.equal(reads, 2);
      assert.equal(document.querySelector(".reconcile-status").textContent, "Needs work");
    } finally { globalThis.fetch = previous; }
  });
});
