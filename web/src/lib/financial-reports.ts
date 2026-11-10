import { bookURL, get, type Book } from "./api";
import { canonicalBookURL } from "./book-routing";
import { dateISO } from "./date-format";
import type { DashboardRange } from "./dashboard-period";
import type { RecordedState } from "./dashboard-projection";
import { financialReportCSV } from "./financial-report-export";

export const reportDefinitions = [
  { id: "balance_sheet", label: "Balance Sheet", group: "Financial statements", position: true },
  { id: "profit_loss", label: "Profit & Loss", group: "Financial statements", position: false },
  { id: "cash_flows", label: "Cash flow", group: "Financial statements", position: false },
  { id: "trial_balance", label: "Trial Balance", group: "Accounting reports", position: true },
  { id: "general_ledger", label: "General Ledger", group: "Accounting reports", position: false },
  { id: "journal", label: "Journal", group: "Accounting reports", position: false },
  { id: "cash_movement", label: "Cash movement", group: "Supporting views", position: false },
] as const;
export type ReportView = (typeof reportDefinitions)[number]["id"];
export type FinancialReportKind = Exclude<ReportView, "notes_review">;
export type ReportComparison = "none" | "previous_period" | "previous_year" | "explicit";
export type ReportLocation = {
  report: ReportView;
  range: DashboardRange;
  comparison: ReportComparison;
  compareFrom?: string;
  compareTo?: string;
  reviewReport?: FinancialReportKind;
  columns?: "summary" | "months";
  invalid: boolean;
};
export type FinancialReportRequest = {
  book_id: string;
  report: FinancialReportKind;
  from?: string;
  to?: string;
  as_of?: string;
  currency?: string;
  comparison?: ReportComparison;
  compare_from?: string;
  compare_to?: string;
  account_ids?: string[];
};
export type ReportPeriod = { from?: string; to: string };
export type ReportColumn = { id: string; label: string; kind: string; period: ReportPeriod };
export type ReportDrilldown = {
  account_ids: string[];
  from?: string;
  to: string;
  exclude_closing: boolean;
  exclude_opening: boolean;
  entry_id?: string;
  entry_reference?: string;
  posting_id?: string;
};
export type ReportRow = {
  id: string;
  label: string;
  role: string;
  account_id?: string;
  account_code?: string;
  gl_code?: string;
  date?: string;
  entry_id?: string;
  posting_id?: string;
  values: Record<string, string>;
  own_values?: Record<string, string>;
  children: ReportRow[];
  drilldown?: ReportDrilldown;
  allocations?: { posting_id: string; column_id: string; amount_minor: string; category?: string; treatment_reference?: string }[];
};
export type ReportCheck = { id: string; status: "pass" | "fail" | "unknown" | "not_applicable"; message: string };
export type FinancialReport = RecordedState & {
  schema_version: number;
  report: FinancialReportKind;
  title: string;
  book: {
    id: string;
    name: string;
    currency: string;
    scale: number;
    start_date?: string;
    fiscal_year_start_month: number;
    fiscal_year_start_day: number;
    recognition_basis: string;
    reporting_framework: string;
  };
  request: FinancialReportRequest;
  scope: {
    mode: string;
    period: ReportPeriod;
    comparison?: ReportPeriod;
    fiscal_start: string;
    ledger_revision: string;
    knowledge_cutoff: string;
    mapping_version: string;
    hierarchy_basis: string;
    generated_at: string;
    population_digest: string;
    coverage: string;
    opening_balances: string;
  };
  columns: ReportColumn[];
  rows: ReportRow[];
  checks: ReportCheck[];
  limitations: string[];
  reconciliation: Record<string, string>;
  status: "ready" | "partial" | "unsupported";
  digest: string;
  /** Produced by the backend from this exact result, without another ledger read. */
  export_csv?: string;
  /** Separate permission; compact browser reads omit the duplicate CSV text. */
  export_available?: boolean;
  /** Reopenings, and closes made with open review items, of the periods covered. */
  period_notes?: PeriodNote[];
};
export type PeriodNote = { period_id: string; from: string; through: string; kind: "reopened" | "closed_with_open_items"; reason: string; at: string };
export type ReportContribution = {
  posting_id: string;
  entry_id: string;
  entry_reference?: string;
  account_id: string;
  date: string;
  side: string;
  amount_minor: string;
  signed_amount_minor: string;
  report_amount_minor: string;
  column_id: string;
  purpose: string;
  category?: string;
  treatment_reference?: string;
};
export type ContributionResult = {
  result_digest: string;
  row_id: string;
  column_id: string;
  contributions: ReportContribution[];
  debit_minor: string;
  credit_minor: string;
  total_minor: string;
};

