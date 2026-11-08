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
/** Waits for a condition the page reaches after a read, up to 5 seconds. */
const until = async (ready) => { for (let waited = 0; waited < 5000 && !ready(); waited += 25) await tick(); };

test("a new Balance Sheet requests and renders the native previous-year result with its actual position dates", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?as_of=2026-03-31");
    const original = globalThis.fetch, requests = [];
    globalThis.fetch = async (url, options) => {
      const query = new URL(String(url), window.location.origin).searchParams;
      requests.push({ comparison: query.get('comparison'), method: options?.method || 'GET' });
      const value = fixture(query), prior = { to: '2025-03-31' };
      value.scope.comparison = prior;
      value.columns.push({ id: 'prior', label: 'Comparison', kind: 'amount', period: prior }, { id: 'variance', label: 'Change', kind: 'amount', period: value.scope.period });
      const addPrior = rows => rows.forEach(row => { row.values = { ...row.values, prior: '0', variance: row.values.current }; addPrior(row.children); });
      addPrior(value.rows);
      value.export_csv = 'Account,Current,Comparison,Change\r\nEquipment,55.00,0.00,55.00\r\n';
      return new Response(JSON.stringify(value), { headers: { 'Content-Type': 'application/json' } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      assert.ok(requests.length);
      assert.ok(requests.every(r => r.comparison === 'previous_year' && r.method === 'GET'));
      assert.equal(document.querySelectorAll('.financial-grid thead th').length, 4);
      assert.match(document.querySelector('.financial-grid thead th:nth-child(3)').getAttribute('aria-label'), /As of March 31, 2025/);
      assert.equal(document.querySelector('[data-grid-row="equipment"][data-grid-column="current"]').textContent, '$55.00');
      assert.equal(document.querySelector('[data-grid-row="equipment"][data-grid-column="prior"]').textContent, '$0.00');
    } finally { globalThis.fetch = original; }
  });
});

function monthlyFixture(query) {
  const value = fixture(query);
  const comparison = query.get("comparison") === "explicit" ? { from: query.get("compare_from"), to: query.get("compare_to") } : undefined;
  const prior = comparison ? String(Number(comparison.to.slice(5, 7)) * 1100) : undefined;
  const values = { current: "6600", ...(comparison ? { prior, variance: String(6600 - Number(prior)) } : {}) };
  value.title = "Profit & Loss";
  value.scope.comparison = comparison;
  value.columns = [{ id: "current", label: "Current", kind: "amount", period: value.scope.period }, ...(comparison ? [{ id: "prior", label: "Comparison", kind: "amount", period: comparison }, { id: "variance", label: "Change", kind: "amount", period: value.scope.period }] : [])];
  if (comparison) Object.assign(value.request, { compare_from: comparison.from, compare_to: comparison.to });
  value.rows = [{ id: "income", label: "Income", role: "section", values, children: [{ id: "revenue", label: "Service revenue", role: "account", values, drilldown: { account_ids: ["revenue"], from: value.scope.period.from, to: value.scope.period.to }, children: [] }] }];
  value.digest = (comparison ? ["b", "c", "d"][Number(comparison.to.slice(5, 7)) - 1] : "a").repeat(64);
  return value;
}

