import { accountCodeLabel } from "../../lib/account-codes";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { ChevronDown, ChevronDownDouble, ChevronRight, ChevronUpDouble } from "../../design-system/icons";
import { money } from "../../lib/api";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { displayCell, omittedAmountNote, type DisplayCell, type DisplayColumn, type ReportDisplay } from "../../lib/financial-report-workspace";
import { reportDefinition, reportFlatRows, type ReportRow } from "../../lib/financial-reports";
import { Button } from "../ui/button";
import { Dialog } from "../ui/dialog";
import { Field } from "../form";
import { Tooltip, TooltipTrigger } from "../../design-system/catalog/components/base/tooltip/tooltip";
import { Button as TreeButton } from "../../design-system/catalog/components/vult-ui/button";
import { useViewState } from "../../design-system/catalog/components/vult-ui/view-state";
import { FinancialGridLayout, STATEMENT_WIDTHS_KEY, validStatementWidths } from "./financial-grid-layout";
import { COLUMN_WIDTH_LIMIT as GRID_WIDTH_PREFERENCE_LIMIT, resolveColumnWidths } from "../../design-system/column-layout";
import { useColumnLayout } from "../../design-system/use-column-layout";
import { ColumnResizer } from "../../design-system/column-resizer";
import { ColumnLayoutMenu } from "../../design-system/column-layout-menu";
import "./financial-report-grid.css";

type Position = { row: string; column: string };
type Props = {
  display: ReportDisplay;
  rows: { row: ReportRow; depth: number }[];
  expanded: ReadonlySet<string>;
  onToggle: (row: ReportRow) => void;
  onInspect: (cell: DisplayCell, trigger: HTMLElement) => void;
  onExpandAll?: () => void;
  onCollapseAll?: () => void;
  rowOffset?: number;
  rowCount?: number;
  selectionContainer?: HTMLElement | null;
  onSelectionChange?: (cell: DisplayCell | undefined) => void;
};

function columnPeriod(display: ReportDisplay, column: DisplayColumn) {
  const period = column.period;
  return reportDefinition(display.base.report).position
    ? `As of ${formatDate(period.to, "full")}`
    : period.from ? formatDateRange(period.from, period.to, "full") : formatDate(period.to, "full");
}

function compactColumnPeriod(display: ReportDisplay, column: DisplayColumn) {
  if (!["balance_sheet", "profit_loss"].includes(display.base.report)) return column.label;
  if (column.sourceColumn === "variance") return column.label;
  return reportDefinition(display.base.report).position || !column.period.from
    ? formatDate(column.period.to)
    : formatDateRange(column.period.from, column.period.to);
}

/** A read-only view of the canonical projection. Values and contributions always
 * come from displayCell, including each independently captured month column. */
export function FinancialReportGrid(props: Props) {
  return <FinancialGridLayout book={props.display.base.book.id} report={props.display.base.report}><FinancialReportGridContent {...props} /></FinancialGridLayout>;
}

