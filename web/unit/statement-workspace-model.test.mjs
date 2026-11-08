import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "book-synthetic", name: "Example Book", start_date: "2025-01-01", currency: "USD", scale: 2 };
const range = { from: "2026-01-15", to: "2026-03-12" };
const selection = { report: "profit_loss", range, comparison: "none", columns: "months", invalid: false };
function fixture(request = { book_id: book.id, report: "profit_loss", from: range.from, to: range.to, currency: "USD", comparison: "none" }, prior = "9007199254740993") {
  const period = { from: request.from, to: request.as_of || request.to };
  const comparison = request.comparison === "explicit" ? { from: request.compare_from, to: request.compare_to } : undefined;
  return { schema_version: 1, report: request.report, title: request.report === "profit_loss" ? "Profit & Loss" : "Balance Sheet", book: { ...book, fiscal_year_start_month: 1, fiscal_year_start_day: 1, recognition_basis: "unknown", reporting_framework: "unknown" }, request,
    scope: { mode: "live", period, comparison, fiscal_start: "2026-01-01", ledger_revision: "population-through-march", knowledge_cutoff: "2026-04-01T00:00:00Z", mapping_version: "presentation-v1", hierarchy_basis: "explicit", generated_at: "2026-04-01T00:00:00Z", population_digest: "b".repeat(64), coverage: "unknown", opening_balances: "unknown" },
    columns: [{ id: "current", label: "Current", kind: "amount", period }, ...(comparison ? [{ id: "prior", label: "Comparison", kind: "amount", period: comparison }, { id: "variance", label: "Change", kind: "amount", period }] : [])],
    rows: [{ id: "income", label: "Income", role: "section", values: { current: "10000000000000000", ...(comparison ? { prior, variance: "1" } : {}) }, children: [{ id: "direct-income", label: "Direct postings", role: "account", drilldown: { account_ids: ["income"], from: request.from, to: period.to }, values: { current: "10000000000000000", ...(comparison ? { prior, variance: "1" } : {}) }, children: [] }] }], checks: [], limitations: ["Coverage unconfirmed."], reconciliation: {}, status: "partial", digest: "a".repeat(64), export_csv: 'EXACT-BACKEND-CSV\r\n' };
}

test("month slicing preserves inclusive partial months, leap day and the bounded span", async () => {
  const m = await loadComponent("src/lib/financial-report-workspace.ts");
  assert.deepEqual(m.reportMonths(range), [{ from: "2026-01-15", to: "2026-01-31" }, { from: "2026-02-01", to: "2026-02-28" }, { from: "2026-03-01", to: "2026-03-12" }]);
  assert.deepEqual(m.reportMonths({ from: "2024-02-01", to: "2024-02-29" }), [{ from: "2024-02-01", to: "2024-02-29" }]);
  assert.throws(() => m.reportMonths({ from: "2025-01-01", to: "2026-01-01" }), /twelve/);
  assert.throws(() => m.reportMonths({ from: "2026-02-30", to: "2026-03-01" }));
});

test("month reads anchor the same population and retain exact source cells, scope and CSV precision", async () => {
  const m = await loadComponent("src/lib/financial-report-workspace.ts");
  const base = fixture(); const ranges = m.reportMonths(range); const children = ranges.map((r, i) => fixture(m.monthRequest(base, r), ["9007199254740993", "-5", "17"][i]));
  assert.ok(children.every(r => r.request.to === range.to));
  const display = m.composeMonthDisplay(base, ranges, children);
  assert.deepEqual(display.columns.map(c => c.sourceColumn), ["prior", "prior", "prior", "current"]);
  const january = m.displayCell(display, base.rows[0].children[0], display.columns[0]);
  assert.equal(january.report, children[0]); assert.equal(january.column, "prior"); assert.equal(january.value, "9007199254740993");
  base.status = "ready"; children[0].limitations.push("One monthly treatment needs review.");
  const output = m.displayedReportCSV(display);
  assert.ok(output.includes('"Status","partial"')); assert.ok(output.includes("One monthly treatment needs review.")); assert.ok(output.includes('"Source scope"'));
  assert.ok(output.includes('"90071992547409.93"')); assert.ok(output.includes('"-0.05"')); assert.ok(output.includes('"0.17"'));
  assert.equal(m.displayedReportCSV(m.summaryDisplay(base)), base.export_csv);
  const future = { ...base, scope: { ...base.scope, comparison: { to: "2026-12-31" } } };
  assert.equal(m.monthRequest(future, ranges[0]).to, "2026-12-31");
});

