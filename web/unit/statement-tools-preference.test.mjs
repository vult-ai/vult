import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "book-a", name: "Example Book", address: { book_id: "book-a", code: "ABCD", slug: "example-book", revision: 0, aliases: ["abcd", "example-book"] }, founder: "", start_date: "2025-01-01", currency: "USD", scale: 2, fiscal_year_start_month: 1, fiscal_year_start_day: 1, accounting_today: "2026-03-31", accounting_timezone: "UTC" };
function fixture(query) {
  const report = query.get("report") || "balance_sheet";
  const to = query.get("as_of") || query.get("to") || "2026-03-31";
  const from = query.get("from");
  return {
    schema_version: 1, report,
    title: "Recorded report", book: { ...book, recognition_basis: "unknown", reporting_framework: "unknown" },
    request: { book_id: book.id, report, ...(from ? { from, to } : { as_of: to }), comparison: query.get("comparison") || "none", currency: "USD" },
    scope: { mode: "live", period: { from: from || book.start_date, to }, fiscal_start: "2026-01-01", ledger_revision: "revision-a", knowledge_cutoff: "2026-04-01T00:00:00Z", mapping_version: "v1", hierarchy_basis: "explicit", generated_at: "2026-04-01T00:00:00Z", population_digest: "b".repeat(64), coverage: "unknown", opening_balances: "unknown" },
    columns: [{ id: "current", label: "Current", kind: "amount", period: { to } }],
    rows: [{ id: "assets", label: "Assets", role: "section", values: { current: "5500" }, children: [{ id: "equipment", label: "Equipment", role: "account", values: { current: "5500" }, own_values: { current: "1000" }, drilldown: { account_ids: ["equipment", "computer"], to }, children: [{ id: "direct:equipment", label: "Posted directly", role: "account", values: { current: "1000" }, drilldown: { account_ids: ["equipment"], to }, children: [] }, { id: "computer", label: "Computers", role: "account", values: { current: "4500" }, drilldown: { account_ids: ["computer"], to }, children: [] }] }] }],
    checks: [{ id: "balance", status: "pass", message: "Recorded postings balance." }], limitations: ["Source completeness has not been established."], reconciliation: {}, status: "partial", digest: "a".repeat(64), export_csv: "Account,Current\r\nEquipment,55.00\r\n",
  };
}
const tick = () => React.act(async () => { await new Promise((resolve) => setTimeout(resolve, 15)); });

const preferenceKey = "vult:statement-tools-expanded:v1";
const props = { book, state: { book }, onShowTransactions() {}, onInspect() {} };
const expanded = () => document.querySelector(".statement-workbench").dataset.toolsExpanded;
const toggle = () => React.act(async () => document.querySelector(".statement-tools-header button").click());
async function withWorkspace(run) {
  await withDOM(async dom => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    dom.window.history.replaceState({}, "", "/example-book/statements?as_of=2026-03-31&compare=none");
    const original = globalThis.fetch, requests = [];
    globalThis.fetch = async (url, options) => {
      requests.push(options?.method || "GET");
      return new Response(JSON.stringify(fixture(new URL(String(url), dom.window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } });
    };
    let mount = 0;
    const reload = async () => { await dom.render(React.createElement(StatementsWorkspace, { ...props, key: mount++ })); await tick(); };
    try { await run({ ...dom, reload, requests }); assert.ok(requests.every(method => method === "GET")); }
    finally { globalThis.fetch = original; }
  });
}

test("both explicit tools layouts survive a fresh workspace mount without changing the left sidebar or report", async () => {
  await withWorkspace(async ({ window, reload, requests }) => {
    window.localStorage.setItem("vult:sidebar-expanded:v1", "false");
    await reload(); assert.equal(expanded(), "true");
    const reads = requests.length, grid = document.querySelector(".financial-grid");
    await toggle(); assert.equal(expanded(), "false");
    assert.equal(requests.length, reads); assert.equal(document.querySelector(".financial-grid"), grid);
    await reload(); assert.equal(expanded(), "false");
    assert.equal(document.querySelector(".statement-workbench").dataset.motion, "instant");
    await toggle(); await reload(); assert.equal(expanded(), "true");
    assert.equal(window.localStorage.getItem("vult:sidebar-expanded:v1"), "false");
    assert.equal(document.querySelector('[data-grid-row="equipment"][data-grid-column="current"]').textContent, "$55.00");
  });
});

test("responsive collapse and pinned panels do not replace an explicit tools preference", async () => {
  await withWorkspace(async ({ window, reload }) => {
    window.localStorage.setItem(preferenceKey, "true");
    const original = window.matchMedia; let changed;
    const narrow = { matches: false, addEventListener(type, callback) { if (type === "change") changed = callback; }, removeEventListener() {} };
    window.matchMedia = query => query === "(max-width: 900px)" ? narrow : original(query);
    await reload();
    await React.act(async () => document.querySelector('.statement-tools-rail [aria-label="Review scope"]').click());
    await React.act(async () => document.querySelector('[aria-label="Pin work panel"]').click());
    assert.equal(document.querySelector(".statement-workbench").dataset.workPinned, "true");
    await React.act(async () => { narrow.matches = true; changed(); });
    assert.equal(expanded(), "false");
    assert.equal(document.querySelector(".statement-workbench").dataset.workPinned, "false");
    assert.equal(document.querySelector(".statement-work-panel").hidden, false);
    assert.equal(window.localStorage.getItem(preferenceKey), "true");
    await React.act(async () => { narrow.matches = false; changed(); }); assert.equal(expanded(), "true");
    await toggle();
    await React.act(async () => { narrow.matches = true; changed(); narrow.matches = false; changed(); });
    assert.equal(expanded(), "false"); await reload(); assert.equal(expanded(), "false");
  });
});

test("a narrow first visit defaults to compact but restores either explicit choice on refresh", async () => {
  await withWorkspace(async ({ window, reload }) => {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    await reload(); assert.equal(expanded(), "false");
    await toggle(); await reload(); assert.equal(expanded(), "true");
    await toggle(); await reload(); assert.equal(expanded(), "false");
  });
});

test("Escape collapse is remembered and blocked browser storage keeps tools operable", async () => {
  await withWorkspace(async ({ window, reload }) => {
    await reload();
    await React.act(async () => document.querySelector(".statement-work-tools").dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await reload(); assert.equal(expanded(), "false");
    const stored = globalThis.localStorage;
    Object.defineProperty(window, "localStorage", { configurable: true, get() { throw new Error("Storage denied"); } });
    Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("Storage denied"); } });
    try {
      await reload(); assert.equal(expanded(), "true");
      await toggle(); assert.equal(expanded(), "false");
      await toggle(); assert.equal(expanded(), "true");
    } finally { Object.defineProperty(globalThis, "localStorage", { configurable: true, writable: true, value: stored }); }
  });
});
