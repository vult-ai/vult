import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "11111111-1111-4111-8111-111111111111", name: "Northwind Studio", currency: "USD", scale: 2, start_date: "2025-01-01", accounting_timezone: "UTC" };
const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

test("position and metadata reads carry no transaction filters; Activity pages bind the first page's revision", async () => {
  await withDOM(async () => {
    const { readBookState } = await loadComponent("src/lib/book-state.ts");
    const { emptyDashboardState } = await loadComponent("tests/fixtures/dashboard-errors.ts");
    const original = globalThis.fetch, urls = [];
    globalThis.fetch = async (url) => {
      const parsed = new URL(String(url), "http://localhost");
      urls.push(parsed);
      return json({ version: 1, book, transactions: [], revision: "r1",
        ...(parsed.searchParams.get("view") === "dashboard" ? emptyDashboardState(book, parsed.searchParams) : {}) });
    };
    try {
      const range = { from: "2026-01-01", to: "2026-03-31" };
      await readBookState(book.id, { search: "rent", account: "a1" }, "UTC", { view: "dashboard", range });
      await readBookState(book.id, {}, "UTC", { view: "settings", range });
      await readBookState(book.id, { search: "rent", kind: "" }, "UTC", { view: "transactions", range, offset: 25, revision: "r1" });
      const [dashboard, settings, activity] = urls.map((u) => u.searchParams);
      for (const key of ["q", "search", "account", "kind", "from", "to"]) {
        assert.equal(dashboard.get(key), null, `dashboard ${key}`);
        assert.equal(settings.get(key), null, `settings ${key}`);
      }
      assert.equal(dashboard.get("dashboard_from"), "2026-01-01");
      assert.equal(dashboard.get("dashboard_unit"), "week");
      assert.equal(settings.get("dashboard_unit"), null);
      assert.equal(settings.get("bootstrap"), "1");
      // The search goes as q, the Book search's name; never as the older search.
      assert.equal(activity.get("q"), "rent");
      assert.equal(activity.get("search"), null);
      assert.equal(activity.get("kind"), null, "empty filters are omitted");
      assert.equal(activity.get("offset"), "25");
      assert.equal(activity.get("revision"), "r1");
    } finally { globalThis.fetch = original; }
  });
});

test("the recorded position is read as stated and checked, never recomputed", async () => {
  const { currentPosition } = await loadComponent("src/lib/v0-ledger.ts");
  const position = { assets_minor: "150000", liabilities_minor: "50000", equity_accounts_minor: "80000", accumulated_result_minor: "20000", net_assets_minor: "100000", basis: "recorded_book_carrying", source_coverage: "unknown" };
  assert.deepEqual(currentPosition({ position }), { assets: "150000", liabilities: "50000", equity: "100000" });
  assert.throws(() => currentPosition({ position: { ...position, net_assets_minor: "99999" } }), /balance/);
  assert.throws(() => currentPosition({ position: { ...position, accumulated_result_minor: "1" } }), /balance/);
});

test("the Activity summary waits for the table's revision, binds to it, and a changed Book refreshes both", async () => {
  await withDOM(async ({ render }) => {
    const { ActivitySummary } = await loadComponent("src/components/book/activity-summary.tsx");
    const original = globalThis.fetch, urls = [];
    let stale = 0;
    globalThis.fetch = async (url) => { urls.push(new URL(String(url), "http://localhost")); return json({ error: "changed" }, 409); };
    try {
      const props = { book, filters: { search: "", kind: "", from: "", to: "", account: "" }, onFilter() {}, onStale: () => stale++ };
      await render(React.createElement(ActivitySummary, { ...props, revision: undefined }));
      await React.act(() => new Promise((r) => setTimeout(r, 10)));
      assert.equal(urls.length, 0, "no summary read before the table answers");
      await render(React.createElement(ActivitySummary, { ...props, revision: "r1" }));
      await React.act(() => new Promise((r) => setTimeout(r, 10)));
      assert.equal(urls.at(-1).searchParams.get("revision"), "r1");
      assert.equal(stale, 1, "a 409 asks the table and summary to refresh together");
      assert.ok(!/Couldn’t load/.test(document.getElementById("root").textContent), "a changed Book is not shown as a failure");
    } finally { globalThis.fetch = original; }
  });
});

test("names never make parents; recorded parent links do, within one account class", async () => {
  const { accountHierarchy } = await loadComponent("src/lib/account-hierarchy.ts");
  const a = (code, name, kind, parent_id) => ({ code, name, kind, usage: "general", currency: "", scale: 2, balance_minor: "0", native_minor: "", posting_count: 1, ...(parent_id ? { parent_id } : {}) });
  const roots = accountHierarchy([
    a("p", "PayPal", "asset"),
    a("eur", "PayPal:PayPal EUR", "asset"),
    a("gbp", "PayPal GBP", "asset", "p"),
    a("x", "Fees", "expense", "p"),
    a("loop1", "Loop one", "asset", "loop2"),
    a("loop2", "Loop two", "asset", "loop1"),
  ]);
  const paypal = roots.find((n) => n.name === "PayPal");
  assert.deepEqual(paypal.children.map((n) => n.name), ["PayPal GBP"]);
  assert.ok(roots.some((n) => n.name === "PayPal:PayPal EUR"), "a colon is part of the name");
  assert.ok(roots.some((n) => n.name === "Fees"), "a parent in another class is ignored");
  assert.ok(roots.some((n) => n.own[0]?.code === "loop1") && roots.some((n) => n.own[0]?.code === "loop2"), "a cycle stays at the root");
});
