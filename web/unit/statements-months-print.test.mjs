import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "synthetic-month-book", name: "Example Reporting Co", currency: "USD", scale: 2, start_date: "2025-01-01", fiscal_year_start_month: 1, fiscal_year_start_day: 1, recognition_basis: "unknown", reporting_framework: "unknown" };
const range = { from: "2026-01-01", to: "2026-09-30" };
const baseRequest = { book_id: book.id, report: "profit_loss", ...range, currency: "USD", comparison: "explicit", compare_from: "2025-01-01", compare_to: "2025-09-30" };
function report(request = baseRequest, includePriorOnly = request.compare_from === baseRequest.compare_from) {
  const monthly = request.compare_from !== baseRequest.compare_from;
  const period = { from: request.from, to: request.to }, comparison = { from: request.compare_from, to: request.compare_to };
  const values = (current, prior) => ({ current, prior, variance: String(BigInt(current) - BigInt(prior)) });
  const live = { id: "account:active", account_id: "active", account_code: "4000", label: "Current income", role: "account", values: values("900", monthly ? "100" : "0"), children: [], drilldown: { account_ids: ["active"], from: period.from, to: period.to } };
  const old = { id: "account:legacy", account_id: "legacy", account_code: "4010", label: "Prior-only income", role: "account", values: values("0", monthly ? "0" : "500"), children: [], drilldown: { account_ids: ["legacy"], from: period.from, to: period.to } };
  const totals = values("900", monthly ? "100" : "500");
  return { schema_version: 1, report: "profit_loss", title: "Profit & Loss", book, request, scope: { mode: "live", period, comparison, fiscal_start: "2026-01-01", ledger_revision: "same-ledger-through-september", knowledge_cutoff: "2026-10-01T00:00:00Z", generated_at: "2026-10-01T00:00:00Z", population_digest: "b".repeat(64), mapping_version: "same-presentation", hierarchy_basis: "explicit", coverage: "unknown", opening_balances: "unknown" },
    columns: [{ id: "current", label: "Current", kind: "amount", period }, { id: "prior", label: "Comparison", kind: "amount", period: comparison }, { id: "variance", label: "Change", kind: "amount", period }],
    rows: [{ id: "section:income", label: "Income", role: "section", values: totals, children: [live, ...(includePriorOnly ? [old] : [])] }, { id: "total:result", label: "Recorded result", role: "total", values: totals, children: [] }],
    checks: [], limitations: ["Population completeness unconfirmed."], reconciliation: {}, status: "partial", digest: String(monthly ? Number(request.compare_from.slice(5, 7)) : 0).padStart(64, "0"), export_csv: "" };
}
async function captured() {
  const model = await loadComponent("src/lib/financial-report-workspace.ts");
  const base = report(), ranges = model.reportMonths(range), sources = ranges.map(r => report(model.monthRequest(base, r)));
  return { model, base, ranges, sources };
}

test("nine months preserve prior-year-only lines without inventing zeroes or changing captured results", async () => {
  const { model, base, ranges, sources } = await captured(), before = JSON.stringify([base, ...sources]);
  const display = model.composeMonthDisplay(base, ranges, sources), old = base.rows[0].children[1];
  assert.equal(display.columns.length, 12);
  for (let i = 0; i < 9; i++) {
    const absent = model.displayCell(display, old, display.columns[i]);
    assert.equal(absent.value, undefined); assert.equal(absent.omitted, true); assert.equal(absent.report, sources[i]);
    assert.equal(model.displayCell(display, base.rows[0].children[0], display.columns[i]).value, "100");
  }
  assert.deepEqual(display.columns.slice(9).map(c => model.displayCell(display, old, c).value), ["0", "500", "-500"]);
  const csv = model.displayedReportCSV(display), line = csv.split("\r\n").find(r => r.startsWith('"  Prior-only income",'));
  assert.deepEqual(line.split(",").slice(1), [...Array(9).fill('""'), '"0.00"', '"5.00"', '"-5.00"']);
  assert.equal(csv.split("\r\n").filter(r => r.startsWith('"Omitted line",')).length, 9);
  assert.ok(csv.includes(model.omittedAmountNote));
  assert.equal(JSON.stringify([base, ...sources]), before);
});

