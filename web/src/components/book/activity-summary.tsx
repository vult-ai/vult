import "../../design-system/styles/chart-palette.css";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { bookURL, get, money, type Book } from "../../lib/api";
import { APIError } from "../../lib/errors";
import { formatMonth } from "../../lib/date-format";
import { useInspectorCompact } from "../../design-system/collection-inspector";
import { Headline, SeriesKey } from "../../design-system/headline";
import { compactMoney } from "../../lib/compact-money";
import { ErrorNotice } from "../ui/error-notice";
import { FailureState } from "../ui/failure-state";
import { LoadingState } from "../ui/loading-state";
import "./activity-summary.css";

export type ActivityFilters = {
  search: string;
  kind: string;
  from: string;
  to: string;
  account: string;
};

type Bucket = {
  key: string;
  label: string;
  income_minor: string;
  expenses_minor: string;
  count: number;
};

export type ActivitySummaryData = {
  version: 1;
  count: number;
  first_date: string;
  last_date: string;
  income_minor: string;
  expenses_minor: string;
  net_minor: string;
  unit: "month" | "year";
  series: Bucket[];
  categories: Bucket[];
  counterparties: Bucket[];
};

type Load =
  // A reload after a failure keeps that failure, so the retry stays visible.
  | { state: "loading"; error?: unknown }
  | { state: "ready"; data: ActivitySummaryData }
  | { state: "failed"; error: unknown };

const minor = /^-?(0|[1-9]\d*)$/;
const day = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$/;
const periodKey = { month: /^\d{4}-(0[1-9]|1[0-2])$/, year: /^\d{4}$/ } as const;
const isCount = (v: unknown) => Number.isSafeInteger(v) && (v as number) >= 0;
/** A real calendar day: 2025-02-31 matches the pattern but is not a date. */
const isDay = (v: unknown): v is string =>
  typeof v === "string" && day.test(v) && new Date(`${v}T00:00:00Z`).toISOString().slice(0, 10) === v;

function bucket(value: unknown, key?: RegExp): value is Bucket {
  const b = value as Partial<Bucket> | null;
  return (
    !!b &&
    typeof b.key === "string" &&
    b.key.length > 0 &&
    (!key || key.test(b.key)) &&
    typeof b.label === "string" &&
    typeof b.income_minor === "string" &&
    minor.test(b.income_minor) &&
    typeof b.expenses_minor === "string" &&
    minor.test(b.expenses_minor) &&
    isCount(b.count)
  );
}

/** Every field the summary renders is checked before it is shown. A response
 * that fails any check becomes a failure the person can retry, never a crash. */
export function readable(data: unknown): data is ActivitySummaryData {
  const d = data as Partial<ActivitySummaryData> | null;
  if (!d || d.version !== 1 || !isCount(d.count)) return false;
  if (![d.income_minor, d.expenses_minor, d.net_minor].every((v) => typeof v === "string" && minor.test(v))) return false;
  if (d.unit !== "month" && d.unit !== "year") return false;
  // An empty result has no dates; a non-empty one has an ordered pair.
  if (d.count === 0) {
    if (d.first_date !== "" || d.last_date !== "") return false;
  } else if (
    !isDay(d.first_date) || !isDay(d.last_date) || d.first_date > d.last_date
  ) return false;
  const lists = [d.series, d.categories, d.counterparties];
  if (!lists.every((list) => Array.isArray(list) && list.length <= 1000)) return false;
  if (!d.series!.every((b) => bucket(b, periodKey[d.unit!]))) return false;
  return d.categories!.every((b) => bucket(b)) && d.counterparties!.every((b) => bucket(b));
}

