import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

test("startup reads data and code in parallel, then mounts the confirmed view once", async () => {
  const { startApplication } = await loadComponent("src/lib/startup.ts");
  let dataReady, codeReady, tableReady;
  const started = [],
    mounts = [];
  const run = startApplication({
    read: () => {
      started.push("data");
      return new Promise((resolve) => {
        dataReady = resolve;
      });
    },
    load: () => {
      started.push("code");
      return new Promise((resolve) => {
        codeReady = resolve;
      });
    },
  });
  assert.deepEqual(started, ["data", "code"]);
  const state = { book: { id: "confirmed-book" } };
  const catalog = { books: [state.book], currencies: [] };
  dataReady({ catalog, state });
  await Promise.resolve();
  assert.deepEqual(mounts, []);
  codeReady({
    mount: (initial) => mounts.push(initial),
    prepareBook: () => {
      started.push("table");
      return new Promise((resolve) => {
        tableReady = resolve;
      });
    },
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(started, ["data", "code", "table"]);
  assert.deepEqual(
    mounts,
    [],
    "the first book frame waits for its table code instead of mounting a second loader",
  );
  tableReady();
  await run;
  assert.deepEqual(mounts, [{ result: { catalog, state } }]);
});

test("an empty installation mounts confirmed setup without downloading book-only code", async () => {
  const { startApplication } = await loadComponent("src/lib/startup.ts");
  let prepared = false,
    initial;
  await startApplication({
    read: async () => ({ catalog: { books: [], currencies: [] } }),
    load: async () => ({
      prepareBook: async () => {
        prepared = true;
      },
      mount: (value) => {
        initial = value;
      },
    }),
  });
  assert.equal(prepared, false);
  assert.deepEqual(initial, {
    result: { catalog: { books: [], currencies: [] } },
  });
});

test("slow startup mounts recovery once and gives that same application the pending read", async () => {
  const { startApplication } = await loadComponent("src/lib/startup.ts");
  let finish;
  const mounts = [];
  const run = startApplication({
    read: () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
    load: async () => ({ mount: (initial) => mounts.push(initial) }),
    slowAfter: 5,
  });
  await new Promise((resolve) => setTimeout(resolve, 15));
  assert.equal(mounts.length, 1);
  assert.ok(mounts[0].pending);
  finish({ catalog: { books: [], currencies: [] } });
  await run;
  assert.deepEqual(await mounts[0].pending, {
    catalog: { books: [], currencies: [] },
  });
  assert.equal(mounts.length, 1, "late data must not remount the application");
});

test("a failed startup read reaches recovery instead of being treated as an empty book", async () => {
  const { startApplication } = await loadComponent("src/lib/startup.ts");
  let initial;
  await startApplication({
    read: async () => {
      throw new Error("Database unavailable");
    },
    load: async () => ({
      mount: (value) => {
        initial = value;
      },
    }),
  });
  assert.deepEqual(initial, { result: { error: "Database unavailable" } });
});

test("a confirmed startup renders the book immediately without a duplicate read or loading screen", async () => {
  await withDOM(async ({ render }) => {
    const { App, Theme } = await loadApplication();
    const originalFetch = globalThis.fetch;
    let reads = 0;
    globalThis.fetch = async (url) => {
      if (new URL(url, "http://localhost").pathname === "/api/v1/state") reads++;
      throw new Error("No other resources in this fixture");
    };
    const state = {
      book: {
        id: "startup-book",
        name: "Synthetic Studio",
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
    };
    try {
      await render(
        React.createElement(
          Theme,
          { initialTheme: "light", initialPreference: "light" },
          React.createElement(App, {
            initial: {
              result: {
                catalog: { books: [state.book], currencies: state.currencies },
                state,
              },
            },
          }),
        ),
      );
      assert.equal(document.querySelector("h1").textContent, "Activity");
      assert.equal(document.querySelector(".onboarding"), null);
      assert.ok(!document.body.textContent.includes("Opening your book"));
      assert.equal(reads, 0);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
