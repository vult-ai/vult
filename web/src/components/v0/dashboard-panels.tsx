import { Fragment, useMemo, useState, type ComponentType, type ReactNode, type SVGProps } from "react";
import { BarChart07, ChevronDown, ChevronRight, Coins01, CreditCard02, Receipt, Scales02, Wallet02, PieChart03 } from "../../design-system/icons";
import { money as baseMoney, type Book, type CashAccount } from "../../lib/api";
import { useUserPreferences } from "../../lib/user-preferences-context";
import { wordsLocale } from "../../lib/user-preferences";
import { nativeUnavailable } from "../../lib/native-money";
import type { CashStatus } from "../../lib/cash-summary";
import { formatDate as baseDate, formatDateRange as baseRange, type DateFormat } from "../../lib/date-format";
import type { PerformanceData, PerformanceSide } from "../../lib/performance-series";
import type { CalendarUnit, DashboardSnapshot, DashboardTrendPoint } from "../../lib/dashboard-projection";
import type { OverviewComparison } from "../../lib/overview-history";
import { BalanceTrend, type BalanceTrendPoint } from "./balance-trend";
import { RollingNumber } from "../ui/rolling-number";
import { DisplayAmount } from "../ui/display-amount";
import { Tooltip, TooltipTrigger } from "../../design-system/catalog/components/base/tooltip/tooltip";
import { Select } from "../ui/select";
import { Headline, SeriesKey } from "../../design-system/headline";
import { compactMoney } from "../../lib/compact-money";
import { DATA_PALETTE } from "../../design-system/data-palette";
import "../../design-system/styles/chart-palette.css";
import "./dashboard-panels.css";

type Icon = ComponentType<SVGProps<SVGSVGElement>>;
type Side = "income" | "expenses";
type Range = { from: string; to: string };

/* ---------- The one dashboard container ---------- */

/** Outer shell with an icon, title and subtitle; content sits on the inner card. */
export function DashboardPanel({
  icon: IconComponent,
  title,
  subtitle,
  action,
  className = "",
  children,
}: {
  icon: Icon;
  title: string;
  subtitle?: ReactNode;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={`dash-panel ${className}`} aria-label={title}>
      <header className="dash-panel-header">
        <span className="dash-panel-icon" aria-hidden="true"><IconComponent /></span>
        <div className="dash-panel-titles">
          <h2 className="vult-type-label">{title}</h2>
          {subtitle && <p className="vult-type-metadata">{subtitle}</p>}
        </div>
        {action && <div className="dash-panel-action">{action}</div>}
      </header>
      <div className="dash-panel-body">{children}</div>
    </section>
  );
}

function ViewAll({ label, onOpen }: { label: string; onOpen: () => void }) {
  return (
    <button type="button" className="dash-link vult-type-metadata vult-focus-target" onClick={onOpen}>
      {label}
    </button>
  );
}

