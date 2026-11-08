import { bookURL, get, type Book, type CashAccount, type DashboardData } from "./api";
import type { FinancialSnapshot } from "./v0-ledger";
import type { OverviewComparison } from "./overview-history";
import type { PerformanceData, PerformanceSide } from "./performance-series";

/** Which recorded Book state a read describes; equal markers mean the same records and inputs. */
export type RecordedState = { state_revision?: string; state_captured_at?: string; state_knowledge_cutoff?: string };

export type CanonicalDashboard = DashboardData & RecordedState & {
  financial: FinancialSnapshot;
  overviewComparison: OverviewComparison;
  performance: PerformanceData;
  projection_contract: {
    version: number;
    book_id: string;
    event_kind: string;
    precision: string;
    currency: string;
    scale: number;
  };
};

/* The route's calendar form (projection contract 2): civil-date buckets in the Book's
   timezone with exclusive upper bounds, and signed amounts per account. Contributions
   are signed too: amount_minor = positive_minor + negative_minor, with
   positive_minor >= 0 and negative_minor <= 0 (a refund of 30.00 is "-3000"). */
type CalendarAccount = { account_id: string; name: string; amount_minor: string; positive_minor: string; negative_minor: string; posting_count: number };
type CalendarSide = { total_minor: string; positive_minor: string; negative_minor: string; accounts: CalendarAccount[] };
type CalendarBucket = { from: string; to: string; covered_from: string; covered_to: string; income: CalendarSide; expenses: CalendarSide };
type CalendarPerformance = {
  status: string; unavailable_reason?: string; period: { from: string; to: string }; unit: CalendarUnit; timezone: string;
  income_minor?: string; expenses_minor?: string; buckets: CalendarBucket[];
};
export type CalendarUnit = "day" | "week" | "month" | "quarter" | "year";

type Range = DashboardData["period"];
type Activity = Pick<FinancialSnapshot, "income" | "expenses" | "result">;
export type DashboardTrendPoint = Activity & { date: string; cash_minor?: string };
export type DashboardSnapshotWire = {
  schema_version: number;
  book_id: string;
  currency: string;
  scale: number;
  period: Range;
  financial: FinancialSnapshot;
  overviewComparison: OverviewComparison;
  performance: CalendarPerformance;
  cash_minor?: string;
  cash_status: "partial" | "unavailable";
  cash_accounts: Pick<CashAccount, "code" | "name" | "carrying_minor">[];
  prior_year?: Activity & { period: Range };
  trends: { current: DashboardTrendPoint[]; previous: DashboardTrendPoint[] };
};
export type DashboardSnapshot = Omit<DashboardSnapshotWire, "performance"> & { version: string; performance: PerformanceData };

export function parseDashboardSnapshot(
  data: DashboardSnapshotWire | undefined, book: Book, period: Range, revision: string | undefined,
): DashboardSnapshot {
  if (!data || data.schema_version !== 1 || !revision || data.book_id !== book.id ||
      data.currency !== book.currency || data.scale !== book.scale ||
      data.period?.from !== period.from || data.period?.to !== period.to ||
      data.financial?.through !== period.to || data.performance?.period?.from !== period.from ||
      data.performance.period.to !== period.to)
    throw new Error("The Dashboard snapshot does not match this Book and reporting period.");
  if (!Array.isArray(data.financial.rows) || !Array.isArray(data.cash_accounts) ||
      !Array.isArray(data.trends?.current) || !Array.isArray(data.trends?.previous) ||
      !Array.isArray(data.overviewComparison?.current) || !Array.isArray(data.overviewComparison?.previous) ||
      !Array.isArray(data.performance.buckets))
    throw new Error("The Dashboard snapshot is incomplete.");
  return { ...data, version: revision, performance: fromCalendar(data.performance) };
}

const DAY = 86400000;
const at = (s: string) => new Date(s + "T00:00:00Z");
const dayBefore = (s: string) => new Date(+at(s) - DAY).toISOString().slice(0, 10);
const MAX_PERIODS = 14;

/** Up to a month draws one period per day, the chart's dense look. */
const MAX_DAYS = 31;

/** The calendar unit for the chart: days up to a month, then weeks, months,
 * quarters or years, each at or under fourteen periods. */
export function calendarUnit(from: string, to: string): CalendarUnit {
  const days = (+at(to) - +at(from)) / DAY + 1;
  if (days <= MAX_DAYS) return "day";
  if (days <= MAX_PERIODS * 7) return "week";
  const months = (Number(to.slice(0, 4)) - Number(from.slice(0, 4))) * 12 + Number(to.slice(5, 7)) - Number(from.slice(5, 7)) + 1;
  if (months <= MAX_PERIODS) return "month";
  if (Math.ceil(months / 3) <= MAX_PERIODS) return "quarter";
  return "year";
}

