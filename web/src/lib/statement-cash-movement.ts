import type { Book } from "./api";
import type { DashboardRange } from "./dashboard-period";
import { loadCashSummary, type CashSummary } from "./cash-summary";
import { errorCopy } from "./error-copy";

export type StatementCashMovement = Pick<
  CashSummary,
  "book_id" | "currency" | "scale" | "period" | "through" | "revision"
> & {
  money_in_minor: string;
  money_out_minor: string;
  movement_count: number;
};

/** The statement currently renders receipts/payments totals, not category or
 * time-series detail. Reuse canonical bank-evidence semantics and its snapshot;
 * do not reconstruct a second financial model from downloaded source files. */
export async function loadStatementCashMovement(
  book: Book,
  range: DashboardRange,
  signal?: AbortSignal,
): Promise<StatementCashMovement> {
  const value = await loadCashSummary(book, range, signal);
  if (
    value.movement_error ||
    value.money_in_minor === undefined ||
    value.money_out_minor === undefined ||
    !/^(0|[1-9]\d*)$/.test(value.money_in_minor) ||
    !/^(0|[1-9]\d*)$/.test(value.money_out_minor) ||
    value.movement_count === undefined ||
    !Number.isSafeInteger(value.movement_count) ||
    value.movement_count < 0
  )
    throw new Error(errorCopy.unavailable);
  return {
    book_id: value.book_id,
    currency: value.currency,
    scale: value.scale,
    period: value.period,
    through: value.through,
    revision: value.revision,
    money_in_minor: value.money_in_minor,
    money_out_minor: value.money_out_minor,
    movement_count: value.movement_count,
  };
}
