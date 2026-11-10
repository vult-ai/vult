import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

test("changing a filter invalidates the previous response during the debounce window", async () => {
  await withDOM(async ({ render, window }) => {
    const { App, Theme } = await loadApplication();
    const savedFetch = globalThis.fetch;
    const pending = new Map();
    globalThis.fetch = (url) => {
      if (String(url).endsWith("/books"))
        return Promise.resolve({
          ok: true,
          json: async () => ({
            books: [state("Current Book").book],
            currencies: state("Current Book").currencies,
          }),
        });
      const endpoint = new URL(url, "http://localhost");
      if (endpoint.pathname !== "/api/v1/state") return Promise.resolve({ ok: false, status: 404 });
      return new Promise((done) => {
        const query = endpoint.searchParams;
        const search = query.get("q");
        pending.set(
          search ?? "",
          done,
        );
      });
    };
    const state = (name) => ({
      book: {
        id: "book",
        name,
        founder: "Founder",
        currency: "USD",
        scale: 2,
        start_date: "2026-01-01",
      },
      currencies: [{ code: "USD", name: "US Dollar", scale: 2 }],
      summary: Object.fromEntries(
        ["cash", "reimbursements", "loans", "owed", "income", "expenses"].map(
          (key) => [key + "_minor", "0"],
        ),
      ),
      cash_accounts: [],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      transactions: [],
      accounts: [],
    });
    const settle = async (key, name) =>
      React.act(async () =>
        pending.get(key)({ ok: true, json: async () => state(name) }),
      );
    // The search debounce runs on a clock this test controls, so a busy
    // machine can't fire it early or late.
    const due = new Map();
    let next = 0;
    const realSet = globalThis.setTimeout, realClear = globalThis.clearTimeout;
    globalThis.setTimeout = (fn, ms, ...args) => {
      if (ms !== 180) return realSet(fn, ms, ...args);
      due.set(++next, () => fn(...args));
      return next;
    };
    globalThis.clearTimeout = (id) => (due.has(id) ? due.delete(id) : realClear(id));
    const debounce = async () =>
      React.act(async () => {
        const fire = [...due.values()];
        due.clear();
        for (const run of fire) run();
      });
    const search = (query) => React.act(async () => {
      window.history.pushState(null, "", `${window.location.pathname}?q=${encodeURIComponent(query)}`);
      window.dispatchEvent(new window.PopStateEvent("popstate"));
    });
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App),
        ),
      );
      await debounce();
      await settle("", "Current Book");
      const main = document.querySelector("main");
      const navigation = document.querySelector(
        'nav[aria-label="Book navigation"]',
      );
      const links = [...navigation.querySelectorAll("a")];
      assert.deepEqual(
        links.map((node) => node.textContent),
        ["Dashboard", "Approvals", "Activity", "Accounts", "Reconcile", "Statements", "Sources"],
      );
      assert.equal(
        links
          .find((link) => link.textContent === "Activity")
          .getAttribute("aria-current"),
        "page",
      );
      const table = document.querySelector("table");
      await search("first query");
      await debounce();
      assert.ok(pending.has("first query"));
      await search("new query");
      assert.equal(pending.has("new query"), false, "the search keeps its debounce");
      await settle("first query", "STALE DURING SEARCH DEBOUNCE");
      assert.equal(document.querySelector(".transactions-section").dataset.bookName, "Current Book");
      await debounce();
      assert.ok(pending.has("new query"));
      await settle("new query", "Current search view");
      assert.equal(document.querySelector(".transactions-section").dataset.bookName, "Current search view");
      assert.equal(document.querySelector("main"), main);
      assert.equal(document.querySelector("table"), table);
    } finally {
      globalThis.fetch = savedFetch;
      globalThis.setTimeout = realSet;
      globalThis.clearTimeout = realClear;
    }
  });
});
