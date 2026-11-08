import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "grid-book", name: "Example Reporting Co", currency: "USD", scale: 2, recognition_basis: "unknown", reporting_framework: "unknown" };
const current = { from: "2026-01-01", to: "2026-09-30" }, prior = { from: "2025-01-01", to: "2025-09-30" };
function fixture(report = "balance_sheet") {
  const row = (id, label, values, children = []) => ({ id, label, role: "account", account_id: id, values, children, drilldown: { account_ids: [id], to: current.to } });
  return {
    schema_version: 1, report, title: report === "balance_sheet" ? "Balance Sheet" : "Profit & Loss", book,
    request: { book_id: book.id, report, as_of: current.to, currency: "USD", comparison: "previous_year" },
    scope: { mode: "live", period: current, comparison: prior, fiscal_start: "2026-01-01", ledger_revision: "ledger-a", mapping_version: "mapping-a", hierarchy_basis: "explicit", population_digest: "c".repeat(64), generated_at: "2026-10-01T00:00:00Z", knowledge_cutoff: "2026-10-01T00:00:00Z", coverage: "unknown", opening_balances: "unknown" },
    digest: "a".repeat(64), status: "partial", checks: [], limitations: ["Source completeness is not confirmed."], reconciliation: {},
    columns: [{ id: "current", label: "Current", kind: "amount", period: current }, { id: "prior", label: "Comparison", kind: "amount", period: prior }, { id: "variance", label: "Change", kind: "amount", period: current }],
    rows: [row("equipment", "Equipment", { current: "7000", prior: "0", variance: "7000" }, [
      row("direct", "Posted directly", { current: "1500", prior: "-500", variance: "2000" }),
      row("laptop", "Computers", { current: "5500", prior: "500", variance: "5000" }),
    ]), row("contra", "Accumulated depreciation", { current: "-500", prior: "0", variance: "-500" }), row("large", "Precise minor units", { current: "9007199254740993", prior: "0", variance: "9007199254740993" })],
    export_csv: 'Account,Current,Comparison,Change\r\nEquipment,70.00,0.00,70.00\r\n',
  };
}
const tick = () => React.act(async () => { await new Promise(resolve => setTimeout(resolve, 30)); });
const cell = (row, column) => document.querySelector(`[data-grid-row="${row}"][data-grid-column="${column}"]`);
const active = () => document.querySelector('.financial-grid-value[tabindex="0"]');
async function press(window, target, key, extra = {}) {
  await React.act(async () => target.dispatchEvent(new window.KeyboardEvent("keydown", { bubbles: true, cancelable: true, key, ...extra })));
}

// The sizing input is scoped preferences + the viewport, never mutated <col>
// geometry. JSDOM supplies the viewport; Chrome separately proves real layout.
async function sizingFixture({ render, window }, saved, viewportWidth = 1040, report = fixture()) {
  const { FinancialReportGrid, UserPreferencesProvider } = await loadComponent("unit/helpers/financial-grid-layout.tsx");
  const { browserScopeKey } = await loadComponent("src/lib/user-preferences.ts");
  const { summaryDisplay, displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
  const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
  const scope = { kind: 'user', id: 'resize-fixture' };
  const storageKey = `vult-statement-layout:v1:${browserScopeKey(scope)}:${report.book.id}:${report.report}:independent-columns-v1`;
  if (saved) window.localStorage.setItem(storageKey, JSON.stringify(saved));
  let viewport = viewportWidth;
  Object.defineProperty(window.HTMLElement.prototype, 'clientWidth', { configurable: true, get() { return this.classList.contains('financial-grid-scroll') ? viewport : 0; } });
  const display = summaryDisplay(report), csv = displayedReportCSV(display), original = JSON.stringify(report);
  const show = value => render(React.createElement(UserPreferencesProvider, { scope }, React.createElement(FinancialReportGrid, { display: value || display, rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} })));
  await show();
  const widths = () => {
    const table = document.querySelector('.financial-grid');
    const values = [...table.querySelectorAll('col')].map((_, index) => Number.parseFloat(table.style.getPropertyValue(`--financial-grid-column-${index}`)));
    // Emulate legacy CSS auto-fill as well, so these regressions fail on its
    // actual geometry/commit behavior rather than merely parsing "auto" as NaN.
    if (!Number.isFinite(values[0])) values[0] = Math.max(320, viewport - values.slice(1).reduce((a, b) => a + b, 0));
    const last = values.length - 1;
    if (!Number.isFinite(values[last])) values[last] = Math.max(viewport, Number.parseFloat(table.style.getPropertyValue('--financial-grid-min-width'))) - values.slice(0, last).reduce((a, b) => a + b, 0);
    return values;
  };
  const width = index => widths()[index];
  const attachGeometry = () => document.querySelectorAll('.financial-grid col').forEach((column, index) => { column.getBoundingClientRect = () => ({ width: widths()[index] }); });
  attachGeometry();
  return { display, csv, original, report, show, width, widths, preferences: () => window.localStorage.getItem(storageKey),
    setViewport(value) { viewport = value; window.dispatchEvent(new window.Event('resize')); },
    async resizeViewport(value) { viewport = value; await React.act(async () => window.dispatchEvent(new window.Event('resize'))); },
    async reload() { await render(null); await show(); attachGeometry(); },
  };
}
async function pointer(window, target, type, clientX, pointerId = 7) {
  const event = new window.MouseEvent(type, { bubbles: true, button: 0, clientX });
  Object.defineProperty(event, 'pointerId', { value: pointerId });
  await React.act(async () => target.dispatchEvent(event));
}

