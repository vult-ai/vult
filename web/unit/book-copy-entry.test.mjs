import assert from "node:assert/strict";
import test from "node:test";
import {React,withDOM} from "./helpers/dom.mjs";
import {loadApplication} from "./helpers/application.mjs";
const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic layout book",
  founder: "Synthetic owner",
  profile: "business",
  currency: "USD",
  scale: 2,
  start_date: "2024-12-13",
  fiscal_year_start_month: 4,
  fiscal_year_start_day: 6,
  accounting_timezone: "Europe/Warsaw",
};
const section = name => [...document.querySelectorAll(".vult-settings-section")].find(n=>n.querySelector("h2")?.textContent===name);
const button = name => [...document.querySelectorAll("button")].find(n=>n.textContent.trim()===name);
const click = async node=>{assert.ok(node);await React.act(async()=>node.click())};
const selectTab = async name => click([...document.querySelectorAll('[role="tab"]')].find(n=>n.textContent.trim()===name));
test("the real Settings route retains the Book name and scoped download controls", async () => {
  await withDOM(async ({ render, window }) => {
    const { App, Theme: AppTheme } = await loadApplication({
      workspace: "settings",
    });
    const oldFetch = globalThis.fetch;
    let current = { ...book };
    const changes = [];
    const currencies = [{ code: "USD", name: "US Dollar", scale: 2 }];
    const state = () => ({
      book: current,
      currencies,
      transactions: [],
      accounts: [],
      cash_accounts: [],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      summary: {
        cash_minor: "0",
        income_minor: "0",
        expenses_minor: "0",
        owed_minor: "0",
        reimbursements_minor: "0",
        loans_minor: "0",
      },
    });
    const result = (data) => ({ ok: true, json: async () => data });
    globalThis.fetch = async (url, init = {}) => {
      const parsed = new URL(url, "http://localhost");
      if (parsed.pathname === "/auth/session")
        return result({ mode: "local", csrf: "synthetic-csrf" });
      if (parsed.pathname === "/api/v1/books")
        return result({ books: [current], currencies });
      if (parsed.pathname === "/api/v1/state") {
        assert.equal(parsed.searchParams.get("book_id"), book.id);
        return result(state());
      }
      if (parsed.pathname === "/api/v1/legal-identity-catalog")
        return result({
          catalog: {
            version: "synthetic-1",
            countries: [],
            jurisdictions: [],
            forms: [],
            overlays: [],
            tax_classifications: [],
          },
        });
      if (parsed.pathname === "/api/v1/book-identity")
        return result({
          subject: {
            id: "synthetic-subject",
            revision: 0,
            kind: null,
            identity: {},
          },
          history: [],
          has_more: false,
        });
      assert.equal(parsed.pathname, "/api/v1/settings");
      assert.equal(init.method, "POST");
      const body = JSON.parse(init.body);
      changes.push(body);
      current = { ...current, name: body.name, founder: body.founder, metadata_revision: body.revision + 1 };
      return result(current);
    };
    try {
      await render(
        React.createElement(
          AppTheme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            initial: {
              result: {
                catalog: { books: [book], currencies },
                state: state(),
              },
            },
          }),
        ),
      );
      const shell = document.querySelector(".vult-app-shell");
      const page = document.querySelector(".book-settings-page");
      const form = section("Name").querySelector("form");
      assert.ok(page);
      assert.equal(button("Edit details"), undefined);
      form.elements.namedItem("name").value = "Inline saved synthetic Book";
      await React.act(async () =>
        form.dispatchEvent(
          new window.Event("submit", { bubbles: true, cancelable: true }),
        ),
      );
      assert.equal(changes.length, 1);
      assert.equal(changes[0].book_id, book.id);
      assert.ok(document.querySelector(".vult-app-shell") === shell);
      assert.ok(document.querySelector(".book-settings-page") === page);
      assert.equal(window.location.pathname, "/settings");
      assert.match(
        document
          .querySelector('button[aria-label^="Switch Book, current Book"]')
          .getAttribute("aria-label"),
        /Inline saved synthetic Book/,
      );
      assert.equal(document.querySelector('[role="dialog"]'), null);
      await click(document.querySelector('button[aria-label="Settings"]'));
      assert.equal(window.location.pathname, "/settings");
      assert.equal(document.querySelector('[role="dialog"]'), null);
      await selectTab("Your data");
      await React.act(async()=>new Promise(done=>setTimeout(done,80)));
      assert.match(document.querySelector(".book-settings-page").textContent, /Download/);
      assert.equal(changes.length, 1, "opening data settings only reads the Book");
    } finally {
      globalThis.fetch = oldFetch;
    }
  });
});
