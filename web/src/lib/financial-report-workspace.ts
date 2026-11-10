import { parseDate } from "@internationalized/date";
import { get, money, type Book } from "./api";
import { dateISO, formatDateRange, formatMonth } from "./date-format";
import type { DashboardRange } from "./dashboard-period";
import { financialReportCSV, periodNoteCells, reportExportAvailable } from "./financial-report-export";
import {
  assertFinancialReport, financialReportQuery, loadFinancialReport, readReportLocation, reportDefinition, reportFlatRows, reportRequest, reportViewURL,
  type FinancialReport, type FinancialReportRequest, type FinancialReportKind, type ReportLocation, type ReportRow, type ReportView,
} from "./financial-reports";

export type DisplayColumn = {
  id: string;
  label: string;
  period: { from?: string; to: string };
  source: FinancialReport;
  sourceColumn: string;
};
export type ReportDisplay = { base: FinancialReport; columns: DisplayColumn[]; rows: FinancialReport["rows"]; sources: FinancialReport[]; months: boolean; omissions?: ReadonlyMap<FinancialReport, ReadonlySet<string>> };
export type DisplayCell = { report: FinancialReport; row: ReportRow; column: string; label: string; value?: string; omitted?: boolean };
const sourceRows = new WeakMap<FinancialReport, Map<string, ReportRow>>();
export const omittedAmountNote = "— Amount not supplied for this period.";

export function reportSupportsMonths(report: ReportView) {
  return report === "balance_sheet" || report === "profit_loss";
}

/** Calendar slicing only; every amount remains a canonical backend calculation. */
export function reportMonths(range: DashboardRange): DashboardRange[] {
  if (!dateISO(range.from) || !dateISO(range.to) || range.from > range.to) throw new Error("Choose a valid reporting period.");
  const end = parseDate(range.to);
  let start = parseDate(range.from);
  const result: DashboardRange[] = [];
  while (start.compare(end) <= 0) {
    if (result.length >= 12) throw new Error("Choose a reporting period of up to twelve months.");
    const boundary = start.set({ day: 1 }).add({ months: 1 });
    const monthEnd = boundary.subtract({ days: 1 });
    result.push({ from: start.toString(), to: (monthEnd.compare(end) > 0 ? end : monthEnd).toString() });
    start = boundary;
  }
  return result;
}

export function monthViewReason(selection: ReportLocation) {
  if (!reportSupportsMonths(selection.report)) return "Month columns are available for Balance Sheet and Profit & Loss.";
  try { reportMonths(selection.range); return undefined; }
  catch { return "Choose a range of up to twelve months for month columns."; }
}

export function summaryDisplay(base: FinancialReport): ReportDisplay {
  return { base, rows: base.rows, sources: [base], months: false, columns: base.columns.map(c => ({ id: c.id, label: c.label, period: c.period, source: base, sourceColumn: c.id })) };
}

