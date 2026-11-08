import { parseDate } from "@internationalized/date";
import type { Book } from "./api";
import type { DashboardRange } from "./dashboard-period";

export function reportFiscalStart(
  book: Pick<Book, "fiscal_year_start_month" | "fiscal_year_start_day">,
  date: string,
): string {
  const end = parseDate(date);
  let start = end.set({
    month: book.fiscal_year_start_month || 1,
    day: book.fiscal_year_start_day || 1,
  });
  if (start.compare(end) > 0) start = start.subtract({ years: 1 });
  return start.toString();
}
export function reportQuickRange(
  id: string,
  book: Book,
  date: string,
): DashboardRange {
  const end = parseDate(date);
  const from =
    id === "fiscal_ytd"
      ? reportFiscalStart(book, date)
      : id === "calendar_ytd"
        ? end.set({ month: 1, day: 1 }).toString()
        : id === "quarter"
          ? end
              .set({ month: Math.floor((end.month - 1) / 3) * 3 + 1, day: 1 })
              .toString()
          : id === "month"
            ? end.set({ day: 1 }).toString()
            : date;
  return { from, to: date };
}
export function reportComparisonDate(
  id: "fiscal_year_end" | "month_end",
  book: Book,
  date: string,
): string {
  return parseDate(
    id === "fiscal_year_end"
      ? reportFiscalStart(book, date)
      : parseDate(date).set({ day: 1 }).toString(),
  )
    .subtract({ days: 1 })
    .toString();
}