const reportIDs = new Set<string>(reportDefinitions.map((r) => r.id));
const comparisons = new Set(["none", "previous_period", "previous_year", "explicit"]);
export function reportDefinition(id: ReportView) {
  return reportDefinitions.find((r) => r.id === id)!;
}
export function reportSupportsComparison(id: ReportView) {
  return !["general_ledger", "journal", "cash_movement"].includes(id);
}
function defaultReportComparison(id: ReportView): ReportComparison {
  return ["balance_sheet", "profit_loss"].includes(id) ? "previous_year" : "none";
}
export function readReportLocation(search: string, fallback: DashboardRange): ReportLocation {
  const query = new URLSearchParams(search);
  const raw = query.get("report") || "balance_sheet";
  const report = reportIDs.has(raw) ? raw as ReportView : "balance_sheet";
  const reviewValue = query.get("review_report");
  const reviewReport = reviewValue && reportIDs.has(reviewValue) && !["notes_review", "cash_movement"].includes(reviewValue)
    ? reviewValue as FinancialReportKind : undefined;
  const context = report;
  const rawComparison = query.get("compare") || defaultReportComparison(context);
  const comparison = comparisons.has(rawComparison) ? rawComparison as ReportComparison : "none";
  const to = query.get("as_of") || query.get("to") || fallback.to;
  const from = query.get("from") || (reportDefinition(context).position && fallback.from > to ? to : fallback.from);
  const compareFrom = query.get("compare_from") || undefined;
  const compareTo = query.get("compare_to") || undefined;
  const rawColumns = query.get("columns");
  const ambiguous = ["report", "from", "to", "as_of", "compare", "compare_from", "compare_to", "review_report", "columns"]
    .some((key) => query.getAll(key).length > 1) ||
    (!!query.get("as_of") && !!query.get("to") && query.get("as_of") !== query.get("to"));
  const validDates = !!dateISO(from) && !!dateISO(to) && from <= to;
  const validComparison = comparison !== "explicit" ||
    (!!dateISO(compareTo) && (reportDefinition(context).position ? !compareFrom :
      (!!dateISO(compareFrom) && compareFrom! <= compareTo!)));
  return {
    report,
    range: validDates ? { from, to } : fallback,
    comparison,
    compareFrom,
    compareTo,
    reviewReport,
    ...(rawColumns === "months" ? { columns: "months" as const } : {}),
    invalid: ambiguous || !reportIDs.has(raw) || !comparisons.has(rawComparison) || !validDates || !validComparison ||
      (!!reviewValue && !reviewReport) || (!reportSupportsComparison(context) && comparison !== "none") ||
      (!!rawColumns && !["summary", "months"].includes(rawColumns)) ||
      (rawColumns === "months" && !["balance_sheet", "profit_loss"].includes(context)),
  };
}