test("one tree action expands hidden nested branches before offering collapse all and preserves focus", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay, displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), nested = report.rows[0].children[1];
    nested.children = [{ ...nested, id: 'device', label: 'Device', children: [] }];
    const display = summaryDisplay(report), original = JSON.stringify(report), csv = displayedReportCSV(display), actions = [];
    const branchIds = ['equipment', 'laptop'];
    function Harness() {
      const [expanded, setExpanded] = React.useState(new Set(['equipment']));
      return React.createElement(FinancialReportGrid, { display, rows: reportFlatRows(display.rows, expanded), expanded, onToggle() {}, onInspect() {},
        onExpandAll() { actions.push('expand'); setExpanded(new Set(branchIds)); },
        onCollapseAll() { actions.push('collapse'); setExpanded(new Set()); },
      });
    }
    await render(React.createElement(Harness));
    const heading = () => document.querySelector('.financial-grid-account-heading');
    const button = heading().querySelector('button[aria-label="Expand all"],button[aria-label="Collapse all"]');
    assert.equal(heading().querySelectorAll('button[aria-label="Expand all"],button[aria-label="Collapse all"]').length, 1);
    assert.equal(button.getAttribute('aria-label'), 'Expand all');
    assert.equal(button.hasAttribute('aria-pressed'), false);
    assert.equal(cell('device', 'current'), null);
    await React.act(async () => button.focus());
    await React.act(async () => button.click());
    assert.deepEqual(actions, ['expand']);
    assert.equal(heading().querySelector('button[aria-label="Expand all"],button[aria-label="Collapse all"]'), button);
    assert.equal(document.activeElement, button);
    assert.equal(button.getAttribute('aria-label'), 'Collapse all');
    assert.ok(cell('device', 'current'));
    await React.act(async () => button.click());
    assert.deepEqual(actions, ['expand', 'collapse']);
    assert.equal(button.getAttribute('aria-label'), 'Expand all');
    assert.equal(cell('direct', 'current'), null);
    assert.equal(document.activeElement, button);
    assert.equal(displayedReportCSV(display), csv);
    assert.equal(JSON.stringify(report), original);
  });
});

test("the single tree action is disabled when the full report has no branches", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), actions = [];
    report.rows = report.rows.filter(row => !row.children.length);
    await render(React.createElement(FinancialReportGrid, { display: summaryDisplay(report), rows: reportFlatRows(report.rows), expanded: new Set(['stale-id']), onToggle() {}, onInspect() {}, onExpandAll: () => actions.push('expand'), onCollapseAll: () => actions.push('collapse') }));
    const buttons = document.querySelectorAll('.financial-grid-account-heading button[aria-label="Expand all"],.financial-grid-account-heading button[aria-label="Collapse all"]');
    assert.equal(buttons.length, 1);
    assert.equal(buttons[0].getAttribute('aria-label'), 'Expand all');
    assert.equal(buttons[0].disabled, true);
    await React.act(async () => buttons[0].click());
    assert.deepEqual(actions, []);
  });
});