/** Exact totals for the transactions the table shows, under the same filters. */
function useActivitySummary(book: Book, filters: ActivityFilters, enabled: boolean, revision?: string | null, onStale?: () => void) {
  const query = useMemo(() => {
    const params = new URLSearchParams();
    // q is the Book search; the list sends the same.
    for (const [key, value] of Object.entries(filters)) if (value) params.set(key === "search" ? "q" : key, value);
    // Bound to the table's revision, so the totals describe the rows shown.
    if (revision) params.set("revision", revision);
    return params.toString();
  }, [filters.search, filters.kind, filters.from, filters.to, filters.account, revision]);
  const [load, setLoad] = useState<Load>({ state: "loading" });
  const [attempt, setAttempt] = useState(0);
  // `undefined` means the table has not answered yet: wait for its revision.
  const waiting = revision === undefined && onStale !== undefined;
  useEffect(() => {
    if (!enabled || waiting) return;
    const controller = new AbortController();
    setLoad((previous) =>
      previous.state === "failed" || (previous.state === "loading" && previous.error !== undefined)
        ? { state: "loading", error: previous.error }
        : { state: "loading" });
    get<unknown>(bookURL(`transactions/summary?${query}`, book.id), controller.signal)
      .then((data) => {
        if (!readable(data)) throw new Error("Unexpected summary response");
        setLoad({ state: "ready", data });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && error.status === 409 && onStale) { onStale(); return; }
        setLoad({ state: "failed", error });
      });
    return () => controller.abort();
  }, [book.id, query, attempt, enabled, waiting]);
  return { load, retry: () => setAttempt((n) => n + 1) };
}

// Summary periods always name their year, including the current one.
const always = new Date(0);
function periodLabel(key: string, unit: "month" | "year") {
  return unit === "year" ? key : formatMonth(key, always);
}
function rangeLabel(first: string, last: string) {
  const a = formatMonth(first.slice(0, 7), always);
  const b = formatMonth(last.slice(0, 7), always);
  return a === b ? a : `${a} – ${b}`;
}
const count = new Intl.NumberFormat("en-US");

type Totals = { income_minor: string; expenses_minor: string; net_minor: string };

/** The Dashboard's headline: net is the figure, income and expenses beside it.
 * `compact` rounds the parts, for the strip above the table; their exact amounts
 * stay in what screen readers hear. */
function Figures({ totals, book, caption, compact = false }: { totals: Totals; book: Book; caption: ReactNode; compact?: boolean }) {
  const part = (v: string) => (compact ? compactMoney(v, book) : money(v, book));
  return (
    <Headline
      stacked
      className="activity-summary-figures"
      value={money(totals.net_minor, book)}
      caption={caption}
      aside={
        <>
          <span><SeriesKey series="income" coloured /><span aria-hidden="true">{part(totals.income_minor)}&nbsp;in</span><span className="sr-only">Income {money(totals.income_minor, book)}</span></span>
          <span><SeriesKey series="expenses" coloured /><span aria-hidden="true">{part(totals.expenses_minor)}&nbsp;out</span><span className="sr-only">Expenses {money(totals.expenses_minor, book)}</span></span>
        </>
      }
    />
  );
}

/** Paired bars per period on one zero baseline; negatives draw below it. */
/** Whole periods joined so the panel never draws more than fourteen pairs: months
 * become quarters or years, years become aligned blocks (2000–2004). Amounts are
 * only ever added, never split. */
type Group = { key: string; label: string; from: string; to: string; income: bigint; expenses: bigint };
const MAX_GROUPS = 14;
const lastDay = (y: number, m: number) => new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10);
function groupSeries(series: Bucket[], unit: "month" | "year"): Group[] {
  const months = series.map((b) => {
    const y = Number(b.key.slice(0, 4)), m = unit === "month" ? Number(b.key.slice(5, 7)) : 0;
    return { b, y, m };
  });
  const join = (keyOf: (y: number, m: number) => { key: string; label: string; from: string; to: string }) => {
    const out = new Map<string, Group>();
    for (const { b, y, m } of months) {
      const k = keyOf(y, m);
      const g = out.get(k.key) ?? { ...k, income: 0n, expenses: 0n };
      g.income += BigInt(b.income_minor);
      g.expenses += BigInt(b.expenses_minor);
      out.set(k.key, g);
    }
    return [...out.values()];
  };
  if (unit === "month") {
    const byMonth = join((y, m) => ({ key: `${y}-${String(m).padStart(2, "0")}`, label: periodLabel(`${y}-${String(m).padStart(2, "0")}`, "month"), from: `${y}-${String(m).padStart(2, "0")}-01`, to: lastDay(y, m) }));
    if (byMonth.length <= MAX_GROUPS) return byMonth;
    const byQuarter = join((y, m) => { const q = Math.floor((m - 1) / 3); return { key: `${y}-Q${q + 1}`, label: `Q${q + 1} ${y}`, from: `${y}-${String(q * 3 + 1).padStart(2, "0")}-01`, to: lastDay(y, q * 3 + 3) }; });
    if (byQuarter.length <= MAX_GROUPS) return byQuarter;
  }
  const years = new Set(months.map(({ y }) => y));
  const span = [1, 2, 5, 10, 25, 50].find((n) => new Set([...years].map((y) => Math.floor(y / n))).size <= MAX_GROUPS) ?? 100;
  const lastYear = Math.max(...years);
  return join((y) => {
    const start = Math.floor(y / span) * span, end = Math.min(start + span - 1, lastYear);
    return { key: String(start), label: start === end ? String(start) : `${start}–${end}`, from: `${start}-01-01`, to: `${end}-12-31` };
  });
}