export function reportRequest(book: Book, selection: ReportLocation): FinancialReportRequest {
  if (selection.invalid) throw new Error("Choose a report and a valid reporting period.");
  const view = selection.report;
  return {
    book_id: book.id,
    report: view,
    ...(reportDefinition(view).position
      ? { as_of: selection.range.to }
      : { from: selection.range.from, to: selection.range.to }),
    currency: book.currency,
    comparison: selection.comparison,
    ...(selection.comparison === "explicit"
      ? { compare_from: selection.compareFrom, compare_to: selection.compareTo }
      : {}),
  };
}

export function financialReportQuery(request: FinancialReportRequest) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(request)) {
    if (Array.isArray(value)) { if (value.length) params.set(key, value.join(",")); }
    else if (value !== undefined && value !== "") params.set(key, value);
  }
  return params.toString();
}

/** A live report link fixes parameters, never promises historical issuance. */
export function reportViewURL(book: Book, selection: ReportLocation, href = window.location.href) {
  const url = canonicalBookURL(book, "statements", href);
  for (const key of ["from", "to", "as_of", "range", "period", "preset", "report", "compare", "compare_from", "compare_to", "group", "review_report", "columns"])
    url.searchParams.delete(key);
  if (selection.report !== "balance_sheet") url.searchParams.set("report", selection.report);
  const context = selection.report;
  if (reportDefinition(context).position && selection.columns !== "months") {
    url.searchParams.set("as_of", selection.range.to);
  } else {
    // Month columns need a complete interval accepted by application startup.
    // Each position column still requests its own as-of date from the ledger.
    url.searchParams.set("from", selection.range.from);
    url.searchParams.set("to", selection.range.to);
  }
  if (selection.comparison !== defaultReportComparison(context)) url.searchParams.set("compare", selection.comparison);
  if (selection.columns === "months") url.searchParams.set("columns", "months");
  if (selection.comparison === "explicit") {
    if (selection.compareFrom) url.searchParams.set("compare_from", selection.compareFrom);
    if (selection.compareTo) url.searchParams.set("compare_to", selection.compareTo);
  }
  return url.href;
}

const integerAmount = (value: unknown): value is string => typeof value === "string" && /^-?\d+$/.test(value);
export function assertFinancialReport(value: FinancialReport, book: Book, request: FinancialReportRequest): FinancialReport {
  if (value?.schema_version !== 1 || value.report !== request.report || value.book?.id !== book.id ||
      value.request?.book_id !== book.id || value.request.report !== request.report || value.book.currency !== book.currency || value.book.scale !== book.scale ||
      value.scope?.mode !== "live" || value.scope.period.to !== (request.as_of || request.to) ||
      (request.from && value.scope.period.from !== request.from) ||
      (value.request.from || "") !== (request.from || "") ||
      (value.request.to || "") !== (request.to || "") ||
      (value.request.as_of || "") !== (request.as_of || "") ||
      value.request.currency !== book.currency ||
      (value.request.compare_from || "") !== (request.compare_from || "") ||
      (value.request.compare_to || "") !== (request.compare_to || "") ||
      [...(value.request.account_ids || [])].sort().join("\u0000") !== [...(request.account_ids || [])].sort().join("\u0000") ||
      (value.request.comparison || "none") !== (request.comparison || "none") ||
      !/^[a-f0-9]{64}$/.test(value.digest || "") || !Array.isArray(value.columns) || !value.columns.length ||
      !Array.isArray(value.rows) || !Array.isArray(value.checks) || !Array.isArray(value.limitations) ||
      !["ready", "partial", "unsupported"].includes(value.status))
    throw new Error("The report does not match this Book and reporting scope.");
  const comparison = request.comparison || "none";
  if ((comparison === "none" && value.scope.comparison) ||
      (comparison !== "none" && (!value.scope.comparison || !dateISO(value.scope.comparison.to) ||
        (value.scope.comparison.from && (!dateISO(value.scope.comparison.from) || value.scope.comparison.from > value.scope.comparison.to)))) ||
      (comparison === "explicit" && (value.scope.comparison?.to !== request.compare_to ||
        (value.scope.comparison?.from || "") !== (request.compare_from || ""))))
    throw new Error("The comparison does not match the requested reporting period.");
  const columns = new Set(value.columns.map((c) => c.id));
  if (columns.size !== value.columns.length || value.columns.some((c) => !c.id || !c.label))
    throw new Error("The report columns could not be confirmed.");
  const ids = new Set<string>();
  const visit = (rows: ReportRow[], depth: number) => {
    if (depth > 128) throw new Error("The report hierarchy could not be confirmed.");
    for (const row of rows) {
      if (!row.id || ids.has(row.id) || ids.size > 500_000 || typeof row.label !== "string" ||
          !Array.isArray(row.children) || !row.values ||
          Object.entries(row.values).some(([key, amount]) => !columns.has(key) || !integerAmount(amount)) ||
          Object.entries(row.own_values || {}).some(([key, amount]) => !columns.has(key) || !integerAmount(amount)))
        throw new Error("The report rows could not be confirmed.");
      ids.add(row.id);
      visit(row.children, depth + 1);
    }
  };
  visit(value.rows, 0);
  if (value.export_csv !== undefined && (typeof value.export_csv !== "string" || value.export_csv.length > 34_000_000))
    throw new Error("The report export could not be confirmed.");
  if (value.export_available !== undefined && typeof value.export_available !== "boolean")
    throw new Error("The report export access could not be confirmed.");
  return value;
}

