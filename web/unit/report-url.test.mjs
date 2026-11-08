import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "example-book", name: "Example Book", currency: "USD", address: { book_id: "example-book", code: "ABCD", slug: "example-book", revision: 0, aliases: ["abcd", "example-book"] } };
const fallback = { from: "2025-10-01", to: "2026-10-29" };
const href = "http://127.0.0.1:8080/example-book/statements";

test("new Balance Sheet and P&L views default to the qualified previous-year comparison while explicit none round-trips", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  for (const report of ['balance_sheet', 'profit_loss']) {
    const automatic = m.readReportLocation(`?report=${report}&from=2026-01-01&to=2026-09-30`, fallback);
    assert.equal(automatic.comparison, 'previous_year');
    assert.equal(m.reportRequest(book, automatic).comparison, 'previous_year');
    const none = { ...automatic, comparison: 'none' };
    const clean = new URL(m.reportViewURL(book, none, href));
    assert.equal(clean.searchParams.get('compare'), 'none');
    const restored = m.readReportLocation(clean.search, fallback);
    assert.equal(restored.comparison, 'none');
    assert.deepEqual(m.reportRequest(book, restored), m.reportRequest(book, none));
  }
  for (const report of ['general_ledger', 'journal', 'trial_balance', 'cash_movement'])
    assert.equal(m.readReportLocation(`?report=${report}&from=2026-01-01&to=2026-09-30`, fallback).comparison, 'none');
});

test("legacy Balance Sheet URLs normalize to one position date without changing the accounting request", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  const legacy = new URL(href + "?report=balance_sheet&as_of=2026-09-30&from=2025-10-01&to=2026-09-30&preset=all&group=all");
  const before = m.readReportLocation(legacy.search, fallback);
  const clean = new URL(m.reportViewURL(book, before, legacy.href));
  assert.equal(clean.pathname, "/example-book/statements");
  assert.equal(clean.search, "?as_of=2026-09-30");
  const after = m.readReportLocation(clean.search, { from: "2026-09-01", to: "2026-10-29" });
  assert.deepEqual(m.reportRequest(book, after), m.reportRequest(book, before));
  assert.equal(after.invalid, false);
  assert.equal(m.reportViewURL(book, after, clean.href), clean.href, "Canonicalization must settle after one replacement.");
});

test("interval links retain exact from/to dates and their nondefault report", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  for (const report of ["profit_loss", "general_ledger", "journal", "cash_movement"]) {
    const before = m.readReportLocation(`?report=${report}&from=2026-01-01&to=2026-09-30`, fallback);
    const clean = new URL(m.reportViewURL(book, before, href + "?as_of=2026-10-29&columns=summary&compare=none"));
    assert.equal(clean.search, `?report=${report}&from=2026-01-01&to=2026-09-30`);
    const after = m.readReportLocation(clean.search, fallback);
    assert.deepEqual(m.reportRequest(book, after), m.reportRequest(book, before));
  }
});

test("monthly positions preserve their start and exact comparison while dropping duplicate ending dates", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  for (const report of ["balance_sheet"]) {
    const before = m.readReportLocation(`?report=${report}&from=2026-01-15&to=2026-09-30&as_of=2026-09-30&columns=months&compare=explicit&compare_to=2025-09-30`, fallback);
    const clean = new URL(m.reportViewURL(book, before, href));
    assert.equal(clean.searchParams.get("from"), "2026-01-15");
    assert.equal(clean.searchParams.get("to"), "2026-09-30");
    assert.equal(clean.searchParams.has("as_of"), false);
    const after = m.readReportLocation(clean.search, fallback);
    assert.deepEqual(after, before);
    assert.deepEqual(m.reportRequest(book, after), m.reportRequest(book, before));
  }
});

test("monthly position links survive shared application startup with the same exact report request", async () => {
  await withDOM(async () => {
    const m = await loadComponent("src/lib/financial-reports.ts");
    const location = await loadComponent("src/lib/dashboard-location.ts");
    const before = m.readReportLocation("?from=2026-01-15&to=2026-09-30&columns=months", fallback);
    const clean = new URL(m.reportViewURL(book, before, href));
    window.history.replaceState(null, "", clean.pathname + clean.search);
    const startup = location.readDashboardLocation("2025-10-01", "2026-10-29", book);
    assert.deepEqual(startup.range, before.range);
    const restored = m.readReportLocation(window.location.search, startup.range);
    assert.equal(restored.invalid, false);
    assert.deepEqual(restored, before);
    assert.deepEqual(m.reportRequest(book, restored), m.reportRequest(book, before));
  });
});

test("conflicting legacy dates remain rejected rather than silently cleaned into a different scope", async () => {
  const m = await loadComponent("src/lib/financial-reports.ts");
  for (const query of ["?as_of=2026-09-30&to=2026-10-01", "?as_of=2026-09-30&as_of=2026-10-01", "?report=profit_loss&from=2026-01-01&from=2026-02-01&to=2026-09-30"]) {
    const selection = m.readReportLocation(query, fallback);
    assert.equal(selection.invalid, true);
    assert.throws(() => m.reportRequest(book, selection), /valid reporting period/);
  }
});