test("canonical zeroes remain distinguishable from omitted amounts and source ordering does not change the presentation", async () => {
  const { model, base, ranges, sources } = await captured();
  sources[0] = report(model.monthRequest(base, ranges[0]), true);
  sources[0].rows[0].children.reverse();
  const display = model.composeMonthDisplay(base, ranges, sources), old = base.rows[0].children[1];
  assert.equal(model.displayCell(display, old, display.columns[0]).value, "0");
  assert.equal(model.displayCell(display, old, display.columns[0]).omitted, undefined);
  assert.equal(model.displayCell(display, old, display.columns[1]).omitted, true);
  assert.equal(display.rows, base.rows);
});

test("month columns and a separate prior-year comparison share one captured Book state", async () => {
  const { model, base, ranges, sources } = await captured();
  base.state_revision = "a".repeat(64);
  for (const source of sources) {
    source.state_revision = base.state_revision;
    source.scope.ledger_revision = "current-period-ledger";
    source.scope.population_digest = "c".repeat(64);
  }
  const display = model.composeMonthDisplay(base, ranges, sources);
  assert.equal(display.columns[0].source, sources[0]);
  assert.equal(display.columns.at(-1).source, base);
  sources[0].state_revision = "d".repeat(64);
  assert.throws(() => model.composeMonthDisplay(base, ranges, sources));
  sources[0].state_revision = base.state_revision;
  sources[0].scope.population_digest = "d".repeat(64);
  assert.throws(() => model.composeMonthDisplay(base, ranges, sources));
  sources[0].scope.population_digest = sources[1].scope.population_digest;
  for (const source of [base, ...sources]) delete source.state_revision;
  assert.throws(() => model.composeMonthDisplay(base, ranges, sources));
});

test("twelve Balance Sheet columns retain as-of semantics and optional prior-year equity detail", async () => {
  const { model } = await captured(), year = { from: "2026-01-01", to: "2026-12-31" };
  const request = { book_id: book.id, report: "balance_sheet", as_of: year.to, currency: "USD", comparison: "explicit", compare_to: "2025-12-31" };
  const position = req => {
    const value = report(), base = req.compare_to === "2025-12-31";
    value.report = "balance_sheet"; value.title = "Balance Sheet"; value.request = req;
    value.scope.period = { to: req.as_of }; value.scope.comparison = { to: req.compare_to };
    value.columns = [{ id: "current", label: "Current", kind: "amount", period: value.scope.period }, { id: "prior", label: "Comparison", kind: "amount", period: value.scope.comparison }, { id: "variance", label: "Change", kind: "amount", period: value.scope.period }];
    const prior = base ? "800" : req.compare_to === year.to ? "900" : String(Number(req.compare_to.slice(5, 7)) * 50);
    value.rows = [{ id: "section:equity", label: "Equity", role: "section", values: { current: "900", prior, variance: String(900n - BigInt(prior)) }, children: [
      { id: "account:capital", account_id: "capital", label: "Capital", role: "account", values: { current: "900", prior: base ? "500" : prior, variance: base ? "400" : String(900n - BigInt(prior)) }, children: [] },
      ...(base ? [{ id: "equity:prior_result", label: "Prior fiscal result", role: "total", values: { current: "0", prior: "300", variance: "-300" }, children: [] }] : []),
    ] }];
    return value;
  };
  const base = position(request), ranges = model.reportMonths(year), sources = ranges.map(r => position(model.monthRequest(base, r)));
  const display = model.composeMonthDisplay(base, ranges, sources), line = base.rows[0].children[1];
  assert.equal(display.columns.length, 14);
  assert.ok(display.columns.slice(0, 12).every(c => !c.period.from));
  assert.equal(display.columns[11].period.to, year.to);
  assert.equal(model.displayCell(display, base.rows[0].children[0], display.columns[11]).value, "900");
  assert.ok(display.columns.slice(0, 12).every(c => model.displayCell(display, line, c).omitted));
  assert.equal(model.displayCell(display, line, display.columns[12]).value, "300");
});

