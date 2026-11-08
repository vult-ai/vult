import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import {
  snapshotCash,
  BookSnapshotProbe,
  chooseLedger,
} from "./helpers/book-controls.mjs";
import { loadApplication } from "./helpers/application.mjs";

test("early startup discovers the selected UUID before reading finances and rejects a mismatched response", async () => {
  await withDOM(async () => {
    const { readInitialBooks, readBookState } = await loadComponent(
      "src/lib/book-state.ts",
    );
    const { emptyDashboardState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
    const original = globalThis.fetch;
    const a = { id: "book-a", name: "A", currency: "USD", scale: 2 },
      b = { ...a, id: "book-b", name: "B" };
    const catalog = { books: [a, b], currencies: [] },
      calls = [];
    let mismatch = false;
    globalThis.fetch = async (url) => {
      if (String(url).endsWith("/auth/session"))
        return {
          ok: true,
          json: async () => ({ mode: "local", csrf: "fixture" }),
        };
      calls.push(String(url));
      return {
        ok: true,
        json: async () =>
          String(url).endsWith("/books") ? catalog :
            emptyDashboardState(mismatch ? a : b, new URL(url, "http://localhost").searchParams),
      };
    };
    try {
      localStorage.setItem("vult:active-book:v1", b.id);
      const data = await readInitialBooks();
      assert.deepEqual(data.catalog, catalog);
      assert.deepEqual(data.state.book, b);
      assert.equal(data.state.dashboard_snapshot.book_id, b.id);
      assert.equal(calls.length, 2);
      assert.ok(calls[0].endsWith("/books"));
      assert.equal(
        new URL(calls[1], "http://localhost").searchParams.get("book_id"),
        b.id,
      );
      mismatch = true;
      await assert.rejects(
        readBookState(b.id),
        /selected Book could not be confirmed/,
      );
      await assert.rejects(readBookState(""), /Select a Book/);
      catalog.books = [];
      calls.length = 0;
      assert.deepEqual(await readInitialBooks(), { catalog, state: undefined });
      assert.equal(
        calls.length,
        1,
        "empty catalogs must not trigger an unscoped financial request",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});

test("a prepared startup snapshot is consumed once and cannot reappear after switching back", async () => {
  await withDOM(async ({ render, window }) => {
    const { App, Theme } = await loadApplication();
    const original = globalThis.fetch;
    const a = {
        id: "book-a",
        name: "A",
        currency: "USD",
        scale: 2,
        founder: "Alex",
        start_date: "2026-01-01",
      },
      b = { ...a, id: "book-b", name: "B" };
    const currencies = [{ code: "USD", scale: 2 }];
    const state = (book, cash) => ({
      book,
      currencies,
      summary: {
        cash_minor: cash,
        owed_minor: "0",
        reimbursements_minor: "0",
        loans_minor: "0",
        income_minor: "0",
        expenses_minor: "0",
      },
      cash_accounts: [],
      payables: [],
      fx_gain_minor: "0",
      fx_loss_minor: "0",
      accounts: [],
      transactions: [],
    });
    const reads = [];
    globalThis.fetch = async (url) => {
      if (String(url).endsWith("/auth/session"))
        return {
          ok: true,
          json: async () => ({ mode: "local", csrf: "fixture" }),
        };
      const parsed = new URL(url, "http://localhost");
      if (parsed.pathname.endsWith("/legal-identity-catalog"))
        return {
          ok: true,
          json: async () => ({
            catalog: {
              countries: [],
              jurisdictions: [],
              forms: [],
              overlays: [],
              tax_classifications: [],
            },
          }),
        };
      const id = parsed.searchParams.get("book_id");
      if (parsed.pathname.endsWith("/book-identity"))
        return {
          ok: true,
          json: async () => ({
            subject: {
              id: `subject-${id}`,
              kind: null,
              revision: 0,
              identity: {},
            },
            history: [],
            has_more: false,
            next_offset: 0,
          }),
        };
      if (
        parsed.pathname.endsWith("/state") &&
        !parsed.searchParams.get("kind")
      )
        reads.push(id);
      assert.ok(id === a.id || id === b.id);
      return {
        ok: true,
        json: async () =>
          state(id === a.id ? a : b, id === a.id ? "2000" : "3000"),
      };
    };
    const choose = (id) => chooseLedger(id === a.id ? a.name : b.name);
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            Table: BookSnapshotProbe,
            initial: {
              result: {
                catalog: { books: [a, b], currencies },
                state: state(a, "1000"),
              },
            },
          }),
        ),
      );
      assert.equal(reads.length, 0);
      assert.equal(snapshotCash(), "1000");
      await choose(b.id);
      assert.equal(snapshotCash(), "3000");
      await choose(a.id);
      assert.equal(snapshotCash(), "2000");
      assert.deepEqual(reads, [b.id, a.id]);
      window.history.pushState(
        null,
        "",
        "/dashboard?book_id=book-b&from=2026-01-01&to=2026-09-30",
      );
      await React.act(async () =>
        window.dispatchEvent(new window.PopStateEvent("popstate")),
      );
      assert.ok(
        document.querySelector(
          'button[aria-label="Switch Book, current Book B"]',
        ),
      );
      assert.deepEqual(
        reads,
        [b.id, a.id, b.id],
        "history restores the linked Book through normal scoped selection",
      );
      localStorage.setItem("vult:active-book:v1", a.id);
      window.history.replaceState(
        null,
        "",
        "/dashboard?book=b.book-b&range=2026-01-01..2026-09-30",
      );
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            key: "shared-link",
            initial: {
              pending: Promise.resolve({
                catalog: { books: [a, b], currencies },
                state: state(b, "3000"),
              }),
            },
          }),
        ),
      );
      assert.ok(
        document.querySelector(
          'button[aria-label="Switch Book, current Book B"]',
        ),
        "cold shared links override the locally remembered Book",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});