const LEADING = 5;
const abs = (n: bigint) => (n < 0n ? -n : n);

/** Converts the calendar form into the chart's series without moving any amount
 * between periods: each bucket keeps exactly the amounts the route assigned to it.
 * Accounts beyond the five largest are summed into "Other" within each bucket. */
export function fromCalendar(p: CalendarPerformance): PerformanceData {
  const buckets = p.buckets.map((b) => ({ from: b.covered_from, to: dayBefore(b.covered_to) }));
  const side = (kind: "income" | "expenses", stated?: string): PerformanceSide => {
    const accounts = new Map<string, { name: string; values: bigint[]; total: bigint }>();
    p.buckets.forEach((b, i) => {
      let sum = 0n;
      for (const a of b[kind].accounts) {
        const v = BigInt(a.amount_minor);
        const up = BigInt(a.positive_minor), down = BigInt(a.negative_minor);
        if (up < 0n || down > 0n || up + down !== v)
          throw new Error("A dashboard account amount does not equal its contributions.");
        sum += v;
        const row = accounts.get(a.account_id) ?? { name: a.name, values: buckets.map(() => 0n), total: 0n };
        row.values[i] += v;
        row.total += v;
        accounts.set(a.account_id, row);
      }
      if (sum !== BigInt(b[kind].total_minor)) throw new Error("A dashboard period does not equal its accounts.");
    });
    const all = [...accounts.entries()]
      .map(([id, a]) => ({ id, ...a }))
      .sort((a, b) => (abs(a.total) === abs(b.total) ? a.name.localeCompare(b.name) : abs(a.total) > abs(b.total) ? -1 : 1));
    const leading = all.slice(0, LEADING);
    const rest = all.slice(LEADING);
    const rows = leading.map((a) => ({ id: `account:${a.id}`, code: a.id, name: a.name, total: a.total, values: a.values }));
    if (rest.length)
      rows.push({ id: "other", code: "", name: "Other", total: rest.reduce((n, a) => n + a.total, 0n), values: buckets.map((_, i) => rest.reduce((n, a) => n + a.values[i], 0n)) });
    const values = buckets.map((_, i) => rows.reduce((n, r) => n + r.values[i], 0n));
    const total = values.reduce((n, v) => n + v, 0n);
    if (stated !== undefined && BigInt(stated) !== total) throw new Error("The dashboard periods do not add up to the stated total.");
    return {
      total: String(total),
      values: values.map(String),
      categories: rows.map((r) => ({ id: r.id, code: r.code, name: r.name, total: String(r.total), values: r.values.map(String) })),
    };
  };
  const income = side("income", p.income_minor), expenses = side("expenses", p.expenses_minor);
  let high = 0n, low = 0n;
  for (const s of [income, expenses])
    buckets.forEach((_, i) => {
      let up = 0n, down = 0n;
      for (const c of s.categories) { const n = BigInt(c.values[i]); n > 0n ? (up += n) : (down += n); }
      if (up > high) high = up;
      if (down < low) low = down;
    });
  return {
    period: p.period,
    grain: p.unit,
    buckets,
    income,
    expenses,
    domain: { min: String(low), max: String(high) },
    ...(p.status === "recorded" ? {} : { unavailable: p.unavailable_reason || p.status }),
  };
}

export async function loadDashboardProjection(
  book: Book,
  period: DashboardData["period"],
  signal?: AbortSignal,
): Promise<CanonicalDashboard> {
  const params = new URLSearchParams({ from: period.from, to: period.to, view: "recognition", unit: calendarUnit(period.from, period.to) });
  const data = await get<Omit<CanonicalDashboard, "performance"> & { performance: CalendarPerformance }>(
    bookURL(`dashboard/projection?${params}`, book.id),
    signal,
  );
  const contract = data.projection_contract;
  if (data.book_id !== book.id || data.period.from !== period.from ||
      data.period.to !== period.to || contract?.version !== 2 ||
      contract.book_id !== book.id || contract.event_kind !== "accounting_recognition" ||
      contract.precision !== "date" || contract.currency !== book.currency ||
      contract.scale !== book.scale)
    throw new Error("The dashboard projection does not match this Book, period or accounting basis.");
  return { ...data, performance: fromCalendar(data.performance) };
}
