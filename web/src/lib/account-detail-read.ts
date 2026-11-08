import type { Book } from "./api";
import type { DashboardRange } from "./dashboard-period";
import type { AccountActivity } from "./account-activity";
import type { AccountLedger, AccountLedgerLine } from "./account-ledger";
import { fetchAccountSelection, parseHierarchyRead } from "./account-hierarchy-activity";
import { APIError, verifiedReadFailure } from "./errors";

export type AccountReadEntry = {
  id: string;
  date: string;
  accounts: string;
  amount: bigint;
};
export type AccountDetailPage = {
  entries: AccountReadEntry[];
  ledger: AccountLedger | null;
  activity: AccountActivity | null;
  revision: string;
  offset: number;
  count: number;
  earliest: string;
};
const date = (value: unknown): value is string => {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value))
    return false;
  const time = Date.parse(value + "T00:00:00Z");
  return (
    Number.isFinite(time) && new Date(time).toISOString().slice(0, 10) === value
  );
};
const minor = (value: unknown): bigint => {
  if (typeof value !== "string" || !/^-?(0|[1-9]\d{0,39})$/.test(value))
    throw new Error("An account amount could not be confirmed.");
  return BigInt(value);
};
export function parseAccountDetail(
  data: unknown,
  book: Pick<Book, "id" | "currency" | "scale">,
  codes: readonly string[],
  range: DashboardRange,
  basis: "period" | "balance",
  expected: bigint,
  offset = 0,
  revision = "",
  previousBalance?: bigint,
  mode: "ledger" | "group" = codes.length === 1 ? "ledger" : "group",
): AccountDetailPage {
  const fail = (): never => {
    throw new Error("The account activity could not be confirmed.");
  };
  if (!data || typeof data !== "object" || Array.isArray(data)) return fail();
  const value = data as Record<string, any>;
  if (
    value.book_id !== book.id ||
    value.currency !== book.currency ||
    value.scale !== book.scale
  )
    throw verifiedReadFailure(
      "book-scope",
      "The account activity belongs to another Book or currency.",
    );
  if (
    value.version !== 1 ||
    JSON.stringify(value.account_codes) !== JSON.stringify([...codes].sort()) ||
    value.from !== range.from ||
    value.to !== range.to ||
    value.basis !== basis ||
    value.offset !== offset ||
    value.limit !== 100 ||
    !Number.isSafeInteger(value.count) ||
    value.count < offset ||
    !/^[a-f0-9]{64}$/.test(value.revision) ||
    (revision && revision !== value.revision) ||
    !Array.isArray(value.entries) ||
    (value.earliest_date !== "" && !date(value.earliest_date))
  )
    return fail();
  if (minor(value.total_minor) !== expected)
    throw new Error("The account changed. Refresh to see the latest totals.");
  const activity =
    offset === 0
      ? parseHierarchyRead(value.activity, book, range.to, codes)
      : null;
  let ledger: AccountLedger | null = null;
  const seen = new Set<string>();
  const entries: AccountReadEntry[] = [];
  if (mode === "ledger") {
    const l = value.ledger;
    if (
      !l ||
      l.account_code !== codes[0] ||
      !["debit", "credit"].includes(l.positive_side) ||
      !Array.isArray(l.lines) ||
      value.entries.length
    )
      return fail();
    const sign = l.positive_side === "credit" ? -1n : 1n;
    const opening = minor(l.opening_minor),
      debits = minor(l.debits_minor),
      credits = minor(l.credits_minor),
      closing = minor(l.closing_minor),
      periodChange = minor(l.period_change_minor);
    if (
      debits < 0n ||
      credits < 0n ||
      (debits - credits) * sign !== periodChange ||
      opening + periodChange !== closing ||
      (basis === "period" ? periodChange : closing) !== expected
    )
      return fail();
    let running = offset === 0 ? opening : previousBalance;
    if (running === undefined) return fail();
    const lines: AccountLedgerLine[] = [];
    for (const row of l.lines) {
      if (
        !row ||
        typeof row.id !== "string" ||
        !row.id ||
        seen.has(row.id) ||
        typeof row.transaction_id !== "string" ||
        !row.transaction_id ||
        !date(row.date) ||
        row.date < range.from ||
        row.date > range.to ||
        (row.posting_date !== "" && !date(row.posting_date)) ||
        typeof row.description !== "string" ||
        typeof row.memo !== "string"
      )
        return fail();
      seen.add(row.id);
      const debit = minor(row.debit_minor),
        credit = minor(row.credit_minor),
        balance = minor(row.balance_minor);
      if (debit < 0n || credit < 0n || (debit > 0n && credit > 0n))
        return fail();
      running += (debit - credit) * sign;
      if (running !== balance) return fail();
      if (lines.length && lines.at(-1)!.date > row.date) return fail();
      lines.push({
        id: row.id,
        transactionId: row.transaction_id,
        date: row.date,
        postingDate: row.posting_date || null,
        description:
          row.description ||
          row.memo ||
          `Journal entry ${row.transaction_id.slice(0, 8)}`,
        memo: row.memo,
        debit,
        credit,
        balance,
      });
    }
    if (
      lines.length !== Math.min(100, value.count - offset) ||
      (offset + lines.length === value.count && running !== closing)
    )
      return fail();
    ledger = {
      accountCode: codes[0],
      range,
      opening,
      debits,
      credits,
      closing,
      periodChange,
      positiveSide: l.positive_side,
      openingBasis: typeof l.opening_basis === "string" ? l.opening_basis : undefined,
      lines,
    };
  } else {
    if (value.ledger) return fail();
    for (const row of value.entries) {
      if (
        !row ||
        typeof row.id !== "string" ||
        !row.id ||
        seen.has(row.id) ||
        !date(row.date) ||
        row.date > range.to ||
        (basis === "period" && row.date < range.from) ||
        typeof row.accounts !== "string"
      )
        return fail();
      seen.add(row.id);
      entries.push({
        id: row.id,
        date: row.date,
        accounts: row.accounts,
        amount: minor(row.amount_minor),
      });
    }
    if (entries.length !== Math.min(100, value.count - offset)) return fail();
    for (let i = 1; i < entries.length; i++) {
      const a = entries[i - 1],
        b = entries[i];
      if (a.date < b.date || (a.date === b.date && a.id > b.id)) return fail();
    }
    if (
      offset === 0 &&
      entries.length === value.count &&
      entries.reduce((sum, row) => sum + row.amount, 0n) !== expected
    )
      return fail();
  }
  return {
    entries,
    ledger,
    activity,
    revision: value.revision,
    offset,
    count: value.count,
    earliest: value.earliest_date,
  };
}

