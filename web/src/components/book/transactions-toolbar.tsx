import type { BookFilters } from "../../lib/book-state";
import { TransactionFilters } from "./transaction-filters";
import "./transactions-toolbar.css";
import {
  CollectionFilters,
  type CollectionFilter,
} from "../../design-system/collection-filters";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { names } from "../../lib/api";
import { CollectionToolbar } from "../../design-system/collection-toolbar";
import { SearchFilter } from "../../design-system/search-filter";
import styles from "../../design-system/catalog/components/application/table/records-table.module.css";
export type TransactionFilters = BookFilters;
/** Shared collection band: counted views, additional filters, Manage. */
export function TransactionsToolbar({
  filters,
  bookId,
  profile,
  counts,
  loading,
  onChange,
}: {
  filters: BookFilters;
  bookId: string;
  profile?: string;
  counts: Record<string, number>;
  loading: boolean;
  onChange: <K extends keyof BookFilters>(key: K, value: string) => void;
}) {
  const views: CollectionFilter[] = [
    { id: "", label: "All", count: counts[""] || 0 },
    {
      id: "income",
      label: "Income",
      count: counts.income || 0,
      series: "income",
    },
    {
      id: "expense",
      label: "Expenses",
      count: counts.expense || 0,
      series: "expenses",
    },
  ];
  // A less frequent type stays visible when deliberately selected in Filter.
  if (filters.kind && !views.some((view) => view.id === filters.kind)) {
    views.push({
      id: filters.kind,
      label: names[filters.kind] || filters.kind,
      count: counts[filters.kind] || 0,
    });
  }
  return (
    <CollectionToolbar
      label="Transactions"
      lead={
      <div
        className={`transaction-toolbar-summary vult-type-ui ${styles.filterSummary}`}
        aria-label="Transaction summary"
        data-vult-region="collection-summary"
      >
        <CollectionFilters
          aria-label="Transaction type filters, matching records"
          items={views}
          value={filters.kind}
          disabled={loading}
          onChange={(id) => onChange("kind", id)}
        />
        {filters.search && <SearchFilter value={filters.search} onRemove={() => onChange("search", "")} />}
        {filters.account && (
          <>
            <Button
              size="sm"
              color="link"
              aria-label="Clear transaction filters"
              onPress={() => {
                // The period lives in the page header, shared with the Dashboard.
                onChange("search", "");
                onChange("kind", "");
                onChange("account", "");
              }}
            >
              Clear filters
            </Button>
          </>
        )}
      </div>
      }
      actions={
      <>
        <TransactionFilters value={filters} onChange={onChange} />
      </>
      }
    />
  );
}