test("Statements renders the reporting collection, exact backend hierarchy and honest scope without changing records", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&from=2026-01-01&to=2026-03-31&compare=none");
    const original = globalThis.fetch; const calls = [];
    globalThis.fetch = async (url, options) => {
      calls.push({ url: String(url), method: options?.method || "GET" });
      return new Response(JSON.stringify(fixture(new URL(String(url), window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} }));
      await tick();
      assert.equal(document.querySelector('[data-testid="statements-workspace"]')?.getAttribute("aria-busy"), "false");
      assert.equal(new URL(window.location.href).search, "?as_of=2026-03-31&compare=none");
      assert.equal(new URL(window.location.href).searchParams.get("as_of"), "2026-03-31");
      const picker = document.querySelector('select[aria-label="Choose report"]');
      assert.equal(document.querySelector('.statement-navigation'), null);
      for (const label of ["Balance Sheet", "Profit & Loss", "Trial Balance", "General Ledger", "Journal", "Cash movement"]) assert.ok(picker.textContent.includes(label), label);
      for (const label of ["Changes in Equity", "Cash Flows", "Notes & Review"]) assert.ok(!picker.textContent.includes(label), `${label} is not in v0.4`);
      const context = document.querySelector('.statement-context');
      assert.equal(context.querySelector('h2').textContent, 'Balance Sheet');
      assert.ok(context.textContent.includes('USD'));
      assert.match(context.textContent, /Basis: .+ · Coverage: .+ · Not audited/);
      assert.ok(!context.textContent.includes(book.name));
      assert.ok(!context.textContent.includes('March 31, 2026'));
      assert.ok(!context.textContent.includes('Recognition basis:'));
      assert.match(document.querySelector('thead th[scope="col"]:nth-child(2)').getAttribute("aria-label"), /As of March 31, 2026/);
      assert.ok(document.querySelector('[data-report-row="equipment"]')?.textContent.includes("$55.00"));
      const expand = document.querySelector('[aria-label="Expand Equipment"]');
      await React.act(async () => expand.click());
      assert.ok(document.querySelector('.financial-grid [data-report-row="direct:equipment"]')?.textContent.includes("$10.00"));
      assert.ok(document.querySelector('.financial-grid [data-report-row="computer"]')?.textContent.includes("$45.00"));
      assert.ok(calls.every((call) => call.method === "GET"));
      assert.equal(document.querySelector('.statement-paper .financial-grid-selection'), null);
      assert.equal(document.querySelector('.statement-footer'), null);
      await React.act(async () => document.querySelector('button[aria-label="Export report"]').click()); await tick();
      assert.equal(document.querySelector('a[title="Separate export of all recorded journal lines"]'), null);
      assert.equal(document.querySelector('.statement-export-popover button').disabled, false);
    } finally { globalThis.fetch = original; }
  });
});

test("collapsing and reopening tools keeps the same controls, selected amount, grid scroll and captured read", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url, options) => { calls.push(options?.method || "GET"); return new Response(JSON.stringify(fixture(new URL(String(url), window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } }); };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      const grid = document.querySelector('.financial-grid'), scroll = document.querySelector('.financial-grid-scroll');
      const selected = document.querySelector('[data-grid-row="equipment"][data-grid-column="current"]');
      await React.act(async () => selected.click());
      scroll.scrollLeft = 63; scroll.scrollTop = 174;
      const rail = document.querySelector('.statement-tools-rail'), buttons = [...rail.querySelectorAll('button')], icons = [...rail.querySelectorAll('svg')];
      const toggle = rail.querySelector('[aria-label="Collapse statement tools"]');
      const requestCount = calls.length;
      await React.act(async () => { toggle.focus(); toggle.click(); });
      assert.equal(document.querySelector('.statement-workbench').dataset.toolsExpanded, 'false');
      assert.equal(document.querySelector('.statement-workbench').dataset.motion, 'instant', 'Keyboard activation does not animate the grid');
      assert.equal(document.activeElement, toggle);
      assert.deepEqual([...rail.querySelectorAll('button')], buttons);
      assert.deepEqual([...rail.querySelectorAll('svg')], icons);
      assert.equal(document.querySelector('.financial-grid'), grid);
      assert.equal(document.querySelector('[data-grid-row="equipment"][data-grid-column="current"]'), selected);
      assert.equal(scroll.scrollLeft, 63); assert.equal(scroll.scrollTop, 174);
      await React.act(async () => { toggle.click(); toggle.click(); toggle.click(); });
      assert.equal(document.querySelector('.statement-workbench').dataset.toolsExpanded, 'true');
      assert.deepEqual([...rail.querySelectorAll('button')], buttons);
      assert.equal(calls.length, requestCount, 'Layout changes never request a different accounting population');
      assert.ok(calls.every(method => method === 'GET'));
      assert.equal(document.querySelector('.statement-paper .financial-grid-selection'), null);
      assert.ok(document.querySelector('.statement-work-panel .financial-grid-selection'));
    } finally { globalThis.fetch = original; }
  });
});

