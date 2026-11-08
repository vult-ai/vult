import { verifiedReadFailure } from "./errors";
import type { Book, State } from "./api";
export type AccountGroup =
  "all" | "asset" | "liability" | "equity" | "income" | "expense" | "cash";
export type Position = NonNullable<State["account_balances"]>[number];
export type FinancialRow = {
  code: string;
  name: string;
  kind: string;
  amount: string;
  /** The account's recorded role, such as cash or receivable. Never derived from names. */
  role?: string;
};
export type FinancialSnapshot = {
  assets: string;
  liabilities: string;
  equity: string;
  equityAccounts: string;
  accumulatedResult: string;
  income: string;
  expenses: string;
  result: string;
  through: string;
  rows: FinancialRow[];
  incomeRows: FinancialRow[];
  expenseRows: FinancialRow[];
};
const incomeKinds = new Set(["revenue", "other_income", "income"]);
const expenseKinds = new Set(["expense", "other_expense"]);
export const performanceKind = (kind: string) =>
  incomeKinds.has(kind)
    ? "income"
    : expenseKinds.has(kind)
      ? "expenses"
      : undefined;
export const creditKind = (kind: string) =>
  kind === "liability" || kind === "equity" || incomeKinds.has(kind);
export function inAccountGroup(
  a: Pick<Position, "kind" | "usage" | "currency">,
  group: AccountGroup,
) {
  if (group === "all") return true;
  if (group === "income") return incomeKinds.has(a.kind);
  if (group === "expense") return expenseKinds.has(a.kind);
  if (group === "cash")
    return (
      a.usage === "cash" ||
      (a.usage === "legacy" && a.kind === "asset" && !!a.currency)
    );
  return a.kind === group;
}
const integer = (value: string) => {
  if (!/^-?\d+$/.test(value))
    throw new Error("A recorded amount could not be confirmed.");
  return BigInt(value);
};
export function currentPosition(state: State) {
  if (state.personal)
    return {
      assets: state.personal.assets_minor,
      liabilities: state.personal.liabilities_minor,
      equity: state.personal.tracked_net_worth_minor,
    };
  // Vult Open states its recorded position; the interface checks it, never recomputes it.
  if (state.position) {
    const p = state.position;
    const [a, l, e, r, n] = [p.assets_minor, p.liabilities_minor, p.equity_accounts_minor, p.accumulated_result_minor, p.net_assets_minor].map(integer);
    if (a - l !== n || e + r !== n)
      throw verifiedReadFailure("ledger-balance", "The recorded position does not balance.");
    return { assets: p.assets_minor, liabilities: p.liabilities_minor, equity: p.net_assets_minor };
  }
  if (!state.account_balances) return undefined;
  let assets = 0n,
    liabilities = 0n,
    contributed = 0n,
    accumulated = 0n,
    balance = 0n,
    unclassified = 0n;
  const seen = new Set<string>();
  for (const a of state.account_balances) {
    if (seen.has(a.code))
      throw verifiedReadFailure(
        "record-identity",
        "Duplicate account identity in this Book.",
      );
    seen.add(a.code);
    const amount = integer(a.balance_minor);
    balance += amount;
    if (a.kind === "asset") assets += amount;
    else if (a.kind === "liability") liabilities -= amount;
    else if (a.kind === "equity") contributed -= amount;
    else if (incomeKinds.has(a.kind) || expenseKinds.has(a.kind))
      accumulated -= amount;
    else if (a.kind === "unknown") {
      unclassified += amount;
    } else if (amount !== 0n)
      throw new Error(
        "An account needs an accounting category before totals can be shown.",
      );
  }
  if (
    balance !== 0n ||
    assets !== liabilities + contributed + accumulated - unclassified
  )
    throw verifiedReadFailure(
      "ledger-balance",
      "The recorded account totals do not balance.",
    );
  return {
    assets: assets.toString(),
    liabilities: liabilities.toString(),
    // This legacy field is tracked assets less liabilities. Unclassified
    // counterparts are separately disclosed, never treated as income or assets.
    equity: (assets - liabilities).toString(),
  };
}
/** Present the amounts already calculated in the Accounts snapshot. */
export function recordedAccountSnapshot(state: State): FinancialSnapshot | undefined {
  const { position, summary, period, account_balances: accounts } = state;
  if (!position || !summary || !period || !accounts || summary.net_minor === undefined) return;
  currentPosition(state);
  const rows: FinancialRow[] = [], incomeRows: FinancialRow[] = [], expenseRows: FinancialRow[] = [];
  for (const { code, name, kind, balance_minor, period_change_minor } of accounts) {
    const performance = performanceKind(kind);
    const amount = integer(performance ? period_change_minor ?? "" : balance_minor);
    const row = { code, name, kind, amount: (creditKind(kind) ? -amount : amount).toString() };
    if (performance === "income") incomeRows.push(row);
    else if (performance === "expenses") expenseRows.push(row);
    else rows.push(row);
  }
  return {
    assets: position.assets_minor,
    liabilities: position.liabilities_minor,
    equity: position.net_assets_minor,
    equityAccounts: position.equity_accounts_minor,
    accumulatedResult: position.accumulated_result_minor,
    income: summary.income_minor,
    expenses: summary.expenses_minor,
    result: summary.net_minor,
    through: period.to,
    rows, incomeRows, expenseRows,
  };
}
export function financialSnapshot(
  book: Book,
  state: State,
  journal: Record<string, string>[],
  period: { from: string; to: string },
): FinancialSnapshot {
  const accounts = new Map(state.accounts.map((a) => [a.code, a]));
  const balances = new Map<string, bigint>(),
    periodBalances = new Map<string, bigint>();
  const postingCounts = new Map<string, number>();
  const seen = new Set<string>();
  for (const line of journal) {
    if (
      line.book_id !== book.id ||
      line.currency !== book.currency ||
      (line.scale && line.scale !== String(book.scale))
    )
      throw verifiedReadFailure(
        "book-scope",
        "Statement evidence belongs to a different Book or reporting currency.",
      );
    if (!accounts.has(line.account_code))
      throw new Error("A journal account is missing from this Book.");
    if (line.line_id) {
      if (seen.has(line.line_id))
        throw verifiedReadFailure(
          "record-identity",
          "Duplicate journal line in statement evidence.",
        );
      seen.add(line.line_id);
    }
    const date = line.economic_date;
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(date) ||
      new Date(date + "T00:00:00Z").toISOString().slice(0, 10) !== date
    )
      throw new Error("An entry date could not be confirmed.");
    if (
      !["debit", "credit"].includes(line.side) ||
      !/^\d+$/.test(line.amount_minor)
    )
      throw new Error("A journal amount could not be confirmed.");
    if (date > period.to) continue;
    postingCounts.set(
      line.account_code,
      (postingCounts.get(line.account_code) || 0) + 1,
    );
    const amount =
      integer(line.amount_minor) * (line.side === "debit" ? 1n : -1n);
    balances.set(
      line.account_code,
      (balances.get(line.account_code) || 0n) + amount,
    );
    if (date >= period.from)
      periodBalances.set(
        line.account_code,
        (periodBalances.get(line.account_code) || 0n) + amount,
      );
  }
  const positions = [...accounts.values()].map((a) => ({
    ...a,
    usage: "general",
    currency: "",
    scale: book.scale,
    native_minor: "",
    posting_count: postingCounts.get(a.code) || 0,
    balance_minor: (balances.get(a.code) || 0n).toString(),
  }));
  const position = currentPosition({
    ...state,
    personal: undefined,
    account_balances: positions,
  });
  if (!position) throw new Error("Recorded accounts are unavailable.");
  const rows: FinancialRow[] = [],
    incomeRows: FinancialRow[] = [],
    expenseRows: FinancialRow[] = [];
  let income = 0n,
    expenses = 0n,
    equityAccounts = 0n,
    accumulated = 0n;
  for (const a of accounts.values()) {
    const signed = balances.get(a.code) || 0n,
      during = periodBalances.get(a.code) || 0n;
    if (
      signed !== 0n ||
      (a.kind === "unknown" && (postingCounts.get(a.code) || 0) > 0)
    )
      rows.push({
        code: a.code,
        name: a.name,
        kind: a.kind,
        amount: (creditKind(a.kind) ? -signed : signed).toString(),
      });
    if (a.kind === "equity") equityAccounts -= signed;
    if (incomeKinds.has(a.kind) || expenseKinds.has(a.kind))
      accumulated -= signed;
    if (incomeKinds.has(a.kind)) {
      income -= during;
      if (during)
        incomeRows.push({
          code: a.code,
          name: a.name,
          kind: a.kind,
          amount: (-during).toString(),
        });
    }
    if (expenseKinds.has(a.kind)) {
      expenses += during;
      if (during)
        expenseRows.push({
          code: a.code,
          name: a.name,
          kind: a.kind,
          amount: during.toString(),
        });
    }
  }
  return {
    ...position,
    equityAccounts: equityAccounts.toString(),
    accumulatedResult: accumulated.toString(),
    income: income.toString(),
    expenses: expenses.toString(),
    result: (income - expenses).toString(),
    through: period.to,
    rows,
    incomeRows,
    expenseRows,
  };
}