test("column dividers preserve unrelated widths and exact screen/export values", async () => {
  await withDOM(async context => {
    const { window } = context;
    const { display, csv, original, report } = await sizingFixture(context, { account: 320 }, 2000);
    const { displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
    const handle = document.querySelector('[data-resize-column="prior"]'), table = document.querySelector('.financial-grid');
    const neighbor = Number(document.querySelector('[data-resize-column="current"]').getAttribute('aria-valuenow'));
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointerdown', { bubbles: true, clientX: 100, button: 0 })));
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointermove', { bubbles: true, clientX: 164 })));
    assert.equal(handle.getAttribute('aria-valuenow'), '248');
    assert.equal(table.style.getPropertyValue('--financial-grid-column-1'), `${neighbor}px`);
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointerup', { bubbles: true, clientX: 164 })));
    await press(window, handle, 'ArrowRight');
    assert.equal(handle.getAttribute('aria-valuenow'), '256');
    const large = document.querySelector('[data-resize-column="current"]');
    await press(window, large, 'Home');
    assert.equal(large.getAttribute('aria-valuenow'), large.getAttribute('aria-valuemin'));
    assert.equal(cell('large', 'current').textContent, '$90,071,992,547,409.93');
    assert.match(document.querySelector('thead th:nth-child(2)').getAttribute('aria-label'), /As of September 30, 2026/);
    assert.equal(document.querySelector('.financial-grid-column-period'), null);
    assert.equal(displayedReportCSV(display), csv);
    assert.equal(JSON.stringify(report), original);
    assert.equal(document.querySelectorAll('.financial-grid-value[tabindex="0"]').length, 1);
  });
});

test("a comparison divider keeps its original column and pointer until the drag ends", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay, displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), display = summaryDisplay(report), csv = displayedReportCSV(display), original = JSON.stringify(report);
    await render(React.createElement(FinancialReportGrid, { display, rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} }));
    const current = document.querySelector('[data-resize-column="current"]'), prior = document.querySelector('[data-resize-column="prior"]');
    const currentWidth = Number(current.getAttribute('aria-valuenow')), priorWidth = Number(prior.getAttribute('aria-valuenow'));
    const pointer = async (target, type, pointerId, clientX) => {
      const event = new window.MouseEvent(type, { bubbles: true, button: 0, clientX });
      Object.defineProperty(event, 'pointerId', { value: pointerId });
      await React.act(async () => target.dispatchEvent(event));
    };
    await pointer(current, 'pointerdown', 7, 100);
    await pointer(current, 'pointermove', 7, 132);
    await pointer(prior, 'pointerdown', 8, 300);
    await pointer(prior, 'pointermove', 8, 364);
    await pointer(prior, 'pointerup', 8, 364);
    assert.equal(current.dataset.resizing, 'true');
    assert.equal(prior.dataset.resizing, undefined);
    assert.equal(Number(current.getAttribute('aria-valuenow')), currentWidth + 32);
    assert.equal(Number(prior.getAttribute('aria-valuenow')), priorWidth);
    await pointer(current, 'pointermove', 7, 148);
    await pointer(current, 'pointerup', 7, 148);
    assert.equal(Number(current.getAttribute('aria-valuenow')), currentWidth + 48);
    assert.equal(Number(prior.getAttribute('aria-valuenow')), priorWidth);
    assert.equal(current.dataset.resizing, undefined);
    assert.equal(displayedReportCSV(display), csv);
    assert.equal(JSON.stringify(report), original);
  });
});

test("compact accounting-report headers retain Debit and Credit meaning with every data column resizable", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture('trial_balance');
    report.columns = ['debit', 'credit'].map(id => ({ id, label: id === 'debit' ? 'Debit' : 'Credit', kind: 'amount', period: report.scope.period }));
    report.rows = [{ id: 'cash', label: 'Cash', role: 'account', values: { debit: '100', credit: '0' }, children: [] }];
    await render(React.createElement(FinancialReportGrid, { display: summaryDisplay(report), rows: reportFlatRows(report.rows), expanded: new Set(), onToggle() {}, onInspect() {} }));
    assert.deepEqual([...document.querySelectorAll('.financial-grid-header-label')].map(e => e.textContent), ['Debit', 'Credit']);
    assert.match(document.querySelector('[data-resize-column="debit"]').getAttribute('aria-label'), /Resize Debit, As of/);
    assert.ok(document.querySelector('[data-resize-column="credit"]'));
    assert.equal(cell('cash', 'debit').textContent, '$1.00');
    assert.equal(cell('cash', 'credit').textContent, '$0.00');
  });
});

test("the terminal amount column uses the shared resize control in comparative and single-column views", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), display = summaryDisplay(report);
    const props = { rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} };
    await render(React.createElement(FinancialReportGrid, { ...props, display }));
    assert.deepEqual([...document.querySelectorAll('[data-resize-column]')].map(e => e.dataset.resizeColumn), ['account', 'current', 'prior', 'variance']);
    assert.ok(document.querySelector('.financial-grid thead th:last-child [role="separator"]'));
    assert.equal(document.querySelectorAll('.financial-grid thead button[aria-label="Columns"]').length, 1);
    assert.equal(document.querySelectorAll('.financial-grid-selection button[aria-label="Columns"]').length, 0);
    await render(React.createElement(FinancialReportGrid, { ...props, display: { ...display, columns: [display.columns[0]] } }));
    assert.deepEqual([...document.querySelectorAll('[data-resize-column]')].map(e => e.dataset.resizeColumn), ['account', 'current']);
    assert.ok(document.querySelector('.financial-grid thead th:last-child [role="separator"]'));
  });
});

