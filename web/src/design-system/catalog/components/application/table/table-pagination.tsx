"use client";

import type { ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "../../../../icons";
import { PageJump } from "../../../../page-jump";
import { Select } from "@vult/components/base/select/select";
import { Button } from "@vult/components/vult-ui/button";

export function TablePagination({
  ariaLabel,
  currentPage,
  onPageChange,
  onPageSizeChange,
  pageCount,
  pageSize,
  recordCount,
  recordCountLabel,
  testId,
  trailing,
  isDisabled = false,
  isPageInputReadOnly = false,
  canPreviousPage,
  canNextPage,
  pageLabel,
  isNextPageLoading = false,
}: {
  ariaLabel: string;
  currentPage: number;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  pageCount: number;
  pageSize: number;
  recordCount: number;
  recordCountLabel?: ReactNode;
  testId?: string;
  trailing?: ReactNode;
  isDisabled?: boolean;
  isPageInputReadOnly?: boolean;
  canPreviousPage?: boolean;
  canNextPage?: boolean;
  pageLabel?: string;
  isNextPageLoading?: boolean;
}) {
  return (
    <div
      className="vult-table-pagination sticky bottom-0 z-10 mt-auto flex min-h-7 shrink-0 flex-wrap items-center gap-2"
      data-vult-region="table-pagination"
      data-testid={testId}
    >
      <nav aria-label={ariaLabel} className="flex flex-wrap items-center gap-2">
        <Button
          aria-label="Previous page"
          className="vult-compact-control"
          color="secondary"
          iconLeading={ChevronLeft}
          isDisabled={isDisabled || !(canPreviousPage ?? currentPage > 1)}
          onPress={() => onPageChange(Math.max(1, currentPage - 1))}
          size="sm"
        />
        {pageLabel ? (
          <span className="vult-type-label text-tertiary">{pageLabel}</span>
        ) : (
          <>
            <span className="vult-type-label text-tertiary">Page</span>
            {isPageInputReadOnly ? (
              <span className="vult-type-label text-primary tabular-nums">
                {currentPage.toLocaleString("en-US")}
              </span>
            ) : (
              <PageJump
                currentPage={currentPage}
                pageCount={pageCount}
                onPageChange={onPageChange}
                isDisabled={isDisabled}
              />
            )}
            <span className="vult-type-label whitespace-nowrap text-tertiary">
              of {pageCount.toLocaleString("en-US")}
            </span>
          </>
        )}
        <Button
          aria-label="Next page"
          className="vult-compact-control"
          color="secondary"
          iconLeading={ChevronRight}
          isDisabled={isDisabled || !(canNextPage ?? currentPage < pageCount)}
          isLoading={isNextPageLoading}
          onPress={() =>
            onPageChange(
              canNextPage === undefined
                ? Math.min(pageCount, currentPage + 1)
                : currentPage + 1,
            )
          }
          size="sm"
        />
        <Select
          aria-label="Rows per page"
          isDisabled={isDisabled}
          className="vult-compact-select w-28"
          items={[
            { id: 25, label: "25 rows" },
            { id: 50, label: "50 rows" },
            { id: 100, label: "100 rows" },
          ]}
          onChange={(value) => onPageSizeChange(Number(value))}
          size="sm"
          value={pageSize}
        >
          {(item) => (
            <Select.Item id={item.id} selectionIndicator="none">
              {item.label}
            </Select.Item>
          )}
        </Select>
        <span className="vult-type-label whitespace-nowrap text-tertiary">
          {recordCountLabel ??
            `${recordCount.toLocaleString("en-US")} ${recordCount === 1 ? "record" : "records"}`}
        </span>
      </nav>
      {trailing ? (
        <div className="ml-auto flex min-w-0 items-center">{trailing}</div>
      ) : null}
    </div>
  );
}