function presentation(report: FinancialReport) {
  return JSON.stringify([report.book, report.scope.mapping_version, report.scope.hierarchy_basis]);
}
function identity(report: FinancialReport) {
  return JSON.stringify([presentation(report), report.scope.ledger_revision]);
}
function presentationRows(report: FinancialReport) {
  const result = new Map<string, { row: ReportRow; parent?: string }>();
  const visit = (rows: ReportRow[], parent?: string) => {
    for (const row of rows) {
      if (result.has(row.id)) throw new Error("The month reports do not share the same account presentation.");
      result.set(row.id, { row, parent }); visit(row.children, row.id);
    }
  };
  visit(report.rows); return result;
}
function compatibleMonthRows(base: FinancialReport, source: FinancialReport) {
  const baseline = presentationRows(base), monthly = presentationRows(source), omitted = new Set<string>();
  const fail = () => { throw new Error("The month reports do not share the same account presentation."); };
  const optionalRole = (row: ReportRow) => ["account", "direct", "total"].includes(row.role);
  for (const [id, item] of baseline) {
    const other = monthly.get(id);
    if (!other) {
      // Canonical reports suppress inactive detail. A prior-only line must not
      // prevent other months from rendering, or acquire an invented zero.
      if (!item.parent || !optionalRole(item.row) || item.row.values.current !== "0") fail();
      omitted.add(id); continue;
    }
    if (item.parent !== other.parent || ["label", "role", "account_id", "account_code", "gl_code"].some(key =>
      (item.row[key as keyof ReportRow] || "") !== (other.row[key as keyof ReportRow] || ""))) fail();
  }
  for (const [id, item] of monthly) {
    if (baseline.has(id)) continue;
    // A later comparison anchor can introduce extra current-window detail.
    // Never discard an amount from the month being presented.
    if (!item.parent || !optionalRole(item.row) || item.row.values.prior !== "0" || source.scope.period.to === base.scope.period.to) fail();
  }
  return omitted;
}
export function assertCommonReportInputs(reports: FinancialReport[]) {
  if (!reports.length || reports.some(r => !r.scope.ledger_revision || !r.scope.mapping_version || identity(r) !== identity(reports[0])))
    throw new Error("The reporting ledger or presentation changed. Refresh to read a consistent report set.");
}

function assertMonthInputs(base: FinancialReport, reports: FinancialReport[]) {
  assertCommonReportInputs(reports);
  const first = reports[0];
  const comparison = base.scope.comparison;
  const period = base.scope.period;
  const comparisonExtendsPopulation = base.report === "profit_loss" && comparison?.from && period.from &&
    (comparison.from < period.from || comparison.to > period.to);
  const samePopulation = first.scope.ledger_revision === base.scope.ledger_revision &&
    first.scope.population_digest === base.scope.population_digest;
  // A comparison outside the current Profit & Loss period has a different
  // standalone population. Accept it only from the same captured Book state.
  if (!base.scope.ledger_revision || presentation(base) !== presentation(first) ||
      (!samePopulation && (!comparisonExtendsPopulation || !/^[a-f0-9]{64}$/.test(base.state_revision || ""))) ||
      reports.some(source => source.state_revision !== base.state_revision ||
        source.scope.knowledge_cutoff !== base.scope.knowledge_cutoff ||
        source.scope.generated_at !== base.scope.generated_at ||
        source.scope.population_digest !== first.scope.population_digest))
    throw new Error("The month reports do not share one captured Book state.");
}

/** Keep the current request's through-date fixed. Its explicit comparison is
 * the month we display, so each revision hashes the same authorized population,
 * rather than naturally differing because an earlier month's input ends sooner. */
export function monthRequest(base: FinancialReport, month: DashboardRange) {
  const anchor = [base.scope.period.to, base.scope.comparison?.to || ""].sort().at(-1)!;
  return {
    ...base.request,
    ...(reportDefinition(base.report).position ? { as_of: anchor } : { to: anchor }),
    comparison: "explicit" as const,
    compare_from: reportDefinition(base.report).position ? undefined : month.from,
    compare_to: month.to,
  };
}

