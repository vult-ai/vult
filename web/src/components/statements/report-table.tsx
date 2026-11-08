import { accountCodeLabel } from "../../lib/account-codes";
import { ChevronDown, ChevronRight } from "../../design-system/icons";
import { money } from "../../lib/api";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { displayCell, omittedAmountNote, printReportDisplays, type DisplayCell, type ReportDisplay } from "../../lib/financial-report-workspace";
import { reportDefinition, type ReportRow } from "../../lib/financial-reports";

export function ReportTable({ display, rows, expanded, onToggle, onInspect, printable = false, captured = false, columnRange }: {
  display: ReportDisplay;
  rows: { row: ReportRow; depth: number }[];
  expanded?: ReadonlySet<string>;
  onToggle?: (row: ReportRow) => void;
  onInspect?: (cell: DisplayCell, trigger: HTMLElement) => void;
  printable?: boolean;
  captured?: boolean;
  columnRange?: { from: number; to: number; total: number };
}) {
  if (printable && !columnRange) {
    const panels = printReportDisplays(display);
    if (panels.length > 1) {
      let offset = 0;
      return <>{panels.map((panel, index) => {
        const from = offset + 1; offset += panel.columns.length;
        return <section className="statement-print-panel" data-first-print-panel={index === 0 || undefined} key={panel.columns[0].id}>
          <ReportTable display={panel} rows={rows} printable captured={captured} columnRange={{ from, to: offset, total: display.columns.length }} />
        </section>;
      })}</>;
    }
  }
  const report = display.base;
  const period = report.scope.period;
  const scope = reportDefinition(report.report).position ? `As of ${formatDate(period.to, "full")}` : period.from ? formatDateRange(period.from, period.to, "full") : formatDate(period.to, "full");
  const qualifier = display.sources.some(s => s.status === "partial") ? captured ? "Captured partial ledger result" : "Partial ledger report" : report.status === "unsupported" ? "Unqualified treatment" : captured ? "Captured ledger result" : "Live ledger report";
  const declaredBasis = report.book.recognition_basis;
  const basisLabel = declaredBasis && !["unknown", "not_set"].includes(declaredBasis) ? `Declared basis: ${declaredBasis}` : "Recognition basis not established";
  const compactPrint = printable && display.columns.length <= 5;
  return <table className={printable ? "statement-print-table" : "statement-table"} data-report-result={report.digest} data-print-compact={compactPrint || undefined}>
    <caption className="sr-only">{report.book.name} · {report.title} · {scope} · {report.book.currency}</caption>
    {compactPrint && <colgroup><col className="statement-print-account-column" />{display.columns.map(column => <col key={column.id} />)}</colgroup>}
    <thead>
      {printable && <tr className="statement-print-identity"><th colSpan={display.columns.length + 1}>
        <span className="statement-print-book">{report.book.name}</span>
        <span className="statement-print-title">{report.title}</span>
        <span className="statement-print-scope">{scope} · {report.book.currency}{columnRange && ` · Columns ${columnRange.from}-${columnRange.to} of ${columnRange.total}`}</span>
        <span className="statement-print-status">{qualifier} · {basisLabel}</span>
      </th></tr>}
      <tr><th scope="col">{["journal", "general_ledger"].includes(report.report) ? "Account / entry" : "Account"}</th>{display.columns.map(column => <th key={column.id} scope="col" data-report-column={column.id}>{column.label}
        {(display.months || report.scope.comparison) && column.sourceColumn !== "variance" && <span className="statement-column-period">{column.period.from ? formatDateRange(column.period.from, column.period.to, "full") : formatDate(column.period.to, "full")}</span>}
      </th>)}</tr>
    </thead>
    <tbody>{rows.map(({ row, depth }) => <tr key={row.id} className={`statement-row statement-row-${row.role}`} data-report-row={row.id}>
      <th scope="row"><div className="statement-row-label" style={{ paddingInlineStart: `${depth * 18}px` }}>
        {row.children.length && onToggle && !printable ? <button type="button" className="statement-disclosure vult-focus-target" aria-label={`${expanded?.has(row.id) ? "Collapse" : "Expand"} ${row.label}`} aria-expanded={expanded?.has(row.id) || false} onClick={() => onToggle(row)}>{expanded?.has(row.id) ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}</button> : <span className="statement-disclosure-spacer" />}
        <span>{accountCodeLabel(row.label, row.gl_code)}{row.date && <span className="statement-row-date">{formatDate(row.date)}</span>}</span>
      </div></th>
      {display.columns.map(column => {
        const cell = displayCell(display, row, column);
        const inspectable = cell.row.drilldown || !!cell.row.allocations?.length;
        return <td key={column.id} data-report-column={column.id} data-column-result={cell.report.digest} data-source-column={cell.column}>
          <span className="statement-cell-value">{cell.value === undefined ? "—" : money(cell.value, report.book)}</span>
          {!printable && onInspect && inspectable && cell.value !== undefined && <button type="button" className="statement-inspect-value vult-focus-target" aria-label={`Inspect ${row.label}, ${column.label}`} onClick={event => onInspect(cell, event.currentTarget)}><span aria-hidden>↗</span></button>}
        </td>;
      })}
    </tr>)}</tbody>
    {printable && !!display.omissions?.size && <tfoot className="statement-print-note"><tr><td colSpan={display.columns.length + 1}>{omittedAmountNote}</td></tr></tfoot>}
  </table>;
}
