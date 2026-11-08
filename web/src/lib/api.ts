import { displayPreferences } from "./display-preferences";
import { APIError, fetchJSON } from "./errors";
import { writeHeaders } from "./session";
export { APIError, isConfirmedRejection } from "./errors";
import type { AccountingSubject } from "./legal-identity";
import type { UserPreferences } from "./user-preferences";
export type Currency = { code: string; name: string; scale: number };
/** How much native history the ledger holds for an account. Only a complete
 * history has a native amount; anything else is unavailable, never zero. */
export type NativeStatus = "complete" | "incomplete" | "not_recorded";
export type NativeAmount = { native_minor: string | null; native_status?: NativeStatus };
/** The native amount when it is known, otherwise null. A missing status is
 * the older contract, where an empty amount already meant unavailable. */
export function nativeMinor(value: NativeAmount): string | null {
  if (value.native_status && value.native_status !== "complete") return null;
  return typeof value.native_minor === "string" && /^-?(0|[1-9]\d*)$/.test(value.native_minor) ? value.native_minor : null;
}
export type CashAccount = NativeAmount & {
  code: string;
  name: string;
  currency: string;
  scale: number;
  /** Book-currency carrying value; separate from the native amount. */
  carrying_minor: string;
};
export type Quote = {
  id: string;
  source: string;
  base: string;
  quote: string;
  requested_date: string;
  observation_date: string;
  rate: string;
  cached: boolean;
};
export type Valuation = {
  card_account?: string;
  refund_of?: string;
  original_scale: number;
  paid_scale: number;
  actual_rate?: string;
  actual_quote_currency?: string;
  original_currency: string;
  original_amount_minor: string;
  paid_currency: string;
  paid_amount_minor: string;
  selection: string;
  quotes: Quote[];
  cash_account?: string;
  destination_account?: string;
  destination_currency?: string;
  destination_amount_minor?: string;
  settlement_currency?: string;
  settlement_amount_minor?: string;
  obligation_currency?: string;
  obligation_amount_minor?: string;
};
export type Book = {
  onboarding_state?: "pending" | "ready";
  onboarding_method?: "" | "fresh" | "import" | "recover";
  address?: import("./book-routing").BookAddress;
  subject_id?: string;
  subject?: AccountingSubject;
  profile?: string;
  supported_purchase_purpose_profile?: string;
 supported_observed_statement_profile?: string;
 supports_statement_evidence_review?: boolean;
  id: string;
  name: string;
  founder: string;
  start_date: string;
  coverage_start_date?: string;
  fiscal_year_start_month?: number;
  fiscal_year_start_day?: number;
  accounting_timezone?: string;
  accounting_today?: string;
  /** Accepted settings revision; 0 before the first change. */
  metadata_revision?: number;
  currency: string;
  scale: number;
  /** The Book's logo or photo, when one is set (lib/book-logo). */
  logo?: { id: string; sha256: string; content_type: string; size: number } | null;
};
export type BookList = {
  books: Book[];
  currencies: Currency[];
  source_books?: Book[] | null;
  prototype_import_enabled?: boolean;
  book_aliases?: Record<string, string>;
};
export function bookURL(path: string, bookId: string) {
  const [pathname, query = ""] = path.split("?");
  const params = new URLSearchParams(query);
  params.set("book_id", bookId);
  return `${pathname}?${params}`;
}
export type Receipt = {
  id: string;
  filename: string;
  content_type: string;
  sha256: string;
  size: number;
};
export type ImportProvenance = {
  version: 1;
  provider: string;
  source_reference?: string;
  source_sha256?: string;
  source_row?: number;
  external_reference?: string;
  batch_id?: string;
  audit_text?: string;
};
export type Transaction = {
  brand?: import("./activity-brands").EntryBrand;
  reference?: string;
  attachment_count?: number;
  user_note?: string;
  import_provenance?: ImportProvenance;
  view_kind?: string;
  import?: {
    import_id: string;
    source_key: string;
    type: string;
    document_number: string;
    artifact_id: string;
    source_row: number;
    source_sha256: string;
    review?: {
      category?: string;
      note?: string;
      user_note?: string;
      document_id?: string;
      reviewed?: boolean;
    };
  };
  fx?: Valuation;
  id: string;
  book_id: string;
  kind: string;
  date: string;
  economic_date: string;
  posting_date: string;
  fiscal_year_start: string;
  fiscal_year_end: string;
  recorded_at: string;
  description: string;
  counterparty: string;
  note: string;
  payer: string;
  category: string;
  currency: string;
  scale: number;
  amount_minor: string;
  reversal_of: string;
  reversed_by: string;
  replacement_of: string;
  receipt: Receipt | null;
};
export type Line = {
  memo?: string;
  native_amount_minor?: string;
  native_currency?: string;
  id: string;
  journal_id: string;
  account_code: string;
  account_name: string;
  side: string;
  amount_minor: string;
};
export type Allocation = {
  native_amount_minor: string;
  native_currency: string;
  id: string;
  source_id: string;
  payment_id: string;
  description: string;
  amount_minor: string;
  effect: number;
  reverses_id: string;
};
export type Detail = Transaction & { fx: Valuation; lines: Line[]; allocations: Allocation[] };
export type DashboardData = {
  // Optional evidence-backed cash movements added by the frontend read model.
  money_in_minor?: string;
  money_out_minor?: string;
  movement_count?: number;
  movement_categories?: import("./movement-categories").MoneyCategory[];
  movement_series?: {
    date: string;
    money_in_minor: string;
    money_out_minor: string;
  }[];
  book_id: string;
  version: string;
  period: { from: string; to: string };
  cash_minor: string;
  profit_minor: string;
  cash_flow_minor: string;
  cash_effects_minor: string;
  transaction_count: number;
  last_date: string;
  interval: "day" | "month" | "year";
  series: { date: string; profit_minor: string; cash_flow_minor: string }[];
  attention: {
    kind: "negative_cash";
    account: string;
    name: string;
    currency: string;
    scale: number;
    amount_minor: string;
  }[];
};
export type PersonalCardAccount = CashAccount & { issuer_owes: boolean };
export type PersonalEstimate = {
  status: "complete" | "partial" | "unavailable";
  as_of: string;
  currency: string;
  scale: number;
  available_subtotal_minor: string;
  basis: string;
  missing_currencies: string[];
  components: {
    account_code: string;
    native_currency: string;
    native_minor: string;
    estimated_minor?: string;
    source: string;
    quote_id?: string;
    observation_date?: string;
    rate?: string;
    freshness_days: number;
  }[];
};
export type PersonalState = {
  recorded_income_minor?: string;
  recorded_spending_minor?: string;
  as_of: string;
  currency: string;
  scale: number;
  assets_minor: string;
  liabilities_minor: string;
  tracked_net_worth_minor: string;
  basis: string;
  scope: string;
  included_account_codes: string[];
  card_accounts: PersonalCardAccount[];
  indicative_liquid_estimate: PersonalEstimate | null;
};
/** Vult Open's recorded position. Equality of the equation does not confirm
 * historical completeness; coverage stays as stated. */