export function composeMonthDisplay(base: FinancialReport, ranges: DashboardRange[], reports: FinancialReport[]): ReportDisplay {
  if (!reportSupportsMonths(base.report) || base.status === "unsupported" || ranges.length !== reports.length || !ranges.length)
    throw new Error("This report does not support month columns.");
  assertMonthInputs(base, reports);
  if (reports.some(r => r.report !== base.report))
    throw new Error("The month reports do not share the same account presentation.");
  const omissions = new Map<FinancialReport, ReadonlySet<string>>();
  const columns = reports.map((source, i) => {
    const period = source.scope.comparison;
    const column = source.columns.find(c => c.id === "prior");
    const anchor = monthRequest(base, ranges[i]);
    if (source.status === "unsupported" || !period || !column || period.to !== ranges[i].to ||
        source.scope.period.to !== (anchor.as_of || anchor.to) ||
        (source.scope.period.from || "") !== (base.scope.period.from || "") ||
        (!reportDefinition(base.report).position && period.from !== ranges[i].from))
      throw new Error("A month does not match the selected reporting scope.");
    const missing = compatibleMonthRows(base, source);
    if (missing.size) omissions.set(source, missing);
    return { id: `month:${ranges[i].from}:${ranges[i].to}`, label: formatMonth(ranges[i].to), period, source, sourceColumn: column.id };
  });
  if (!reportDefinition(base.report).position) columns.push({ id: "current", label: "Period total", period: base.scope.period, source: base, sourceColumn: "current" });
  for (const c of base.columns.filter(c => c.id !== "current")) columns.push({ id: c.id, label: c.label, period: c.period, source: base, sourceColumn: c.id });
  return { base, rows: base.rows, sources: [base, ...reports], months: true, columns, omissions };
}

export async function loadMonthDisplay(book: Book, request: FinancialReportRequest, range: DashboardRange, signal?: AbortSignal) {
  const ranges = reportMonths(range);
  const query = new URLSearchParams(financialReportQuery(request));
  query.set("months_from", range.from); query.set("months_to", range.to); query.set("include_csv", "false");
  const result = await get<{schema_version:number; base:FinancialReport; months:FinancialReport[]}>(`financial-reports/months?${query}`,signal);
  if (signal?.aborted) throw new DOMException("Read cancelled", "AbortError");
  if (result.schema_version !== 1 || !Array.isArray(result.months) || result.months.length !== ranges.length) throw new Error("The month reports could not be confirmed.");
  const base = assertFinancialReport(result.base,book,request);
  const reports = result.months.map((source,i)=>assertFinancialReport(source,book,monthRequest(base,ranges[i])));
  return composeMonthDisplay(base, ranges, reports);
}

export function displayCell(display: ReportDisplay, row: ReportRow, column: DisplayColumn): DisplayCell {
  if (!sourceRows.has(column.source)) sourceRows.set(column.source, new Map(reportFlatRows(column.source.rows).map(item => [item.row.id, item.row])));
  const sourceRow = column.source === display.base ? row : sourceRows.get(column.source)?.get(row.id);
  if (!sourceRow) {
    if (display.omissions?.get(column.source)?.has(row.id))
      return { report: column.source, row, column: column.sourceColumn, label: column.label, omitted: true };
    throw new Error("The selected line is not present in this report read.");
  }
  return { report: column.source, row: sourceRow, column: column.sourceColumn, label: column.label, value: sourceRow.values[column.sourceColumn] };
}

const safeCSV = (value: string) => `"${(/^(?:\s*[=+\-@]|[\t\r\n])/.test(value) ? "'" : "") + value.replaceAll('"', '""')}"`;
function decimal(value: string, scale: number) {
  const amount = BigInt(value), digits = (amount < 0n ? -amount : amount).toString().padStart(scale + 1, "0");
  return `${amount < 0n ? "-" : ""}${scale ? `${digits.slice(0, -scale)}.${digits.slice(-scale)}` : digits}`;
}
/** Re-encode captured cells only. This never sums, classifies or re-reads entries. */
export function displayedReportCSV(display: ReportDisplay) {
  if (!display.months) return financialReportCSV(display.base);
  const rows = [
    ["Report", display.base.title], ["Book", display.base.book.name], ["Currency", display.base.book.currency],
    ["View", "Month columns"], ["Status", display.sources.some(s => s.status === "partial") ? "partial" : display.base.status], ["Recognition basis", display.base.book.recognition_basis],
    ["Reporting framework", display.base.book.reporting_framework], ["Ledger revision", display.base.scope.ledger_revision],
    ["Mapping version", display.base.scope.mapping_version], ["Account", ...display.columns.map(c => c.label)],
    ["Period", ...display.columns.map(c => c.period.from ? formatDateRange(c.period.from, c.period.to, "full") : c.period.to)],
    ...display.columns.map(c => ["Column result", c.id, c.source.digest, c.source.scope.knowledge_cutoff, c.source.scope.generated_at]),
    ...(display.omissions?.size ? [["Presentation note", omittedAmountNote]] : []),
    ...(display.base.period_notes ?? []).map(periodNoteCells),
    ...display.columns.flatMap(c => [...(display.omissions?.get(c.source) || [])].map(id => ["Omitted line", id, c.id, c.source.digest])),
    ...display.sources.flatMap(source => [
      ["Source scope", source.digest, source.status, source.scope.coverage, source.scope.opening_balances],
      ...source.limitations.map(limit => ["Scope limitation", source.digest, limit]),
      ...source.checks.map(check => ["Report check", source.digest, check.id, check.status, check.message]),
    ]),
  ].map(row => row.map(safeCSV).join(","));
  for (const { row, depth } of reportFlatRows(display.rows)) {
    const values = display.columns.map(c => {
      const value = displayCell(display, row, c).value;
      return value === undefined ? "" : decimal(value, display.base.book.scale);
    });
    rows.push([safeCSV(`${"  ".repeat(depth)}${row.label}`), ...values.map(v => `"${v}"`)].join(","));
  }
  return rows.join("\r\n") + "\r\n";
}