/** Two views of one card, side by side in its header: In and Out, Owe and Own. */
function ModeSwitch<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: readonly { value: T; label: string }[]; onChange: (value: T) => void }) {
  return (
    <div className="dash-mode" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" className="dash-mode-option vult-type-metadata vult-focus-target" aria-pressed={value === o.value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

/* ---------- Shared figures ---------- */

const day = 86400000;
const at = (s: string) => new Date(s + "T00:00:00Z");
const monthName = (s: string, style: "short" | "long" = "short", locale = "en-US") => at(s).toLocaleString(locale, { month: style, timeZone: "UTC" });
const ratio = (n?: string, d?: string) =>
  n !== undefined && d !== undefined && BigInt(d) !== 0n ? (Number(n) / Number(d)) * 100 : undefined;
/** The viewer's regional format, date style and negative style, on every figure. */
function usePreferenceValue() {
  try {
    return useUserPreferences().value;
  } catch {
    return undefined;
  }
}
function useFormat(book: Book) {
  const prefs = usePreferenceValue();
  const locale = prefs?.regional_format || "en-US";
  // Words (month names) follow the interface language; numbers follow the region.
  const words = wordsLocale(locale);
  return {
    locale: words,
    money: (minor: string) => baseMoney(minor, book, prefs),
    compact: (minor: number) => compactMoney(minor, book, locale),
    date: (value: string, format: DateFormat = "compact") => baseDate(value, format, new Date(), prefs),
    range: (from: string, to: string, format: DateFormat = "compact") => baseRange(from, to, format, new Date(), prefs),
    month: (value: string, style: "short" | "long" = "short") => monthName(value, style, words),
    /** A percentage in the viewer's regional format: 50.7% or 50,7 %, with the
     * same true minus sign as amounts. */
    percent: (value: number, digits = 1) =>
      new Intl.NumberFormat(locale, { style: "percent", minimumFractionDigits: digits, maximumFractionDigits: digits }).format(value / 100).replace("-", "\u2212"),
  };
}
type Format = ReturnType<typeof useFormat>;

/** Business and personal Books share one dashboard; only the words differ. */
function wordsFor(book: Book) {
  const personal = book.profile === "personal";
  return {
    expenses: personal ? "Spending" : "Expenses",
    expensesLower: personal ? "spending" : "expenses",
    net: personal ? "Left over" : "Net result",
    netLower: personal ? "left over" : "net",
    equity: personal ? "Net worth" : "Equity",
    balanceSheet: personal ? "What you own and owe" : "Balance sheet",
    chart: personal ? "Income and spending" : "Income and expenses",
    other: personal ? "Other spending" : "Other expenses",
  };
}

/** A change and, in words beside it, what it is measured against ("vs last year",
 * "since Jan 1"), so the comparison never depends on hovering. Its colour says
 * whether the change is good news: for a cost (lowerIsBetter), a rise takes the
 * colour of a fall and a fall the colour of a rise. */
function Change({ now, before, book, against, label, percent = false, lowerIsBetter = false }: { now?: string; before?: string; book: Book; against?: string; label?: string; percent?: boolean; lowerIsBetter?: boolean }) {
  const f = useFormat(book);
  if (now === undefined || before === undefined) return null;
  const delta = BigInt(now) - BigInt(before);
  const base = BigInt(before) < 0n ? -BigInt(before) : BigInt(before);
  // The change as a share of where it started; none from zero, where it has no meaning.
  const share = percent && base > 0n ? Number((delta * 10000n) / base) / 100 : undefined;
  return (
    <span className="dash-change-group" title={against}>
      <span className="dash-change" data-tone={delta === 0n ? "flat" : (delta > 0n) !== lowerIsBetter ? "up" : "down"}>
        <RollingNumber value={`${delta > 0n ? "+" : ""}${f.money(delta.toString())}`} />
        {share !== undefined && <>{" "}(<RollingNumber value={`${share > 0 ? "+" : ""}${f.percent(share)}`} />)</>}
      </span>
      {label && <span className="dash-change-label">{" "}{label}</span>}
    </span>
  );
}

/* ---------- Summary ---------- */

type TrendAmount = "cash_minor" | "income" | "expenses" | "result";
const trendAmounts = (points: DashboardTrendPoint[], key: TrendAmount): BalanceTrendPoint[] =>
  points.flatMap((point) => point[key] === undefined ? [] : [{ date: point.date, amount: point[key] }]);

/** The four figures everyone checks, for the selected dates, each with its trend. */
export function SummaryPanel({ book, snapshot }: { book: Book; snapshot: DashboardSnapshot }) {
  const f = useFormat(book);
  const w = wordsFor(book);
  const { period: range, financial, prior_year: prior, cash_minor: cash, cash_status: cashStatus, trends } = snapshot;
  const { income: revenue, expenses, result } = financial;
  const comparison = prior?.period;
  const lastYear = (d: string) => `${String(Number(d.slice(0, 4)) - 1).padStart(4, "0")}${d.slice(4)}`;
  const vs = !comparison ? undefined : comparison.from === lastYear(range.from) && comparison.to === lastYear(range.to)
    ? "Compared with the same dates last year"
    : `Compared with ${f.range(comparison.from || comparison.to, comparison.to)}`;
  const vsShort = !comparison ? undefined : comparison.from === lastYear(range.from) && comparison.to === lastYear(range.to) ? "vs last year" : "vs earlier";
  const show = (v?: string) => (v === undefined ? "—" : f.money(v));
  const stat = (label: string, value: string, foot: ReactNode, key: Side | "net" | "cash", amount: TrendAmount) => {
    const current = trendAmounts(trends.current, amount);
    return (
      <div className="dash-stat" data-series={key}>
        <span className="dash-stat-label vult-type-metadata">{label}</span>
        <strong className="dash-figure"><DisplayAmount value={value} /></strong>
        <span className="dash-stat-foot vult-type-metadata">{foot}</span>
        {current.length > 1 && <BalanceTrend name={label} book={book} current={current} range={range} previous={trendAmounts(trends.previous, amount)} previousRange={comparison} />}
      </div>
    );
  };
  return (
    <DashboardPanel icon={PieChart03} title="Summary" className="dash-summary">
      <div className="dash-stats">
        {stat("Cash", cash === undefined && cashStatus === "unavailable" ? nativeUnavailable : show(cash), `${cashStatus === "partial" ? "Partial · " : ""}On ${f.date(range.to, "compact")}`, "cash", "cash_minor")}
        {stat("Income", show(revenue), <Change now={revenue} before={prior?.income} book={book} against={vs} label={vsShort} percent />, "income", "income")}
        {stat(w.expenses, show(expenses), <Change now={expenses} before={prior?.expenses} book={book} against={vs} label={vsShort} percent lowerIsBetter />, "expenses", "expenses")}
        {stat(w.net, show(result), <>{ratio(result, revenue) !== undefined && <span>{f.percent(ratio(result, revenue)!)} {book.profile === "personal" ? "of income" : "margin"}</span>} <Change now={result} before={prior?.result} book={book} against={vs} label={vsShort} /></>, "net", "result")}
      </div>
    </DashboardPanel>
  );
}

const share = (part: bigint, whole: bigint) => (whole > 0n ? Number((part * 1000n) / whole) / 10 : 0);

/** Why amounts could not be placed in calendar periods, in plain words. */
const unavailableText = (reason: string) =>
  reason === "book_timezone_not_set"
    ? "Set the Book's timezone to see amounts by period."
    : "Amounts by period are not available for this Book.";

/* ---------- Income and expenses chart ---------- */

type Segment = { id: string; value: bigint };
type Group = { key: string; unit: string; from: string; to: string; label: string; income: Segment[]; expenses: Segment[] };
/** The original Dashboard assignment: Vult's palette in order across income, then
 * expense accounts, so each account keeps one colour; the remainder is pink. */
function accountColors(performance?: PerformanceData) {
  const order = DATA_PALETTE.filter((c) => c !== "pink");
  const colors = new Map<string, string>();
  for (const side of ["income", "expenses"] as const)
    for (const c of performance?.[side].categories ?? []) {
      const key = `${side}:${c.id}`;
      colors.set(key, `var(--color-chart-${c.id === "other" ? "pink" : order[[...colors.keys()].filter((k) => !k.endsWith(":other")).length % order.length]})`);
    }
  return (side: Side, id: string) => colors.get(`${side}:${id}`) ?? "var(--color-chart-pink)";
}
/** Every figure is the signed net, refunds and credits included. */
const total = (s: Segment[]) => s.reduce((n, x) => n + x.value, 0n);
/** Positive amounts stack up from zero; refunds and credits that leave an account
 * negative for a period stack down from it, so up minus down is the net shown. */
const above = (s: Segment[]) => s.reduce((n, x) => n + (x.value > 0n ? x.value : 0n), 0n);
const below = (s: Segment[]) => s.reduce((n, x) => n + (x.value < 0n ? -x.value : 0n), 0n);

type Unit = CalendarUnit;
const MAX_GROUPS = 14;
/** Up to this many days draw one day a pair; more draw two. */
const MAX_DAY_GROUPS = 16;

function keyOf(date: string, unit: Unit) {
  if (unit === "day") return date;
  if (unit === "month") return date.slice(0, 7);
  if (unit === "year") return date.slice(0, 4);
  if (unit === "quarter") return `${date.slice(0, 4)}-Q${Math.floor((Number(date.slice(5, 7)) - 1) / 3) + 1}`;
  const d = at(date);
  return new Date(+d - ((d.getUTCDay() + 6) % 7) * day).toISOString().slice(0, 10);
}

export type Grouping = { unit: Unit; years?: number; days?: number; groups: Group[]; income: PerformanceSide; expenses: PerformanceSide };

/** One group per calendar period the route returned; amounts are never split or apportioned. */
export function groupPerformance(data: PerformanceData, locale = "en-US"): Grouping {
  const unit = data.grain;
  const source = data;
  const crossesYears = data.period.from.slice(0, 4) !== data.period.to.slice(0, 4);
  const shortYear = (date: string) => `’${date.slice(2, 4)}`;
  const groups = new Map<string, Group>();
  const blank = (side: PerformanceSide) => side.categories.map((c) => ({ id: c.id, value: 0n }));
  source.buckets.forEach((bucket, i) => {
    const key = keyOf(bucket.from, unit);
    // Days carry their month once there are more than two weeks of them.
    let label = source.buckets.length > 14 ? `${monthName(bucket.from, "short", locale)} ${at(bucket.from).getUTCDate()}` : String(at(bucket.from).getUTCDate());
    if (unit === "week") label = `${monthName(bucket.from, "short", locale)} ${at(bucket.from).getUTCDate()}`;
    if (unit === "month") label = `${monthName(bucket.from, "short", locale)}${crossesYears && (bucket.from.slice(5, 7) === "01" || groups.size === 0) ? ` ${shortYear(bucket.from)}` : ""}`;
    if (unit === "quarter") label = `${key.slice(5)} ${shortYear(bucket.from)}`;
    if (unit === "year") label = key;
    const group = groups.get(key) ?? { key, unit, from: bucket.from, to: bucket.to, label, income: blank(source.income), expenses: blank(source.expenses) };
    group.to = bucket.to;
    source.income.categories.forEach((c, j) => { group.income[j].value += BigInt(c.values[i] ?? "0"); });
    source.expenses.categories.forEach((c, j) => { group.expenses[j].value += BigInt(c.values[i] ?? "0"); });
    groups.set(key, group);
  });
  let list = [...groups.values()];
  let years: number | undefined;
  // Years before the first recorded income or expense carry nothing to compare.
  const firstActive = list.findIndex((g) => g.income.some((x) => x.value !== 0n) || g.expenses.some((x) => x.value !== 0n));
  if (firstActive > 0 && unit === "year") list = list.slice(firstActive);
  // Very long histories: aligned blocks of whole years (2000–2004), never partial ones.
  if (unit === "year" && list.length > MAX_GROUPS) {
    const blocksOf = (n: number) => new Set(list.map((g) => Math.floor(Number(g.key) / n))).size;
    const span = [2, 5, 10, 25].find((n) => blocksOf(n) <= MAX_GROUPS) ?? 50;
    years = span;
    const blocks = new Map<string, Group>();
    for (const g of list) {
      const start = Math.floor(Number(g.key) / span) * span;
      const key = `${start}`;
      const block = blocks.get(key) ?? { ...g, key, label: `${start}`, income: g.income.map((x) => ({ ...x, value: 0n })), expenses: g.expenses.map((x) => ({ ...x, value: 0n })) };
      block.to = g.to;
      g.income.forEach((x, j) => { block.income[j].value += x.value; });
      g.expenses.forEach((x, j) => { block.expenses[j].value += x.value; });
      blocks.set(key, block);
    }
    list = [...blocks.values()];
  }
  // More than sixteen days draw two days a pair (30 days is 15), so a month is as
  // full as a year's 12. Pairs count back from the last day, so the latest is
  // whole; amounts are only added, never split.
  let days: number | undefined;
  if (unit === "day" && list.length > MAX_DAY_GROUPS) {
    days = 2;
    const pairs: Group[] = [];
    for (let end = list.length; end > 0; end -= days) {
      const part = list.slice(Math.max(0, end - days), end);
      const first = part[0];
      pairs.unshift({
        ...first,
        to: part[part.length - 1].to,
        income: first.income.map((x, j) => ({ ...x, value: part.reduce((s, g) => s + g.income[j].value, 0n) })),
        expenses: first.expenses.map((x, j) => ({ ...x, value: part.reduce((s, g) => s + g.expenses[j].value, 0n) })),
      });
    }
    list = pairs;
  }
  return { unit, years, days, groups: list, income: source.income, expenses: source.expenses };
}
/** The round figure at or above a value, for a floor below zero. */
function niceTop(v: number) {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return ([1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].find((m) => m * p >= v) ?? 10) * p;
}
/** The largest round step (1, 2, 2.5 or 5 times a power of ten) at or below a value. */
function niceStep(v: number) {
  const p = 10 ** Math.floor(Math.log10(v));
  return ([5, 2.5, 2, 1].find((m) => m * p <= v) ?? 1) * p;
}
/** The tallest column fills this share of the space above zero. */
const FILL = 0.96;
/** One scale above and below zero, so a refund is drawn as large as it is.
 * The chart ends just above the tallest column, whatever the figures, so no
 * period ever opens with empty headroom; the gridlines sit at round steps
 * inside it (two to four of them), not at the edge. Below zero the chart only
 * reaches as deep as the amounts do: deep enough for a step, it gets the same
 * round gridlines; a reduction big enough to read but short of a step gets a
 * round labelled floor; a small one keeps just a sliver under the line for its
 * mark, unlabelled, since the tooltip gives it exactly. */
export const FLOOR_LABEL_SHARE = 0.12;
export function chartScale(highest: number, deepest: number) {
  if (highest <= 0) highest = Math.max(deepest, 1);
  const top = highest / FILL;
  const step = niceStep(highest / 2);
  const ticks = Array.from({ length: Math.floor(highest / step + 1e-9) }, (_, i) => step * (i + 1));
  if (deepest <= 0) return { top, low: 0, ticks, below: [] as number[], floor: undefined };
  if (deepest >= step) {
    const below = Array.from({ length: Math.floor(deepest / step + 1e-9) }, (_, i) => step * (i + 1));
    return { top, low: deepest / FILL, ticks, below, floor: undefined };
  }
  if (deepest < top * FLOOR_LABEL_SHARE) return { top, low: deepest, ticks, below: [] as number[], floor: undefined };
  const floor = niceTop(deepest);
  return { top, low: floor, ticks, below: [] as number[], floor };
}
const periodName = (g: Group, f: Format) =>
  g.unit === "month" ? `${f.month(g.from, "long")} ${g.from.slice(0, 4)}`
    : g.unit === "quarter" ? `${g.key.slice(5)} ${g.from.slice(0, 4)}`
    : g.unit === "year" ? (g.from.slice(0, 4) === g.to.slice(0, 4) ? g.key : `${g.from.slice(0, 4)}–${g.to.slice(0, 4)}`)
    : f.range(g.from, g.to);

/** Paired columns per period, each stacked by account. Chips choose the accounts;
 * the headline totals whatever is chosen, for the period or the month pointed at. */
export function IncomeExpensesPanel({
  book,
  performance,
  range,
  onShowPeriod,
}: {
  book: Book;
  performance?: PerformanceData;
  range: Range;
  onShowPeriod: (range: Range) => void;
}) {
  const f = useFormat(book);
  const w = wordsFor(book);
  const grouping = useMemo(() => (performance ? groupPerformance(performance, f.locale) : undefined), [performance, f.locale]);
  const groups = grouping?.groups ?? [];
  const colorOf = useMemo(() => accountColors(performance), [performance]);
  // Hover only previews a period in its tooltip. The headline follows a period
  // the viewer chose with a click (or Enter); clicking it again, Show all or
  // Escape returns to the whole range.
  const [hover, setHover] = useState<number>();
  const [pinnedKey, setPinnedKey] = useState<string>();
  // The same figures as a table: exact amounts for keyboard, touch and screen readers.
  const [asTable, setAsTable] = useState(false);
  const [opened, setOpened] = useState<Set<string>>(new Set());
  // One account can be in focus: its segments stay lit and the headline follows it.
  const [selected, setSelected] = useState<{ side: Side; id: string }>();
  const names = (side: Side) => grouping?.[side].categories ?? [];
  const isFocus = (side: Side, id: string) => !selected || (selected.side === side && selected.id === id);
  const sumOf = (_side: Side, segments: Segment[]) => total(segments);
  const extent = (of: (s: Segment[]) => bigint) => groups.reduce((m, g) => { const a = of(g.income), b = of(g.expenses); const v = a > b ? a : b; return v > m ? v : m; }, 0n);
  const [belowHelp, setBelowHelp] = useState(false);
  const deepest = extent(below);
  const { top, low, ticks, below: belowTicks, floor } = chartScale(Number(extent(above)), Number(deepest));
  // Where zero sits, as a share of the plot height measured from the bottom.
  const zero = low / (top + low);
  // Long labels (weeks, years, quarters) take every other slot past eight periods.
  const longest = groups.reduce((n, g) => Math.max(n, g.label.length), 0);
  const every = groups.length > 8 && longest >= 4 ? 2 : groups.length > 16 ? Math.ceil(groups.length / 8) : 1;
  // Days count back from the last, so the latest period is always named.
  const labelled = (i: number) => (groups[0]?.unit === "day" ? (groups.length - 1 - i) % every === 0 : i % every === 0);
  const focus = hover !== undefined ? groups[hover] : undefined;
  const pinned = groups.find((g) => g.key === pinnedKey);
  const scope = pinned ? [pinned] : groups;
  const income = scope.reduce((n, g) => n + sumOf("income", g.income), 0n);
  const expenses = scope.reduce((n, g) => n + sumOf("expenses", g.expenses), 0n);
  const onIncome = names("income");
  const onExpenses = names("expenses");
  const when = pinned ? ` in ${periodName(pinned, f)}` : "";
  const label = (c: { id: string; name: string }, side: Side) => (c.id === "other" ? (side === "income" ? "other income" : w.other.toLowerCase()) : c.name);
  const focusAccount = selected && names(selected.side).find((c) => c.id === selected.id);
  const focusIndex = selected ? names(selected.side).findIndex((c) => c.id === selected.id) : -1;
  const focusAmount = selected && focusIndex >= 0 ? scope.reduce((n, g) => n + (g[selected.side][focusIndex]?.value ?? 0n), 0n) : 0n;
  const sideTotal = selected?.side === "income" ? income : expenses;
  const headline = selected && focusAccount
    ? { value: focusAmount, caption: `${selected.side === "income" ? "from" : "on"} ${label(focusAccount, selected.side)}${when}` }
    : !onExpenses.length
    ? { value: income, caption: `${onIncome.length === 1 ? `from ${label(onIncome[0], "income")}` : "income"}${when}` }
    : !onIncome.length
      ? { value: expenses, caption: `${onExpenses.length === 1 ? `on ${label(onExpenses[0], "expenses")}` : w.expensesLower}${when}` }
      : { value: income - expenses, caption: `${w.netLower}${when}` };
  const column = (side: Side, segments: Segment[], index: number) => (
    <span className="dash-column" data-series={side}>
      <span className="dash-column-up">
        {segments.map((seg) => seg.value > 0n && (
          <span key={seg.id} className="dash-segment" data-muted={!isFocus(side, seg.id) || undefined} style={{ height: `${(Number(seg.value) / top) * 100}%`, background: colorOf(side, seg.id), animationDelay: `${index * 24}ms` }} />
        ))}
      </span>
      {low > 0 && (
        <span className="dash-column-down">
          {segments.map((seg) => seg.value < 0n && (
            <span key={seg.id} className="dash-segment dash-segment-negative" data-muted={!isFocus(side, seg.id) || undefined} style={{ height: `calc((var(--plot-h) - var(--zero-px)) * ${Number(-seg.value) / top})`, ["--seg" as string]: colorOf(side, seg.id), animationDelay: `${index * 24}ms` }} />
          ))}
        </span>
      )}
    </span>
  );
  const tipRows = (side: Side, g: Group) =>
    names(side)
      .map((c, j) => ({ id: c.id, name: label(c, side), value: g[side][j]?.value ?? 0n }))
      .filter((r) => r.value !== 0n)
      .sort((a, b) => (b.value > a.value ? 1 : b.value < a.value ? -1 : 0));
  return (
    <DashboardPanel
      icon={BarChart07}
      title={w.chart}
      className="dash-chart"
      action={groups.length > 0 && (
        <span className="dash-chart-actions">
          {!asTable && (
            <Select
              size="sm"
              aria-label="Focus one account"
              className="dash-account-select"
              value={selected ? `${selected.side}:${selected.id}` : ""}
              options={[
                { value: "", label: "All accounts" },
                ...(["income", "expenses"] as const).flatMap((side) => names(side).map((c) => ({
                  value: `${side}:${c.id}`,
                  label: c.id === "other" ? (side === "income" ? "Other income" : w.other) : c.name,
                  icon: <i className="dash-swatch dash-option-key" style={{ background: colorOf(side, c.id) }} aria-hidden="true" />,
                }))),
              ]}
              onChange={(event) => {
                const v = event.currentTarget.value;
                const at = v.indexOf(":");
                setSelected(v ? { side: v.slice(0, at) as Side, id: v.slice(at + 1) } : undefined);
              }}
            />
          )}
          <button type="button" className="dash-link vult-type-metadata vult-focus-target" aria-pressed={asTable} onClick={() => setAsTable((v) => !v)}>
            {asTable ? "Show chart" : "Show table"}
          </button>
        </span>
      )}
    >
      <Headline
        value={performance && !performance.unavailable ? f.money(headline.value.toString()) : "—"}
        caption={headline.caption}
        aside={!performance || performance.unavailable ? undefined : pinned ? (
          <>
            <button type="button" className="dash-link vult-type-metadata vult-focus-target" onClick={() => onShowPeriod({ from: pinned.from, to: pinned.to })}>View entries</button>
            <button type="button" className="dash-link vult-type-metadata vult-focus-target" onClick={() => setPinnedKey(undefined)}>Show all</button>
          </>
        ) : selected && focusAccount ? (
          <span><i className="dash-key" style={{ background: colorOf(selected.side, selected.id) }} aria-hidden="true" /><RollingNumber value={sideTotal > 0n ? `${f.percent(share(focusAmount, sideTotal), 1)}` : "—"} />&nbsp;of {selected.side === "income" ? "income" : w.expensesLower}</span>
        ) : (
          <>
            <span><SeriesKey series="income" /><span aria-hidden="true"><RollingNumber value={f.compact(Number(income))} />&nbsp;in</span><span className="sr-only">Income {f.money(String(income))}</span></span>
            <span><SeriesKey series="expenses" /><span aria-hidden="true"><RollingNumber value={f.compact(Number(expenses))} />&nbsp;out</span><span className="sr-only">{w.expenses} {f.money(String(expenses))}</span></span>
            {/* Only when the chart draws below zero; the explanation waits in a tooltip. */}
            {deepest > 0n && (
              <Tooltip title="Below zero" description="Refunds and credits. Every total is net of them." placement="bottom" motion="none" isOpen={belowHelp} onOpenChange={setBelowHelp}>
                <TooltipTrigger
                  className="dash-legend-help vult-focus-target"
                  aria-expanded={belowHelp}
                  onPress={() => setBelowHelp((open) => !open)}
                  onKeyDown={(event) => {
                    if (event.key === "Escape" && belowHelp) {
                      event.preventDefault();
                      setBelowHelp(false);
                    }
                  }}
                >
                  <i className="dash-key-negative" aria-hidden="true" />below zero
                </TooltipTrigger>
              </Tooltip>
            )}
          </>
        )}
      />
      {groups.length === 0 ? (
        <div className="dash-empty vult-type-metadata">{performance?.unavailable ? unavailableText(performance.unavailable) : performance ? "No income or expenses in these dates." : "Loading…"}</div>
      ) : asTable ? (
        <div className="dash-table-wrap">
          <table className="dash-table vult-type-ui">
            <caption className="sr-only">{w.chart} by period, exact amounts</caption>
            <thead className="vult-type-metadata">
              <tr><th scope="col">Period</th><th scope="col">Income</th><th scope="col">{w.expenses}</th><th scope="col">{w.net}</th></tr>
            </thead>
            <tbody>
              {groups.map((g) => {
                const a = total(g.income), b = total(g.expenses);
                const isOpen = opened.has(g.key);
                const accounts = [...tipRows("income", g).map((r) => ({ ...r, side: "income" as const })), ...tipRows("expenses", g).map((r) => ({ ...r, side: "expenses" as const }))];
                return (
                  <Fragment key={g.key}>
                    <tr>
                      <th scope="row">
                        <span className="dash-table-head">
                          <button type="button" className="dash-table-toggle vult-focus-target" aria-expanded={isOpen} disabled={!accounts.length}
                            aria-label={`${isOpen ? "Hide" : "Show"} accounts for ${periodName(g, f)}`}
                            onClick={() => setOpened((s) => { const n = new Set(s); n.has(g.key) ? n.delete(g.key) : n.add(g.key); return n; })}>
                            {isOpen ? <ChevronDown className="size-4" aria-hidden /> : <ChevronRight className="size-4" aria-hidden />}
                          </button>
                          <button type="button" className="dash-table-period vult-focus-target" onClick={() => onShowPeriod({ from: g.from, to: g.to })}>{periodName(g, f)}</button>
                        </span>
                      </th>
                      <td>{f.money(a.toString())}</td><td>{f.money(b.toString())}</td><td>{f.money((a - b).toString())}</td>
                    </tr>
                    {isOpen && accounts.map((r) => (
                      <tr key={`${r.side}:${r.id}`} className="dash-table-account vult-type-metadata">
                        <th scope="row"><i style={{ background: colorOf(r.side, r.id) }} aria-hidden="true" />{r.name.charAt(0).toUpperCase() + r.name.slice(1)}</th>
                        <td>{r.side === "income" ? f.money(r.value.toString()) : ""}</td>
                        <td>{r.side === "expenses" ? f.money(r.value.toString()) : ""}</td>
                        <td />
                      </tr>
                    ))}
                  </Fragment>
                );
              })}
            </tbody>
            <tfoot>
              {(() => {
                const a = groups.reduce((n, g) => n + total(g.income), 0n), b = groups.reduce((n, g) => n + total(g.expenses), 0n);
                return <tr><th scope="row">Total</th><td>{f.money(a.toString())}</td><td>{f.money(b.toString())}</td><td>{f.money((a - b).toString())}</td></tr>;
              })()}
            </tfoot>
          </table>
        </div>
      ) : (
        <div className="dash-plot" data-below={low > 0 || undefined} style={{ ["--zero" as string]: zero }} onMouseLeave={() => setHover(undefined)}
          onKeyDown={(event) => { if (event.key === "Escape" && pinnedKey) { event.stopPropagation(); setPinnedKey(undefined); } }}>
          <div className="dash-grid" aria-hidden="true">
            <span style={{ ["--t" as string]: 0 }} data-zero>
              <b className="vult-type-metadata">0</b>
            </span>
            {ticks.map((v) => (
              <span key={v} style={{ ["--t" as string]: v / top }}>
                <b className="vult-type-metadata">{f.compact(v)}</b>
              </span>
            ))}
            {belowTicks.map((v) => (
              <span key={-v} style={{ ["--t" as string]: -v / top }}>
                <b className="vult-type-metadata">{f.compact(-v)}</b>
              </span>
            ))}
            {floor !== undefined && (
              <span data-floor>
                <b className="vult-type-metadata">{f.compact(-floor)}</b>
              </span>
            )}
          </div>
          <div className="dash-bars" role="list" style={{ ["--groups" as string]: groups.length }}>
            {groups.map((g, i) => (
              <button
                key={g.key}
                type="button"
                role="listitem"
                className="dash-group vult-focus-target"
                data-active={hover === i || pinnedKey === g.key || undefined}
                data-dimmed={(pinned !== undefined && pinnedKey !== g.key) || undefined}
                aria-pressed={pinnedKey === g.key}
                aria-label={`${periodName(g, f)}: income ${f.money(sumOf("income", g.income).toString())}, expenses ${f.money(sumOf("expenses", g.expenses).toString())}`}
                onMouseEnter={() => setHover(i)}
                onFocus={() => setHover(i)}
                onBlur={() => setHover(undefined)}
                onClick={() => setPinnedKey((key) => (key === g.key ? undefined : g.key))}
              >
                {column("income", g.income, i)}
                {column("expenses", g.expenses, i)}
              </button>
            ))}
            {focus && hover !== undefined && (
              <div
                className="dash-tip vult-type-metadata"
                aria-hidden="true"
                data-side={hover > groups.length / 2 ? "left" : "right"}
                style={{ ["--at" as string]: `${((hover + (hover > groups.length / 2 ? 0 : 1)) / groups.length) * 100}%` }}
              >
                <p className="dash-tip-title"><span>{periodName(focus, f)}</span><span>{f.money((sumOf("income", focus.income) - sumOf("expenses", focus.expenses)).toString())} {w.netLower}</span></p>
                {(["income", "expenses"] as const).map((side) => {
                  const rows = tipRows(side, focus);
                  return rows.length > 0 && (
                    <div key={side} className="dash-tip-group">
                      <p className="dash-tip-head"><span>{side === "income" ? "Income" : w.expenses}</span><span>{f.money(sumOf(side, focus[side]).toString())}</span></p>
                      {rows.map((r) => (
                        <p key={r.id} className="dash-tip-row">
                          <i style={{ background: colorOf(side, r.id) }} />
                          <span>{r.name}</span>
                          <span>{f.money(String(r.value))}</span>
                        </p>
                      ))}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
          <div className="dash-axis vult-type-metadata" aria-hidden="true" style={{ ["--groups" as string]: groups.length }}>
            {groups.map((g, i) => <span key={g.key} data-active={hover === i || pinnedKey === g.key || undefined}>{labelled(i) ? g.label : ""}</span>)}
          </div>
        </div>
      )}
    </DashboardPanel>
  );
}

/* ---------- Lists ---------- */


export function SpendingPanel({ book, performance, range, onOpen, onViewStatement }: {
  book: Book; performance?: PerformanceData; range: Range; onOpen: (code: string) => void; onViewStatement: () => void;
}) {
  const f = useFormat(book);
  const w = wordsFor(book);
  // Hover highlights an account; a click (or Enter) chooses it for the headline.
  // Clicking it again, Show all or Escape returns to all spending.
  const [hover, setHover] = useState<string>();
  const [pinnedId, setPinnedId] = useState<string>();
  // Out shows where the money went; In shows where it came from.
  const [side, setSide] = useState<Side>("expenses");
  const out = side === "expenses";
  const other = out ? w.other : "Other income";
  const colorOf = accountColors(performance);
  const all = (performance?.[side].categories ?? []).map((c) => ({ ...c, amount: BigInt(c.total), color: colorOf(side, c.id) }));
  const named = all.filter((c) => c.code && c.amount > 0n);
  const whole = all.reduce((s, c) => s + (c.amount > 0n ? c.amount : 0n), 0n);
  // Accounts whose refunds exceeded spending for the period reduce the total; they are
  // listed after the rest with their signed amount and no share of the bar.
  const credits = all.filter((c) => c.amount < 0n);
  const shown = [...named, ...all.filter((c) => !c.code && c.amount > 0n)];
  const listed = [...shown, ...credits];
  return (
    <DashboardPanel icon={Receipt} title={out ? "Where the money went" : "Where the money came from"}
      action={<>
        <ModeSwitch label="Show money" value={side} onChange={(next) => { setSide(next); setPinnedId(undefined); setHover(undefined); }}
          options={[{ value: "income", label: "In" }, { value: "expenses", label: "Out" }] as const} />
        <ViewAll label="Statement" onOpen={onViewStatement} />
      </>}>
      {(() => {
        const focus = listed.find((c) => c.id === pinnedId);
        const actions = focus && (
          <>
            {focus.code && <button type="button" className="dash-link vult-type-metadata vult-focus-target" onClick={() => onOpen(focus.code)}>View account</button>}
            <button type="button" className="dash-link vult-type-metadata vult-focus-target" onClick={() => setPinnedId(undefined)}>Show all</button>
          </>
        );
        return focus ? (
          <Headline value={f.money(focus.total)}
            caption={focus.amount < 0n
              ? `${out ? "on" : "from"} ${focus.code ? focus.name : other.toLowerCase()}, after refunds`
              : <>{out ? "on" : "from"} {focus.code ? focus.name : other.toLowerCase()} · <RollingNumber value={`${f.percent(share(focus.amount, whole), 1)}`} /></>}
            aside={actions} />
        ) : (
          <Headline value={performance && !performance.unavailable ? f.money(performance[side].total) : "—"} caption={out ? "spent" : "earned"} aside={named.length ? `${f.percent(share(named.reduce((s, c) => s + c.amount, 0n), whole), 0)} in ${named.length} accounts` : undefined} />
        );
      })()}
      {listed.length === 0 ? <div className="dash-empty vult-type-metadata">{performance?.unavailable ? unavailableText(performance.unavailable) : performance ? (out ? `No ${w.expensesLower} in these dates.` : "No income in these dates.") : "Loading…"}</div> : (
        <>
          {shown.length > 0 && <div className="dash-share" data-focus={pinnedId ? "" : undefined} role="img" aria-label={`Share of ${out ? w.expensesLower : "income"}${credits.length ? " before refunds" : ""}: ${shown.map((c) => `${c.code ? c.name : other} ${f.percent(share(c.amount, whole), 1)}`).join(", ")}`}>
            {shown.map((c) => (
              <span key={c.id} data-active={hover === c.id || pinnedId === c.id || undefined} onMouseEnter={() => setHover(c.id)} onMouseLeave={() => setHover(undefined)} title={`${c.code ? c.name : other}: ${f.percent(share(c.amount, whole), 1)}`} style={{ flexGrow: Number(c.amount * 10000n / (whole || 1n)), background: c.color }} />
            ))}
          </div>}
          <ol className="dash-rows" onMouseLeave={() => setHover(undefined)}
            onKeyDown={(event) => { if (event.key === "Escape" && pinnedId) { event.stopPropagation(); setPinnedId(undefined); } }}>
            {listed.map((c) => {
              const body = (
                <>
                  <span className="dash-row-name vult-type-ui"><i className="dash-swatch" style={{ background: c.color }} aria-hidden="true" />{c.code ? c.name : other}</span>
                  <span className="dash-row-meta vult-type-metadata">{c.amount < 0n ? (out ? "Refunds exceeded spending" : "Refunds exceeded income") : `${f.percent(share(c.amount, whole), 1)}`}</span>
                  <span className="dash-row-value vult-money vult-type-ui">{f.money(c.total)}</span>
                </>
              );
              return <li key={c.id} onMouseEnter={() => setHover(c.id)} onFocus={() => setHover(c.id)} onBlur={() => setHover(undefined)} data-active={hover === c.id || pinnedId === c.id || undefined}>
                <button type="button" className="dash-row dash-row-share vult-focus-target" aria-pressed={pinnedId === c.id}
                  onClick={() => setPinnedId((id) => (id === c.id ? undefined : c.id))}>{body}</button>
              </li>;
            })}
          </ol>
        </>
      )}
    </DashboardPanel>
  );
}

export function CashPanel({ book, accounts, total: cash, status, range, onOpen, onViewAccounts }: {
  book: Book; accounts?: Pick<CashAccount, "code" | "name" | "carrying_minor">[]; total?: string; status?: CashStatus; range: Range; onOpen: (code: string) => void; onViewAccounts: () => void;
}) {
  const f = useFormat(book);
  const w = wordsFor(book);
  const sorted = accounts ? [...accounts].sort((a, b) => { const d = BigInt(b.carrying_minor) - BigInt(a.carrying_minor); return d > 0n ? 1 : d < 0n ? -1 : 0; }) : undefined;
  const top = sorted?.slice(0, 5);
  const rest = sorted?.slice(5).reduce((s, a) => s + BigInt(a.carrying_minor), 0n);
  return (
    <DashboardPanel icon={Wallet02} title="Cash by account" action={<ViewAll label="All accounts" onOpen={onViewAccounts} />}>
      <Headline value={cash !== undefined ? f.money(cash) : status === "unavailable" ? nativeUnavailable : "—"} caption={`${status === "partial" ? "cash · partial" : "cash"} on ${f.date(range.to, "compact")}`} aside={accounts ? `${accounts.length} account${accounts.length === 1 ? "" : "s"}` : undefined} />
      {status === "unavailable" ? <div className="dash-empty vult-type-metadata">Cash can’t be totalled until an account is marked as cash.</div> : !top ? <div className="dash-empty vult-type-metadata">Loading…</div> : top.length === 0 ? <div className="dash-empty vult-type-metadata">No cash accounts yet.</div> : (
        <ol className="dash-rows">
          {top.map((a) => (
            <li key={a.code}>
              {/* The name opens the account; the amount stays selectable. */}
              <div className="dash-row dash-row-plain">
                <button type="button" className="dash-row-name dash-row-open vult-type-ui vult-focus-target" aria-label={`${a.name}: open in Accounts`} onClick={() => onOpen(a.code)}>{a.name}</button>
                <span className="dash-row-value vult-money vult-type-ui">{f.money(a.carrying_minor)}</span>
              </div>
            </li>
          ))}
          {rest !== undefined && sorted!.length > 5 && (
            <li><div className="dash-row dash-row-plain"><span className="dash-row-name vult-type-ui text-tertiary">{sorted!.length - 5} more accounts</span><span className="dash-row-value vult-money vult-type-ui text-tertiary">{f.money(rest.toString())}</span></div></li>
          )}
        </ol>
      )}
    </DashboardPanel>
  );
}

/* ---------- What you owe ---------- */

/** Liability balances on the period's last date, largest first: the same rows the
 * balance sheet totals, so this card always agrees with it. Its other view is
 * what others owe the Book: assets recorded with the receivable role. */
export function OwedPanel({ book, position, range, onOpen, onViewAccounts }: {
  book: Book;
  /** Balance-sheet rows through the period's end, from the Dashboard read. */
  position?: { through: string; rows: { code: string; name: string; kind: string; amount: string; role?: string }[] };
  range: Range;
  onOpen: (code: string) => void;
  onViewAccounts: () => void;
}) {
  const f = useFormat(book);
  const [mode, setMode] = useState<"owe" | "owed">("owe");
  const owe = mode === "owe";
  const balances = position && position.through === range.to ? position.rows : undefined;
  const rows = (balances ?? [])
    .filter((a) => BigInt(a.amount) > 0n && (owe ? a.kind === "liability" : a.kind === "asset" && a.role === "receivable"))
    .map((a) => ({ code: a.code, name: a.name, owed: BigInt(a.amount) }))
    .sort((a, b) => (b.owed > a.owed ? 1 : b.owed < a.owed ? -1 : 0));
  const owed = rows.reduce((n, r) => n + r.owed, 0n);
  const top = rows.slice(0, 5);
  const rest = rows.slice(5).reduce((n, r) => n + r.owed, 0n);
  return (
    <DashboardPanel icon={owe ? CreditCard02 : Coins01} title={owe ? "What you owe" : "Owed to you"}
      action={<>
        <ModeSwitch label="Show balances" value={mode} onChange={setMode}
          options={[{ value: "owe", label: "You owe" }, { value: "owed", label: "Owed to you" }] as const} />
        <ViewAll label="All accounts" onOpen={onViewAccounts} />
      </>}>
      <Headline value={balances === undefined ? "—" : f.money(owed.toString())} caption={`${owe ? "owed" : "owed to you"} on ${f.date(range.to, "compact")}`} aside={balances ? `${rows.length} account${rows.length === 1 ? "" : "s"}` : undefined} />
      {balances === undefined ? <div className="dash-empty vult-type-metadata">Loading…</div> : rows.length === 0 ? <div className="dash-empty vult-type-metadata">{owe ? "Nothing owed in this Book." : "Nothing owed to you on this date."}</div> : (
        <ol className="dash-rows">
          {top.map((r) => (
            <li key={r.code}>
              {/* The name opens the account; the amount stays selectable. */}
              <div className="dash-row dash-row-plain">
                <button type="button" className="dash-row-name dash-row-open vult-type-ui vult-focus-target" aria-label={`${r.name}: open in Accounts`} onClick={() => onOpen(r.code)}>{r.name}</button>
                <span className="dash-row-value vult-money vult-type-ui">{f.money(r.owed.toString())}</span>
              </div>
            </li>
          ))}
          {rows.length > 5 && (
            <li><div className="dash-row dash-row-plain"><span className="dash-row-name vult-type-ui text-tertiary">{rows.length - 5} more accounts</span><span className="dash-row-value vult-money vult-type-ui text-tertiary">{f.money(rest.toString())}</span></div></li>
          )}
        </ol>
      )}
    </DashboardPanel>
  );
}

/* ---------- Balance sheet ---------- */

export function BalanceSheetPanel({ book, position, comparison, range, onOpen }: {
  book: Book;
  position?: { assets: string; liabilities: string; equity: string };
  comparison?: OverviewComparison;
  range: Range;
  onOpen: (group: "asset" | "liability" | "equity") => void;
}) {
  const f = useFormat(book);
  const w = wordsFor(book);
  const start = comparison?.previous?.at(-1) ?? comparison?.current?.[0];
  const items = [
    { label: "Assets", key: "assets" as const, group: "asset" as const },
    { label: "Liabilities", key: "liabilities" as const, group: "liability" as const },
    { label: w.equity, key: "equity" as const, group: "equity" as const },
  ];
  return (
    <DashboardPanel
      icon={Scales02}
      title={w.balanceSheet}
      className="dash-balance"
      // The dates every card shares, said once rather than under each figure.
      action={<span className="dash-panel-note vult-type-metadata">
        {f.date(range.to, "compact")}
        {position && start ? ` vs ${f.date(start.date, "compact")}` : ""}
      </span>}
    >
      <div className="dash-stats dash-stats-3">
        {items.map((m) => (
          // The card is for reading and copying its figures; its label opens Accounts.
          <div key={m.key} className="dash-stat">
            <button
              type="button"
              className="dash-stat-label dash-stat-open vult-type-metadata vult-focus-target"
              aria-label={`${m.label}: open in Accounts`}
              onClick={() => onOpen(m.group)}
            >
              {m.label}
              <ChevronRight className="dash-stat-open-icon" aria-hidden="true" />
            </button>
            <strong className="dash-figure"><DisplayAmount value={position ? f.money(position[m.key]) : "—"} /></strong>
            <span className="dash-stat-foot vult-type-metadata">
              {position && start ? (
                <Change now={position[m.key]} before={start[m.key]} book={book} against={`Change since ${f.date(start.date, "compact")}`} percent lowerIsBetter={m.key === "liabilities"} />
              ) : <>&nbsp;</>}
            </span>
            {comparison && comparison.current.length > 1 && comparison.current.at(-1)?.[m.key] === position?.[m.key] && (
              <BalanceTrend
                name={m.label}
                book={book}
                current={comparison.current.map((p) => ({ date: p.date, amount: p[m.key] }))}
                previous={comparison.previous.map((p) => ({ date: p.date, amount: p[m.key] }))}
                range={comparison.range}
                previousRange={comparison.previousRange}
              />
            )}
          </div>
        ))}
      </div>
    </DashboardPanel>
  );
}