test("cancelled resize restores geometry and resetting a divider restores its preferred width", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture();
    await render(React.createElement(FinancialReportGrid, { display: summaryDisplay(report), rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} }));
    const handle = document.querySelector('[data-resize-column="account"]'), table = document.querySelector('.financial-grid'), initial = table.style.cssText;
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointerdown', { bubbles: true, clientX: 100, button: 0 })));
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointermove', { bubbles: true, clientX: 0 })));
    assert.equal(handle.getAttribute('aria-valuenow'), '220');
    await React.act(async () => handle.dispatchEvent(new window.MouseEvent('pointercancel', { bubbles: true })));
    assert.equal(table.style.cssText, initial);
    await press(window, handle, 'ArrowLeft');
    assert.equal(handle.getAttribute('aria-valuenow'), '312');
    await press(window, handle, 'Enter');
    assert.equal(handle.getAttribute('aria-valuenow'), '320');
  });
});

test("saved column widths restore before paint and remain isolated by explicit User and Book", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid, UserPreferencesProvider } = await loadComponent("unit/helpers/financial-grid-layout.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture();
    const view = (user, book = report.book.id) => {
      const value = { ...report, book: { ...report.book, id: book } };
      return React.createElement(UserPreferencesProvider, { scope: { kind: 'user', id: user } }, React.createElement(FinancialReportGrid, { display: summaryDisplay(value), rows: reportFlatRows(value.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} }));
    };
    const account = () => document.querySelector('[data-resize-column="account"]');
    await render(view('user-a'));
    await press(window, account(), 'ArrowLeft');
    assert.equal(account().getAttribute('aria-valuenow'), '312');
    await render(view('user-b'));
    assert.equal(account().getAttribute('aria-valuenow'), '320');
    await render(view('user-a', 'another-book'));
    assert.equal(account().getAttribute('aria-valuenow'), '320');
    await render(view('user-a'));
    assert.equal(account().getAttribute('aria-valuenow'), '312');
    assert.equal(document.querySelector('.financial-grid').style.getPropertyValue('--financial-grid-column-0'), '312px');
  });
});

test("resizing a single-column view preserves hidden comparison-column widths", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), display = summaryDisplay(report);
    const props = { rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} };
    const show = value => render(React.createElement(FinancialReportGrid, { ...props, display: value }));
    await show(display);
    const prior = document.querySelector('[data-resize-column="prior"]');
    await press(window, prior, 'ArrowLeft');
    const width = prior.getAttribute('aria-valuenow');
    await show({ ...display, columns: [display.columns[0]] });
    await press(window, document.querySelector('[data-resize-column="account"]'), 'ArrowLeft');
    await show(display);
    assert.equal(document.querySelector('[data-resize-column="prior"]').getAttribute('aria-valuenow'), width);
    assert.equal(document.querySelector('[data-resize-column="account"]').getAttribute('aria-valuenow'), '312');
  });
});

test("month headings group actual years while retaining exact position dates, row indices and captured amounts", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay, displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), display = summaryDisplay(report), original = JSON.stringify(report);
    display.months = true;
    display.columns = ["2025-12-31", "2026-01-31", "2026-02-15"].map(to => ({ id: `month:${to.slice(0, 7)}-01:${to}`, label: to, period: { to }, source: report, sourceColumn: "current" }));
    const beforeCSV = displayedReportCSV(display), rows = reportFlatRows(report.rows);
    await render(React.createElement(FinancialReportGrid, { display, rows, expanded: new Set(["equipment"]), onToggle() {}, onInspect() {} }));
    assert.deepEqual([...document.querySelectorAll('th[scope="colgroup"]')].map(e => [e.textContent, e.colSpan]), [["2025", 1], ["2026", 2]]);
    const dates = [...document.querySelectorAll('.financial-grid-month-heading th')];
    assert.match(dates[2].getAttribute("aria-label"), /As of February 15, 2026/);
    assert.match(dates[2].getAttribute("title"), /February 15, 2026/);
    assert.equal(document.querySelector('.financial-grid').getAttribute('aria-rowcount'), String(rows.length + 2));
    assert.equal(document.querySelector('tbody tr').getAttribute('aria-rowindex'), '3');
    assert.equal(cell('large', display.columns[2].id).textContent, '$90,071,992,547,409.93');
    assert.equal(displayedReportCSV(display), beforeCSV);
    assert.equal(JSON.stringify(report), original);
  });
});