export async function readAccountDetail(
  book: Book,
  codes: readonly string[],
  range: DashboardRange,
  basis: "period" | "balance",
  expected: bigint,
  signal: AbortSignal,
  mode: "ledger" | "group" = codes.length === 1 ? "ledger" : "group",
  continuation?: { offset: number; revision: string; balance?: bigint },
) {
  const query = new URLSearchParams({
    from: range.from,
    to: range.to,
    limit: "100",
    view: mode,
    offset: String(continuation?.offset || 0),
  });
  if (continuation) query.set("revision", continuation.revision);
  const response = await fetchAccountSelection("account-detail", book.id, query, signal, codes);
  if (response.status === 409)
    throw new APIError("The account changed. Refresh to see the latest totals.", 409);
  if (!response.ok)
    throw new Error("Account activity could not be loaded. Try again.");
  if (
    !response.headers
      .get("content-type")
      ?.toLowerCase()
      .includes("application/json") ||
    Number(response.headers.get("content-length")) > 8 * 1024 * 1024
  )
    throw new Error("The account response could not be confirmed.");
  return parseAccountDetail(
    await response.json(),
    book,
    codes,
    range,
    basis,
    expected,
    continuation?.offset,
    continuation?.revision,
    continuation?.balance,
    mode,
  );
}