test("changed ledger, presentation, account shape or month scope rejects the whole composition", async () => {
  const m = await loadComponent("src/lib/financial-report-workspace.ts"); const base = fixture(); const ranges = m.reportMonths(range);
  for (const mutate of [r => r.scope.ledger_revision = "changed", r => r.scope.mapping_version = "changed", r => r.rows[0].children = [], r => r.scope.comparison.to = "2026-01-30", r => r.book.currency = "EUR"]){
    const reports = ranges.map(r => fixture(m.monthRequest(base, r))); mutate(reports[0]);
    assert.throws(() => m.composeMonthDisplay(base, ranges, reports));
  }
});

test("month URL roundtrip preserves presentation while rejecting ambiguous or unsupported layouts", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  await withDOM(async () => {
    const location = m.readReportLocation("?report=profit_loss&from=2026-01-15&to=2026-03-12&columns=months", range);
    const url = new URL(m.reportViewURL(book, location)); assert.equal(url.searchParams.get("columns"), "months");
    assert.deepEqual(m.readReportLocation(url.search, range), location);
    for (const query of ["?columns=months&columns=summary", "?columns=other", "?report=journal&columns=months"]) assert.equal(m.readReportLocation(query, range).invalid, true);
  });
});

test("saved views are Book-scoped parameters only, not a persisted financial population", async () => {
  const m = await loadComponent("src/lib/financial-report-workspace.ts");
  await withDOM(async () => {
    m.saveReportView(book, "Monthly view", selection);
    assert.equal(m.readSavedReportViews(book)[0].name, "Monthly view");
    assert.deepEqual(m.readSavedReportViews({ ...book, id: "other-book" }), []);
    const stored = localStorage.getItem(m.savedViewKey(book)); assert.ok(!stored.includes('"rows"')); assert.ok(!stored.includes('"amount_minor"'));
    assert.throws(() => m.saveReportView(book, "", selection));
  });
});

test("draft pack pins the displayed result and rejects a changed population or another Book", async () => {
  const m = await loadComponent("src/lib/financial-report-workspace.ts");
  await withDOM(async () => {
    const base = fixture(); const captured = m.summaryDisplay(base); const original = globalThis.fetch;
    try {
      globalThis.fetch = async url => { const query = new URL(String(url), window.location.origin).searchParams; const request = Object.fromEntries(query); return new Response(JSON.stringify(fixture(request)), { headers: { "Content-Type": "application/json" } }); };
      const pack = await m.loadDraftReportPack(book, selection, ["profit_loss"], "Review draft", "Notes", undefined, captured);
      assert.equal(pack.status, "draft"); assert.equal(pack.reports[0].digest, base.digest); assert.equal(pack.reports[0].export_csv, base.export_csv);
      base.rows[0].values.current = "1"; assert.equal(pack.reports[0].rows[0].values.current, "10000000000000000");
      await assert.rejects(m.loadDraftReportPack({ ...book, id: "other-book" }, selection, ["profit_loss"], "Draft", "", undefined, captured));
      globalThis.fetch = async url => { const request = Object.fromEntries(new URL(String(url), window.location.origin).searchParams); const value = fixture(request); value.scope.ledger_revision = "changed"; return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } }); };
      await assert.rejects(m.loadDraftReportPack(book, selection, ["profit_loss", "balance_sheet"], "Draft", "", undefined, captured), /changed/);
    } finally { globalThis.fetch = original; }
  });
});
