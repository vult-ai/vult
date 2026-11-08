export { V0Overview } from "../../src/components/v0/v0-overview";
export { default as BookScreens } from "../../src/components/workspaces/book-screens";
export { responseFailure, verifiedReadFailure } from "../../src/lib/errors";
export { readBookState } from "../../src/lib/book-state";

export function emptyDashboardSnapshot(book: { id: string; currency: string; scale: number }, period: { from: string; to: string }) {
  const activity = { income: "0", expenses: "0", result: "0" };
  return {
    schema_version: 1, book_id: book.id, currency: book.currency, scale: book.scale, period,
    cash_accounts: [], cash_status: "unavailable" as const,
    financial: { ...activity, assets: "0", liabilities: "0", equity: "0", equityAccounts: "0", accumulatedResult: "0", through: period.to, rows: [], incomeRows: [], expenseRows: [] },
    overviewComparison: { range: period, current: [], previous: [] },
    trends: { current: [], previous: [] },
    performance: { status: "recorded", period, timezone: "UTC", unit: "month" as const, buckets: [], income_minor: "0", expenses_minor: "0" },
  };
}


export function emptyDashboardState(book: { id: string; currency: string; scale: number }, query: URLSearchParams) {
  const period = { from: query.get("dashboard_from")!, to: query.get("dashboard_to")! };
  return { book, state_revision: "a".repeat(64), dashboard_snapshot: emptyDashboardSnapshot(book, period) };
}