test("review flyout can be pinned and dismissed without remounting the report or hiding its material scope", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch; let reads = 0;
    globalThis.fetch = async url => { reads++; return new Response(JSON.stringify(fixture(new URL(String(url), window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } }); };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      const grid = document.querySelector('.financial-grid'), trigger = document.querySelector('.statement-tools-rail [aria-label="Review scope"]');
      await React.act(async () => trigger.click());
      const panel = document.querySelector('.statement-work-panel');
      assert.equal(panel.hidden, false);
      assert.ok(panel.textContent.includes('Source completeness has not been established.'));
      await React.act(async () => panel.querySelector('[aria-label="Pin work panel"]').click());
      assert.equal(document.querySelector('.statement-workbench').dataset.workPinned, 'true');
      assert.equal(document.querySelector('.statement-workbench').dataset.toolsExpanded, 'false');
      assert.equal(document.querySelector('.financial-grid'), grid);
      await React.act(async () => panel.querySelector('[aria-label="Unpin work panel"]').click());
      assert.equal(document.querySelector('.statement-workbench').dataset.workPinned, 'false');
      await React.act(async () => panel.querySelector('[aria-label="Close work panel"]').click()); await tick(); await tick();
      assert.equal(panel.hidden, true); assert.equal(document.activeElement, trigger);
      assert.match(document.querySelector('.statement-context').textContent, /Not audited/);
      const contextTrigger = document.querySelector('.statement-context .statement-review-link');
      await React.act(async () => contextTrigger.click());
      assert.equal(contextTrigger.getAttribute('aria-expanded'), 'true');
      assert.ok(panel.textContent.includes('Recognition basis'));
      assert.ok(panel.textContent.includes('Not established'));
      await React.act(async () => panel.querySelector('[aria-label="Close work panel"]').click()); await tick(); await tick();
      assert.equal(document.activeElement, contextTrigger);
      assert.equal(reads, 1);
    } finally { globalThis.fetch = original; }
  });
});

test("a pinned work panel becomes a compact flyout on narrow resize without dropping its context", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch, originalMedia = window.matchMedia;
    let changed;
    const narrow = { matches: false, addEventListener(type, callback) { if (type === 'change') changed = callback; }, removeEventListener() {} };
    window.matchMedia = query => query === '(max-width: 900px)' ? narrow : originalMedia(query);
    globalThis.fetch = async url => new Response(JSON.stringify(fixture(new URL(String(url), window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } });
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      await React.act(async () => document.querySelector('.statement-tools-rail [aria-label="Review scope"]').click());
      await React.act(async () => document.querySelector('[aria-label="Pin work panel"]').click());
      const panel = document.querySelector('.statement-work-panel'), grid = document.querySelector('.financial-grid');
      assert.equal(document.querySelector('.statement-workbench').dataset.workPinned, 'true');
      await React.act(async () => { narrow.matches = true; changed(); });
      assert.equal(document.querySelector('.statement-workbench').dataset.workPinned, 'false');
      assert.equal(document.querySelector('.statement-workbench').dataset.toolsExpanded, 'false');
      assert.equal(document.querySelector('.statement-work-panel'), panel); assert.equal(panel.hidden, false);
      assert.ok(panel.textContent.includes('Source completeness has not been established.'));
      assert.equal(document.querySelector('.financial-grid'), grid);
    } finally { globalThis.fetch = original; window.matchMedia = originalMedia; }
  });
});

test("compact report options retain saved views and preparation, and nested dialogs return to the stable toolbar trigger", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url, options) => {
      calls.push(options?.method || "GET");
      return new Response(JSON.stringify(fixture(new URL(String(url), window.location.origin).searchParams)), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      assert.equal(document.querySelector('select[aria-label="Saved report view"]'), null);
      const more = document.querySelector('button[aria-label="Report options"]');
      await React.act(async () => { more.focus(); more.click(); }); await tick();
      const options = document.querySelector('[role="dialog"][aria-label="Report options"]');
      assert.ok(options);
      assert.ok(options.querySelector('select[aria-label="Saved report view"]'));
      assert.ok(![...options.querySelectorAll("button")].some(button => button.textContent === "Prepare pack"));
      await React.act(async () => [...options.querySelectorAll("button")].find(button => button.textContent === "Save view").click()); await tick(); await tick(); await tick();
      const dialog = [...document.querySelectorAll('[role="dialog"]')].find(element => element.textContent.includes("View name"));
      assert.ok(dialog);
      await React.act(async () => [...dialog.querySelectorAll("button")].find(button => button.textContent === "Cancel").click()); await tick(); await tick();
      assert.equal(document.activeElement, more);
      assert.ok(calls.every(method => method === "GET"));
    } finally { globalThis.fetch = original; }
  });
});

