import { bookURL, get, type Book, type CashAccount } from "./api";
import { verifiedReadFailure } from "./errors";
import type { DashboardRange } from "./dashboard-period";

export type CashSummary = {
  book_id: string;
  currency: string;
  scale: number;
  period: DashboardRange;
  through: string;
  revision: string;
  /** How much of cash the ledger can state. Vult Open reports partial (the
   * recorded cash accounts only) or unavailable; older reads omit it. */
  cash_status?: CashStatus;
  cash_minor?: string;
  cash_accounts: CashAccount[];
  money_in_minor?: string;
  money_out_minor?: string;
  movement_count?: number;
  position_error?: string;
  movement_error?: string;
  integrity_failure?: boolean;
};

export type CashStatus = "complete" | "partial" | "unavailable";

export async function loadCashSummary(book: Book, period: DashboardRange, signal?: AbortSignal) {
  const params = new URLSearchParams({from:period.from,to:period.to});
  const result = await get<CashSummary>(bookURL(`dashboard/cash-summary?${params}`,book.id),signal);
  if (result.book_id!==book.id || result.currency!==book.currency || result.scale!==book.scale || result.period.from!==period.from || result.period.to!==period.to || !/^[a-f0-9]{64}$/.test(result.revision))
    throw new Error("The cash summary does not match this Book and reporting period.");
  for (const value of [result.cash_minor,result.money_in_minor,result.money_out_minor])
    if(value!==undefined && !/^-?(0|[1-9]\d*)$/.test(value)) throw new Error("Cash amounts could not be confirmed.");
  if(result.integrity_failure) throw verifiedReadFailure("source-digest","The cash source bytes could not be verified.");
  if(result.cash_status!==undefined){
    // An explicit status says which figures exist; a missing one is unavailable, never 0.
    if(!["complete","partial","unavailable"].includes(result.cash_status)) throw new Error("Cash status could not be confirmed.");
    if(!Array.isArray(result.cash_accounts)) throw new Error("Cash balances could not be confirmed.");
    if(result.cash_status==="unavailable" && result.cash_minor!==undefined) throw new Error("Cash balances could not be confirmed.");
    if(result.cash_status!=="unavailable" && result.cash_minor===undefined && !result.position_error) throw new Error("Cash balances could not be confirmed.");
    if((result.money_in_minor===undefined)!==(result.money_out_minor===undefined)) throw new Error("Money movements could not be confirmed.");
    return result;
  }
  if(!result.position_error && (result.cash_minor===undefined || !Array.isArray(result.cash_accounts))) throw new Error("Cash balances could not be confirmed.");
  if(!result.movement_error && (result.money_in_minor===undefined || result.money_out_minor===undefined)) throw new Error("Money movements could not be confirmed.");
  return result;
}