test("clipped account labels reveal their complete assigned code and name on keyboard focus without adding grid tab stops", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture();
    report.rows[0].label = 'Long-lived equipment and software assets with a complete historical account description';
    report.rows[0].gl_code = '0100';
    await render(React.createElement(FinancialReportGrid, { display: summaryDisplay(report), rows: reportFlatRows(report.rows), expanded: new Set(['equipment']), onToggle() {}, onInspect() {} }));
    const label = document.querySelector('[data-report-row="equipment"] .financial-grid-label-text');
    Object.defineProperties(label, { clientWidth: { configurable: true, value: 100 }, scrollWidth: { configurable: true, value: 680 } });
    await React.act(async () => cell('equipment', 'prior').focus());
    const tooltip = document.querySelector('[role="tooltip"]');
    assert.ok(tooltip);
    assert.equal(tooltip.textContent, `0100 · ${report.rows[0].label}`);
    assert.equal(document.querySelector('[data-report-row="equipment"] .financial-grid-label').getAttribute('tabindex'), '-1');
    assert.equal(document.querySelectorAll('.financial-grid-value[tabindex="0"]').length, 1);
    assert.match(cell('equipment', 'prior').getAttribute('aria-label'), /complete historical account description/);
    await React.act(async () => cell('contra', 'prior').focus());
    assert.equal(document.querySelector('[role="tooltip"]'), null);
  });
});

test("financial grid is read-only, preserves exact signed values and uses position dates", async () => {
  await withDOM(async ({ render }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture();
    report.rows[0].gl_code = "0100";
    const display = summaryDisplay(report);
    await render(React.createElement(FinancialReportGrid, { display, rows: reportFlatRows(report.rows), expanded: new Set(["equipment"]), onToggle() {}, onInspect() {} }));
    assert.equal(document.querySelector('[role="grid"]').getAttribute("aria-readonly"), "true");
    assert.equal(document.querySelectorAll('.financial-grid-value[tabindex="0"]').length, 1);
    assert.equal(document.querySelectorAll("input, textarea, [contenteditable]").length, 0);
    assert.equal(cell("direct", "prior").textContent, "−$5.00");
    assert.equal(cell("contra", "prior").textContent, "$0.00");
    assert.equal(cell("large", "current").textContent, "$90,071,992,547,409.93");
    assert.match(cell("laptop", "current").getAttribute("aria-label"), /As of September 30, 2026/);
    assert.doesNotMatch(document.querySelector("thead").textContent, /January/);
    assert.equal(document.querySelectorAll("thead th").length, 4);
    assert.match(document.querySelector('[data-report-row="equipment"]').textContent, /0100 · Equipment/);
    const { ReportTable } = await loadComponent("src/components/statements/report-table.tsx");
    await render(React.createElement(ReportTable, { display, rows: reportFlatRows(report.rows), printable: true }));
    assert.match(document.querySelector('[data-report-row="equipment"]').textContent, /0100 · Equipment/);
    assert.match(document.querySelector('[data-report-row="large"]').textContent, /90,071,992,547,409\.93/);
    assert.doesNotMatch(document.querySelector("thead").textContent, /%/);
  });
});

test("arrows, row/page bounds, exact clipboard copy and Enter preserve selected column and result", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const copied = [], inspected = [], report = fixture();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async value => copied.push(value) } });
    await render(React.createElement(FinancialReportGrid, { display: summaryDisplay(report), rows: reportFlatRows(report.rows), expanded: new Set(["equipment"]), onToggle() {}, onInspect: (value, trigger) => inspected.push({ value, trigger }) }));
    await React.act(async () => active().focus());
    await press(window, active(), "ArrowRight");
    assert.equal(document.activeElement, cell("equipment", "prior"));
    await press(window, active(), "ArrowDown");
    assert.equal(document.activeElement, cell("direct", "prior"));
    await press(window, active(), "c", { metaKey: true });
    const nativeCopy = new window.Event("copy", { bubbles: true, cancelable: true });
    Object.defineProperty(nativeCopy, "clipboardData", { value: { setData: (type, value) => { assert.equal(type, "text/plain"); copied.push(value); } } });
    await React.act(async () => active().dispatchEvent(nativeCopy));
    assert.equal(nativeCopy.defaultPrevented, true);
    assert.deepEqual(copied, ["−$5.00"]);
    await press(window, active(), "Enter");
    assert.equal(inspected[0].value.report, report);
    assert.equal(inspected[0].value.row.id, "direct");
    assert.equal(inspected[0].value.column, "prior");
    assert.equal(inspected[0].value.value, "-500");
    assert.equal(inspected[0].trigger, cell("direct", "prior"));
    await press(window, active(), "End");
    assert.equal(document.activeElement, cell("direct", "variance"));
    await press(window, active(), "Home");
    assert.equal(document.activeElement, cell("direct", "current"));
    await press(window, active(), "End", { ctrlKey: true });
    assert.equal(document.activeElement, cell("large", "variance"));
    await press(window, active(), "ArrowDown"); await press(window, active(), "ArrowRight");
    assert.equal(document.activeElement, cell("large", "variance"));
    await press(window, active(), "Home", { metaKey: true });
    assert.equal(document.activeElement, cell("equipment", "current"));
    await press(window, active(), "ArrowUp"); await press(window, active(), "ArrowLeft");
    assert.equal(document.activeElement, cell("equipment", "current"));
    assert.equal(document.querySelectorAll('.financial-grid-value[tabindex="0"]').length, 1);
  });
});