function FinancialReportGridContent({ display, rows, expanded, onToggle, onInspect, onExpandAll, onCollapseAll, rowOffset = 0, rowCount = rows.length, selectionContainer, onSelectionChange }: Props) {
  const report = display.base;
  const branches = useMemo(() => reportFlatRows(display.rows).filter(({ row }) => row.children.length), [display.rows]);
  const allExpanded = branches.length > 0 && branches.every(({ row }) => expanded.has(row.id));
  const treeToggleLabel = allExpanded ? "Collapse all" : "Expand all";
  const grid = useRef<HTMLDivElement>(null);
  const footer = useRef<HTMLElement>(null);
  const buttons = useRef(new Map<string, HTMLButtonElement>());
  const labels = useRef(new Map<string, HTMLSpanElement>());
  const [nameHelp, setNameHelp] = useState<string>();
  const help = useId();
  const [selection, setSelection] = useState<Position>();
  const [notice, setNotice] = useState("");
  const [manualCopy, setManualCopy] = useState<{ value: string; trigger: HTMLElement | null }>();
  const generation = useRef(0);
  const identity = display.columns.map(c => `${c.id}:${c.source.digest}:${c.sourceColumn}`).join("|");
  const initialRow = rows.find(({ row }) => row.role === "account")?.row || rows[0]?.row;
  const selectedRow = rows.find(({ row }) => row.id === selection?.row)?.row || initialRow;
  const selectedColumn = display.columns.find(c => c.id === selection?.column) || display.columns[0];
  const selected = selectedRow && selectedColumn ? displayCell(display, selectedRow, selectedColumn) : undefined;
  const [savedWidths, setSavedWidths] = useViewState<Record<string, number>>(STATEMENT_WIDTHS_KEY, {}, { storage: "local", valid: validStatementWidths });
  const columnSizes = useMemo(() => [{ id: "account", min: 160, preferred: 320, max: GRID_WIDTH_PREFERENCE_LIMIT }, ...display.columns.map(column => {
    const longest = reportFlatRows(display.rows).reduce((length, { row }) => {
      const value = displayCell(display, row, column).value;
      return value === undefined ? length : Math.max(length, money(value, report.book).length);
    }, 0);
    const min = Math.max(128, longest * 9 + 40);
    const heading = Math.ceil(compactColumnPeriod(display, column).length * 6.5 + 32);
    return { id: column.id, min, preferred: Math.max(184, min, heading), max: Math.max(GRID_WIDTH_PREFERENCE_LIMIT, min, heading) };
  })], [display]);
  const [viewport, setViewport] = useState({ width: 0, accountBase: 320 });
  const idleWidths = resolveColumnWidths(columnSizes, savedWidths);
  const amountMinimum = Math.max(...columnSizes.slice(1).map(size => size.min), 0);
  const unfreezeAccounts = viewport.width > 0 && idleWidths.account + amountMinimum > viewport.width;
  // Keep the same coordinate system throughout a resize and after release.
  // While frozen, Account cannot grow so wide that it hides every amount.
  const sizes = columnSizes.map((size, index) => index === 0 && viewport.width > 0 && !unfreezeAccounts
    ? { ...size, max: Math.min(size.max, Math.max(size.min, viewport.width - amountMinimum)) } : size);
  const layout = useColumnLayout({ columns: sizes, view: STATEMENT_WIDTHS_KEY, available: viewport.width, scroll: grid,
    external: { identity: JSON.stringify([identity, viewport.accountBase]), widths: savedWidths, update: setSavedWidths } });
  const { widths, extent } = layout;
  const tableStyle: CSSProperties = {
    "--financial-grid-account-width": "var(--financial-grid-column-0)",
    "--financial-grid-frozen-width": `${widths.account}px`,
    "--financial-grid-width": `${extent}px`,
    "--financial-grid-min-width": `${extent}px`,
    ...Object.fromEntries(sizes.map((size, index) => [`--financial-grid-column-${index}`, `${widths[size.id]}px`])),
  } as CSSProperties;
  const columnGroups = useMemo(() => {
    if (!display.months) return [];
    const groups: { key: string; label: string; count: number }[] = [];
    for (const column of display.columns) {
      const month = column.id.startsWith("month:");
      const key = month ? column.period.to.slice(0, 4) : column.id;
      const previous = groups[groups.length - 1];
      if (previous?.key === key) previous.count++;
      else groups.push({ key, label: month ? key : column.label, count: 1 });
    }
    return groups;
  }, [display]);
  const headingRows = columnGroups.length ? 2 : 1;
  useEffect(() => { onSelectionChange?.(selected); }, [identity, selectedRow?.id, selectedColumn?.id, onSelectionChange]);
  const key = (row: string, column: string) => JSON.stringify([row, column]);
  const inspectable = (cell: DisplayCell) => cell.value !== undefined && !!(cell.row.drilldown || cell.row.allocations?.length);
  const selectedButton = () => selectedRow && selectedColumn ? buttons.current.get(key(selectedRow.id, selectedColumn.id)) : undefined;

  useEffect(() => {
    generation.current++;
    layout.cancel();
    setSelection(undefined); setNotice(""); setManualCopy(undefined); setNameHelp(undefined);
    if (grid.current) { grid.current.scrollTop = 0; grid.current.scrollLeft = 0; }
    return () => { generation.current++; };
  }, [identity]);
  useEffect(() => {
    // Collapsing a selected descendant transfers selection to its nearest
    // visible ancestor, rather than leaving an invisible keyboard target.
    if (!selection || rows.some(({ row }) => row.id === selection.row)) return;
    const ancestors: string[] = [];
    const find = (items: ReportRow[], parents: string[]): boolean => items.some(row => {
      if (row.id === selection.row) { ancestors.push(...parents); return true; }
      return find(row.children, [...parents, row.id]);
    });
    find(report.rows, []);
    const parent = ancestors.reverse().find(id => rows.some(({ row }) => row.id === id));
    setSelection({ row: parent || initialRow?.id || "", column: selectedColumn?.id || "" });
    setNotice("");
  }, [rows, selection, report.rows, initialRow?.id, selectedColumn?.id]);
  useLayoutEffect(() => {
    const host = grid.current;
    if (!host) return;
    const fit = () => {
      const paperFooter = host.closest(".statement-paper")?.querySelector<HTMLElement>(".statement-footer");
      const pagination = host.closest(".statement-paper")?.querySelector<HTMLElement>(".statement-pagination");
      const reserved = (selectionContainer ? 0 : footer.current?.getBoundingClientRect().height || 64) + (paperFooter?.getBoundingClientRect().height || 0) + (pagination?.getBoundingClientRect().height || 0) + 24;
      host.style.maxHeight = `${Math.max(240, window.innerHeight - host.getBoundingClientRect().top - reserved)}px`;
      const width = host.clientWidth;
      const accountBase = Number.parseFloat(window.getComputedStyle(host.querySelector("table")!).getPropertyValue("--financial-grid-account-base")) || 320;
      setViewport(previous => previous.width === width && previous.accountBase === accountBase ? previous : { width, accountBase });
    };
    fit();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(fit);
    observer?.observe(host);
    if (host.parentElement) observer?.observe(host.parentElement);
    const paper = host.closest(".statement-paper");
    if (paper) observer?.observe(paper);
    window.addEventListener("resize", fit);
    return () => { observer?.disconnect(); window.removeEventListener("resize", fit); };
  }, [identity, rows.length, selectionContainer]);
  const choose = (row: ReportRow, column: DisplayColumn) => {
    generation.current++;
    setSelection({ row: row.id, column: column.id }); setNotice("");
  };
  const revealName = (row: ReportRow) => {
    const text = labels.current.get(row.id);
    setNameHelp(text && text.scrollWidth > text.clientWidth ? row.id : undefined);
  };
  const reveal = (target: HTMLButtonElement) => {
    const host = grid.current;
    if (!host) return;
    const bounds = host.getBoundingClientRect(), cell = target.getBoundingClientRect();
    const accountHeading = host.querySelector<HTMLElement>("thead th");
    const frozen = accountHeading && window.getComputedStyle(accountHeading).insetInlineStart !== "auto" ? accountHeading.getBoundingClientRect().width : 0;
    const heading = host.querySelector("thead")?.getBoundingClientRect().height || 0;
    if (cell.width > bounds.width - frozen) host.scrollLeft += cell.right - bounds.right;
    else if (cell.left < bounds.left + frozen) host.scrollLeft += cell.left - bounds.left - frozen;
    else if (cell.right > bounds.right) host.scrollLeft += cell.right - bounds.right;
    if (cell.top < bounds.top + heading) host.scrollTop += cell.top - bounds.top - heading;
    else if (cell.bottom > bounds.bottom) host.scrollTop += cell.bottom - bounds.bottom;
  };
  const resizer = (id: string, label: string) => <ColumnResizer layout={layout} id={id} label={label} className="financial-grid-resizer" />;
  const inspect = (cell = selected, trigger = selectedButton()) => {
    if (cell && trigger && inspectable(cell)) onInspect(cell, trigger);
  };
  const copy = (cell = selected, trigger: HTMLElement | null = selectedButton() || null) => {
    if (!cell || cell.value === undefined) return;
    const value = money(cell.value, report.book), seq = ++generation.current;
    const failed = () => { if (seq === generation.current) setManualCopy({ value, trigger }); };
    try {
      if (!navigator.clipboard?.writeText) { failed(); return; }
      void navigator.clipboard.writeText(value).then(() => {
        if (seq === generation.current) setNotice("Displayed amount copied.");
      }, failed);
    } catch { failed(); }
  };
  const navigate = (event: KeyboardEvent<HTMLButtonElement>, ri: number, ci: number) => {
    const cell = displayCell(display, rows[ri].row, display.columns[ci]);
    if (event.key === "Enter") { event.preventDefault(); inspect(cell, event.currentTarget); return; }
    let r = ri, c = ci;
    if (event.key === "ArrowDown") r++;
    else if (event.key === "ArrowUp") r--;
    else if (event.key === "ArrowRight") c++;
    else if (event.key === "ArrowLeft") c--;
    else if (event.key === "Home") { c = 0; if (event.ctrlKey || event.metaKey) r = 0; }
    else if (event.key === "End") { c = display.columns.length - 1; if (event.ctrlKey || event.metaKey) r = rows.length - 1; }
    else return;
    event.preventDefault();
    r = Math.max(0, Math.min(rows.length - 1, r)); c = Math.max(0, Math.min(display.columns.length - 1, c));
    const target = buttons.current.get(key(rows[r].row.id, display.columns[c].id));
    if (target) { choose(rows[r].row, display.columns[c]); target.focus({ preventScroll: true }); reveal(target); }
  };

  return <div className="financial-grid-region">
    <p className="sr-only" id={help}>Read-only financial report. Arrow keys move between amounts; Home and End move across a row. Control or Command with Home or End moves to the first or last amount on this page. Enter inspects contributing postings. Control or Command C copies the displayed amount. Drag header dividers to resize columns, or focus a divider and use Left and Right. Enter resets its width. Reported amounts cannot be edited.</p>
    <div className="financial-grid-scroll" ref={grid} data-vult-view-scroll data-unfreeze-accounts={unfreezeAccounts || undefined}>
      <table className="financial-grid" role="grid" aria-readonly="true" data-report-result={report.digest} data-month-headings={!!columnGroups.length || undefined} aria-label={`${report.book.name} · ${report.title} · ${report.book.currency}`} aria-describedby={help} aria-rowcount={rowCount + headingRows} aria-colcount={display.columns.length + 1} style={tableStyle}>
        <colgroup><col className="financial-grid-account-column" />{display.columns.map((column, index) => <col key={column.id} className="financial-grid-value-column" style={{ width: `var(--financial-grid-column-${index + 1})` }} />)}</colgroup>
        <thead><tr aria-rowindex={1} className={columnGroups.length ? "financial-grid-year-heading" : undefined}><th scope="col" rowSpan={headingRows}><div className="financial-grid-account-heading"><div className="items-center"><span>{["journal", "general_ledger"].includes(report.report) ? "Account / entry" : "Account"}</span><ColumnLayoutMenu layout={layout} /></div>{onExpandAll && onCollapseAll && <div><Tooltip title={treeToggleLabel} placement="bottom" motion="none"><TreeButton size="sm" color="secondary" aria-label={treeToggleLabel} iconLeading={allExpanded ? ChevronUpDouble : ChevronDownDouble} isDisabled={!branches.length} onClick={allExpanded ? onCollapseAll : onExpandAll} /></Tooltip></div>}</div>{resizer("account", "account")}</th>{columnGroups.length ? columnGroups.map(group => <th scope="colgroup" colSpan={group.count} key={group.key}><span className="financial-grid-year-label">{group.label}</span></th>) : display.columns.map(column => <th scope="col" key={column.id} data-selected-column={selectedColumn?.id === column.id || undefined} aria-label={`${column.label}, ${columnPeriod(display, column)}`} title={columnPeriod(display, column)}>
          <span className="financial-grid-header-label">{compactColumnPeriod(display, column)}</span>{resizer(column.id, `${column.label}, ${columnPeriod(display, column)}`)}
        </th>)}</tr>{!!columnGroups.length && <tr aria-rowindex={2} className="financial-grid-month-heading">{display.columns.map(column => <th scope="col" key={column.id} data-report-column={column.id} data-selected-column={selectedColumn?.id === column.id || undefined} aria-label={`${column.label}, ${columnPeriod(display, column)}${column.sourceColumn === "variance" ? ", current less comparison" : ""}`} title={columnPeriod(display, column)}>
          <span className="financial-grid-header-label">{compactColumnPeriod(display, column)}</span>{resizer(column.id, `${column.label}, ${columnPeriod(display, column)}`)}
        </th>)}</tr>}</thead>
        <tbody>{rows.map(({ row, depth }, ri) => <tr key={row.id} className={`financial-grid-row financial-grid-row-${row.role}`} data-report-row={row.id} data-selected-row={row.id === selectedRow?.id || undefined} aria-rowindex={rowOffset + ri + headingRows + 1}>
          <th scope="row" aria-colindex={1}><div className="financial-grid-account" style={{ paddingInlineStart: `${depth * 16}px` }}>
            {row.children.length ? <button type="button" className="financial-grid-disclosure vult-focus-target" aria-label={`${expanded.has(row.id) ? "Collapse" : "Expand"} ${row.label}`} aria-expanded={expanded.has(row.id)} onClick={() => onToggle(row)}>{expanded.has(row.id) ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}</button> : <span className="financial-grid-disclosure-space" />}
            <Tooltip title={<span className="financial-grid-name-tooltip">{row.gl_code?.trim() && <><span className="vult-code">{row.gl_code.trim()}</span>{" · "}</>}{row.label}</span>} placement="top start" motion="none" isOpen={nameHelp === row.id} onOpenChange={open => open ? revealName(row) : setNameHelp(undefined)}>
              <TooltipTrigger excludeFromTabOrder className="financial-grid-label" aria-label={`Select ${accountCodeLabel(row.label, row.gl_code)}`} onMouseEnter={() => revealName(row)} onMouseLeave={() => setNameHelp(undefined)} onFocus={() => revealName(row)} onBlur={() => setNameHelp(undefined)} onPress={() => { const column = selectedColumn || display.columns[0]; if (column) { choose(row, column); const target = buttons.current.get(key(row.id, column.id)); if (target) { target.focus({ preventScroll: true }); reveal(target); } } }}>
                <span className="financial-grid-label-text" ref={node => { if (node) labels.current.set(row.id, node); else labels.current.delete(row.id); }}>{row.gl_code?.trim() && <><span className="vult-code">{row.gl_code.trim()}</span>{" · "}</>}{row.label}</span>
                {row.date && <span className="statement-row-date">{formatDate(row.date)}</span>}
              </TooltipTrigger>
            </Tooltip>
          </div></th>
          {display.columns.map((column, ci) => {
            const cell = displayCell(display, row, column), active = row.id === selectedRow?.id && column.id === selectedColumn?.id;
            const value = cell.value === undefined ? "—" : money(cell.value, report.book);
            return <td key={column.id} role="gridcell" aria-colindex={ci + 2} aria-selected={active} data-column-result={cell.report.digest} data-source-column={cell.column} data-selected-column={column.id === selectedColumn?.id || undefined}>
              <button type="button" className="financial-grid-value" ref={node => { const id = key(row.id, column.id); if (node) buttons.current.set(id, node); else buttons.current.delete(id); }} tabIndex={active ? 0 : -1} data-grid-row={row.id} data-grid-column={column.id} aria-label={`${row.label}, ${column.label}, ${columnPeriod(display, column)}, ${value}${column.sourceColumn === "variance" ? ", current less comparison" : ""}`}
                onFocus={() => { choose(row, column); revealName(row); }} onBlur={() => setNameHelp(undefined)} onClick={() => choose(row, column)} onDoubleClick={event => { choose(row, column); inspect(cell, event.currentTarget); }} onKeyDown={event => navigate(event, ri, ci)} onCopy={event => {
                  if (cell.value === undefined) return;
                  event.preventDefault(); event.clipboardData.setData("text/plain", value); setNotice("Displayed amount copied.");
                }}>{value}</button>
            </td>;
          })}
        </tr>)}</tbody>
      </table>
    </div>
    {(() => { const tools = <footer ref={footer} className="financial-grid-selection" aria-label="Selected financial value">
      <div><span>{selectedRow?.label || "Select an amount"}</span><span>Read-only{selectedColumn && ` · ${selectedColumn.label} · ${columnPeriod(display, selectedColumn)} · ${report.book.currency}`}{selectedColumn?.sourceColumn === "variance" && report.scope.comparison && ` · against ${report.scope.comparison.from ? formatDateRange(report.scope.comparison.from, report.scope.comparison.to, "full") : formatDate(report.scope.comparison.to, "full")}`}</span>{selected?.omitted && <span>{omittedAmountNote}</span>}</div>
      <div className="financial-grid-selection-actions"><span role="status">{notice}</span><Button size="sm" color="tertiary" disabled={selected?.value === undefined} onClick={() => copy()}>Copy amount</Button><Button size="sm" color="secondary" disabled={!selected || !inspectable(selected)} onClick={() => inspect()}>Inspect postings</Button></div>
    </footer>; return selectionContainer ? createPortal(tools, selectionContainer) : tools; })()}
    <Dialog open={!!manualCopy} title="Copy amount" description="Clipboard access is unavailable. Copy this exact displayed amount." returnTo={manualCopy?.trigger} onOpenChange={open => { if (!open) setManualCopy(undefined); }}>
      <Field label="Displayed amount"><input autoFocus readOnly value={manualCopy?.value || ""} onFocus={event => event.currentTarget.select()} /></Field>
    </Dialog>
  </div>;
}