function Bars({
  groups,
  book,
  unitName,
  active,
  onPick,
}: {
  groups: Group[];
  book: Book;
  unitName: string;
  active?: string;
  onPick: (group: Group) => void;
}) {
  // The Dashboard's paired columns: income and expenses side by side, positive amounts
  // up from zero, refunds that leave a side negative hatched below it. Scales use exact
  // integers; only the final share of the height becomes a number.
  // Hovering only highlights a pair; a click shows that period's transactions.
  const values = groups.flatMap((g) => [g.income, g.expenses]);
  const top = values.reduce((a, v) => (v > a ? v : a), 0n);
  const low = values.reduce((a, v) => (v < -a ? -v : a), 0n);
  const whole = top + low || 1n;
  const zero = Number((low * 10000n) / whole) / 100;
  const share = (v: bigint, of: bigint) => (of > 0n ? Number((v * 10000n) / of) / 100 : 0);
  const column = (v: bigint, side: "income" | "expenses") => (
    <span className="activity-summary-column" data-series={side}>
      <span className="activity-summary-up" style={{ height: `${100 - zero}%` }}>
        {v > 0n && <i style={{ height: `${share(v, top)}%` }} />}
      </span>
      {low > 0n && (
        <span className="activity-summary-down" style={{ height: `${zero}%` }}>
          {v < 0n && <i data-negative="" style={{ height: `${share(-v, low)}%` }} />}
        </span>
      )}
    </span>
  );
  return (
    <div className="activity-summary-chart">
      <div
        className="activity-summary-bars"
        role="list"
        aria-label={`Income and expenses by ${unitName}`}
        style={{ ["--groups" as string]: groups.length }}
      >
        {groups.map((g) => (
          <span key={g.key} role="listitem" className="activity-summary-slot">
            <button
              type="button"
              className="activity-summary-group vult-focus-target"
              data-active={active === g.key || undefined}
              title={`${g.label}: ${money((g.income - g.expenses).toString(), book)} net`}
              aria-label={`${g.label}: income ${money(g.income.toString(), book)}, expenses ${money(g.expenses.toString(), book)}. Show these transactions.`}
              onClick={() => onPick(g)}
            >
              {column(g.income, "income")}
              {column(g.expenses, "expenses")}
            </button>
          </span>
        ))}
      </div>
      <div className="activity-summary-axis vult-type-metadata text-tertiary" aria-hidden="true">
        <span>{groups[0].label}</span>
        <span>{groups.at(-1)!.label}</span>
      </div>
    </div>
  );
}

/** Items on one side only, ranked by their net on that side. Income and expenses are
 * never ranked against each other; a net below zero (refunds) keeps its sign. */
function side(items: Bucket[], which: "income" | "expenses") {
  const net = (b: Bucket) => which === "income" ? BigInt(b.income_minor) - BigInt(b.expenses_minor) : BigInt(b.expenses_minor) - BigInt(b.income_minor);
  return items
    .filter((b) => (which === "income" ? BigInt(b.income_minor) > BigInt(b.expenses_minor) : BigInt(b.expenses_minor) >= BigInt(b.income_minor)))
    .map((b) => ({ ...b, net: net(b) }))
    .sort((a, b) => (b.net > a.net ? 1 : b.net < a.net ? -1 : 0));
}

