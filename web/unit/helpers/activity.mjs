export const activitySeries = (last = 0) => [...Array(51).fill(0), last];
export function activityData(book, accounts = {}) {
  return {
    book_id: book.id,
    as_of: book.accounting_today || "2026-10-25",
    from: "2025-10-27",
    accounts,
    empty_weekly: activitySeries(),
  };
}
export const activityResponse = (book, accounts = {}) =>
  new Response(JSON.stringify(activityData(book, accounts)), {
    headers: { "Content-Type": "application/json" },
  });
