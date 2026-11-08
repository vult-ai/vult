import { accountCodeLabel } from "./account-codes";
import { verifiedReadFailure } from "./errors";
import { bookURL, type Book, type State, type Transaction } from "./api";
import { systemAccounts, valuationRoles } from "./system-accounts";

export type JournalPosting = {
  id: string;
  account: string;
  amount: bigint;
};
export type TransactionJournal = Map<string, JournalPosting[]>;
export type TransactionDirection =
  "in" | "out" | "transfer" | "reversal" | "neutral";
export type TransactionFacts = {
  category: string;
  categoryTitle: string;
  categoryAccount?: State["accounts"][number];
  direction: TransactionDirection;
  directionLabel: string;
};

const maxCSV = 32 * 1024 * 1024;
const maxMinor = 9223372036854775807n;
const incomeKinds = new Set(["revenue", "other_income"]);
const expenseKinds = new Set(["expense", "other_expense"]);

/** A complete Book-scoped journal read: exact minor units, balanced entries,
 * duplicate-line detection and bounded RFC 4180 records. No source writes. */
export function parseTransactionJournal(
  csv: string,
  book: Pick<Book, "id" | "currency">,
): TransactionJournal {
  if (csv.length > maxCSV)
    throw new Error("The journal exceeds the supported read size.");
  const result: TransactionJournal = new Map();
  const seen = new Map<string, string>();
  let header: string[] | undefined;
  let indexes: Record<string, number> = {};
  let count = 0;
  const consume = (row: string[]) => {
    if (!header) {
      header = row;
      for (const key of [
        "book_id",
        "currency",
        "transaction_id",
        "line_id",
        "account_code",
        "side",
        "amount_minor",
      ]) {
        if (row.filter((field) => field === key).length !== 1)
          throw new Error("The journal header is incomplete.");
        indexes[key] = row.indexOf(key);
      }
      return;
    }
    if (row.length === 1 && row[0] === "") return;
    if (++count > 500_000 || row.length !== header.length)
      throw new Error("The journal read is incomplete or too large.");
    if (
      row[indexes.book_id] !== book.id ||
      row[indexes.currency] !== book.currency
    )
      throw verifiedReadFailure(
        "book-scope",
        "The journal belongs to another Book or currency.",
      );
    const transaction = row[indexes.transaction_id],
      id = row[indexes.line_id],
      account = row[indexes.account_code],
      side = row[indexes.side],
      raw = row[indexes.amount_minor];
    if (
      !transaction ||
      transaction.length > 128 ||
      !id ||
      id.length > 128 ||
      !account ||
      account.length > 512 ||
      !["debit", "credit"].includes(side) ||
      !/^(0|[1-9]\d{0,18})$/.test(raw) ||
      BigInt(raw) > maxMinor
    )
      throw new Error("The journal contains an invalid posting.");
    const signature = JSON.stringify([transaction, account, side, raw]);
    const previous = seen.get(id);
    if (previous !== undefined) {
      if (previous !== signature)
        throw verifiedReadFailure(
          "record-identity",
          "A journal line has conflicting values.",
        );
      return;
    }
    seen.set(id, signature);
    const lines = result.get(transaction) || [];
    lines.push({
      id,
      account,
      amount: BigInt(raw) * (side === "debit" ? 1n : -1n),
    });
    result.set(transaction, lines);
  };
  let row: string[] = [],
    field = "",
    quoted = false,
    closed = false;
  const fieldEnd = () => {
    row.push(field);
    field = "";
    closed = false;
    if (row.length > 64) throw new Error("The journal has too many fields.");
  };
  for (let i = csv.charCodeAt(0) === 0xfeff ? 1 : 0; i < csv.length; i++) {
    const char = csv[i];
    if (quoted) {
      if (char === '"') {
        if (csv[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          quoted = false;
          closed = true;
        }
      } else field += char;
    } else if (char === '"') {
      if (field || closed) throw new Error("The journal CSV is invalid.");
      quoted = true;
    } else if (char === ",") fieldEnd();
    else if (char === "\n" || char === "\r") {
      fieldEnd();
      consume(row);
      row = [];
      if (char === "\r" && csv[i + 1] === "\n") i++;
    } else {
      if (closed) throw new Error("The journal CSV is invalid.");
      field += char;
    }
    if (field.length > 131_072)
      throw new Error("The journal has an oversized field.");
  }
  if (quoted) throw new Error("The journal CSV is truncated.");
  if (field || row.length || closed) {
    fieldEnd();
    consume(row);
  }
  if (!header) throw new Error("The journal is missing.");
  for (const lines of result.values()) {
    if (lines.reduce((sum, line) => sum + line.amount, 0n) !== 0n)
      throw verifiedReadFailure(
        "ledger-balance",
        "The journal read contains an unbalanced entry.",
      );
  }
  return result;
}

/** A page read must contain every line of exactly the requested entries. */
export function parseScopedPostings(
  data: unknown,
  book: Pick<Book, "id" | "currency" | "scale">,
  requested: readonly string[],
): TransactionJournal {
  const fail = () => { throw new Error("The selected entry postings are incomplete or invalid."); };
  if (!data || typeof data !== "object" || Array.isArray(data)) return fail();
  const value = data as Record<string, unknown>;
  if (Object.keys(value).some(key => !["version", "book_id", "currency", "scale", "entries", "content_sha256"].includes(key))) return fail();
  if (value.book_id !== book.id || value.currency !== book.currency || value.scale !== book.scale)
    throw verifiedReadFailure("book-scope", "The postings belong to another Book or currency.");
  if (value.version !== 1 || typeof value.content_sha256 !== "string" || !/^[a-f0-9]{64}$/.test(value.content_sha256) || !Array.isArray(value.entries) ||
      requested.length < 1 || requested.length > 100 || new Set(requested).size !== requested.length || value.entries.length !== requested.length) return fail();
  const allowed = new Set(requested), result: TransactionJournal = new Map(), seen = new Set<string>();
  let count = 0;
  for (const entry of value.entries) {
    if (!entry || typeof entry !== "object" || typeof entry.id !== "string" || !allowed.has(entry.id) || result.has(entry.id) ||
        Object.keys(entry).some(key => !["id", "lines"].includes(key)) ||
        !Array.isArray(entry.lines) || entry.lines.length < 2) return fail();
    const lines: JournalPosting[] = [];
    for (const line of entry.lines) {
      if (++count > 10000 || !line || typeof line.id !== "string" || !line.id || line.id.length > 128 || seen.has(line.id) ||
          Object.keys(line).some(key => !["id", "account_code", "side", "amount_minor"].includes(key)) ||
          typeof line.account_code !== "string" || !line.account_code || line.account_code.length > 512 ||
          !["debit", "credit"].includes(line.side) || typeof line.amount_minor !== "string" || !/^(0|[1-9]\d{0,18})$/.test(line.amount_minor) ||
          BigInt(line.amount_minor) > maxMinor) return fail();
      seen.add(line.id);
      lines.push({ id: line.id, account: line.account_code, amount: BigInt(line.amount_minor) * (line.side === "debit" ? 1n : -1n) });
    }
    if (lines.reduce((sum, line) => sum + line.amount, 0n) !== 0n)
      throw verifiedReadFailure("ledger-balance", "The journal read contains an unbalanced entry.");
    result.set(entry.id, lines);
  }
  return result;
}

export async function readTransactionJournal(book: Book, signal: AbortSignal, ids: readonly string[]) {
  if (!ids.length || ids.length > 100 || new Set(ids).size !== ids.length)
    throw new Error("Choose up to 100 distinct entries.");
  const query = new URLSearchParams({ ids: [...ids].sort().join(",") });
  const response = await fetch(
    `/api/v1/${bookURL(`transaction-postings?${query}`, book.id)}`,
    { signal },
  );
  if (!response.ok)
    throw new Error("Transaction categories could not be loaded.");
  if (!response.headers.get("content-type")?.toLowerCase().includes("application/json"))
    throw new Error("The journal returned an unexpected response.");
  if (Number(response.headers.get("content-length")) > maxCSV)
    throw new Error("The journal exceeds the supported read size.");
  return parseScopedPostings(await response.json(), book, ids);
}

// Recorded names are whole; separators never establish parents.
const leaf = (name: string) => name;

/** Categories come from posted accounts, never descriptions or merchant names.
 * Direction cues describe the recorded effect without changing source amounts. */
export function transactionFacts(
  state: State,
  journal?: TransactionJournal | null,
  explicitCashAccounts: Iterable<string> = [],
): (t: Transaction) => TransactionFacts {
  const chart = new Map(
    state.accounts.map((account) => [account.code, account]),
  );
  const valuation = systemAccounts(state.accounts, valuationRoles);
  const cash = new Set([
    ...explicitCashAccounts,
    ...(state.cash_accounts || []).map((account) => account.code),
    ...(state.account_balances || [])
      .filter((account) => account.usage === "cash")
      .map((account) => account.code),
  ]);
  return (t) => {
    const lines = journal?.get(t.id);
    const net = new Map<string, bigint>();
    for (const line of lines || [])
      net.set(line.account, (net.get(line.account) || 0n) + line.amount);
    const active = [...net].filter(([, amount]) => amount !== 0n);
    const cashLines = active.filter(([code]) => cash.has(code));
    const transfer =
      t.kind === "transfer" ||
      (cashLines.some(([, n]) => n > 0n) && cashLines.some(([, n]) => n < 0n));
    const posted = active.length ? active : [...net];
    const pnl = posted.filter(([code]) => {
      const kind = chart.get(code)?.kind || "";
      return incomeKinds.has(kind) || expenseKinds.has(kind);
    });
    const counterpart = posted.filter(([code]) => !cash.has(code));
    // Canonical FX adjustments are valuation legs, not another purchase category.
    // Keep a standalone FX journal visible, but do not call a single-category
    // foreign expense a split merely because it realizes a gain or loss.
    const corePnl = pnl.filter(([code]) => !valuation.has(code));
    const categoriesPnl = corePnl.length ? corePnl : pnl;
    const codes = (categoriesPnl.length ? categoriesPnl : counterpart).map(
      ([code]) => code,
    );
    const categories = [...new Set(codes)].map(
      (code) => chart.get(code)?.name || code,
    );
    const explicit =
      chart.get(t.category)?.name || t.import?.review?.category?.trim() || "";
    const names = categories.length ? categories : explicit ? [explicit] : [];
    const categoryAccount =
      codes.length === 1
        ? chart.get(codes[0])
        : !codes.length
          ? chart.get(t.category)
          : undefined;
    // A transfer reads as where the money went: one source and one destination
    // among the non-income, non-expense legs; otherwise just "Transfer".
    const pnlCodes = new Set(pnl.map(([code]) => code));
    const legs = posted.filter(([code]) => !pnlCodes.has(code));
    const from = legs.filter(([, amount]) => amount < 0n);
    const to = legs.filter(([, amount]) => amount > 0n);
    const accountName = (code: string) => leaf(chart.get(code)?.name || code);
    const route =
      from.length === 1 && to.length === 1
        ? `${accountName(from[0][0])} → ${accountName(to[0][0])}`
        : "Transfer";
    const category = transfer
      ? route
      : Math.max(names.length, new Set(codes).size) > 1
        ? `Split (${Math.max(names.length, new Set(codes).size)})`
        : names[0]
          ? accountCodeLabel(leaf(names[0]), categoryAccount?.gl_code)
          : "—";
    const categoryTitle = transfer
      ? [
          ...cashLines.map(([code]) => chart.get(code)?.name || code),
          ...categories,
        ].join(" · ") || "Transfer between accounts"
      : names.join(" · ") || "See the recorded journal in entry details";
    const cashNet = cashLines.reduce((sum, [, amount]) => sum + amount, 0n);
    const pnlNet = pnl.reduce((sum, [, amount]) => sum + amount, 0n);
    const view = t.view_kind || t.kind;
    let direction: TransactionDirection = "neutral";
    let directionLabel = "Journal entry";
    if (t.kind === "reversal") {
      direction = "reversal";
      directionLabel = "Reversal";
    } else if (transfer) {
      direction = "transfer";
      directionLabel = "Transfer";
    } else if (t.kind === "card_refund") {
      direction = "in";
      directionLabel = "Refund";
    } else if (cashNet !== 0n) {
      direction = cashNet > 0n ? "in" : "out";
      // Money coming back on an expense, or going back out on income, is a refund.
      directionLabel =
        view === "income"
          ? cashNet > 0n ? "Income" : "Refund"
          : view === "expense"
            ? cashNet < 0n ? "Expense" : "Refund"
            : cashNet > 0n
              ? "Received"
              : "Paid";
    } else if (pnlNet !== 0n) {
      direction = pnlNet < 0n ? "in" : "out";
      directionLabel =
        pnlNet < 0n ? (view === "expense" ? "Refund" : "Income") : "Expense";
    } else if (["income", "capital", "loan"].includes(view)) {
      direction = "in";
      directionLabel = view === "income" ? "Income" : "Funding";
    } else if (
      [
        "expense",
        "card_purchase",
        "card_payment",
        "reimbursement",
        "loan_repayment",
      ].includes(view)
    ) {
      direction = "out";
      directionLabel =
        view === "expense" || view === "card_purchase" ? "Expense" : "Payment";
    }
    return {
      category,
      categoryTitle,
      ...(categoryAccount && !transfer && new Set(codes).size <= 1
        ? { categoryAccount }
        : {}),
      direction,
      directionLabel,
    };
  };
}