test("print lifecycle mounts the complete report only for printing and keeps the scope qualifier", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch;
    globalThis.fetch = async (url) => {
      const report = fixture(new URL(String(url), window.location.origin).searchParams);
      report.rows[0].children.push(...Array.from({ length: 120 }, (_, i) => ({ id: `zero-${i}`, label: `Zero account ${i}`, role: "account", values: { current: "0" }, children: [] })));
      return new Response(JSON.stringify(report), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      assert.equal(document.querySelector(".statement-print-table"), null);
      assert.equal(document.querySelectorAll(".financial-grid tbody tr").length, 100);
      await React.act(async () => window.dispatchEvent(new window.Event("beforeprint")));
      assert.equal(document.documentElement.dataset.vultReportPrint, "true");
      assert.equal(document.querySelectorAll(".statement-print-table tbody tr").length, 124);
      assert.match(document.querySelector(".statement-context").textContent, /Not audited/);
      assert.ok(document.querySelector(".statement-print-table thead").textContent.includes("Partial ledger report"));
      assert.ok(document.querySelector(".statement-print-table thead").textContent.includes("Example Book"));
      assert.ok(document.querySelector(".statement-print-table").textContent.includes("Posted directly"));
      await React.act(async () => window.dispatchEvent(new window.Event("afterprint")));
      assert.equal(document.querySelector(".statement-print-table"), null);
      assert.equal(document.documentElement.dataset.vultReportPrint, undefined);
      assert.equal(document.querySelectorAll(".financial-grid tbody tr").length, 100);
    } finally { globalThis.fetch = original; }
  });
});

test("inspection uses the captured scope and restores keyboard focus after Escape", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=balance_sheet&as_of=2026-03-31&compare=none");
    const original = globalThis.fetch; const calls = []; const opened = [];
    globalThis.fetch = async (url) => {
      const target = new URL(String(url), window.location.origin); calls.push(target);
      const report = fixture(target.searchParams);
      const value = target.pathname.endsWith("/contributions") ? {
        result_digest: report.digest, row_id: "equipment", column_id: "current", total_minor: "5500", debit_minor: "5500", credit_minor: "0",
        contributions: [{ posting_id: "posting-a", entry_id: "entry-a", account_id: "equipment", date: "2026-03-01", side: "debit", amount_minor: "5500", signed_amount_minor: "5500", report_amount_minor: "5500", column_id: "current", purpose: "ordinary" }],
      } : report;
      return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect(id) { opened.push(id); } })); await tick();
      const trigger = document.querySelector('[data-grid-row="equipment"][data-grid-column="current"]');
      const gridScroll = document.querySelector('.financial-grid-scroll');
      gridScroll.scrollLeft = 61; gridScroll.scrollTop = 211;
      await React.act(async () => { trigger.click(); trigger.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })); }); await tick(); await tick();
      const inspector = document.querySelector(".statement-inspector");
      assert.equal(document.activeElement, inspector);
      assert.ok(inspector.textContent.includes("Report contribution"));
      const scope = calls.find((u) => u.pathname.endsWith("/contributions")).searchParams;
      assert.equal(scope.get("digest"), "a".repeat(64));
      assert.equal(scope.get("knowledge_cutoff"), "2026-04-01T00:00:00Z");
      assert.equal(scope.get("generated_at"), "2026-04-01T00:00:00Z");
      await React.act(async () => inspector.querySelector('[aria-label="Inspect contributing entry 1"]').click());
      assert.deepEqual(opened, ["entry-a"]);
      gridScroll.scrollLeft = 180; gridScroll.scrollTop = 350;
      await React.act(async () => inspector.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }))); await tick(); await tick();
      assert.equal(document.querySelector(".statement-inspector"), null);
      assert.equal(document.activeElement, trigger);
      assert.equal(gridScroll.scrollLeft, 61);
      assert.equal(gridScroll.scrollTop, 211);
    } finally { globalThis.fetch = original; }
  });
});

