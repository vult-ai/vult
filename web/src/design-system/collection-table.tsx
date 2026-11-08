import { RecordsTable } from "./catalog/components/application/table/records-table";

/** Full-page collections share one source grid, sizing policy and outer spacing. */
export function CollectionTable<T extends object>({
  className = "",
  fitColumns = true,
  showPagination = true,
  ...props
}: Parameters<typeof RecordsTable<T>>[0]) {
  return (
    <RecordsTable
      {...props}
      fitColumns={fitColumns}
      showPagination={showPagination}
      className={`vult-collection-layout ${props.toolbar ? "vult-collection-with-toolbar" : ""} ${props.onRowAction ? "vult-collection-row-focus" : ""} ${className}`}
    />
  );
}