function Ranked({
  title,
  items,
  which,
  book,
  onPick,
}: {
  title: string;
  items: Bucket[];
  which: "income" | "expenses";
  book: Book;
  onPick: (item: Bucket) => void;
}) {
  const ranked = side(items, which);
  if (!ranked.length) return null;
  return (
    <section className="activity-summary-section">
      <h3 className="vult-type-metadata text-tertiary">{title}</h3>
      <ol className="activity-summary-ranked">
        {ranked.map((item) => {
          return (
            <li key={item.key}>
              <button
                type="button"
                className="vult-focus-target"
                aria-label={`Show ${item.label} transactions`}
                onClick={() => onPick(item)}
              >
                <span className="activity-summary-ranked-name vult-type-ui text-secondary">{item.label}</span>
                <span className="activity-summary-ranked-amount vult-type-ui text-primary">
                  {money(item.net.toString(), book)}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** Shown in the details panel while no transaction is selected. */
export function ActivitySummary({
  book,
  filters,
  onFilter,
  revision,
  onStale,
}: {
  book: Book;
  filters: ActivityFilters;
  onFilter: (key: keyof ActivityFilters, value: string) => void;
  revision?: string | null;
  onStale?: () => void;
}) {
  const compact = useInspectorCompact();
  const { load, retry } = useActivitySummary(book, filters, !compact, revision, onStale);
  const groups = useMemo(() => (load.state === "ready" ? groupSeries(load.data.series, load.data.unit) : []), [load]);
  if (compact) return null;
  if (load.state === "loading") return <LoadingState scope="region" label="Loading summary…" />;
  if (load.state === "failed")
    return (
      <FailureState
        scope="region"
        error={load.error}
        intent="read"
        resource="activity"
        title="Couldn’t load this summary"
        onRetry={retry}
      />
    );
  const data = load.data;
  // The figures follow the filters, so they only change on a deliberate choice.
  const pick = (g: Group) => {
    const from = filters.from && filters.from > g.from ? filters.from : g.from;
    const to = filters.to && filters.to < g.to ? filters.to : g.to;
    onFilter("from", from);
    onFilter("to", to);
  };

  return (
    <div className="activity-summary">
      <p className="vult-type-metadata text-tertiary">
        {count.format(data.count)} {data.count === 1 ? "transaction" : "transactions"}
      </p>
      {data.count === 0 ? (
        <p className="vult-type-ui text-secondary">No transactions match these filters.</p>
      ) : (
        <>
          <section className="activity-summary-section">
            <Figures
              totals={data}
              book={book}
              caption={<span aria-live="polite">net · {rangeLabel(data.first_date, data.last_date)}</span>}
            />
            {groups.length > 1 && (
              <Bars groups={groups} book={book} unitName={data.unit} onPick={pick} />
            )}
          </section>
          {!filters.account && (
            <>
              <Ranked title="Top income" which="income" items={data.categories} book={book} onPick={(item) => onFilter("account", item.key)} />
              <Ranked title="Top expenses" which="expenses" items={data.categories} book={book} onPick={(item) => onFilter("account", item.key)} />
            </>
          )}
          {!filters.search && (
            <>
              <Ranked title="Received most from" which="income" items={data.counterparties} book={book} onPick={(item) => onFilter("search", item.label)} />
              <Ranked title="Paid most to" which="expenses" items={data.counterparties} book={book} onPick={(item) => onFilter("search", item.label)} />
            </>
          )}
        </>
      )}
    </div>
  );
}

/** At widths where the panel would squeeze the table, the totals sit above it. */
export function ActivitySummaryStrip({ book, filters, revision, onStale }: { book: Book; filters: ActivityFilters; revision?: string | null; onStale?: () => void }) {
  const compact = useInspectorCompact();
  const { load, retry } = useActivitySummary(book, filters, compact, revision, onStale);
  if (!compact) return null;
  // The table beside it still works, so a failed summary is a notice, not a gap.
  // A retry keeps the notice in place, showing it is checking, until it
  // succeeds or fails again.
  const failure = load.state === "failed" || load.state === "loading" ? load.error : undefined;
  const totals = load.state === "ready" && load.data.count ? load.data : undefined;
  if (failure === undefined && !totals) return null;
  return (
    <div className="activity-summary-strip">
      {failure !== undefined && <ErrorNotice error={failure} intent="read" resource="activity" pending={load.state === "loading"} onRetry={retry} />}
      {failure === undefined && totals && (
        <Figures
          compact
          totals={totals}
          book={book}
          caption={`net · ${rangeLabel(totals.first_date, totals.last_date)} · ${count.format(totals.count)} ${totals.count === 1 ? "transaction" : "transactions"}`}
        />
      )}
    </div>
  );
}