export async function loadFinancialReport(book: Book, request: FinancialReportRequest, signal?: AbortSignal) {
  const response = await get<FinancialReport>(`financial-reports?${financialReportQuery(request)}&include_csv=false`, signal);
  return assertFinancialReport(response, book, request);
}

export async function loadReportContributions(result: FinancialReport, row: ReportRow, column: string, signal?: AbortSignal) {
  const query = new URLSearchParams(financialReportQuery(result.request));
  query.set("digest", result.digest);
  query.set("row_id", row.id);
  query.set("column_id", column);
  query.set("knowledge_cutoff", result.scope.knowledge_cutoff);
  query.set("generated_at", result.scope.generated_at);
  const data = await get<ContributionResult>(`financial-reports/contributions?${query}`, signal);
  if (data.result_digest !== result.digest || data.row_id !== row.id || data.column_id !== column ||
      !Array.isArray(data.contributions) || !integerAmount(data.debit_minor) || !integerAmount(data.credit_minor) ||
      !integerAmount(data.total_minor) || data.total_minor !== row.values[column] ||
      data.contributions.some((c) => !c.posting_id || !c.entry_id || !dateISO(c.date) ||
        !["debit", "credit"].includes(c.side) || !/^\d+$/.test(c.amount_minor) || !integerAmount(c.signed_amount_minor) ||
        !integerAmount(c.report_amount_minor) ||
        BigInt(c.signed_amount_minor) !== BigInt(c.amount_minor) * (c.side === "debit" ? 1n : -1n)))
    throw new Error("The contributing entries do not match this report.");
  if (data.contributions.reduce((total, entry) => total + BigInt(entry.report_amount_minor), 0n) !== BigInt(data.total_minor))
    throw new Error("The contributing entries do not reconcile to this report line.");
  return data;
}

export function reportFlatRows(rows: ReportRow[], expanded?: ReadonlySet<string>, depth = 0): { row: ReportRow; depth: number }[] {
  return rows.flatMap((row) => [
    { row, depth },
    ...(!expanded || expanded.has(row.id) ? reportFlatRows(row.children, expanded, depth + 1) : []),
  ]);
}

export async function authorizeReportExport(bookID: string, signal?: AbortSignal) {
  const response = await get<{ book_id: string; allowed: boolean }>(bookURL("financial-reports/export-permission", bookID), signal);
  if (response.book_id !== bookID || response.allowed !== true) throw new Error("Export isn't available for this Book.");
  if (signal?.aborted) throw new DOMException("Export cancelled", "AbortError");
}

