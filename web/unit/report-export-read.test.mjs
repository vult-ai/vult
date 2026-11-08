import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const goldens = JSON.parse(await readFile(new URL("./fixtures/report-exports.json", import.meta.url), "utf8"));
const { financialReportCSV, maxReportCSVBytes } = await loadComponent("src/lib/financial-report-export.ts");
const { loadFinancialReport, authorizeReportExport } = await loadComponent("src/lib/financial-reports.ts");
const { displayedReportCSV, summaryDisplay, loadDraftReportPack } = await loadComponent("src/lib/financial-report-workspace.ts");
const { StatementsWorkspace } = await loadComponent("src/components/statements/statements-workspace.tsx");
const fixture = name => structuredClone(goldens.find(v => v.name === name).result);
const response = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const compact = result => ({ ...result, export_available: true });

test("all captured browser exports match Go byte for byte, including comparison, hierarchy, omitted cells, injection protection and exact decimals", () => {
  for (const { name, result, csv } of goldens) {
    const captured = compact(result), before = JSON.stringify(captured);
    assert.equal(financialReportCSV(captured), csv, name);
    assert.equal(displayedReportCSV(summaryDisplay(captured)), csv, name);
    assert.equal(JSON.stringify(captured), before, "serialization does not mutate captured results");
    assert.throws(() => financialReportCSV({ ...captured, export_available: false }), /isn't available/);
    assert.equal(financialReportCSV({ ...captured, export_csv: csv }), csv, "legacy captured exports stay exact");
  }
});

test("CSV limit counts UTF-8 bytes and fails without a truncated download", () => {
  const result = compact(fixture("balance_sheet"));
  result.limitations = ["界".repeat(Math.floor(maxReportCSVBytes / 3) + 1)];
  assert.throws(() => financialReportCSV(result), /smaller period/);
});

test("browser reads omit eager CSV and export checks use only current scoped authority, without a fresh report", async () => {
  await withDOM(async () => {
    const result = compact(fixture("balance_sheet")), book = result.book, calls = [];
    const original = globalThis.fetch;
    globalThis.fetch = async (url, options) => {
      const parsed = new URL(url, window.location.origin); calls.push({ parsed, options });
      return response(parsed.pathname.endsWith("export-permission") ? { book_id: book.id, allowed: true } : result);
    };
    try {
      const captured = await loadFinancialReport(book, result.request);
      assert.equal(captured.export_csv, undefined);
      assert.equal(calls[0].parsed.searchParams.get("include_csv"), "false");
      assert.equal(captured.request.include_csv, undefined, "transport format is not part of the accounting request");
      await authorizeReportExport(book.id);
      assert.equal(calls.length, 2);
      assert.equal(calls[1].parsed.pathname, "/api/v1/financial-reports/export-permission");
      assert.equal(calls[1].parsed.searchParams.toString(), new URLSearchParams({ book_id: book.id }).toString());
      assert.ok(calls.every(c => !c.options?.method || c.options.method === "GET"));
      for (const rejected of [{ book_id: "other", allowed: true }, { book_id: book.id, allowed: false }]) {
        globalThis.fetch = async () => response(rejected);
        await assert.rejects(authorizeReportExport(book.id));
      }
      globalThis.fetch = async () => response({ error: "Access revoked" }, 403);
      await assert.rejects(authorizeReportExport(book.id));
    } finally { globalThis.fetch = original; }
  });
});

test("explicit packs retain exact captured exports while ordinary report views remain compact", async () => {
  await withDOM(async () => {
    const base = compact(fixture("profit_loss")), selection = { report: base.report, range: base.scope.period, comparison: "previous_year", columns: "summary", invalid: false };
    const pack = await loadDraftReportPack(base.book, selection, [base.report], "Captured pack", "Original notes", undefined, summaryDisplay(base));
    assert.equal(pack.reports[0].export_csv, goldens.find(v => v.name === "profit_loss").csv);
    assert.equal(base.export_csv, undefined);
    assert.equal(pack.reports[0].digest, base.digest);
  });
});

test("Statements exports the clicked captured result, never a second read; revoked permission keeps the view and prevents download", async () => {
  await withDOM(async ({ render, window }) => {
    const base = compact(fixture("balance_sheet"));
    window.history.replaceState({}, "", "/statements?as_of=2024-12-31&compare=previous_year");
    const original = globalThis.fetch, originalCreate = URL.createObjectURL, originalRevoke = URL.revokeObjectURL, originalClick = window.HTMLAnchorElement.prototype.click;
    const calls = [], blobs = []; let allowed = true, downloads = 0;
    URL.createObjectURL = blob => { blobs.push(blob); return "blob:captured"; };
    URL.revokeObjectURL = () => {};
    window.HTMLAnchorElement.prototype.click = () => { downloads++; };
    globalThis.fetch = async url => {
      const parsed = new URL(url, window.location.origin); calls.push(parsed);
      return parsed.pathname.endsWith("export-permission") ? response(allowed ? { book_id: base.book.id, allowed: true } : { error: "Revoked" }, allowed ? 200 : 403) : response(base);
    };
    try {
      await render(React.createElement(StatementsWorkspace, { book: base.book, state: { book: base.book }, onInspect() {}, onShowTransactions() {} }));
      await React.act(async () => document.querySelector('button[aria-label="Export report"]').click());
      const button = [...document.querySelectorAll("button")].find(v => v.textContent === "Export CSV");
      assert.ok(button && !button.disabled);
      await React.act(async () => button.click());
      assert.equal(downloads, 1);
      assert.equal(await blobs[0].text(), goldens.find(v => v.name === "balance_sheet").csv);
      allowed = false;
      await React.act(async () => button.click());
      assert.equal(downloads, 1);
      assert.equal(calls.filter(v => v.pathname === "/api/v1/financial-reports").length, 1);
      assert.ok(document.querySelector(".financial-grid"));
    } finally { globalThis.fetch = original; URL.createObjectURL = originalCreate; URL.revokeObjectURL = originalRevoke; window.HTMLAnchorElement.prototype.click = originalClick; }
  });
});
