import assert from "node:assert/strict";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

test("workspace navigation retains the real table without prototype chrome", async () => {
  await withDOM(async ({ render, window }) => {
    window.history.replaceState(null, "", "/sample-company/activity");
    const { App, Theme } = await loadApplication();
    const { emptyDashboardState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
    const book = {
      id: "real-book",
      name: "Real Company",
      address: { book_id: "real-book", code: "K7FM", slug: "sample-company", revision: 1, aliases: ["k7fm", "sample-company"] },
      founder: "Owner",
      currency: "USD",
      scale: 2,
      start_date: "2026-01-01",
    };
    const currencies = [{ code: "USD", name: "US Dollar", scale: 2 }];
    const state = {
      book,
      currencies,
      transactions: [],
      accounts: [],
      cash_accounts: [
        {
          code: "1000",
          name: "Business cash",
          currency: "USD",
          scale: 2,
          native_minor: "250000",
          carrying_minor: "250000",
        },
      ],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      dashboard: {
        book_id: book.id,
        version: "fixture",
        period: { from: "2026-10-01", to: "2026-10-25" },
        cash_minor: "250000",
        profit_minor: "0",
        cash_flow_minor: "0",
        cash_effects_minor: "0",
        transaction_count: 0,
        last_date: "",
        interval: "day",
        series: [],
        attention: [],
      },
      summary: {
        cash_minor: "250000",
        income_minor: "0",
        expenses_minor: "0",
        owed_minor: "0",
        reimbursements_minor: "0",
        loans_minor: "0",
      },
    };
    const requests = [],
      original = globalThis.fetch;
    globalThis.fetch = async (...args) => {
      if (
        String(args[0]).endsWith("/legal-identity-catalog") ||
        String(args[0]).includes("/book-identity?") ||
        String(args[0]).includes("state?kind=opening")
      )
        throw new Error("Synthetic Settings read unavailable");
      if (args[0] === "/auth/session")
        return { ok: true, json: async () => ({ mode: "local" }) };
      requests.push(args);
      const url = new URL(args[0], window.location.origin);
      if (url.pathname === "/api/v1/state") {
        const read_view = url.searchParams.get("view");
        const data = read_view === "dashboard" ? emptyDashboardState(book, url.searchParams) : {};
        return { ok: true, json: async () => ({ ...state, ...data, read_view, account_balances: [] }) };
      }
      throw new Error("No other resource in this fixture");
    };
    const click = async (element) => React.act(async () => element.click());
    const button = (label) =>
      [...document.querySelectorAll("button")].find(
        (node) =>
          node.textContent.trim() === label && !node.closest("[hidden]"),
      );
    const navigate = (page) =>
      click(
        document.querySelector(
          `nav[aria-label="Book navigation"] a[href="/sample-company${page === "dashboard" ? "" : "/" + (page === "transactions" ? "activity" : page)}"]`,
        ),
      );
    function Table() {
      return React.createElement(
        "div",
        null,
        React.createElement("input", {
          "aria-label": "Retained table draft",
          defaultValue: "Original",
        }),
        React.createElement("table", { "data-testid": "retained-table" }),
      );
    }
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            initial: {
              result: {
                catalog: {
                  books: [book],
                  addresses: [book.address],
                  currencies,
                  prototype_import_enabled: true,
                },
                state,
              },
            },
            Table,
          }),
        ),
      );
      const shell = document.querySelector(".vult-app-shell"),
        main = document.querySelector("main"),
        table = document.querySelector('[data-testid="retained-table"]'),
        draft = document.querySelector('[aria-label="Retained table draft"]');
      draft.value = "Keep this draft";
      await navigate("dashboard");
      assert.equal(document.querySelector(".vult-app-shell") === shell, true);
      assert.equal(document.querySelector("main") === main, true);
      const dashboard=document.querySelector('[data-testid="v0-book-overview"]');
      assert.ok(dashboard,"current overview frame is mounted");
      assert.equal(document.querySelector(".workspace-mode-band"), null);
      assert.doesNotMatch(
        shell.textContent,
        /Stored locally|Your recorded data|Northstar/,
      );
      await navigate("transactions");
      await navigate("dashboard");
      assert.ok(document.querySelector('[data-testid="v0-book-overview"]'));
      await navigate("accounts");
      assert.equal(document.querySelector("main").dataset.workspace, "accounts");
      await navigate("transactions");
      assert.equal(
        document.querySelector('[data-testid="retained-table"]') === table,
        true,
      );
      await click(document.querySelector('button[aria-label="Settings"]'));
      assert.equal(document.querySelector("h1").textContent, "Settings");
      assert.equal(button("Edit details"), undefined);
      assert.equal(
        document.querySelector('input[aria-label="Book name"]').value,
        book.name,
      );
      assert.ok(
        document.querySelector(".vult-settings-section-actions button"),
      );
      assert.equal(document.querySelector('[role="dialog"]'), null);
      assert.ok(![...document.querySelectorAll("button")].some((b) => /^Import from /.test(b.textContent.trim())));
      assert.equal(button("Preview onboarding"), undefined);
      await navigate("transactions");
      assert.equal(
        document.querySelector('[data-testid="retained-table"]') === table,
        true,
      );
      assert.equal(draft.value, "Keep this draft");
      assert.equal(
        requests.filter(([,init]) => (init?.method || "GET").toUpperCase() !== "GET").length,
        0,
        "presentation navigation must preserve the current native book without API writes",
      );
      assert.equal(book.name, "Real Company");
    } finally {
      globalThis.fetch = original;
    }
  });
});
