"use client";

import {
  type CSSProperties,
  type FC,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { ArrowDown, XClose } from "../../../../icons";
import {
  Cell,
  Column,
  Row,
  type Selection,
  type SortDescriptor,
  Table,
  TableBody,
  TableHeader,
} from "react-aria-components";
import { TablePagination } from "@vult/components/application/table/table-pagination";
import { Button } from "@vult/components/vult-ui/button";
import { Checkbox } from "@vult/components/vult-ui/checkbox";
import { cx } from "@vult/utils/cx";
import styles from "./records-table.module.css";
import { useViewState } from "@vult/components/vult-ui/view-state";
import {
  viewNumber,
  viewStringList,
} from "@vult/lib/view-state";
import { focusWithoutScroll } from "../../../../focus-without-scroll";
import { useColumnLayout } from "../../../../use-column-layout";
import { ColumnResizer } from "../../../../column-resizer";
import { ColumnLayoutMenu } from "../../../../column-layout-menu";

/** Table standard v1 (docs/design/tables.md). A column's kind gives its
 * starting width and its minimum. Every data column can be resized; a resize
 * changes only that column, and the table scrolls when columns exceed it. */
export type RecordsTableColumnKind = "text" | "date" | "money" | "status" | "control";
export const COLUMN_KIND_WIDTHS = { date: 112, money: 160, status: 128 } as const;
export const COLUMN_KIND_MIN_WIDTHS = { date: 88, money: 96, status: 96 } as const;
export const TEXT_COLUMN_MIN_WIDTH = 120;
export const TEXT_COLUMN_WIDTH = 240;
/** The row selection checkbox shares the first column. */
export const SELECTION_SLOT_WIDTH = 32;

export type RecordsTableColumn = {
  id: string;
  label: string;
  icon?: FC<{ className?: string }> | null;
  headerTrailing?: ReactNode;
  kind: RecordsTableColumnKind;
  /** Text: its starting width (default 240). Control: the control's width. */
  width?: number;
  /** Text only: a minimum above 120px when the content is unreadable below it. */
  minWidth?: number;
  /** Fix a column at its width without hiding its heading. */
  isResizable?: boolean;
  isRowHeader?: boolean;
  allowsSorting?: boolean;
  /** Hide secondary columns when the table's own viewport is narrower than this. */
  hideBelow?: number;
  /** A fixed action column has an accessible heading without a visible label or resizer. */
  isUtility?: boolean;
  /** Keep an action column reachable while the table scrolls horizontally. */
  pin?: "end";
  /** Text only: the column takes the table's whole width, never less than its
   * minimum, for a phone row that composes a record in one cell. */
  fill?: boolean;
};

export type RecordsTableViewState = {
  page: number;
  pageSize: number;
  scrollTop: number;
  scrollLeft: number;
};
export type RecordsTableViewStateRef = {
  current: RecordsTableViewState | null;
};

/** Text cut short by its column shows in full on hover, unless the cell
 * already gives its own hover text. */
function revealTruncated(event: { target: EventTarget | null; currentTarget: HTMLElement }) {
  const cell = event.currentTarget;
  let element = event.target instanceof HTMLElement ? event.target : null;
  const titled = element?.closest("[title]");
  if (!element || (titled && cell.contains(titled))) return;
  while (element && cell.contains(element)) {
    if (element.scrollWidth > element.clientWidth + 1) {
      const text = element.innerText.trim();
      if (text) element.title = text;
      return;
    }
    element = element.parentElement;
  }
}

function isRowActionTarget(target: EventTarget | null) {
  return (
    target instanceof Element &&
    !target.closest(
      "a,button,input,select,textarea,label,[contenteditable=true],[role=button],[role=link],[role=checkbox],[role=combobox],[role=switch]",
    )
  );
}

export function RecordsTable<T extends object>({
  ariaLabel,
  columns: configuredColumns,
  fitColumns = false,
  selectionEnabled = true,
  rows,
  rowId,
  rowLabel,
  renderCell,
  emptyState,
  toolbar,
  notice,
  isDisabled = false,
  className,
  onAtTopChange,
  onSortChange,
  sortDescriptor,
  pageResetKey,
  pagination,
  pageStart: externalPageStart = 0,
  paginationAriaLabel = `${ariaLabel} pages`,
  paginationTrailing,
  showPagination = rows.length > 0,
  testId,
  frameTestId,
  columnHeaderTestId,
  paginationTestId,
  rowTestId,
  rowVariant,
  onRowAction,
  activeRowId,
  viewState,
  remotePagination,
}: {
  ariaLabel: string;
  columns: readonly RecordsTableColumn[];
  /** @deprecated Widths are stable; the Columns menu provides explicit Fit. */
  fitColumns?: boolean;
  /** Read-only tables can omit row selection. */
  selectionEnabled?: boolean;
  rows: readonly T[];
  rowId: (row: T) => string;
  rowLabel: (row: T) => string;
  renderCell: (row: T, column: RecordsTableColumn, index: number) => ReactNode;
  emptyState: ReactNode;
  toolbar?: ReactNode;
  notice?: ReactNode;
  isDisabled?: boolean;
  className?: string;
  onAtTopChange?: (atTop: boolean) => void;
  /** The caller sorts the complete collection, or requests a sorted server page. */
  onSortChange?: (descriptor: SortDescriptor) => void;
  sortDescriptor?: SortDescriptor;
  /** Reset the pager on an intentional filter or sort change, not a live row update. */
  pageResetKey?: string;
  /** Supply the existing server pager, or false to show its rows without a footer. */
  pagination?: ReactNode;
  pageStart?: number;
  paginationAriaLabel?: string;
  paginationTrailing?: ReactNode;
  showPagination?: boolean;
  testId: string;
  frameTestId?: string;
  columnHeaderTestId?: string;
  paginationTestId?: string;
  rowTestId?: (row: T) => string;
  /** Give a checked financial total a distinct summary treatment. */
  rowVariant?: (row: T) => "default" | "summary";
  /** Open details from the row while keeping checkbox selection separate. */
  onRowAction?: (row: T) => void;
  /** The record being inspected, independent of the selected checkbox set. */
  activeRowId?: string | null;
  /** The owning view retains navigation while this table is temporarily closed. */
  viewState?: RecordsTableViewStateRef;
  remotePagination?: {
    hasMore: boolean;
    loading: boolean;
    onNextPage: () => void;
  };
}) {
  const [scroll, setScroll] = useState<HTMLElement | null>(null);
  const [header, setHeader] = useState<HTMLElement | null>(null);
  // Columns past the visible edge are signalled by a fade, never hidden silently.
  const [overflowEnd, setOverflowEnd] = useState(false);
  // The frozen first column shows its edge once content scrolls beneath it.
  const [scrolledStart, setScrolledStart] = useState(false);
  const stateKey = `table:${testId}`;
  const [page, setPage] = useViewState(
    `${stateKey}:page`,
    () => viewState?.current?.page ?? 1,
    {
      valid: (value): value is number =>
        viewNumber(value) && Number.isSafeInteger(value) && value >= 1,
    },
  );
  const [pageSize, setPageSize] = useViewState(
    `${stateKey}:page-size`,
    () => viewState?.current?.pageSize ?? 25,
    {
      valid: (value): value is number =>
        typeof value === "number" && [25, 50, 100].includes(value),
    },
  );
  const [requestedPage, setRequestedPage] = useState<number | null>(null);
  const filledPage = useRef<string | null>(null);
  const [selected, setSelected, selectionReady] = useViewState<Selection>(
    `${stateKey}:selected`,
    () => new Set(),
    {
      codec: {
        encode: (value) =>
          value === "all" ? JSON.parse(pageRowIds) : Array.from(value, String),
        decode: (value) => (viewStringList(value) ? new Set(value) : undefined),
      },
    },
  );
  const [availableWidth, setAvailableWidth] = useState(0);
  const externalPagination = pagination !== undefined;
  const pageCount = Math.max(1, Math.ceil(rows.length / pageSize));
  const currentPage = Math.min(page, pageCount);
  const pageStart = externalPagination
    ? externalPageStart
    : (currentPage - 1) * pageSize;
  const visibleRows = externalPagination
    ? rows
    : rows.slice(pageStart, pageStart + pageSize);
  const pageRowIds = JSON.stringify(visibleRows.map(rowId));
  const selectedCount = visibleRows.filter(
    (row) => selected === "all" || selected.has(rowId(row)),
  ).length;

  useLayoutEffect(() => {
    if (!scroll || !viewState) return;
    if (viewState.current) {
      scroll.scrollTop = viewState.current.scrollTop;
      scroll.scrollLeft = viewState.current.scrollLeft;
    }
    const retain = () => {
      viewState.current = {
        page: currentPage,
        pageSize,
        scrollTop: scroll.scrollTop,
        scrollLeft: scroll.scrollLeft,
      };
    };
    retain();
    scroll.addEventListener("scroll", retain, { passive: true });
    return () => {
      retain();
      scroll.removeEventListener("scroll", retain);
    };
  }, [scroll, viewState, currentPage, pageSize]);

  useEffect(() => {
    if (requestedPage === null) return;
    if (requestedPage <= pageCount) {
      setPage(requestedPage);
      setRequestedPage(null);
      scroll?.scrollTo({ top: 0 });
      onAtTopChange?.(requestedPage === 1);
    } else if (!remotePagination?.hasMore) setRequestedPage(null);
  }, [
    requestedPage,
    pageCount,
    remotePagination?.hasMore,
    scroll,
    onAtTopChange,
  ]);

  // Fill a short batch before exposing another page, without repeatedly
  // requesting an unchanged cursor after a failed read.
  useEffect(() => {
    const key = `${currentPage}:${pageSize}:${rows.length}`;
    if (
      rows.length > 0 &&
      rows.length < currentPage * pageSize &&
      remotePagination?.hasMore &&
      !remotePagination.loading &&
      requestedPage === null &&
      filledPage.current !== key
    ) {
      filledPage.current = key;
      remotePagination.onNextPage();
    }
  }, [currentPage, pageSize, rows.length, remotePagination, requestedPage]);
  const columns = configuredColumns.filter(
    (column) =>
      !availableWidth ||
      !column.hideBelow ||
      availableWidth >= column.hideBelow,
  );
  // Fit is an explicit action; retain the catalog prop for API compatibility.
  void fitColumns;
  // Only controls are fixed. Dates, amounts and statuses resize like text.
  const fixedColumn = (column: RecordsTableColumn) =>
    column.kind === "control" || column.isUtility || column.isResizable === false;
  const resizeScroll = useRef<HTMLElement | null>(null);
  resizeScroll.current = scroll;
  const sizes = columns.map((column, index) => {
    const slot = selectionEnabled && index === 0 ? SELECTION_SLOT_WIDTH : 0;
    if (column.kind === "control") {
      const width = (column.width ?? 48) + slot;
      return { id: column.id, min: width, preferred: width, fixed: true };
    }
    const min = (column.kind === "text"
      ? Math.max(TEXT_COLUMN_MIN_WIDTH, column.minWidth ?? 0)
      : COLUMN_KIND_MIN_WIDTHS[column.kind]) + slot;
    const start = column.kind === "text" ? column.width ?? TEXT_COLUMN_WIDTH : COLUMN_KIND_WIDTHS[column.kind];
    return { id: column.id, min, preferred: Math.max(min, start + slot), fixed: fixedColumn(column) };
  });
  const sizeOf = (id: string) => sizes.find((size) => size.id === id)!;
  const layout = useColumnLayout({ columns: sizes, view: stateKey, available: availableWidth, scroll: resizeScroll, disabled: isDisabled });
  const widths = layout.widths;
  const filling = columns.find((column) => column.fill);
  // A filling column is drawn at its minimum and grows into the rest.
  const renderedWidth = filling ? layout.extent - widths[filling.id] + sizeOf(filling.id).min : layout.extent;

  const hasPinnedEndUtility = Boolean(
    columns.at(-1)?.isUtility && columns.at(-1)?.pin === "end",
  );
  const columnTracks = columns.map(column =>
    column.fill ? `minmax(${sizeOf(column.id).min}px, 1fr)` : `${widths[column.id]}px`);
  // Spare space belongs to the canvas, never to the fixed row-action buttons.
  // A filling column takes it itself.
  if (!filling) {
    if (hasPinnedEndUtility) columnTracks.splice(columnTracks.length - 1, 0, "minmax(0, 1fr)");
    else columnTracks.push("minmax(0, 1fr)");
  }

  useLayoutEffect(() => {
    if (!scroll || !header) return;
    const edge = () => {
      setOverflowEnd(scroll.scrollLeft + scroll.clientWidth < scroll.scrollWidth - 1);
      setScrolledStart(scroll.scrollLeft > 0);
    };
    const measure = () => {
      setAvailableWidth(scroll.clientWidth);
      edge();
    };
    const syncHeader = () => {
      header.scrollLeft = scroll.scrollLeft;
      edge();
    };
    const syncBody = () => {
      scroll.scrollLeft = header.scrollLeft;
    };
    measure();
    syncHeader();
    const observer = new ResizeObserver(measure);
    observer.observe(scroll);
    scroll.addEventListener("scroll", syncHeader, { passive: true });
    header.addEventListener("scroll", syncBody, { passive: true });
    return () => {
      observer.disconnect();
      scroll.removeEventListener("scroll", syncHeader);
      header.removeEventListener("scroll", syncBody);
    };
  }, [scroll, header]);

  // Live updates retain selected rows still on this page, without selecting new arrivals.
  useEffect(() => {
    if (!selectionReady) return;
    const ids = new Set<string>(JSON.parse(pageRowIds));
    setSelected(
      (previous) =>
        new Set(
          previous === "all"
            ? ids
            : [...previous].filter((id) => ids.has(String(id))),
        ),
    );
  }, [pageRowIds, selectionReady]);

  const previousPageStart = useRef(externalPageStart);
  useEffect(() => {
    const changed = previousPageStart.current !== externalPageStart;
    previousPageStart.current = externalPageStart;
    if (!externalPagination || !changed) return;
    setSelected(new Set());
    scroll?.scrollTo({ top: 0 });
  }, [externalPageStart, externalPagination, scroll]);

  const previousResetKey = useRef(pageResetKey);
  useEffect(() => {
    const changed = previousResetKey.current !== pageResetKey;
    previousResetKey.current = pageResetKey;
    if (pageResetKey === undefined || !changed) return;
    setPage(1);
    setSelected(new Set());
    scroll?.scrollTo({ top: 0 });
  }, [pageResetKey, scroll]);

  useEffect(() => {
    if (!scroll || !onAtTopChange) return;
    const reportPosition = () =>
      onAtTopChange(currentPage === 1 && scroll.scrollTop <= 1);
    reportPosition();
    scroll.addEventListener("scroll", reportPosition, { passive: true });
    return () => scroll.removeEventListener("scroll", reportPosition);
  }, [scroll, currentPage, onAtTopChange]);

  function selectPage(nextPage: number) {
    if (remotePagination?.hasMore && nextPage > pageCount) {
      setRequestedPage(visibleRows.length < pageSize ? currentPage : nextPage);
      remotePagination.onNextPage();
      return;
    }
    setRequestedPage(null);
    setPage(nextPage);
    setSelected(new Set());
    onAtTopChange?.(nextPage === 1);
    scroll?.scrollTo({ top: 0 });
  }

  return (
    <div
      className={cx(styles.records, className)}
      data-vult-region="records-table"
      onKeyDownCapture={
        (event) => {
              const resizeTarget = event.target instanceof Element ? event.target.closest<HTMLElement>("[data-resize-column]") : null;
              if (resizeTarget && columns.some(column => column.id === resizeTarget.dataset.resizeColumn)) {
                layout.resizer(resizeTarget.dataset.resizeColumn!).onKeyDown(event);
                return;
              }
              if (!onRowAction || isDisabled) return;
              if (event.key !== "Enter" || !isRowActionTarget(event.target))
                return;
              const recordId = (event.target as Element)
                .closest("tr[data-record-id]")
                ?.getAttribute("data-record-id");
              const record = visibleRows.find(
                (item) => rowId(item) === recordId,
              );
              if (record) {
                event.preventDefault();
                event.stopPropagation();
                onRowAction(record);
              }
            }
      }
    >
      {notice}
      {(toolbar || selectedCount > 0) && (
        <div className={styles.toolbar}>
          {selectedCount > 0 ? (
            <div
              className="flex min-h-8 min-w-fit flex-1 items-center gap-3 whitespace-nowrap"
              role="status"
              aria-live="polite"
            >
              <Button
                size="sm"
                color="tertiary"
                iconLeading={XClose}
                aria-label="Clear selection"
                onPress={() => {
                  // Move focus before removing the selected-state toolbar.
                  const selector = header?.querySelector<HTMLInputElement>(
                    'input[type="checkbox"]',
                  );
                  if (selector) focusWithoutScroll(selector);
                  setSelected(new Set());
                }}
              />
              <span className="vult-type-label text-primary">
                {selectedCount.toLocaleString()} selected
              </span>
              <span className="vult-type-ui text-secondary">On this page</span>
            </div>
          ) : (
            toolbar
          )}
          <ColumnLayoutMenu layout={layout} />
        </div>
      )}
      <div className={styles.shell} data-testid={frameTestId}>
        <div className={styles.bodyScrollContainer}>
          <Table
            aria-label={ariaLabel}
            selectionMode={selectionEnabled ? "multiple" : "none"}
            selectionBehavior="toggle"
            selectedKeys={selected}
            sortDescriptor={sortDescriptor}
            onSortChange={onSortChange}
            onSelectionChange={(keys) =>
              setSelected(
                keys === "all" ? new Set(visibleRows.map(rowId)) : keys,
              )
            }
            disabledKeys={isDisabled ? visibleRows.map(rowId) : []}
            disabledBehavior="all"
            className={cx(
              styles.table,
              styles.bodyScrollTable,
              !selectionEnabled && styles.withoutSelection,
              "vult-type-ui",
            )}
            style={
              {
                "--records-columns": columnTracks.join(" "),
                "--records-width": `max(100%, ${renderedWidth}px)`,
              } as CSSProperties
            }
            data-testid={testId}
            data-pinned-end-utility={hasPinnedEndUtility || undefined}
            data-overflow-end={overflowEnd || undefined}
            data-scrolled-start={scrolledStart || undefined}
          >
            <TableHeader
              ref={setHeader}
              className={styles.fixedHeader}
              columns={columns}
              dependencies={[
                widths,
                isDisabled,
                visibleRows.length,
                sortDescriptor,
                selectionEnabled,
              ]}
              data-testid={columnHeaderTestId}
            >
              {(column) => (
                <Column
                  id={column.id}
                  isRowHeader={column.isRowHeader}
                  allowsSorting={column.allowsSorting}
                  focusMode={column.allowsSorting ? "cell" : undefined}
                  textValue={column.label}
                  width={widths[column.id]}
                  minWidth={sizeOf(column.id).min}
                  maxWidth={fixedColumn(column) ? sizeOf(column.id).preferred : undefined}
                  data-kind={column.kind}
                  className={cx(
                    styles.head,
                    column.pin === "end" && styles.pinnedEnd,
                    column.allowsSorting && styles.sortable,
                    "vult-type-label vult-focus-target vult-focus-contained",
                  )}
                >
                  <div className={styles.heading}>
                    {selectionEnabled && column.id === columns[0]?.id ? (
                      <span className={styles.selection}>
                        <Checkbox
                          slot="selection"
                          aria-label="Select rows on this page"
                          isDisabled={isDisabled || visibleRows.length === 0}
                        />
                      </span>
                    ) : column.icon ? (
                      <column.icon
                        aria-hidden="true"
                        className="size-4 shrink-0 text-fg-quaternary"
                      />
                    ) : null}
                    <span
                      className={
                        column.isUtility ? "sr-only" : "min-w-0 truncate"
                      }
                      title={column.isUtility ? undefined : column.label}
                    >
                      {column.label}
                    </span>
                    {column.headerTrailing}
                    {!toolbar && selectedCount === 0 && column.id === columns[0]?.id && <ColumnLayoutMenu layout={layout} />}
                    {column.allowsSorting ? (
                      <ArrowDown
                        aria-hidden="true"
                        className={cx(
                          "size-3 shrink-0 text-fg-quaternary",
                          styles.sortArrow,
                          sortDescriptor?.column === column.id &&
                            styles.activeSort,
                          sortDescriptor?.column === column.id &&
                            sortDescriptor.direction === "ascending" &&
                            "rotate-180",
                        )}
                      />
                    ) : null}
                  </div>
                  {!fixedColumn(column) ? (
                    <ColumnResizer layout={layout} id={column.id} label={column.label} />
                  ) : null}
                </Column>
              )}
            </TableHeader>
            <TableBody
              ref={setScroll}
              data-vult-view-scroll={`${testId}:body`}
              className={styles.scrollingBody}
              items={visibleRows}
              dependencies={[
                columns,
                pageStart,
                pageRowIds,
                renderCell,
                rowVariant,
                selectionEnabled,
                onRowAction,
                activeRowId,
                isDisabled,
              ]}
              renderEmptyState={() => (
                <div className="px-6 py-16 text-center">{emptyState}</div>
              )}
            >
              {(row) => (
                <Row
                  id={rowId(row)}
                  textValue={rowLabel(row)}
                  columns={columns}
                  dependencies={[
                    pageStart + visibleRows.indexOf(row),
                    renderCell,
                    selectionEnabled,
                    onRowAction,
                    activeRowId,
                    isDisabled,
                  ]}
                  data-testid={rowTestId?.(row)}
                  onAction={
                    onRowAction && !isDisabled
                      ? () => onRowAction(row)
                      : undefined
                  }
                  onPointerDownCapture={
                    onRowAction && !isDisabled
                      ? (event) => {
                          if (
                            event.button === 0 &&
                            isRowActionTarget(event.target)
                          ) {
                            // Keep row navigation distinct from the checkbox set, including during bulk selection.
                            event.stopPropagation();
                          }
                        }
                      : undefined
                  }
                  onClickCapture={
                    onRowAction && !isDisabled
                      ? (event) => {
                          if (isRowActionTarget(event.target)) {
                            event.stopPropagation();
                            onRowAction(row);
                          }
                        }
                      : undefined
                  }
                  data-record-id={onRowAction ? rowId(row) : undefined}
                  data-details-open={rowId(row) === activeRowId || undefined}
                  data-actionable={
                    (Boolean(onRowAction) && !isDisabled) || undefined
                  }
                  className={cx(
                    styles.row,
                    rowVariant?.(row) === "summary" && styles.summaryRow,
                    "vult-focus-target vult-focus-contained",
                  )}
                >
                  {(column) => (
                    <Cell
                      data-kind={column.kind}
                      className={cx(
                        styles.cell,
                        column.pin === "end" && styles.pinnedEnd,
                        "vult-focus-target vult-focus-contained",
                      )}
                    >
                      {selectionEnabled && column.id === columns[0]?.id ? (
                        <div className={styles.identity}>
                          <span className={styles.rowSelection}>
                            <Checkbox
                              slot="selection"
                              className={styles.rowCheckbox}
                              aria-label={`Select ${rowLabel(row)}`}
                            />
                          </span>
                          <div className={styles.cellContent} onPointerOver={revealTruncated}>
                            {renderCell(
                              row,
                              column,
                              pageStart + visibleRows.indexOf(row),
                            )}
                          </div>
                        </div>
                      ) : (
                        <div className={styles.cellContent} onPointerOver={revealTruncated}>
                          {renderCell(
                            row,
                            column,
                            pageStart + visibleRows.indexOf(row),
                          )}
                        </div>
                      )}
                    </Cell>
                  )}
                </Row>
              )}
            </TableBody>
          </Table>
        </div>
        {pagination !== false && (externalPagination || showPagination) && (
          <div className={styles.footer}>
            {pagination ?? (
              <TablePagination
                ariaLabel={paginationAriaLabel}
                currentPage={currentPage}
                onPageChange={selectPage}
                onPageSizeChange={(size) => {
                  setPageSize(size);
                  selectPage(1);
                }}
                pageCount={pageCount}
                pageSize={pageSize}
                recordCount={rows.length}
                testId={paginationTestId}
                trailing={paginationTrailing}
                isDisabled={isDisabled}
                isPageInputReadOnly={!!remotePagination}
                canNextPage={
                  remotePagination
                    ? !remotePagination.loading &&
                      (currentPage < pageCount || remotePagination.hasMore)
                    : undefined
                }
                isNextPageLoading={
                  remotePagination?.loading && requestedPage !== null
                }
                pageLabel={
                  remotePagination
                    ? `Page ${currentPage.toLocaleString("en-US")}`
                    : undefined
                }
                recordCountLabel={
                  remotePagination
                    ? visibleRows.length > 0
                      ? `${(pageStart + 1).toLocaleString("en-US")}–${(pageStart + visibleRows.length).toLocaleString("en-US")}`
                      : ""
                    : undefined
                }
              />
            )}
          </div>
        )}
      </div>
    </div>
  );
}

export function RecordIdentity({
  email,
  href,
}: {
  email: string | null;
  href?: string;
}) {
  const label = email ?? "No email address";
  return (
    <>
      <span
        className={cx(styles.monogram, "vult-type-metadata")}
        aria-hidden="true"
      >
        {email?.charAt(0).toUpperCase() || "?"}
      </span>
      {href ? (
        <a
          href={href}
          className={cx(styles.contact, "vult-type-label vult-focus-target")}
          title={label}
        >
          {label}
        </a>
      ) : (
        <span
          className="vult-type-label min-w-0 truncate text-primary"
          title={label}
        >
          {label}
        </span>
      )}
    </>
  );
}

const statusColors = {
  positive: "bg-utility-green-500",
  negative: "bg-utility-red-500",
  warning: "bg-utility-yellow-500",
  neutral: "bg-fg-quaternary",
  brand: "bg-utility-blue-500",
};

export function RecordStatus({
  children,
  tone,
}: {
  children: ReactNode;
  tone: keyof typeof statusColors;
}) {
  return (
    <span className="flex items-center gap-2 whitespace-nowrap">
      <span
        aria-hidden="true"
        className={cx("size-1.5 shrink-0 rounded-full", statusColors[tone])}
      />
      {children}
    </span>
  );
}
