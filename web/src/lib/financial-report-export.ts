import type { FinancialReport, ReportRow } from "./financial-reports";

export const maxReportCSVBytes = 32 << 20;
export function reportExportAvailable(result: FinancialReport) {
  return result.export_available ?? !!result.export_csv;
}

// Mirrors reporting.safeText. Apply only to text: signed decimal cells retain
// their numeric meaning. Both adapters are checked against the same Go goldens.
function safeText(value: string) {
  return /^[ \t\r\n]*[=+\-@]/.test(value) || /[\t\r]/.test(value) ? `'${value}` : value;
}
function field(value: string) {
  // encoding/csv also quotes leading Unicode whitespace and PostgreSQL's \.
  // terminator. JS \s differs from Go unicode.IsSpace at FEFF and NEL.
  const leadingSpace = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]/.test(value);
  return value === "\\." || /[",\r\n]/.test(value) || leadingSpace ? `"${value.replaceAll('"', '""')}"` : value;
}
export function reportDecimal(value: string, scale: number) {
  if (!/^-?\d+$/.test(value) || !Number.isInteger(scale) || scale < 0 || scale > 9) throw new Error("The report amount could not be confirmed.");
  const negative = value.startsWith("-") && BigInt(value) !== 0n;
  let digits = value.replace(/^-/, "");
  if (scale) {
    digits = digits.padStart(scale + 1, "0");
    digits = `${digits.slice(0, -scale)}.${digits.slice(-scale)}`;
  }
  return `${negative ? "-" : ""}${digits}`;
}

/** Serialization only. Every figure and omission comes from this captured
 * canonical result; no ledger request or accounting calculation happens here. */
export function financialReportCSV(result: FinancialReport) {
  if (!reportExportAvailable(result)) throw new Error("Export isn't available for this report.");
  if (result.export_csv) return result.export_csv;
  const rows: string[] = [], encoder = new TextEncoder();
  let bytes = 0;
  const write = (cells: string[]) => {
    const row = cells.map(field).join(",") + "\n";
    bytes += encoder.encode(row).byteLength;
    if (bytes > maxReportCSVBytes) throw new Error("Choose a smaller period to export this report.");
    rows.push(row);
  };
  const s = result.scope, b = result.book;
  const meta = [
    ["Report", safeText(result.title)], ["Book", safeText(b.name)], ["Book ID", safeText(b.id)], ["Currency", b.currency],
    ["Recognition basis", safeText(b.recognition_basis)], ["Reporting framework", safeText(b.reporting_framework)],
    ["Mode", s.mode], ["Status", result.status], ["From", s.period.from || ""], ["Through", s.period.to],
    ["Ledger revision", safeText(s.ledger_revision)], ["Knowledge cutoff", safeText(s.knowledge_cutoff)],
    ["Mapping version", safeText(s.mapping_version)], ["Hierarchy basis", safeText(s.hierarchy_basis)],
    ["Population digest", s.population_digest], ["Result digest", result.digest], ["Coverage", s.coverage], ["Opening balances", s.opening_balances],
  ];
  if (s.comparison) meta.push(["Comparison from", s.comparison.from || ""], ["Comparison through", s.comparison.to]);
  meta.forEach(write);
  result.checks.forEach(c => write(["Check", safeText(c.id), c.status, safeText(c.message)]));
  result.limitations.forEach(l => write(["Limitation", safeText(l)]));
  write([]);
  write(["Row ID", "Parent row ID", "Depth", "Role", "Account ID", "Account code", "Label", "Date", "Entry ID", "Posting ID", ...result.columns.map(c => `${safeText(c.label)} (${b.currency})`), "GL code"]);
  const visit = (items: ReportRow[], parent: string, depth: number) => {
    for (const row of items) {
      write([safeText(row.id), safeText(parent), String(depth), safeText(row.role), safeText(row.account_id || ""), safeText(row.account_code || ""), safeText(row.label), row.date || "", safeText(row.entry_id || ""), safeText(row.posting_id || ""),
        ...result.columns.map(c => row.values[c.id] === undefined ? "" : reportDecimal(row.values[c.id], b.scale)), safeText(row.gl_code || "")]);
      visit(row.children, row.id, depth + 1);
    }
  };
  visit(result.rows, "", 0);
  return rows.join("");
}
