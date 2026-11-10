import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const { V0Overview, BookScreens, responseFailure, verifiedReadFailure, emptyDashboardSnapshot } =
  await loadComponent("tests/fixtures/dashboard-errors.ts");
const book = {
  id: "11111111-1111-4111-8111-111111111111", name: "Example Book",
  start_date: "2026-01-01", currency: "USD", scale: 2,
};
const state = {
  book, accounts: [], account_balances: [], cash_accounts: [], transactions: [],
  currencies: [{ code: "USD", scale: 2, name: "US Dollar" }],
};
const props = { page: "dashboard", state, book, onNavigate() {}, onShowTransactions() {}, onInspect() {} };
const notices = () => [...document.querySelectorAll(".error-notice")];
const configureLocation = window => window.history.replaceState(null, "", "/dashboard?from=2026-10-01&to=2026-10-31");

test("an unplugged dashboard keeps its Book context without loading figures or offering retry", async () => {
  await withDOM(async ({ render, window }) => {
    configureLocation(window);
    const original = globalThis.fetch;
    globalThis.fetch = () => assert.fail("unplugged dashboard must not request figure data");
    try {
      await render(React.createElement(BookScreens, {
        ...props, state: { ...state, capabilities: { dashboard: false } },
        onPosted() {}, onOpening() {}, onAccount() {}, onSettings() {}, onStart() {},
      }));
      const unavailable = document.querySelector('.vult-state[data-kind="failure"][data-scope="page"]');
      assert.ok(unavailable, "an unavailable dashboard replaces the screen with the page failure state");
      assert.equal(unavailable.querySelector(".vult-state-title").textContent, "Dashboard isn’t available yet");
      assert.equal(unavailable.querySelector("button"), null);
      assert.equal(notices().length, 0);
      assert.doesNotMatch(document.body.textContent, /Could not open|couldn’t load|couldn’t finish/);
    } finally { globalThis.fetch = original; }
  });
});

const range = { from: "2026-10-01", to: "2026-10-31" };
const side = { total: "0", values: [], categories: [] };
const ready = {
  ...state,
  dashboard_snapshot: {
    ...emptyDashboardSnapshot(book, range),
    version: "state",
    performance: { period: range, grain: "month", buckets: [], income: side, expenses: side, domain: { min: "0", max: "0" }, unavailable: "book_timezone_not_set" },
  },
};

test("a failed Dashboard period retries one snapshot and retains the confirmed figures", async () => {
  await withDOM(async ({ render, window }) => {
    configureLocation(window);
    const original = globalThis.fetch, reads = [];
    globalThis.fetch = async (url, options) => {
      assert.equal(options?.method || "GET", "GET");
      const request = new URL(url, window.location.href);
      reads.push(request.pathname);
      if (reads.length === 1) {
        const next = { from: request.searchParams.get("dashboard_from"), to: request.searchParams.get("dashboard_to") };
        const { trends, ...incomplete } = emptyDashboardSnapshot(book, next);
        return new Response(JSON.stringify({ book, state_revision: "next", dashboard_snapshot: incomplete }));
      }
      return new Response(JSON.stringify({ error: "Unavailable" }), { status: 503 });
    };
    try {
      await render(React.createElement(V0Overview, { ...props, state: ready }));
      assert.equal(reads.length, 0);
      await React.act(async () => {
        window.history.pushState(null, "", "/dashboard?from=2026-09-01&to=2026-09-30");
        window.dispatchEvent(new window.PopStateEvent("popstate"));
      });
      assert.equal(notices().length, 1);
      assert.match(notices()[0].textContent, /The Dashboard snapshot is incomplete\./);
      await React.act(async () => notices()[0].querySelector("button").click());
      assert.deepEqual(reads, ["/api/v1/state", "/api/v1/state"]);
      assert.equal(notices().length, 1);
      assert.match(notices()[0].textContent, /We couldn’t refresh the dashboard\./);
    } finally { globalThis.fetch = original; }
  });
});

test("a verified workspace failure retains priority over routine dashboard failures", async () => {
  await withDOM(async ({ render, window }) => {
    configureLocation(window);
    const original = globalThis.fetch;
    globalThis.fetch = async () => new Response("{}", { status: 503 });
    try {
      await render(React.createElement(V0Overview, {
        ...props, state: ready,
        workspaceRead: { failure: verifiedReadFailure("book-scope", "Different Book"), pending: false, retry: async () => {} },
      }));
      assert.equal(notices().length, 1);
      assert.equal(notices()[0].dataset.severity, "critical");
      assert.match(notices()[0].textContent, /We couldn’t verify these records\./);
    } finally { globalThis.fetch = original; }
  });
});

test("a metadata-only workspace still exposes its dashboard error and retry", async () => {
  await withDOM(async ({ render, window }) => {
    configureLocation(window);
    let retries = 0;
    await render(React.createElement(BookScreens, {
      ...props, state: { ...state, bootstrap: true, account_balances: undefined },
      onPosted() {}, onOpening() {}, onAccount() {}, onSettings() {}, onStart() {},
      workspaceRead: { failure: responseFailure(503, {}), pending: false, retry: async () => { retries++; } },
    }));
    const failure = () => document.querySelector('.vult-state[data-kind="failure"][data-scope="page"]');
    assert.equal(failure().querySelector(".vult-state-title").textContent, "Couldn’t load the dashboard");
    await React.act(async () => failure().querySelector("button").click());
    assert.equal(retries, 1);
    assert.ok(failure());
  });
});