test("collapsing an inspected descendant selects its visible parent; rejected clipboard has a read-only fallback", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { summaryDisplay } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture(), display = summaryDisplay(report);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async () => { throw new Error("Denied"); } } });
    function Harness() {
      const [expanded, expand] = React.useState(new Set(["equipment"]));
      return React.createElement(FinancialReportGrid, { display, rows: reportFlatRows(report.rows, expanded), expanded, onToggle: () => expand(new Set()), onInspect() {} });
    }
    await render(React.createElement(Harness));
    await React.act(async () => cell("laptop", "prior").focus());
    await React.act(async () => document.querySelector('[aria-label="Collapse Equipment"]').click());
    assert.equal(cell("laptop", "prior"), null);
    assert.equal(active(), cell("equipment", "prior"));
    await React.act(async () => [...document.querySelectorAll("button")].find(button => button.textContent === "Copy amount").click()); await tick();
    const fallback = document.querySelector('input[readonly]');
    assert.ok(fallback);
    assert.equal(fallback.value, "$0.00");
    assert.equal(fallback.selectionEnd, fallback.value.length);
  });
});

test("period columns, captured month inspection and print values share the canonical display", async () => {
  await withDOM(async ({ render, window }) => {
    const { FinancialReportGrid } = await loadComponent("src/components/statements/financial-report-grid.tsx");
    const { ReportTable } = await loadComponent("src/components/statements/report-table.tsx");
    const { summaryDisplay, displayedReportCSV } = await loadComponent("src/lib/financial-report-workspace.ts");
    const { reportFlatRows } = await loadComponent("src/lib/financial-reports.ts");
    const report = fixture("profit_loss"), captured = structuredClone(report);
    captured.digest = "b".repeat(64); captured.scope.comparison = { from: "2026-02-01", to: "2026-02-28" };
    const display = summaryDisplay(report), inspected = [];
    display.months = true;
    display.sources.push(captured);
    display.columns.unshift({ id: "month:2026-02-01:2026-02-28", label: "Feb", source: captured, sourceColumn: "prior", period: captured.scope.comparison });
    const rows = reportFlatRows(report.rows);
    await render(React.createElement(React.Fragment, null, React.createElement(FinancialReportGrid, { display, rows, expanded: new Set(["equipment"]), onToggle() {}, onInspect: value => inspected.push(value) }), React.createElement(ReportTable, { display, rows, printable: true })));
    assert.match(cell("direct", "current").getAttribute("aria-label"), /January 1, 2026 – September 30, 2026/);
    const feb = cell("direct", "month:2026-02-01:2026-02-28");
    await React.act(async () => feb.focus()); await press(window, feb, "Enter");
    assert.equal(inspected[0].report, captured);
    assert.equal(inspected[0].column, "prior");
    assert.equal(inspected[0].report.digest, "b".repeat(64));
    const printed = [...document.querySelectorAll(".statement-print-table tbody tr")];
    for (const { row } of rows) {
      const pieces = printed.filter(item => item.dataset.reportRow === row.id);
      assert.deepEqual(pieces.flatMap(tr => [...tr.querySelectorAll("td")].map(td => td.textContent)), display.columns.map(c => cell(row.id, c.id).textContent));
    }
    const csv = displayedReportCSV(display);
    assert.ok(csv.includes("2026-02-28"));
    assert.ok(csv.includes(captured.digest));
    assert.ok(csv.includes("-5.00"));
    assert.ok(csv.includes("90071992547409.93"));
  });
});