/** Horizontal pagination only: each captured column and amount prints once. */
export function printReportDisplays(display: ReportDisplay) {
  if (display.columns.length <= 1) return [display];
  let longest = 0;
  for (const { row } of reportFlatRows(display.rows)) {
    for (const column of display.columns) {
      const value = displayCell(display, row, column).value;
      if (value !== undefined) longest = Math.max(longest, money(value, display.base.book).length);
    }
  }
  const width = longest > 20 ? 2 : longest > 14 ? 3 : 4;
  if (display.columns.length <= width) return [display];
  const result: ReportDisplay[] = [];
  for (let i = 0; i < display.columns.length; i += width)
    result.push({ ...display, columns: display.columns.slice(i, i + width) });
  return result;
}

export function downloadReportFile(content: string, filename: string, type = "application/json") {
  const url = URL.createObjectURL(new Blob([content], { type }));
  const link = document.createElement("a"); link.href = url; link.download = filename;
  document.body.append(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

export type SavedReportView = { id: string; name: string; selection: ReportLocation };
export function savedViewKey(book: Book) { return `vult-statement-views:v1:${book.id}`; }
export function readSavedReportViews(book: Book): SavedReportView[] {
  try {
    const raw = window.localStorage.getItem(savedViewKey(book)) || "[]";
    if (raw.length > 100_000) return [];
    const value = JSON.parse(raw);
    if (!Array.isArray(value)) return [];
    return value.filter(v => typeof v.id === "string" && typeof v.name === "string" && v.name.length <= 80 &&
      v.selection && !v.selection.invalid && typeof v.selection.report === "string" &&
      reportDefinition(v.selection.report as ReportView) && dateISO(v.selection.range?.from) && dateISO(v.selection.range?.to) &&
      v.selection.range.from <= v.selection.range.to &&
      !readReportLocation(new URL(reportViewURL(book, v.selection)).search, v.selection.range).invalid).slice(0, 20);
  } catch { return []; }
}
export function saveReportView(book: Book, name: string, selection: ReportLocation) {
  const clean = name.trim(); if (!clean || clean.length > 80 || selection.invalid) throw new Error("Give this view a name of up to eighty characters.");
  const views = readSavedReportViews(book).filter(v => v.name !== clean);
  const next = [{ id: crypto.randomUUID(), name: clean, selection }, ...views].slice(0, 20);
  window.localStorage.setItem(savedViewKey(book), JSON.stringify(next));
  return next;
}

export function reportPreset(preset: string, book: Book, selection: ReportLocation, report?: FinancialReport): ReportLocation {
  const end = parseDate(selection.range.to);
  let from = end.set({ day: 1 });
  let kind: FinancialReportKind = "profit_loss", columns: ReportLocation["columns"] = "summary";
  if (preset === "quarterly") {
    const fiscal = report?.scope.fiscal_start && dateISO(report.scope.fiscal_start) ? parseDate(report.scope.fiscal_start) : parseDate(`${selection.range.to.slice(0, 4)}-01-01`);
    from = fiscal;
    while (from.add({ months: 3 }).compare(end) <= 0) from = from.add({ months: 3 });
    columns = "months";
  }
  if (preset === "balance") { kind = "balance_sheet"; from = parseDate(report?.scope.fiscal_start || `${selection.range.to.slice(0, 4)}-01-01`); }
  // The view is parameter-only; no account or Book metadata is edited.
  return { report: kind, range: { from: from.toString(), to: end.toString() }, comparison: preset === "quarterly" ? "none" : "previous_period", columns, invalid: false };
}

export type DraftReportPack = {
  format: "vult-draft-report-pack-v1"; status: "draft"; title: string; commentary: string; captured_at: string;
  book_id: string; currency: string; range: DashboardRange; reports: FinancialReport[];
  monthly?: { report: FinancialReportKind; sources: FinancialReport[] };
};
export async function loadDraftReportPack(book: Book, selection: ReportLocation, kinds: FinancialReportKind[], title: string, commentary: string, signal?: AbortSignal, captured?: ReportDisplay): Promise<DraftReportPack> {
  if (!title.trim() || title.length > 120 || commentary.length > 4000 || selection.invalid) throw new Error("Confirm the report title, notes and reporting scope.");
  if (captured) {
    assertFinancialReport(captured.base, book, reportRequest(book, { ...selection, report: captured.base.report }));
    if (captured.months) assertMonthInputs(captured.base, captured.sources.slice(1));
    else assertCommonReportInputs(captured.sources);
  }
  const chosen = [...new Set(kinds)];
  if (!chosen.length || chosen.length > 7 || chosen.some(k => k === "cash_movement")) throw new Error("Choose at least one supported financial or accounting report.");
  const reports = [];
  for (const kind of chosen) {
    const location: ReportLocation = { ...selection, report: kind, columns: "summary", reviewReport: undefined,
      comparison: ["general_ledger", "journal"].includes(kind) ? "none" : selection.comparison,
      compareFrom: reportDefinition(kind).position ? undefined : selection.compareFrom };
    // Custom position and flow comparisons have different date semantics.
    if (selection.comparison === "explicit" && reportDefinition(kind).position !== reportDefinition(selection.report).position) {
      location.comparison = "none"; location.compareFrom = undefined; location.compareTo = undefined;
    }
    const result = captured?.base.report === kind ? captured.base : await loadFinancialReport(book, reportRequest(book, location), signal);
    if (result.status === "unsupported") throw new Error("A selected statement requires an established accounting treatment. Review its scope before adding it.");
    reports.push(reportExportAvailable(result) ? { ...result, export_csv: financialReportCSV(result) } : result);
  }
  assertCommonReportInputs(reports);
  if (reports.reduce((n, r) => n + reportFlatRows(r.rows).length, 0) > 10000) throw new Error("Choose a shorter period for a report pack of up to ten thousand lines.");
  const serialized = JSON.stringify({ format: "vult-draft-report-pack-v1", status: "draft", title: title.trim(), commentary, captured_at: new Date().toISOString(), book_id: book.id, currency: book.currency, range: selection.range, reports,
    ...(captured?.months && chosen.includes(captured.base.report) ? { monthly: { report: captured.base.report, sources: captured.sources.slice(1).map(source => reportExportAvailable(source) ? { ...source, export_csv: financialReportCSV(source) } : source) } } : {}) });
  if (new TextEncoder().encode(serialized).byteLength > 32_000_000) throw new Error("Choose a smaller report pack.");
  return JSON.parse(serialized);
}
