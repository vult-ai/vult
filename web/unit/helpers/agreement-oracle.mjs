/* Independent oracle for the agreement Book. Plain double-entry arithmetic over the
   fixture's entries; it imports nothing from the interface or the engine, so its
   answers can check both. Dates are civil dates in the Book's timezone. */
import { readFileSync } from "node:fs";

export const book = JSON.parse(readFileSync(new URL("../fixtures/agreement-book.json", import.meta.url), "utf8"));
const accounts = new Map(book.accounts.map((a) => [a.key, a]));
const lines = book.entries.flatMap((e) => [
  { date: e.date, account: e.debit, debit: BigInt(e.amount) },
  { date: e.date, account: e.credit, debit: -BigInt(e.amount) },
]);
// Natural sign: assets and expenses grow with debits; the rest with credits.
const natural = (key, debit) => (["asset", "expense"].includes(accounts.get(key).class) ? debit : -debit);

const day = (s) => new Date(s + "T00:00:00Z");
const iso = (d) => d.toISOString().slice(0, 10);
const addDays = (s, n) => iso(new Date(+day(s) + n * 86400000));
function bucketStart(date, unit) {
  const [y, m] = [Number(date.slice(0, 4)), Number(date.slice(5, 7))];
  if (unit === "day") return date;
  if (unit === "week") return addDays(date, -((day(date).getUTCDay() + 6) % 7));
  if (unit === "month") return `${date.slice(0, 7)}-01`;
  if (unit === "quarter") return `${y}-${String(Math.floor((m - 1) / 3) * 3 + 1).padStart(2, "0")}-01`;
  return `${y}-01-01`;
}
function nextStart(start, unit) {
  if (unit === "day") return addDays(start, 1);
  if (unit === "week") return addDays(start, 7);
  const d = day(start);
  d.setUTCMonth(d.getUTCMonth() + (unit === "month" ? 1 : unit === "quarter" ? 3 : 12));
  return iso(d);
}

function side(selected, cls) {
  const out = new Map();
  for (const l of selected) {
    if (accounts.get(l.account).class !== cls) continue;
    const v = natural(l.account, l.debit);
    const a = out.get(l.account) ?? { key: l.account, name: accounts.get(l.account).name, amount: 0n, positive: 0n, negative: 0n, postings: 0 };
    // Signed contributions, as the engine publishes them: amount = positive + negative.
    a.amount += v; v > 0n ? (a.positive += v) : (a.negative += v); a.postings += 1;
    out.set(l.account, a);
  }
  const list = [...out.values()].sort((a, b) => a.key.localeCompare(b.key));
  const sum = (f) => list.reduce((n, a) => n + a[f], 0n);
  return { total: sum("amount"), positive: sum("positive"), negative: sum("negative"), accounts: list };
}

/** Income and expenses for an inclusive period, in calendar buckets of `unit`. */
export function performance(from, to, unit) {
  const end = addDays(to, 1);
  const buckets = [];
  for (let s = bucketStart(from, unit); s < end; s = nextStart(s, unit)) {
    const e = nextStart(s, unit);
    const coveredFrom = s < from ? from : s, coveredTo = e > end ? end : e;
    const inside = lines.filter((l) => l.date >= coveredFrom && l.date < coveredTo);
    buckets.push({ from: s, to: e, covered_from: coveredFrom, covered_to: coveredTo, income: side(inside, "income"), expenses: side(inside, "expense") });
  }
  const all = lines.filter((l) => l.date >= from && l.date <= to);
  return { from, to, unit, income: side(all, "income"), expenses: side(all, "expense"), buckets };
}

/** Balances after every entry dated on or before `date`. */
export function position(date) {
  const balances = new Map(book.accounts.map((a) => [a.key, 0n]));
  for (const l of lines) if (l.date <= date) balances.set(l.account, balances.get(l.account) + natural(l.account, l.debit));
  const total = (cls, role) => book.accounts.filter((a) => a.class === cls && (!role || a.role === role)).reduce((n, a) => n + balances.get(a.key), 0n);
  const result = total("income") - total("expense");
  return {
    date, balances: Object.fromEntries(balances),
    cash: total("asset", "cash"), assets: total("asset"), liabilities: total("liability"),
    equity: total("equity") + result, result,
  };
}

const json = (v) => JSON.stringify(v, (_, x) => (typeof x === "bigint" ? x.toString() : x), 2);
if (import.meta.url === `file://${process.argv[1]}`) {
  console.log(json({
    q1_months: performance("2026-01-01", "2026-03-31", "month"),
    q1_weeks: performance("2026-01-01", "2026-03-31", "week"),
    q1_quarter: performance("2026-01-01", "2026-03-31", "quarter"),
    position_2026_03_31: position("2026-03-31"),
    position_2026_04_30: position("2026-04-30"),
  }));
}