test("the existing entry overlay preserves the contribution panel, selected comparison, expansion and scroll", async () => {
  await withDOM(async ({ render, window }) => {
    const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
    const { ContextualEntryPreview } = await loadComponent("src/components/book/entry-preview.tsx");
    const report = fixture(), actualBook = { ...book, start_date: "2026-01-01", accounting_today: "2026-09-30", accounting_timezone: "UTC", address: { book_id: book.id, code: "GRID", slug: "example-reporting-co", revision: 0, aliases: ["grid", "example-reporting-co"] } };
    const state = { book: actualBook }, original = globalThis.fetch, calls = [];
    window.history.replaceState({}, "", "/example-reporting-co/statements?report=balance_sheet&as_of=2026-09-30&compare=previous_year");
    globalThis.fetch = async (url, options) => {
      const target = new URL(String(url), window.location.origin);
      calls.push({ target, method: options?.method || "GET" });
      const value = target.pathname.endsWith("/contributions") ? {
        result_digest: report.digest, row_id: "direct", column_id: "prior", total_minor: "-500", debit_minor: "0", credit_minor: "500",
        contributions: [{ posting_id: "posting-a", entry_id: "entry-a", account_id: "direct", date: "2025-09-01", side: "credit", amount_minor: "500", signed_amount_minor: "-500", report_amount_minor: "-500", column_id: "prior", purpose: "ordinary" }],
      } : report;
      return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
    };
    function Harness() {
      const [entry, open] = React.useState(), [trigger, setTrigger] = React.useState();
      return React.createElement(React.Fragment, null,
        React.createElement(StatementsWorkspace, { book: actualBook, state, onShowTransactions() {}, onInspect: (id, target) => { setTrigger(target); open(id); } }),
        React.createElement(ContextualEntryPreview, { id: entry || "", pending: false, returnTo: trigger, onClose: () => open(undefined) }, id => React.createElement("p", null, `Synthetic entry ${id}`)));
    }
    try {
      await render(React.createElement(Harness)); await tick();
      await React.act(async () => document.querySelector('[aria-label="Expand Equipment"]').click());
      const selected = cell("direct", "prior"), scroll = document.querySelector(".financial-grid-scroll");
      scroll.scrollLeft = 61; scroll.scrollTop = 180;
      await React.act(async () => selected.focus()); await press(window, selected, "Enter"); await tick(); await tick();
      const inspector = document.querySelector(".statement-inspector"), content = inspector.querySelector(".statement-inspector-content");
      content.scrollTop = 96;
      const entryLink = inspector.querySelector('[aria-label="Inspect contributing entry 1"]');
      await React.act(async () => entryLink.click()); await tick();
      assert.ok(document.querySelector('[role="dialog"]').textContent.includes("Synthetic entry entry-a"));
      const close = document.querySelector('[role="dialog"] [aria-label="Close dialog"]');
      assert.ok(close, "existing entry dialog close control");
      await React.act(async () => close.click()); await tick(); await tick();
      assert.equal(document.querySelector(".statement-inspector"), inspector);
      assert.equal(content.scrollTop, 96);
      assert.equal(active(), selected);
      assert.equal(document.querySelector('[aria-label="Collapse Equipment"]').getAttribute("aria-expanded"), "true");
      assert.equal(calls.filter(call => call.target.pathname.endsWith("/contributions")).length, 1);
      await React.act(async () => document.querySelector('[aria-label="Close work panel"]').click()); await tick(); await tick();
      assert.equal(document.activeElement, selected);
      assert.equal(scroll.scrollLeft, 61); assert.equal(scroll.scrollTop, 180);
      assert.equal(document.querySelector('.statement-work-panel').hidden, true);
      assert.equal(document.querySelector('.statement-inspector'), inspector, "Closing the work panel keeps its captured contributions mounted");
      assert.ok(calls.every(call => call.method === "GET"));
    } finally { globalThis.fetch = original; }
  });
});

test("clicks, round-trip drags and viewport changes preserve stable default widths without writing preferences", async () => {
  await withDOM(async context => {
    const report = fixture(); report.rows = report.rows.slice(0, 1);
    const view = await sizingFixture(context, undefined, 1200, report), before = view.widths(), preferences = view.preferences();
    const handle = document.querySelector('[data-resize-column="current"]');
    await pointer(context.window, handle, 'pointerdown', 100); await pointer(context.window, handle, 'pointerup', 100);
    assert.deepEqual(view.widths(), before); assert.equal(view.preferences(), preferences);
    await pointer(context.window, handle, 'pointerdown', 100); await pointer(context.window, handle, 'pointermove', 116); await pointer(context.window, handle, 'pointerup', 100);
    assert.deepEqual(view.widths(), before); assert.equal(view.preferences(), preferences);
    await view.resizeViewport(1400); assert.deepEqual(view.widths(), before);
  });
});

test("same-task viewport change and pointerup cannot save an invalidated capture", async () => {
  await withDOM(async context => {
    const report = fixture(); report.rows = report.rows.slice(0, 1);
    const view = await sizingFixture(context, { account: 320, current: 220, prior: 220, variance: 240 }, 1040, report);
    const saved = view.preferences(), handle = document.querySelector('[data-resize-column="current"]');
    await pointer(context.window, handle, 'pointerdown', 100);
    await React.act(async () => {
      view.setViewport(900);
      const event = new context.window.MouseEvent('pointerup', { bubbles: true, clientX: 164 });
      Object.defineProperty(event, 'pointerId', { value: 7 });
      handle.dispatchEvent(event);
    });
    assert.equal(view.preferences(), saved); assert.equal(document.querySelector('[data-resizing]'), null);
    assert.deepEqual(view.widths(), [320, 220, 220, 240]);
  });
});

