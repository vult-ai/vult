import type { DashboardRange } from "./dashboard-period";
import { formatMonthName } from "./date-format";

const at = (date: string) => new Date(`${date}T00:00:00Z`);
const iso = (date: Date) => date.toISOString().slice(0, 10);
const parts = (date: string) => date.split("-").map(Number) as [number, number, number];
/** The first day of a month; months past twelve or below one roll the year. */
const monthStart = (year: number, month: number) => iso(new Date(Date.UTC(year, month - 1, 1)));
/** The last day of the month that ends `months` months after the one starting `from`. */
const monthEnd = (from: string, months: number) => {
  const [y, m] = parts(from);
  return iso(new Date(Date.UTC(y, m - 1 + months, 0)));
};

/** How many whole calendar months a range covers, or 0 when it does not
 * start on a 1st and end on a month's last day. */
export function wholeMonths(range: DashboardRange) {
  if (range.from.slice(8) !== "01") return 0;
  const [fy, fm] = parts(range.from);
  const [ty, tm] = parts(range.to);
  const months = (ty - fy) * 12 + tm - fm + 1;
  return months > 0 && monthEnd(range.from, months) === range.to ? months : 0;
}

/** The same length of time just before or after: whole months move by whole
 * months (October to September, a quarter to the one before, a year to the
 * year before); any other range moves by its own number of days. */
export function stepRange(range: DashboardRange, direction: -1 | 1): DashboardRange {
  const months = wholeMonths(range);
  if (months) {
    const [y, m] = parts(range.from);
    const from = monthStart(y, m + direction * months);
    return { from, to: monthEnd(from, months) };
  }
  const days = Math.round((+at(range.to) - +at(range.from)) / 86400000) + 1;
  const shift = (date: string) => {
    const d = at(date);
    d.setUTCDate(d.getUTCDate() + direction * days);
    return iso(d);
  };
  return { from: shift(range.from), to: shift(range.to) };
}

/** The name of a whole calendar period: "October 2026", "Q3 2026", "2025".
 * Undefined for any other range. */
export function wholePeriodName(range: DashboardRange) {
  const months = wholeMonths(range);
  const [y, m] = parts(range.from);
  if (months === 1) return formatMonthName(range.from);
  if (months === 3 && (m - 1) % 3 === 0) return `Q${(m - 1) / 3 + 1} ${y}`;
  if (months === 12 && m === 1) return String(y);
  return undefined;
}

/** The whole month containing a date, as a range. */
export function monthRange(year: number, month: number): DashboardRange {
  const from = monthStart(year, month);
  return { from, to: monthEnd(from, 1) };
}

/** A whole calendar period as a short link token: "2020", "2020-Q3" or
 * "2020-10". Undefined for any other range, which keeps its exact dates. */
export function rangeToken(range: DashboardRange) {
  const months = wholeMonths(range);
  const [y, m] = parts(range.from);
  if (months === 12 && m === 1) return String(y);
  if (months === 3 && (m - 1) % 3 === 0) return `${y}-Q${(m - 1) / 3 + 1}`;
  if (months === 1) return range.from.slice(0, 7);
  return undefined;
}

/** The range a short link token names, or undefined when it names none. */
export function tokenRange(token: string): DashboardRange | undefined {
  const year = /^(\d{4})$/.exec(token);
  if (year) return { from: `${year[1]}-01-01`, to: `${year[1]}-12-31` };
  const quarter = /^(\d{4})-[Qq]([1-4])$/.exec(token);
  if (quarter) {
    const from = monthRange(Number(quarter[1]), (Number(quarter[2]) - 1) * 3 + 1).from;
    return { from, to: monthEnd(from, 3) };
  }
  const month = /^(\d{4})-(0[1-9]|1[0-2])$/.exec(token);
  if (month) return monthRange(Number(month[1]), Number(month[2]));
  return undefined;
}