export type RecordedPosition = {
  assets_minor: string;
  liabilities_minor: string;
  equity_accounts_minor: string;
  accumulated_result_minor: string;
  net_assets_minor: string;
  basis: string;
  source_coverage: string;
};
export type State = {
  version?: number;
  state_revision?: string;
  account_activity?: unknown;
  /** Binds Activity pages and the summary; a change answers 409. */
  revision?: string;
  position?: RecordedPosition;
  as_of?: string;
  period?: { from: string; to: string };
  /** Cash is the subtotal of cash-role accounts only: partial, or unavailable. */
  cash_status?: "complete" | "partial" | "unavailable" | "not_requested";
  /** `flat_recorded_accounts`: names never establish parents. */
  hierarchy_basis?: string;
  source_coverage?: string;
  capabilities?: { dashboard?: boolean; stored_account_parents?: boolean; [key: string]: boolean | undefined };
  /** Explicit workspace metadata, not a financial balance population. */
  bootstrap?: boolean;
  read_view?:
    "dashboard" | "transactions" | "accounts" | "documents" | "settings" | "statements";
  transaction_counts?: Record<string, number>;
  coverage?: {
    has_more: boolean;
    next_offset: number;
    limit: number;
    complete: boolean;
    basis: string;
  };
  personal?: PersonalState;
  dashboard?: DashboardData;
  dashboard_snapshot?: import("./dashboard-projection").DashboardSnapshot;
  account_balances?: {
    code: string;
    name: string;
    kind: string;
    gl_code?: string;
    gl_code_revision?: number;
    usage: string;
    currency: string;
    scale: number;
    balance_minor: string;
    period_change_minor?: string;
    native_minor: string | null;
    native_status?: NativeStatus;
    posting_count: number;
    /** A recorded parent account, when the Book stores parents. Never derived from names. */
    parent_id?: string;
  }[];
  months?: { date: string; income_minor: string; expenses_minor: string }[];
  source_import?: {
    id: string;
    captured_at: string;
    through: string;
    source_sha256: string;
    transactions: number;
    documents: number;
  };
  currencies: Currency[];
  cash_accounts: CashAccount[];
  /** Managed reads only; Vult Open states currency positions per account. */
  payables?: {
    currency: string;
    scale: number;
    purpose: string;
    native_minor: string | null;
    native_status?: NativeStatus;
    carrying_minor: string;
  }[];
  fx_gain_minor?: string;
  fx_loss_minor?: string;
  book: Book | null;
  /** Vult Open: period income, expenses and net, with cash only when its scope is known.
   * Managed reads add reimbursement, loan and owed subtotals. */
  summary?: {
    cash_minor?: string;
    reimbursements_minor?: string;
    loans_minor?: string;
    owed_minor?: string;
    income_minor: string;
    expenses_minor: string;
    net_minor?: string;
  };
  transactions: Transaction[];
  /** `code` is the account's stable identity; `gl_code` is the editable code. */
  accounts: { code: string; name: string; kind: string; role?: string; gl_code?: string; gl_code_revision?: number }[];
};
export async function get<T>(url: string, signal?: AbortSignal): Promise<T> {
  return fetchJSON<T>(`/api/v1/${url}`, signal ? { signal } : undefined);
}
export async function post<T>(
  url: string,
  body: unknown,
  receipt?: File,
  securityOperation?: string,
): Promise<T> {
  if (receipt && receipt.size > 10 * 1024 * 1024)
    throw new APIError("Receipt must be 10 MiB or smaller.", 400);
  const dataBody = receipt ? new FormData() : null;
  if (dataBody) {
    dataBody.set("request", JSON.stringify(body));
    dataBody.set("file", receipt!);
  }
  return fetchJSON<T>(`/api/v1/${url}`, {
    method: "POST",
    headers: {
      ...(await writeHeaders(!receipt)),
      ...(securityOperation ? { "X-Vult-Security-Operation": securityOperation } : {}),
    },
    body: dataBody || JSON.stringify(body),
  });
}
export function today(timeZone = "UTC") {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const value = (name: string) => parts.find((p) => p.type === name)!.value;
  return `${value("year")}-${value("month")}-${value("day")}`;
}
export function parseMoney(text: string, scale: number, allowZero = false) {
  const s = text.trim();
  if (!/^\d+(\.\d+)?$/.test(s))
    throw new Error("Enter a positive decimal amount without separators.");
  const [whole, fraction = ""] = s.split(".");
  if (fraction.length > scale)
    throw new Error(`This currency supports ${scale} decimal places.`);
  const n = BigInt(whole + fraction.padEnd(scale, "0"));
  if (n > 9223372036854775807n || n < 0n || (!allowZero && n === 0n))
    throw new Error("Enter a positive amount within the supported range.");
  return n.toString();
}
const moneyFormats = new Map<string, Intl.NumberFormat>();
/** Exact minor units stay integers; formatting never rounds through a Number. */
export function money(
  minor: string,
  book: Pick<Book, "currency" | "scale">,
  preferences?: Pick<
    UserPreferences,
    "regional_format" | "digit_grouping" | "negative_style"
  >,
) {
  preferences ??= displayPreferences();
  let value = BigInt(minor);
  const negative = value < 0n;
  const sign =
    negative && preferences?.negative_style !== "parentheses" ? "−" : "";
  if (value < 0n) value = -value;
  const raw = value.toString().padStart(book.scale + 1, "0");
  const whole = BigInt(book.scale ? raw.slice(0, -book.scale) : raw);
  const locale = preferences?.regional_format || "en-US";
  const grouping = preferences?.digit_grouping ?? true;
  const key = `${locale}:${book.currency}:${book.scale}:${grouping}`;
  let format = moneyFormats.get(key);
  if (!format) {
    format = new Intl.NumberFormat(locale, {
      style: "currency",
      currency: book.currency,
      currencyDisplay: "symbol",
      minimumFractionDigits: book.scale,
      maximumFractionDigits: book.scale,
      useGrouping: grouping,
    });
    moneyFormats.set(key, format);
  }
  const formatted =
    sign +
    format
      .formatToParts(whole)
      .map((part) =>
        part.type === "fraction" ? raw.slice(-book.scale) : part.value,
      )
      .join("");
  return negative && preferences?.negative_style === "parentheses"
    ? `(${formatted})`
    : formatted;
}
export const names: Record<string, string> = {
  opening: "Opening balance",
  opening_bank: "Opening bank balance",
  opening_card: "Opening card debt",
  card_purchase: "Card purchase",
  card_payment: "Card payment",
  card_refund: "Card refund",
  expense_reclassification: "Expense account correction",
  income: "Income",
  expense: "Expense",
  capital: "Capital contribution",
  loan: "Founder loan",
  reimbursement: "Expense reimbursement",
  loan_repayment: "Loan repayment",
  reversal: "Reversal",
  transfer: "Cash transfer",
  journal: "Journal entry",
};
export function context(t: Transaction) {
  if (t.kind === "journal")
    return t.import?.document_number
      ? `#${t.import.document_number}`
      : "Journal entry";
  return t.payer === "founder"
    ? "Founder personally"
    : t.payer === "business"
      ? "Business cash"
      : names[t.kind];
}

export function currencyMoney(
  minor: string,
  currency: string,
  catalog: Currency[],
) {
  const item = catalog.find((c) => c.code === currency);
  return money(minor, { currency, scale: item?.scale ?? 2 });
}

// Display only: the full exact rational remains in the posted provenance.
export function rateDisplay(rate: string) {
  if (!rate.includes("/")) return rate;
  const [n, d] = rate.split("/").map(BigInt);
  const precision = 1000000000000n;
  const q = (n * precision) / d;
  const text = q.toString().padStart(13, "0");
  return `${text.slice(0, -12)}.${text.slice(-12).replace(/0+$/, "") || "0"}${(n * precision) % d ? "…" : ""}`;
}