test("Activity parity: each divider changes only its own width through release and reload", async () => {
  await withDOM(async context => {
    const report=fixture();report.rows=report.rows.slice(0,1);
    const view=await sizingFixture(context,{account:575,current:198,prior:177,variance:147},1040,report);
    for(const [index,id] of ['account','current','prior'].entries()){
      const before=view.widths(),handle=document.querySelector(`[data-resize-column="${id}"]`);
      await pointer(context.window,handle,'pointerdown',100);
      await pointer(context.window,handle,'pointermove',92);
      await pointer(context.window,handle,'pointerup',84);
      const expected=before.map((w,i)=>i===index?w-16:w);
      assert.deepEqual(view.widths(),expected);
      assert.equal(Number.parseFloat(document.querySelector('.financial-grid').style.getPropertyValue('--financial-grid-width')),expected.reduce((a,b)=>a+b,0));
      await view.reload();assert.deepEqual(view.widths(),expected);
    }
    const saved=view.widths();await view.resizeViewport(2000);assert.deepEqual(view.widths(),saved);
    const {displayedReportCSV}=await loadComponent('src/lib/financial-report-workspace.ts');
    assert.equal(displayedReportCSV(view.display),view.csv);assert.equal(JSON.stringify(report),view.original);
  });
});

test("reset shrinks a bloated date column instead of transferring its width into another column", async () => {
  await withDOM(async context => {
    const report=fixture();report.rows=report.rows.slice(0,1);
    const view=await sizingFixture(context,{account:320,current:982,prior:177,variance:147},1040,report);
    await press(context.window,document.querySelector('[data-resize-column="current"]'),'Enter');
    assert.deepEqual(view.widths(),[320,184,177,147]);
    await view.reload();assert.deepEqual(view.widths(),[320,184,177,147]);
    const saved=view.preferences(),handle=document.querySelector('[data-resize-column="current"]');
    await pointer(context.window,handle,'pointerdown',100);await pointer(context.window,handle,'pointermove',132);
    await view.resizeViewport(900);await pointer(context.window,handle,'pointerup',132);
    assert.deepEqual(view.widths(),[320,184,177,147]);assert.equal(view.preferences(),saved);
  });
});

test("new shared layout starts at stable preferred widths while retaining the old coupled preferences unchanged", async () => {
  await withDOM(async context => {
    const {browserScopeKey}=await loadComponent('src/lib/user-preferences.ts');
    const key=`vult-statement-layout:v1:${browserScopeKey({kind:'user',id:'resize-fixture'})}:grid-book:balance_sheet:columns`;
    const legacy=JSON.stringify({account:320,current:982,prior:177,variance:147});context.window.localStorage.setItem(key,legacy);
    const report=fixture();report.rows=report.rows.slice(0,1);
    const view=await sizingFixture(context,undefined,1040,report);
    assert.deepEqual(view.widths(),[320,184,184,184]);
    await press(context.window,document.querySelector('[data-resize-column="prior"]'),'ArrowRight');
    assert.deepEqual(view.widths(),[320,184,192,184]);
    await view.reload();assert.deepEqual(view.widths(),[320,184,192,184]);
    assert.equal(context.window.localStorage.getItem(key),legacy);
  });
});

test("the always-available header menu fits and resets persisted Statements widths without changing financial data", async () => {
  await withDOM(async context => {
    const report = fixture(); report.rows = report.rows.slice(0, 1);
    const original = JSON.stringify(report);
    const view = await sizingFixture(context, { account: 320, current: 220, prior: 220, variance: 240 }, 1040, report);
    const choose = async label => {
      const trigger = document.querySelector('.financial-grid thead button[aria-label="Columns"]');
      assert.ok(trigger);
      await React.act(async () => trigger.click());
      const action = [...document.querySelectorAll('[role="menuitem"]')].find(element => element.textContent.includes(label));
      assert.ok(action);
      await React.act(async () => action.click());
    };
    await choose("Fit columns");
    assert.ok(Math.abs(view.widths().reduce((sum, width) => sum + width, 0) - 1040) < 0.001);
    const fitted = view.widths(); await view.reload(); assert.deepEqual(view.widths(), fitted);
    await choose("Reset column"); assert.deepEqual(view.widths(), [320, 184, 184, 184]);
    await view.reload(); assert.deepEqual(view.widths(), [320, 184, 184, 184]);
    assert.equal(JSON.stringify(report), original);
  });
});
