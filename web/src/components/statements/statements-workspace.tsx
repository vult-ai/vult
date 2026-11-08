import { NameLink } from "../ui/name-link";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { Download01, Printer } from "../../design-system/icons";
import { File06, Calendar, Columns03, SwitchHorizontal01, SearchLg, ClipboardCheck, Sliders01, ChevronRight } from "../../design-system/icons";
import { Dialog as AriaDialog, Popover } from "react-aria-components";
import { bookURL, money, today, type Book, type State } from "../../lib/api";
import { formatDate, formatDateRange, formatDateTime } from "../../lib/date-format";
import { readDashboardLocation } from "../../lib/dashboard-location";
import type { DashboardRange } from "../../lib/dashboard-period";
import { loadStatementCashMovement, type StatementCashMovement } from "../../lib/statement-cash-movement";
import {
  authorizeReportExport, financialReportQuery, loadFinancialReport,
  readReportLocation, reportDefinition, reportDefinitions, reportFlatRows,
  reportRequest, reportViewURL, reportSupportsComparison,
  type FinancialReport, type ReportLocation, type ReportRow, type ReportView,
} from "../../lib/financial-reports";
import { reportExportAvailable } from "../../lib/financial-report-export";
import {
  displayedReportCSV, downloadReportFile, loadMonthDisplay, monthViewReason, omittedAmountNote, readSavedReportViews,
  reportPreset, reportSupportsMonths, saveReportView, summaryDisplay,
  type DisplayCell, type ReportDisplay,
} from "../../lib/financial-report-workspace";
import { Button } from "../ui/button";
import { Select } from "../ui/select";
import { ErrorMessage, Field } from "../form";
import { Dialog } from "../ui/dialog";
import { CopyCurrentView } from "../book/copy-current-view";
import { HeaderActions } from "../shell/header-actions";
import { PageToolbar } from "../workspaces/workspace-frame";
import { ReportPeriodPicker } from "./report-period-picker";
import { ReportInspector } from "./report-inspector";
import { ReportTable } from "./report-table";
import { FinancialReportGrid } from "./financial-report-grid";
import { captureInspectorScroll, restoreInspectorScroll, type InspectorScroll } from "../../design-system/collection-inspector";
import { ReportComparison } from "./report-comparison";
import { reportComparisonDate } from "../../lib/report-period";
import { StatementToolAction, StatementToolSelect, StatementTools } from "./statement-tools";
import { reportScopeStatus } from "./report-scope-status";
import { formatReadableDateTime } from "../../lib/date-format";
import "./statements.css";

import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";
import { FailureState } from "../ui/failure-state";

type Props = {
  book: Book;
  state: State;
  onShowTransactions: (range: DashboardRange) => void;
  onInspect: (id: string, trigger: HTMLElement) => void;
};
type Loaded = { key: string; report?: FinancialReport; display?: ReportDisplay; movement?: StatementCashMovement };
const groups = ["Financial statements", "Accounting reports", "Supporting views"];
const descriptions: Record<ReportView, string> = {
  balance_sheet: "Assets, liabilities and equity at a date.",
  profit_loss: "Recorded income, expenses and results for the period.",
  trial_balance: "Account balances and exact debit and credit control.",
  general_ledger: "Account activity, opening balances and closing balances.",
  journal: "Posted entries and their debit and credit lines.",
  cash_movement: "Recorded receipts and payments, excluding internal cash transfers.",
};
const established = (value?: string) => !!value && !["unknown", "not_set", "unset"].includes(value);
const toolsPreferenceKey = "vult:statement-tools-expanded:v1";
function initialToolsExpanded() {
  try {
    const saved = localStorage.getItem(toolsPreferenceKey);
    if (saved === "true" || saved === "false") return saved === "true";
  } catch {
    /* Layout remains usable with blocked storage. */
  }
  return window.innerWidth > 900;
}