test("sparse alignment still rejects changed identities, parents, active lines, totals, populations and month scopes", async () => {
  const changes = [
    r => r.rows[0].children = [],
    r => r.rows.pop(),
    r => r.rows[0].children[0].label = "Changed identity",
    r => r.rows[0].children[0].account_id = "other-account",
    r => r.rows[0].children[0].account_code = "9999",
    r => r.rows[1].children.push(r.rows[0].children.pop()),
    r => r.rows[0].children.push({ ...structuredClone(r.rows[0].children[0]), id: "account:extra", account_id: "extra", values: { current: "0", prior: "25", variance: "-25" } }),
    r => r.scope.ledger_revision = "different-population",
    r => r.scope.mapping_version = "different-presentation",
    r => r.scope.period.to = "2026-10-31",
    r => r.scope.comparison.to = "2026-01-30",
  ];
  for (const change of changes) {
    const { model, base, ranges, sources } = await captured(); change(sources[0]);
    assert.throws(() => model.composeMonthDisplay(base, ranges, sources));
  }
});

test("a future comparison anchor may include zero-valued month detail outside the displayed base population", async () => {
  const { model } = await captured();
  const base = report({ ...baseRequest, compare_from: "2026-11-01", compare_to: "2026-12-31" }), ranges = model.reportMonths(range);
  const sources = ranges.map(r => report(model.monthRequest(base, r)));
  sources[0].rows[0].children.push({ id: "account:october", account_id: "october", label: "October-only line", role: "account", values: { current: "100", prior: "0", variance: "100" }, children: [] });
  const display = model.composeMonthDisplay(base, ranges, sources);
  assert.equal(display.rows, base.rows);
  assert.ok(!model.displayedReportCSV(display).includes("October-only line"));
  sources[0].rows[0].children.at(-1).values.prior = "1";
  assert.throws(() => model.composeMonthDisplay(base, ranges, sources));
});

test("wide print panels retain every exact column, omission and source reference once", async () => {
  const { model, base, ranges, sources } = await captured();
  const display = model.composeMonthDisplay(base, ranges, sources), before = JSON.stringify([base, ...sources]);
  const panels = model.printReportDisplays(display);
  assert.equal(panels.length, 3);
  assert.deepEqual(panels.map(p => p.columns.length), [4, 4, 4]);
  assert.deepEqual(panels.flatMap(p => p.columns), display.columns);
  for (const panel of panels) {
    assert.equal(panel.rows, display.rows); assert.equal(panel.sources, display.sources);
    for (const row of [base.rows[0], ...base.rows[0].children, base.rows[1]])
      for (const column of panel.columns) assert.deepEqual(model.displayCell(panel, row, column), model.displayCell(display, row, column));
  }
  assert.equal(JSON.stringify([base, ...sources]), before);
  base.rows[0].children[0].values.current = "9007199254740993";
  assert.ok(model.printReportDisplays(display).every(p => p.columns.length <= 2), "Very wide exact amounts receive more room, not smaller or rounded numbers.");
});

test("print DOM contains each captured column once, repeats identification and retains all blank-cell explanations", async () => {
  await withDOM(async ({ render }) => {
    const { model, base, ranges, sources } = await captured();
    const { ReportTable } = await loadComponent("src/components/statements/report-table.tsx");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const display = model.composeMonthDisplay(base, ranges, sources);
    await render(React.createElement(ReportTable, { display, rows: reportFlatRows(display.rows), printable: true }));
    assert.equal(document.querySelectorAll(".statement-print-panel").length, 3);
    assert.equal(document.querySelectorAll(".statement-print-table tbody td").length, 48);
    assert.equal([...document.querySelectorAll(".statement-cell-value")].filter(e => e.textContent === "—").length, 9);
    assert.deepEqual([...document.querySelectorAll("thead th[data-report-column]")].map(e => e.dataset.reportColumn), display.columns.map(c => c.id));
    for (const [i, table] of [...document.querySelectorAll(".statement-print-table")].entries()) {
      assert.match(table.querySelector("thead").textContent, /Example Reporting Co.*Profit & Loss/);
      assert.equal(table.querySelector(".statement-print-book").textContent, book.name);
      assert.equal(table.querySelector(".statement-print-title").textContent, base.title);
      assert.match(table.querySelector(".statement-print-status").textContent, /Partial ledger report · Recognition basis not established/);
      assert.ok(table.querySelector("thead").textContent.includes(`Columns ${i * 4 + 1}-${i * 4 + 4} of 12`));
      assert.equal(table.querySelector("tfoot").textContent, model.omittedAmountNote);
      assert.equal(table.dataset.reportResult, base.digest);
    }
    assert.equal(document.querySelectorAll("style[data-statement-print-pages]").length, 0, "Print rules respect the existing self-only stylesheet policy.");
    assert.equal(document.querySelectorAll("link[data-statement-print-pages]").length, 0, "The print snapshot does not depend on a newly injected stylesheet.");
  });
});

