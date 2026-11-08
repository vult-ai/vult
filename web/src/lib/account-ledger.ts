import type { Book } from "./api";
import type { DashboardRange } from "./dashboard-period";
import type { HierarchyAccount } from "./account-hierarchy";
import { creditKind } from "./v0-ledger";

type JournalRow = Readonly<Record<string, string>>;
export type AccountLedgerLine = {
  id: string;
  transactionId: string;
  date: string;
  postingDate: string | null;
  description: string;
  memo: string;
  debit: bigint;
  credit: bigint;
  balance: bigint;
};
export type AccountLedger = {
  accountCode: string;
  range: DashboardRange;
  opening: bigint;
  debits: bigint;
  credits: bigint;
  closing: bigint;
  periodChange: bigint;
  positiveSide: "debit" | "credit";
  /** `recorded_prior_postings`: the sum of earlier accepted postings, not an
   * assertion of a complete opening balance. */
  openingBasis?: string;
  lines: AccountLedgerLine[];
};
function validDate(value: string) {
  return (
    /^\d{4}-\d{2}-\d{2}$/.test(value) &&
    new Date(value + "T00:00:00Z").toISOString().slice(0, 10) === value
  );
}

/** Recorded Book-currency ledger. No reset, FX conversion or inferred opening entry. */
export function accountLedger(
  book: Pick<Book, "id" | "currency" | "scale">,
  account: Pick<HierarchyAccount, "code" | "kind">,
  journal: readonly JournalRow[],
  range: DashboardRange,
  transactions: readonly JournalRow[] = [],
): AccountLedger {
  if (!validDate(range.from) || !validDate(range.to) || range.from > range.to)
    throw new Error("The account ledger period is invalid.");
  const metadata = new Map<string, JournalRow>();
  for (const transaction of transactions) {
    if (
      transaction.book_id !== book.id ||
      !transaction.transaction_id ||
      metadata.has(transaction.transaction_id)
    )
      throw new Error("The ledger entry references could not be confirmed.");
    metadata.set(transaction.transaction_id, transaction);
  }
  const seen = new Set<string>();
  const selected: { row: JournalRow; index: number }[] = [];
  for (const [index, row] of journal.entries()) {
    if (
      row.book_id !== book.id ||
      row.currency !== book.currency ||
      (row.scale && row.scale !== String(book.scale))
    )
      throw new Error(
        "The account ledger belongs to another Book or currency.",
      );
    if (
      !row.line_id ||
      seen.has(row.line_id) ||
      !row.transaction_id ||
      !validDate(row.economic_date) ||
      !["debit", "credit"].includes(row.side) ||
      !/^\d+$/.test(row.amount_minor)
    )
      throw new Error("The recorded ledger postings could not be confirmed.");
    seen.add(row.line_id);
    if (row.posting_date && !validDate(row.posting_date))
      throw new Error("A recorded posting date could not be confirmed.");
    if (row.account_code === account.code && row.economic_date <= range.to)
      selected.push({ row, index });
  }
  // The complete export orders same-date lines by recorded instant, transaction
  // and line identity. Preserve that authoritative order without inventing times.
  selected.sort(
    (a, b) =>
      a.row.economic_date.localeCompare(b.row.economic_date) ||
      a.index - b.index,
  );
  const positiveSide = creditKind(account.kind) ? "credit" : "debit";
  const sign = positiveSide === "credit" ? -1n : 1n;
  let opening = 0n,
    debits = 0n,
    credits = 0n,
    balance = 0n;
  const lines: AccountLedgerLine[] = [];
  for (const { row } of selected) {
    const amount = BigInt(row.amount_minor);
    const debit = row.side === "debit" ? amount : 0n;
    const credit = row.side === "credit" ? amount : 0n;
    balance += (debit - credit) * sign;
    if (row.economic_date < range.from) {
      opening = balance;
      continue;
    }
    debits += debit;
    credits += credit;
    const entry = metadata.get(row.transaction_id);
    lines.push({
      id: row.line_id,
      transactionId: row.transaction_id,
      date: row.economic_date,
      postingDate: row.posting_date || null,
      description:
        entry?.description ||
        entry?.counterparty ||
        row.line_memo ||
        `Journal entry ${row.transaction_id.slice(0, 8)}`,
      memo: row.line_memo || "",
      debit,
      credit,
      balance,
    });
  }
  const periodChange = (debits - credits) * sign;
  if (opening + periodChange !== balance)
    throw new Error("The account ledger could not be reconciled.");
  return {
    accountCode: account.code,
    range,
    opening,
    debits,
    credits,
    closing: balance,
    periodChange,
    positiveSide,
    lines,
  };
}
