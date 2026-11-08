import type { RecordsTableColumn } from "../../design-system/catalog/components/application/table/records-table";
import { Calendar, Tag01, User01, Wallet01 } from "../../design-system/icons";

export const transactionColumns: readonly RecordsTableColumn[] = [
  {
    id: "date",
    label: "Date",
    kind: "date" as const,
    icon: Calendar,
  },
  {
    id: "counterparty",
    label: "To/From",
    kind: "text" as const,
    icon: User01,
    width: 256,
    isRowHeader: true,
  },
  {
    id: "amount",
    label: "Amount",
    kind: "money" as const,
    icon: Wallet01,
  },
  {
    id: "category",
    label: "Category",
    kind: "text" as const,
    icon: Tag01,
    // With the details panel open at a 1440px window the four columns fill
    // the table exactly, so every resize handle starts in view.
    width: 240,
  },
];

/** A phone row is a full-width transaction summary, with no pinned tracks. */
export const phoneTransactionColumns: readonly RecordsTableColumn[] = [
  {
    id: "summary",
    label: "Transactions",
    kind: "text" as const,
    width: 320,
    fill: true,
    isRowHeader: true,
  },
];
