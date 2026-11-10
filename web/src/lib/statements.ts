import { fetchJSON } from "./errors";

export type StatementStatus = "reconciled" | "needs_work" | "not_reviewed";
export type StatementReview = { revision: number; outcome: "reconciled" | "needs_work"; reason?: string; report_sha256: string; actor_id: string; recorded_at: string };
/** One bank or card statement the Book is compared against. */
export type Statement = {
  id: string;
  account_id: string;
  account_name?: string;
  kind: "cash" | "card";
  currency: string;
  scale: number;
  document_id: string;
  period_start: string;
  period_end: string;
  opening_balance_minor: string;
  closing_balance_minor: string;
  evidence_state: "complete" | "incomplete" | "disputed";
  note?: string;
  row_count: number;
  review?: StatementReview;
  status: StatementStatus;
};
export type StatementRowState = "matched" | "adjusted" | "duplicate" | "pending" | "cancelled" | "proposed" | "not_in_book";
export type StatementRow = {
  id: string;
  row_number: number;
  date: string;
  amount_minor: string;
  description: string;
  external_id?: string;
  lifecycle: string;
  state: StatementRowState;
  matched_minor: string;
  remaining_minor: string;
  match_ids: string[];
  adjustment_entry_id?: string;
  duplicate_of?: string;
};
export type OutstandingLine = {
  line_id: string;
  entry_id: string;
  date: string;
  description: string;
  amount_minor?: string;
  remaining_minor?: string;
  unresolved?: boolean;
  proposed?: boolean;
};
export type StatementMatch = { id: string; rows: { row_id: string; amount_minor: string }[]; lines: { line_id: string; amount_minor: string }[]; reason?: string };
export type ReconciliationSummary = {
  opening_balance_minor: string;
  closing_balance_minor: string;
  matched_minor: string;
  adjusted_minor: string;
  duplicate_minor: string;
  on_statement_not_in_book_minor: string;
  on_statement_not_in_book_count: number;
  outstanding_in_book_minor: string;
  outstanding_in_book_count: number;
  unresolved_lines: number;
  duplicates: number;
  pending_rows: number;
  proposed_rows: number;
  statement_arithmetic_difference_minor: string;
  remaining_difference_minor: string;
  fully_explained: boolean;
};
export type Reconciliation = { statement: Statement; summary: ReconciliationSummary; rows: StatementRow[]; outstanding: OutstandingLine[]; matches: StatementMatch[] };
export type StatementSuggestion = { kind: "match" | "duplicate"; row_id: string; line_ids?: string[]; entry_ids?: string[]; canonical_row_id?: string; amount_minor: string; reason: string };

const integer = /^-?(0|[1-9]\d*)$/;

export async function readStatements(book: string, signal?: AbortSignal): Promise<Statement[]> {
  const out = await fetchJSON<{ statements: Statement[] }>(`/api/v1/statements?${new URLSearchParams({ book_id: book })}`, signal ? { signal } : undefined);
  if (!Array.isArray(out.statements) || out.statements.some((s) => !integer.test(s.closing_balance_minor) || !integer.test(s.opening_balance_minor)))
    throw new Error("Statements could not be confirmed.");
  return out.statements;
}

export async function readReconciliation(book: string, statement: string, signal?: AbortSignal) {
  const out = await fetchJSON<{ reconciliation: Reconciliation; suggestions: StatementSuggestion[] }>(
    `/api/v1/statements?${new URLSearchParams({ book_id: book, statement_id: statement })}`, signal ? { signal } : undefined);
  const r = out.reconciliation;
  if (!r || r.statement?.id !== statement || !Array.isArray(r.rows) || !Array.isArray(r.outstanding) || !Array.isArray(out.suggestions) ||
      ![r.summary.matched_minor, r.summary.duplicate_minor, r.summary.remaining_difference_minor, r.summary.outstanding_in_book_minor, r.summary.on_statement_not_in_book_minor].every((v) => integer.test(v)))
    throw new Error("The reconciliation could not be confirmed.");
  return out;
}

export const statementStatusLabel: Record<StatementStatus, string> = { reconciled: "Reconciled", needs_work: "Needs work", not_reviewed: "Not reviewed" };
export const rowStateLabel: Record<StatementRowState, string> = {
  matched: "Matched", adjusted: "Adjusted", duplicate: "Duplicate", pending: "Pending", cancelled: "Cancelled", proposed: "Proposed", not_in_book: "Not in the Book",
};
