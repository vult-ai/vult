import { NameLink } from "../ui/name-link";
import { useEffect, useRef, useState } from "react";
import { ArrowUpRight, XClose } from "../../design-system/icons";
import { APIError, money } from "../../lib/api";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { loadReportContributions, type FinancialReport, type ReportRow, type ContributionResult } from "../../lib/financial-reports";
import { ErrorMessage } from "../form";
import { Button } from "../ui/button";
import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";

export function ReportInspector({ report, row, column, displayLabel, onClose, onInspect, onRefresh, embedded=false, loadContributions=loadReportContributions }: {
  report: FinancialReport;
  row: ReportRow;
  column: string;
  displayLabel?: string;
  embedded?: boolean;
  onClose: () => void;
  onInspect: (id: string, trigger: HTMLElement) => void;
  onRefresh: () => void;
  loadContributions?: typeof loadReportContributions;
}) {
  const [loaded, setLoaded] = useState<ContributionResult>();
  const [failure, setFailure] = useState<unknown>();
  const [pending, setPending] = useState(false);
  const [retry, setRetry] = useState(0);
  const [page, setPage] = useState(0);
  const host = useRef<HTMLElement>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() => {
      host.current?.focus({ preventScroll: true });
      host.current?.scrollIntoView?.({ block: "nearest" });
    });
    return () => cancelAnimationFrame(frame);
  }, [row.id, column]);
  useEffect(() => {
    const controller = new AbortController();
    setPending(true); setFailure(undefined); setLoaded(undefined); setPage(0);
    loadContributions(report, row, column, controller.signal).then((value) => {
      if (!controller.signal.aborted) { setLoaded(value); setPending(false); }
    }).catch((error) => {
      if (!controller.signal.aborted) { setFailure(error); setPending(false); }
    });
    return () => controller.abort();
  }, [report.digest, row.id, column, retry,loadContributions]);
  const matching = loaded?.result_digest === report.digest && loaded.row_id === row.id && loaded.column_id === column ? loaded : undefined;
  const entries = matching?.contributions || [];
  const offset = page * 30;
  const label = displayLabel || report.columns.find((c) => c.id === column)?.label || "Current";
  const period = report.columns.find(c => c.id === column)?.period;
  return <aside ref={host} tabIndex={-1} className="statement-inspector" data-embedded={embedded || undefined} aria-label={`Contributions to ${row.label}`} onKeyDown={(event) => { if (event.key === "Escape" && !event.defaultPrevented) { event.preventDefault(); onClose(); } }}>
    <header>
      <div><span className="vult-type-metadata">Contributing entries</span><h3>{row.label}</h3></div>
      {!embedded && <Button color="tertiary" size="sm" aria-label="Close report inspection" onClick={onClose}><XClose aria-hidden /></Button>}
    </header>
    <div className="statement-inspector-content" data-vult-view-scroll>
      <p className="statement-inspector-period">{label}{period && <> · {period.from ? formatDateRange(period.from, period.to, "full") : formatDate(period.to, "full")}</>}</p>
      {column === "variance" && report.scope.comparison && <p className="statement-inspector-period">Against {report.scope.comparison.from ? formatDateRange(report.scope.comparison.from, report.scope.comparison.to, "full") : formatDate(report.scope.comparison.to, "full")}</p>}
      <p className="statement-inspector-amount">{row.values[column] !== undefined ? money(row.values[column], report.book) : "—"}</p>
      <p className="statement-caption">These postings belong to the same report read. Opening, closing and allocation treatment follow this line’s calculation.</p>
      <ErrorMessage message={failure} intent="read" resource="statement" pending={pending} onRetry={() => failure instanceof APIError && failure.status === 409 ? onRefresh() : setRetry((r) => r + 1)} />
      {pending && !matching && <LoadingState scope="inline" label="Loading contributing entries…" />}
      {matching && !entries.length && <EmptyState scope="inline" title="No postings contribute to this line in this reporting scope" />}
      <ol className="statement-contributions">
        {entries.slice(offset, offset + 30).map((entry, index) => <li key={`${entry.posting_id}:${offset + index}`}>
          <div><span>{formatDate(entry.date, "full")}</span><NameLink
type="button"
className="statement-entry-link vult-focus-target"
aria-label={`Inspect contributing entry ${offset + index + 1}`}
onClick={(event) => onInspect(entry.entry_id, event.currentTarget)}
name={entry.entry_reference || "Inspect entry"}
trailing={<ArrowUpRight aria-hidden />}
/></div>
          <div className="statement-contribution-value"><span>{entry.side === "debit" ? "Debit" : "Credit"}</span><span>{money(entry.amount_minor, report.book)}</span></div>
          <div className="statement-contribution-value"><span>Report contribution</span><span>{money(entry.report_amount_minor, report.book)}</span></div>
          {entry.category && <p className="statement-caption">Classification: {entry.category.replaceAll("_", " ")}</p>}
          {entry.treatment_reference && <details className="statement-treatment"><summary>Accounting treatment</summary><p className="statement-caption">Reviewed reference: {entry.treatment_reference}</p></details>}
        </li>)}
      </ol>
      {entries.length > 30 && <div className="statement-pagination"><Button color="tertiary" size="sm" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>Previous</Button><span>{offset + 1}–{Math.min(offset + 30, entries.length)} of {entries.length}</span><Button color="tertiary" size="sm" disabled={offset + 30 >= entries.length} onClick={() => setPage((p) => p + 1)}>Next</Button></div>}
    </div>
  </aside>;
}