export function StatementsWorkspace({ book, state, onShowTransactions, onInspect }: Props) {
  const initial = () => {
    const fallback = readDashboardLocation((book.coverage_start_date || book.start_date), book.accounting_today || today(book.accounting_timezone), book).range;
    return readReportLocation(window.location.search, fallback);
  };
  const [savedSelection, setSelection] = useState<ReportLocation>(initial);
  const [loaded, setLoaded] = useState<Loaded>();
  const [failure, setFailure] = useState<unknown>();
  const [pending, setPending] = useState(false);
  const [retry, setRetry] = useState(0);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [page, setPage] = useState(0);
  const [inspection, setInspection] = useState<{ cell: DisplayCell; trigger: HTMLElement; scroll: InspectorScroll | null }>();
  const [activePanel, setActivePanel] = useState<"inspect" | "review">();
  const [toolsExpanded, setToolsExpanded] = useState(initialToolsExpanded);
  const preferredToolsExpanded = useRef(toolsExpanded);
  const [toolsAnimate, setToolsAnimate] = useState(false);
  const [workPinned, setWorkPinned] = useState(false);
  const [selectionHost, setSelectionHost] = useState<HTMLDivElement | null>(null);
  const panelTrigger = useRef<HTMLElement | null>(null);
  const reviewId = useId();
  const toolsTrigger = useRef<HTMLButtonElement>(null);
  const [toolsOpen, setToolsOpen] = useState(false);
  const exportTrigger = useRef<HTMLButtonElement>(null);
  const [exportOpen, setExportOpen] = useState(false);
  const [exportFailure, setExportFailure] = useState<unknown>();
  const [exportPending, setExportPending] = useState(false);
  const exportController = useRef<AbortController | undefined>(undefined);
  useEffect(() => {
    setExportPending(false);
    return () => { exportController.current?.abort(); exportController.current = undefined; };
  }, [book.id]);
  const [printing, setPrinting] = useState(false);
  const [savedViews, setSavedViews] = useState(() => readSavedReportViews(book));
  const [activeView, setActiveView] = useState("current");
  const [saveViewTrigger, setSaveViewTrigger] = useState<HTMLElement>();
  const [viewName, setViewName] = useState("");
  const [comparisonTrigger,setComparisonTrigger]=useState<HTMLElement>();
  const [viewFailure, setViewFailure] = useState<unknown>();
  const initialBook = useRef(book.id);
  const selection = initialBook.current === book.id ? savedSelection : initial();
  const selectionRef=useRef(selection);selectionRef.current=selection;
  const definition = reportDefinition(selection.report);
  const contextReport = selection.report;
  const contextDefinition = reportDefinition(contextReport);
  const documentTitle = `${book.name} · ${contextDefinition.label} · ${contextDefinition.position ? `As of ${formatDate(selection.range.to, "full")}` : formatDateRange(selection.range.from, selection.range.to, "full")} · ${book.currency}`;
  useEffect(() => {
    const narrow = window.matchMedia("(max-width: 900px)");
    const fitTools = () => {
      setToolsAnimate(false);
      if (narrow.matches) {
        setToolsExpanded(false); setWorkPinned(false);
      } else setToolsExpanded(preferredToolsExpanded.current);
    };
    narrow.addEventListener("change", fitTools);
    return () => narrow.removeEventListener("change", fitTools);
  }, []);
  useEffect(() => {
    const previous = document.title; document.title = documentTitle;
    return () => { if (document.title === documentTitle) document.title = previous; };
  }, [documentTitle]);
  const monthReason = selection.columns === "months" ? monthViewReason({ ...selection, report: contextReport }) : undefined;
  const request = useMemo(() => selection.invalid || monthReason ? undefined : reportRequest(book, selection), [book.id, book.currency, selection, monthReason]);
  const key = request ? `${book.id}:${financialReportQuery(request)}:${selection.columns || "summary"}` : `${book.id}:invalid`;
  const data = loaded?.key === key ? loaded : undefined;
  const report = data?.report;
  const display = data?.display;
  const exportCSV = async () => {
    if (!display || exportController.current) return;
    const captured = display, controller = new AbortController();
    exportController.current = controller;
    setExportPending(true); setExportFailure(undefined);
    try {
      await authorizeReportExport(captured.base.book.id, controller.signal);
      if (!controller.signal.aborted) downloadReportFile(displayedReportCSV(captured), `vult-${captured.base.report}-${captured.base.scope.period.to}.csv`, "text/csv;charset=utf-8");
    } catch (error) { if (!controller.signal.aborted) setExportFailure(error); }
    finally { if (exportController.current === controller) { exportController.current = undefined; setExportPending(false); } }
  };
  const scopeStatus = reportScopeStatus(display?.sources || (report ? [report] : []));
  const movement = data?.movement;
  const viewURL = selection.invalid ? undefined : reportViewURL(book, selection);
  useEffect(() => {
    if (!data || !viewURL || selection.invalid) return;
    const canonical = new URL(viewURL);
    if (canonical.pathname === window.location.pathname && canonical.href !== window.location.href) {
      window.history.replaceState(window.history.state, "", canonical.href);
    }
  }, [data, viewURL, selection.invalid]);
  useEffect(() => {
    const before = () => {
      document.documentElement.dataset.vultReportPrint = "true";
      flushSync(() => setPrinting(true));
    };
    const after = () => { delete document.documentElement.dataset.vultReportPrint; setPrinting(false); };
    window.addEventListener("beforeprint", before);
    window.addEventListener("afterprint", after);
    return () => { window.removeEventListener("beforeprint", before); window.removeEventListener("afterprint", after); delete document.documentElement.dataset.vultReportPrint; };
  }, []);

  useEffect(() => {
    if (initialBook.current !== book.id) {
      initialBook.current = book.id;
      setSelection(initial()); setLoaded(undefined); setFailure(undefined); setInspection(undefined); setExpanded(new Set());
      setSaveViewTrigger(undefined); setSavedViews(readSavedReportViews(book)); setActiveView("current");
      setComparisonTrigger(undefined);setToolsOpen(false);setExportOpen(false);setActivePanel(undefined);setWorkPinned(false);
    }
  }, [book.id]);
  useEffect(() => {
    const pop = () => {
      const next=initial();
      // Entry inspection and review overlays keep the same report population.
      // Closing them must not discard contribution position or expanded rows.
      const current = selectionRef.current;
      if (next.invalid === current.invalid && next.reviewReport === current.reviewReport &&
          reportViewURL(book, next) === reportViewURL(book, current)) return;
      setSelection(next);setInspection(undefined);setPage(0);setActiveView("current");
    };
    window.addEventListener("popstate", pop);
    return () => window.removeEventListener("popstate", pop);
  }, [book.id]);
  useEffect(() => {
    if (!request || initialBook.current !== book.id) { setPending(false); return; }
    // Navigation still carries the previous workspace until its scoped bootstrap
    // arrives. Avoid starting a report only to abort/repeat it for that bootstrap.
    if (state.read_view && state.read_view !== "statements") { setPending(true); return; }
    const controller = new AbortController();
    setPending(true); setFailure(undefined); setInspection(undefined); setPage(0);
    const operation = selection.report === "cash_movement"
      ? loadStatementCashMovement(book, selection.range, controller.signal).then((movement) => ({ key, movement }))
      : selection.columns === "months" && reportSupportsMonths(contextReport)
        ? loadMonthDisplay(book, request, selection.range, controller.signal).then(display => ({key,report:display.base,display}))
        : loadFinancialReport(book, request, controller.signal).then(report => ({key,report,display:summaryDisplay(report)}));
    operation.then((value) => {
      if (controller.signal.aborted) return;
      setLoaded(value); setPending(false);
      if ("report" in value) {
        const top = value.report.rows.filter((r) => r.children.length && ["section", "total"].includes(r.role));
        setExpanded(new Set(top.map((r) => r.id)));
      }
    }).catch((error) => {
      if (!controller.signal.aborted) { setFailure(error); setPending(false); }
    });
    return () => controller.abort();
  }, [key, state.read_view, state.state_revision, retry]);

  const change = (next: Partial<ReportLocation>, view = "current") => {
    const value = { ...selection, ...next, invalid: false };
    const url = reportViewURL(book, value);
    if (url !== window.location.href) window.history.pushState(window.history.state, "", url);
    setSelection(value); setInspection(undefined); setPage(0); setExportFailure(undefined);
    setActiveView(view); setToolsOpen(false);
  };
  const openReport = (id: ReportView) => change({
    report: id,
    reviewReport: undefined,
    columns: reportSupportsMonths(id) ? selection.columns : "summary",
    ...(!reportSupportsComparison(id) || (selection.comparison === "explicit" && reportDefinition(id).position !== contextDefinition.position)
      ? { comparison: "none", compareFrom: undefined, compareTo: undefined } : {}),
  });
  const allRows = useMemo(() => report ? reportFlatRows(report.rows) : [], [report]);
  const visibleRows = useMemo(() => report ? reportFlatRows(report.rows, expanded) : [], [report, expanded]);
  const visible = visibleRows.slice(page * 100, page * 100 + 100);
  const inspect = (cell: DisplayCell, trigger: HTMLElement) => {
    panelTrigger.current = trigger; setToolsOpen(false); setExportOpen(false);
    setInspection({ cell, trigger, scroll: captureInspectorScroll(trigger) }); setActivePanel("inspect");
  };
  const selectedAmountChanged = useCallback((cell: DisplayCell | undefined) => {
    setInspection(before => before && cell && before.cell.row.id === cell.row.id && before.cell.report.digest === cell.report.digest && before.cell.column === cell.column ? before : undefined);
  }, []);
  const openPanel = (panel: "inspect" | "review", trigger: HTMLElement) => {
    panelTrigger.current = trigger; setToolsOpen(false); setExportOpen(false);
    setActivePanel(before => before === panel ? undefined : panel);
  };
  const closePanel = useCallback((restoreFocus: boolean) => {
    setActivePanel(undefined); setWorkPinned(false);
    const trigger = panelTrigger.current, scroll = activePanel === "inspect" && trigger === inspection?.trigger ? inspection.scroll : null;
    if (restoreFocus) requestAnimationFrame(() => { if (trigger?.isConnected) { restoreInspectorScroll(scroll); trigger.focus({ preventScroll: true }); } });
  }, [activePanel, inspection]);
  const toggleRow = (row: ReportRow) => {
    setExpanded(before => { const next = new Set(before); next.has(row.id) ? next.delete(row.id) : next.add(row.id); return next; }); setPage(0);
  };
  const closeInspection = () => {
    const trigger = inspection?.trigger;
    setInspection(undefined); setActivePanel(undefined); setWorkPinned(false);
    const scroll = inspection?.scroll;
    requestAnimationFrame(() => { if (trigger?.isConnected) { restoreInspectorScroll(scroll || null); trigger.focus({ preventScroll: true }); } });
  };
  const printDocument = () => {
    document.documentElement.dataset.vultReportPrint = "true";
    flushSync(() => setPrinting(true));
    window.print();
  };
  const unresolvedKeys = report ? Object.keys(report.reconciliation).filter((key) => key.endsWith("unresolved_gross_minor") || key.endsWith("unresolved_count")) : [];
  const periodTitle = contextDefinition.position
    ? `As of ${formatDate(selection.range.to, "full")}`
    : formatDateRange(selection.range.from, selection.range.to, "full");
  const afterToolsClose = (open: (trigger: HTMLButtonElement) => void) => {
    const trigger = toolsTrigger.current;
    setToolsOpen(false);
    // Finish the popover's focus restoration before opening the existing modal.
    requestAnimationFrame(() => requestAnimationFrame(() => { if (trigger?.isConnected && initialBook.current === book.id) open(trigger); }));
  };
  const compactTools = !toolsExpanded || workPinned;
  const controls = <>
    <section className="statement-tools-group" aria-label="Report scope">
    <StatementToolSelect label="Report" collapsed={compactTools} icon={File06} aria-label="Choose report" value={selection.report} onChange={event => openReport(event.currentTarget.value as ReportView)} options={groups.flatMap(group => [
      { value: `group:${group}`, label: group, disabled: true },
      ...reportDefinitions.filter(item => item.group === group).map(item => ({ value: item.id, label: item.label })),
    ])} />
    <div className="statement-tool-date" data-tool="period" title={periodTitle}><span className="statement-tool-date-label">{selection.columns==="months" ? "Column period" : contextDefinition.position ? "Date" : "Period"}</span><Calendar className="statement-tool-date-icon" aria-hidden />
    <ReportPeriodPicker book={book} range={selection.range} position={contextDefinition.position} months={selection.columns==="months"} triggerLabel={contextDefinition.position && selection.columns !== "months" ? formatDate(selection.range.to) : formatDateRange(selection.range.from, selection.range.to)} onRange={(range) => change({ range })} />
    </div>
    </section>
    <section className="statement-tools-group" aria-label="Report presentation">
    <StatementToolSelect label="Comparison" collapsed={compactTools} icon={SwitchHorizontal01} disabled={!reportSupportsComparison(contextReport)} aria-label="Compare report" value={selection.comparison} onChange={(e) => {
      const value=e.currentTarget.value;
      if(value==="custom"){setComparisonTrigger(e.currentTarget);return;}
      if(value==="fiscal_year_end"||value==="month_end"){change({comparison:"explicit",compareFrom:undefined,compareTo:reportComparisonDate(value,book,selection.range.to)});return;}
      change({comparison:value as ReportLocation["comparison"],compareFrom:undefined,compareTo:undefined});
    }} options={[
      { value: "none", label: "No comparison" },
      { value: "previous_period", label: contextDefinition.position ? "Previous month" : "Previous period" },
      { value: "previous_year", label: "Previous year" },
      ...(contextDefinition.position ? [{value:"month_end",label:"Previous month end"},{value:"fiscal_year_end",label:"Previous fiscal year end"}]:[]),
      ...(selection.comparison==="explicit" ? [{value:"explicit",label:"Custom comparison"}]:[]),
      { value: "custom", label: "Choose comparison dates…" },
    ]} />
    <StatementToolSelect label="Columns" collapsed={compactTools} icon={Columns03} disabled={!reportSupportsMonths(selection.report)} aria-label="Report columns" value={selection.columns || "summary"} onChange={event => change({ columns: event.currentTarget.value as ReportLocation["columns"] })} options={[
      { value: "summary", label: "Summary" }, { value: "months", label: "Months", disabled: !!monthViewReason(selection) && selection.columns !== "months" },
    ]} />
    </section>
  </>;
  const savedControls = <>
    <div className="statement-control statement-saved-control"><span>Saved view</span><Select size="sm" aria-label="Saved report view" value={activeView} disabled={pending} onChange={event => {
      const id = event.currentTarget.value;
      const saved = savedViews.find(item => item.id === id);
      if (saved) change({ ...saved.selection, compareFrom: saved.selection.compareFrom, compareTo: saved.selection.compareTo, reviewReport: saved.selection.reviewReport }, id);
      else if (["monthly", "quarterly", "balance"].includes(id)) change(reportPreset(id, book, selection, report), id);
    }} options={[
      { value: "current", label: "Current view" }, { value: "monthly", label: "Monthly P&L" }, { value: "quarterly", label: "Quarterly breakdown", disabled: !report }, { value: "balance", label: "Balance Sheet review", disabled: !report },
      ...savedViews.map(item => ({ value: item.id, label: item.name })),
    ]} /></div>
    <Button size="sm" color="tertiary" disabled={selection.invalid} onClick={event => { setViewName(""); setViewFailure(undefined); afterToolsClose(trigger => setSaveViewTrigger(trigger)); }}>Save view</Button>
  </>;

  const reviewPanel = report && <section id={reviewId} className="statement-grid-review" aria-label="Report scope and limitations">
            <dl className="statement-policy-grid">
              <div><dt>Recognition basis</dt><dd>{established(report.book.recognition_basis) ? report.book.recognition_basis : "Not established"}</dd></div>
              <div><dt>Reporting framework</dt><dd>{established(report.book.reporting_framework) ? report.book.reporting_framework : "Not established"}</dd></div>
              <div><dt>Source population</dt><dd>{report.scope.coverage === "complete" ? "Confirmed for this population" : report.scope.coverage === "partial" ? "Partial population" : "Completeness not confirmed"}</dd></div>
              <div><dt>Opening balances</dt><dd>{report.scope.opening_balances === "confirmed" ? "Confirmed" : "Not confirmed"}</dd></div>
              <div><dt>Audit</dt><dd>None</dd></div>
              {report.scope.knowledge_cutoff && <div><dt>Recorded through</dt><dd>{formatReadableDateTime(report.scope.knowledge_cutoff, "full")}{report.scope.ledger_revision ? `, ledger revision ${report.scope.ledger_revision.slice(0, 12)}` : ""}. Later or backdated entries can change these figures.</dd></div>}
            </dl>
            <ul className="statement-checks">{report.checks.map(check => <li key={check.id}><span className="statement-check-state">{check.status === "pass" ? "Passed" : check.status === "fail" ? "Needs attention" : check.status === "not_applicable" ? "Not applicable" : "Not confirmed"}</span><span>{check.message}</span></li>)}</ul>
            <ul className="statement-limitations">{[...new Set((display?.sources || [report]).flatMap(source => source.limitations))].map(limit => <li key={limit}>{limit}</li>)}</ul>
            <p className="statement-caption">Live reports may change when entries or presentation change. Calculation checks do not establish independent professional review.</p>
          </section>;

  return <div className="workspace-page statements-workspace" data-testid="statements-workspace" data-vult-context-inspector aria-busy={pending}>
    <HeaderActions active fallback={<PageToolbar actions={<CopyCurrentView book={book} range={selection.range} href={viewURL} disabled={!data || pending || selection.invalid} />} />}><CopyCurrentView book={book} range={selection.range} href={viewURL} disabled={!data || pending || selection.invalid} /></HeaderActions>
    <div className="statements-layout statement-workbench collection-inspector" data-tools-expanded={toolsExpanded && !workPinned} data-work-pinned={workPinned && !!activePanel} data-motion={toolsAnimate ? "pointer" : "instant"}>
      <div className="statement-main collection-inspector-collection">
        <header className="statement-context">
          <div className="statement-workbench-identity"><h2>{definition.label}</h2><span className="statement-context-currency"><span aria-hidden>·</span> {book.currency}</span></div>
          {report && <div className="statement-workbench-scope" data-scope-state={scopeStatus.state}>
            <NameLink type="button" className="statement-review-link" name={scopeStatus.label}
              aria-label={`${scopeStatus.label}. Review scope`} title={scopeStatus.explanation}
              aria-expanded={activePanel === "review"} aria-controls={reviewId}
              trailing={<ChevronRight aria-hidden />}
              onClick={event => openPanel("review", event.currentTarget)} />
            <span className="statement-scope-print" aria-hidden="true">{scopeStatus.label}</span>
          </div>}
        </header>
        <div className="statement-paper">
          <div className="statement-feedback">
            {failure && !data
              ? <FailureState scope="region" error={failure} intent="read" resource="statement" pending={pending} onRetry={() => { setExportFailure(undefined); setRetry((n) => n + 1); }} />
              : <ErrorMessage message={failure || exportFailure} intent="read" resource="statement" pending={pending} retained={data ? "view" : undefined} onRetry={() => { setExportFailure(undefined); setRetry((n) => n + 1); }} />}
            {selection.invalid && <p className="statement-caption">Choose a report and a valid reporting period to continue.</p>}
            {monthReason && <p className="statement-caption">{monthReason} Choose Summary or adjust the reporting period.</p>}
          </div>
          {pending && !data && <LoadingState scope="region" label="Loading report…" />}
          {!pending && !failure && !data && !selection.invalid && <EmptyState scope="region" title="Choose a report to begin" />}
          {selection.report === "cash_movement" && movement ? <div className="statement-cash-movement">
            <p className="statement-caption">{periodTitle}. {descriptions.cash_movement} This view does not classify operating, investing or financing activities.</p>
            <dl><div><dt>Money in</dt><dd>{money(movement.money_in_minor, book)}</dd></div><div><dt>Money out</dt><dd>{money(movement.money_out_minor, book)}</dd></div><div className="statement-cash-net"><dt>Net cash movement</dt><dd>{money((BigInt(movement.money_in_minor) - BigInt(movement.money_out_minor)).toString(), book)}</dd></div></dl>
            <Button size="sm" color="tertiary" onClick={() => onShowTransactions(selection.range)}>Open period activity</Button>
          </div> : report ? <>
            {report.status === "unsupported" && <div className="statement-empty"><h3>Accounting treatment is not established</h3><p>{descriptions[selection.report]}</p><p>Review the required scope and classification before this statement can be prepared.</p>{!!report.limitations.length && <ul className="statement-limitations">{report.limitations.map((limit, i) => <li key={i}>{limit}</li>)}</ul>}</div>}
            {report.rows.length > 0 && <>
              {display && <FinancialReportGrid display={display} rows={visible} expanded={expanded} onToggle={toggleRow} onInspect={inspect} rowOffset={page * 100} rowCount={visibleRows.length}
                selectionContainer={selectionHost} onSelectionChange={selectedAmountChanged}
                onExpandAll={() => { setExpanded(new Set(allRows.filter(({ row }) => row.children.length).map(({ row }) => row.id))); setPage(0); }}
                onCollapseAll={() => { setExpanded(new Set()); setPage(0); }} />}
              {visibleRows.length > 100 && <div className="statement-pagination"><span>{page * 100 + 1}–{Math.min((page + 1) * 100, visibleRows.length)} of {visibleRows.length} displayed rows</span><div><Button size="sm" color="tertiary" disabled={page === 0} onClick={() => setPage((p) => p - 1)}>Previous</Button><Button size="sm" color="tertiary" disabled={(page + 1) * 100 >= visibleRows.length} onClick={() => setPage((p) => p + 1)}>Next</Button></div></div>}
              {printing && display && (allRows.length <= 10000 ? <ReportTable display={display} rows={allRows} printable /> : <p className="statement-print-warning">Choose a shorter reporting period to print. The complete report remains available as CSV.</p>)}
            </>}
            {!report.rows.length && report.status !== "unsupported" && <EmptyState scope="region" title="No activity in this reporting scope" />}
          </> : null}

        </div>
      </div>
      <StatementTools expanded={toolsExpanded && !workPinned} animate={toolsAnimate} pinned={workPinned} panelOpen={!!activePanel} panelLabel={activePanel === "review" ? "Review scope" : "Inspect amount"}
        onClose={closePanel} onPin={() => { setToolsAnimate(false); setWorkPinned(value => !value); }}
        onToggle={animate => {
          setToolsAnimate(animate); setToolsOpen(false); setExportOpen(false); setActivePanel(undefined);
          setToolsExpanded(value => {
            const next = workPinned ? false : !value;
            preferredToolsExpanded.current = next;
            try { localStorage.setItem(toolsPreferenceKey, String(next)); } catch { /* Keep the current layout usable. */ }
            return next;
          });
          if (workPinned) setWorkPinned(false);
        }}
        footer={<Button size="sm" color="secondary" className="statement-tool-export" aria-label="Export report" aria-expanded={exportOpen} onClick={event => { exportTrigger.current = event.currentTarget; setToolsOpen(false); if (!workPinned) setActivePanel(undefined); setExportOpen(value => !value); }}><Download01 aria-hidden /><span>Export</span></Button>}
        panel={<>
          <div hidden={activePanel !== "inspect"} className="statement-work-inspect"><div ref={setSelectionHost} />
            {inspection && report && <ReportInspector embedded report={inspection.cell.report} row={inspection.cell.row} column={inspection.cell.column} displayLabel={inspection.cell.label} onClose={closeInspection} onInspect={onInspect} onRefresh={() => { closeInspection(); setRetry(n => n + 1); }} />}
            {!display && <p className="statement-caption">Select a supported ledger report to inspect its amounts.</p>}
          </div>
          <div hidden={activePanel !== "review"}>{reviewPanel}</div>
        </>}>
        {controls}
        <section className="statement-tools-group statement-tools-actions" aria-label="Investigation and views">
          <StatementToolAction collapsed={compactTools} label="Inspect amount" icon={<SearchLg aria-hidden />} disabled={!display} expanded={activePanel === "inspect"} onClick={trigger => openPanel("inspect", trigger)} />
          <StatementToolAction collapsed={compactTools} label="Review scope" icon={<ClipboardCheck aria-hidden />} disabled={!report} expanded={activePanel === "review"} onClick={trigger => openPanel("review", trigger)} />
          <StatementToolAction collapsed={compactTools} label="Report options" icon={<Sliders01 aria-hidden />} expanded={toolsOpen} onClick={trigger => { toolsTrigger.current = trigger; setExportOpen(false); if (!workPinned) setActivePanel(undefined); setToolsOpen(value => !value); }} />
        </section>
      </StatementTools>
    </div>
    <Popover triggerRef={toolsTrigger} isOpen={toolsOpen} onOpenChange={setToolsOpen} placement="left bottom" offset={8} containerPadding={12} isNonModal data-motion="instant" className="statement-tools-popover vult-surface-paper">
            <AriaDialog aria-label="Report options">
              <div className="statement-tools-views">{savedControls}</div>
              <details className="statement-result-details"><summary>Result details</summary><p>{report?.scope.generated_at ? `Calculated ${formatDateTime(report.scope.generated_at, "full")}` : "Recorded receipts and payments"}</p><p>Live reports may change when entries or report presentation change.</p>{selection.report !== "cash_movement" && <Button size="sm" color="tertiary" disabled={pending} onClick={() => { setToolsOpen(false); setRetry(n => n + 1); }}>Refresh report</Button>}</details>
            </AriaDialog>
          </Popover>
    <Popover triggerRef={exportTrigger} isOpen={exportOpen} onOpenChange={setExportOpen} placement="left bottom" offset={8} containerPadding={12} isNonModal data-motion="instant" className="statement-tools-popover statement-export-popover vult-surface-paper">
      <AriaDialog aria-label="Export report">
        {selection.report !== "cash_movement" && <Button size="sm" color="secondary" busy={exportPending} disabled={!display || !display.sources.every(reportExportAvailable) || report?.status === "unsupported" || pending} title={report?.status === "unsupported" ? "Establish the accounting treatment before exporting this statement." : undefined} onClick={() => void exportCSV()}><Download01 aria-hidden />Export CSV</Button>}
        <Button size="sm" color="tertiary" disabled={!data || pending || allRows.length > 10000} title={allRows.length > 10000 ? "Choose a shorter period to print, or export the complete CSV." : undefined} onClick={() => { setExportOpen(false); printDocument(); }}><Printer aria-hidden />Print / PDF</Button>
      </AriaDialog>
    </Popover>
    <Dialog open={!!saveViewTrigger} title="Save report view" description="Save these reporting parameters in this browser for this Book. This saves a live view, not frozen financial results." returnTo={saveViewTrigger} onOpenChange={open => { if (!open) setSaveViewTrigger(undefined); }}>
      <form className="statement-save-view" onSubmit={event => {
        event.preventDefault();
        try { const views = saveReportView(book, viewName, selection); setSavedViews(views); setActiveView(views[0].id); setSaveViewTrigger(undefined); }
        catch (error) { setViewFailure(error); }
      }}>
        <Field label="View name"><input value={viewName} maxLength={80} required onChange={event => setViewName(event.currentTarget.value)} /></Field>
        <ErrorMessage message={viewFailure} intent="read" resource="statement" />
        <div className="statement-pack-actions"><Button size="sm" color="tertiary" onClick={() => setSaveViewTrigger(undefined)}>Cancel</Button><Button size="sm" type="submit" disabled={!viewName.trim()}>Save view</Button></div>
      </form>
    </Dialog>
    {comparisonTrigger && <ReportComparison key={`${book.id}:${selection.report}`} selection={selection} position={contextDefinition.position} returnTo={comparisonTrigger} onClose={()=>setComparisonTrigger(undefined)} onApply={value=>{change(value);setComparisonTrigger(undefined);}} />}
  </div>;
}