test("a declared print basis does not claim that the underlying accounting treatment is established", async () => {
  await withDOM(async ({ render }) => {
    const { model, base, ranges, sources } = await captured();
    const { ReportTable } = await loadComponent("src/components/statements/report-table.tsx");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    base.book = { ...base.book, recognition_basis: "accrual" };
    for (const source of sources) source.book = base.book;
    const display = model.composeMonthDisplay(base, ranges, sources);
    await render(React.createElement(ReportTable, { display, rows: reportFlatRows(display.rows), printable: true }));
    for (const status of document.querySelectorAll(".statement-print-status")) {
      assert.equal(status.textContent, "Partial ledger report · Declared basis: accrual");
    }
    assert.equal(document.querySelectorAll(".statement-print-table button,.statement-print-table a,.statement-print-table input").length, 0);
  });
});

test("omitted month cells explain their source, cannot copy or inspect, and retain keyboard navigation", async () => {
  await withDOM(async ({ render, window }) => {
    const { model, base, ranges, sources } = await captured();
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const inspected = [], display = model.composeMonthDisplay(base, ranges, sources);
    await render(React.createElement(FinancialReportGrid, { display, rows: reportFlatRows(display.rows), expanded: new Set(["section:income"]), onToggle() {}, onInspect: c => inspected.push(c) }));
    const cell = document.querySelector('[data-grid-row="account:legacy"][data-grid-column="month:2026-01-01:2026-01-31"]');
    await React.act(async () => { cell.click(); cell.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });
    assert.equal(cell.textContent, "—"); assert.equal(inspected.length, 0);
    assert.ok(document.querySelector(".financial-grid-selection").textContent.includes(model.omittedAmountNote));
    for (const label of ["Copy amount", "Inspect postings"]) assert.equal([...document.querySelectorAll("button")].find(b => b.textContent === label).disabled, true);
    await React.act(async () => cell.dispatchEvent(new window.KeyboardEvent("keydown", { key: "End", bubbles: true })));
    assert.equal(document.activeElement.dataset.gridColumn, "variance");
    assert.equal(document.querySelectorAll('.financial-grid-value[tabindex="0"]').length, 1);
  });
});

test("the full nine-month workspace loads by read only and has a distinct, reversible report title", async () => {
  await withDOM(async ({ render, window, unmount }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    window.history.replaceState({}, "", "/example-reporting-co/statements?report=profit_loss&from=2026-01-01&to=2026-09-30&compare=explicit&compare_from=2025-01-01&compare_to=2025-09-30&columns=months");
    document.title = "Previous application title";
    const original = globalThis.fetch, calls = [];
    globalThis.fetch = async (url, options) => {
      const target = new URL(String(url), window.location.origin);
      const request = Object.fromEntries(target.searchParams);
      calls.push(options?.method || "GET");
      let result = report(request);
      if (target.pathname.endsWith("/months")) {
        const model = await loadComponent("src/lib/financial-report-workspace.ts");
        result = { schema_version: 1, base: result, months: model.reportMonths(range).map(r => report(model.monthRequest(result, r))) };
      }
      return new Response(JSON.stringify(result), { headers: { "Content-Type": "application/json" } });
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book, state: { book }, onShowTransactions() {}, onInspect() {} }));
      // The mock server bundles a module on the months read, which can take longer than
      // a fixed pause when the whole suite runs at once: wait for the table, up to 5s.
      for (let waited = 0; waited < 5000 && document.querySelectorAll(".financial-grid tbody td").length < 48; waited += 25)
        await React.act(async () => { await new Promise(resolve => setTimeout(resolve, 25)); });
      assert.equal(document.querySelectorAll(".financial-grid tbody td").length, 48);
      assert.equal(calls.length, 1); assert.ok(calls.every(method => method === "GET"));
      assert.match(document.title, /Example Reporting Co.*Profit & Loss.*January 1, 2026.*September 30, 2026.*USD/);
      assert.ok(!document.body.textContent.includes("do not share the same account presentation"));
      await unmount(); assert.equal(document.title, "Previous application title");
    } finally { globalThis.fetch = original; }
  });
});