test("a month cell inspects its native comparison read and keeps its displayed month label", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=profit_loss&from=2026-01-01&to=2026-03-31&columns=months&compare=none");
    const original = globalThis.fetch; const calls = [];
    globalThis.fetch = async (url, options) => {
      const target = new URL(String(url), window.location.origin); calls.push({ target, method: options?.method || "GET" });
      const report = monthlyFixture(target.searchParams);
      if (target.pathname.endsWith("/months")) {
        const model = await loadComponent("src/lib/financial-report-workspace.ts");
        const ranges = model.reportMonths({ from: target.searchParams.get("months_from"), to: target.searchParams.get("months_to") });
        return new Response(JSON.stringify({ schema_version: 1, base: report, months: ranges.map(r => monthlyFixture(new URLSearchParams(model.monthRequest(report, r)))) }), { headers: { "Content-Type": "application/json" } });
      }
      const value = target.pathname.endsWith("/contributions") ? {
        result_digest: report.digest, row_id: "revenue", column_id: "prior", total_minor: "2200", debit_minor: "0", credit_minor: "2200",
        contributions: [{ posting_id: "posting-feb", entry_id: "entry-feb", account_id: "revenue", date: "2026-02-10", side: "credit", amount_minor: "2200", signed_amount_minor: "-2200", report_amount_minor: "2200", column_id: "prior", purpose: "ordinary" }],
      } : report;
      return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} }));
      // The mock server bundles a module on the months read, which can outlast a
      // fixed tick when the whole suite runs at once: wait for the row, up to 5s.
      await until(() => document.querySelector('[data-report-row="revenue"]'));
      const row = document.querySelector('[data-report-row="revenue"]');
      assert.ok(row, "Monthly report row: " + document.body.textContent); assert.ok(row.textContent.includes("$22.00")); assert.ok(row.textContent.includes("$66.00"));
      const trigger = document.querySelector('[data-grid-row="revenue"][data-grid-column="month:2026-02-01:2026-02-28"]');
      await React.act(async () => { trigger.click(); trigger.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });
      await until(() => document.querySelector(".statement-inspector .statement-inspector-amount")?.textContent);
      const inspector = document.querySelector(".statement-inspector");
      assert.match(inspector.querySelector(".statement-inspector-period").textContent, /^Feb · February 1, 2026 – February 28, 2026$/);
      assert.equal(inspector.querySelector(".statement-inspector-amount").textContent, "$22.00");
      const query = calls.find(c => c.target.pathname.endsWith("/contributions")).target.searchParams;
      assert.equal(query.get("column_id"), "prior"); assert.equal(query.get("digest"), "c".repeat(64));
      assert.equal(query.get("to"), "2026-03-31"); assert.equal(query.get("compare_from"), "2026-02-01"); assert.equal(query.get("compare_to"), "2026-02-28");
      assert.ok(calls.every(c => c.method === "GET"));
    } finally { globalThis.fetch = original; }
  });
});

test("an overlong month view explains its limit without requesting financial data", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-book/statements?report=profit_loss&from=2025-01-01&to=2026-03-31&columns=months&compare=none");
    const original = globalThis.fetch; let calls = 0;
    globalThis.fetch = async () => { calls++; throw new Error("This view must not read reports."); };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} })); await tick();
      assert.equal(calls, 0); assert.ok(document.body.textContent.includes("up to twelve months"));
      assert.equal(document.querySelector('[data-testid="statements-workspace"]').getAttribute("aria-busy"), "false");
    } finally { globalThis.fetch = original; }
  });
});

for (const profile of ["personal", "business"]) test(`${profile} Statements routes metadata directly to the shared reporting workspace`, async () => {
 await withDOM(async ({render,window}) => {
  const {default:BookScreens}=await loadComponent("src/components/workspaces/book-screens.tsx");
  const selected={...book,profile};window.history.replaceState({},"","/example-book/statements?as_of=2026-03-31&compare=none");
  const original=globalThis.fetch,calls=[];
  globalThis.fetch=async(url,options)=>{const q=new URL(url,window.location.origin);calls.push({path:q.pathname,method:options?.method||"GET",book:q.searchParams.get("book_id")});return Response.json(fixture(q.searchParams))};
  const noop=()=>{};
  try {
   const props={book:selected,onPosted:noop,onOpening:noop,onNavigate:noop,onShowTransactions:noop,onInspect:noop,onAccount:noop,onSettings:noop,onStart:noop};
   // Retain an Accounts read before navigating to Statements: the new metadata
   // must reach the reporting workspace instead of the cached financial state.
   await render(React.createElement(BookScreens,{...props,state:{book:selected,read_view:"accounts",account_balances:[]},page:"books"}));
   await render(React.createElement(BookScreens,{...props,state:{book:selected,bootstrap:true,read_view:"statements"},page:"statements"}));await tick();
   assert.ok(document.querySelector('[data-testid="statements-workspace"]'));
   assert.equal(document.querySelector('[data-testid="v0-book-overview"]'),null);
   assert.ok(document.querySelector('.financial-grid'));
   assert.ok(document.querySelector('select[aria-label="Choose report"]'));
   assert.ok(calls.length);assert.ok(calls.every(c=>c.path.startsWith("/api/v1/financial-reports")&&c.method==="GET"&&c.book===book.id));
   assert.equal(document.querySelector('[role="alert"]'),null);
  } finally {globalThis.fetch=original}
 });
});
