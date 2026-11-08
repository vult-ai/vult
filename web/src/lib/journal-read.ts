import type { Book } from "./api";

/** One measured quantity: a currency or a defined unit at an exact scale. */
export type CurrencyAmount = {
  currency: string;
  scale: number | null;
  amount_minor: string;
  unit_definition?: string;
};
export type NativeMovement = CurrencyAmount & { account_revision?: number };
export type ContextMeasurement = { role: string; amount: CurrencyAmount; source?: string };
export type AppliedRate = {
  base: string;
  quote: string;
  rate: string;
  requested_date: string;
  observation_date: string;
  provenance: string;
};
export type CarryingAllocation = {
  policy: string;
  account_id: string;
  account_revision: number;
  role_revision: number;
  before_sequence: number;
  before_native: CurrencyAmount;
  before_book: CurrencyAmount;
  consumed_native: CurrencyAmount;
  consumed_book: CurrencyAmount;
};
export type LineValuation = {
  basis: string;
  policy: string;
  input?: CurrencyAmount;
  rate?: AppliedRate;
  source?: string;
  allocation_group?: number;
  carrying?: CarryingAllocation;
};
export type JournalLine = {
  id: string;
  account_id: string;
  side: "debit" | "credit";
  amount_minor: string;
  memo?: string;
  reverses_line_id?: string;
  native?: NativeMovement;
  contexts?: ContextMeasurement[];
  valuation?: LineValuation;
};
export type MeasurementPolicy = {
  version: number;
  native_transfers?: { lines: number[] }[];
  allocations?: { total: CurrencyAmount; method: string; parts: { line: number; weight: string }[] }[];
};
export type JournalEntry = {
  id: string;
  sequence: number;
  receipt_id: string;
  actor_id: string;
  economic_date: string;
  accounting_date: string;
  description: string;
  reversal_of?: string;
  recorded_at: string;
  lines: JournalLine[];
  measurement_policy?: MeasurementPolicy;
};
export type JournalRecorder =
  | { kind: "owner"; name: string }
  | { kind: "agent"; name: string; connection_id: string };

/** The complete immutable entry and its owner-scoped read metadata. */
export type JournalRead = {
  schema_version?: 1;
  version: 1;
  book_id: string;
  currency: string;
  scale: number;
  state_revision?: string;
  state_captured_at?: string;
  state_knowledge_cutoff?: string;
  recorded_by?: JournalRecorder;
  entry: JournalEntry;
};

const minor = /^(0|[1-9]\d{0,18})$/;
const signed = /^(0|-?[1-9]\d{0,18})$/;
const day = /^\d{4}-(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01])$/;
const id = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const role = /^[a-z][a-z0-9_]{0,63}$/;
const rate = /^(0|[1-9]\d{0,63})(\.\d{1,64})?$/;

type Value = Record<string, unknown>;
const object = (v: unknown): v is Value => !!v && typeof v === "object" && !Array.isArray(v);
const text = (v: unknown, max = 4096) => typeof v === "string" && v.length <= max;
const optional = (v: unknown, check: (v: unknown) => boolean) => v === undefined || check(v);
const count = (v: unknown) => Number.isSafeInteger(v) && (v as number) >= 0;
const only = (v: Value, keys: string[]) => Object.keys(v).every((k) => keys.includes(k));

function amount(v: unknown, sign = false): v is CurrencyAmount {
  return object(v) && only(v, ["currency", "scale", "amount_minor", "unit_definition"]) &&
    text(v.currency, 64) && !!v.currency &&
    (v.scale === null || (Number.isSafeInteger(v.scale) && (v.scale as number) >= 0 && (v.scale as number) <= 18)) &&
    typeof v.amount_minor === "string" && (sign ? signed : minor).test(v.amount_minor) &&
    optional(v.unit_definition, (u) => text(u));
}
function appliedRate(v: unknown) {
  return object(v) && only(v, ["base", "quote", "rate", "requested_date", "observation_date", "provenance"]) &&
    text(v.base, 64) && text(v.quote, 64) && typeof v.rate === "string" && rate.test(v.rate) &&
    typeof v.requested_date === "string" && (v.requested_date === "" || day.test(v.requested_date)) &&
    typeof v.observation_date === "string" && (v.observation_date === "" || day.test(v.observation_date)) &&
    text(v.provenance);
}
function carrying(v: unknown) {
  return object(v) && text(v.policy, 64) && typeof v.account_id === "string" && id.test(v.account_id) &&
    count(v.account_revision) && count(v.role_revision) && count(v.before_sequence) &&
    amount(v.before_native, true) && amount(v.before_book, true) && amount(v.consumed_native, true) && amount(v.consumed_book, true);
}
function valuation(v: unknown) {
  return object(v) && only(v, ["basis", "policy", "input", "rate", "source", "allocation_group", "carrying"]) &&
    text(v.basis, 64) && text(v.policy, 64) && optional(v.input, (i) => amount(i)) &&
    optional(v.rate, appliedRate) && optional(v.source, (s) => text(s)) &&
    optional(v.allocation_group, count) && optional(v.carrying, carrying);
}
function line(v: unknown): v is JournalLine {
  return object(v) && only(v, ["id", "account_id", "side", "amount_minor", "memo", "reverses_line_id", "native", "contexts", "valuation"]) &&
    typeof v.id === "string" && id.test(v.id) && typeof v.account_id === "string" && id.test(v.account_id) &&
    (v.side === "debit" || v.side === "credit") && typeof v.amount_minor === "string" && minor.test(v.amount_minor) &&
    optional(v.memo, (m) => text(m)) &&
    optional(v.reverses_line_id, (r) => typeof r === "string" && id.test(r)) &&
    optional(v.native, (n) => object(n) && only(n, ["currency", "scale", "amount_minor", "account_revision", "unit_definition"]) &&
      amount({ currency: n.currency, scale: n.scale, amount_minor: n.amount_minor, ...(n.unit_definition !== undefined ? { unit_definition: n.unit_definition } : {}) }, true) &&
      optional(n.account_revision, count)) &&
    optional(v.contexts, (c) => Array.isArray(c) && c.length <= 64 && c.every((x) =>
      object(x) && only(x, ["role", "amount", "source"]) && typeof x.role === "string" && role.test(x.role) &&
      amount(x.amount, true) && optional(x.source, (s) => text(s)))) &&
    optional(v.valuation, valuation);
}
function policy(v: unknown, lines: number) {
  const index = (n: unknown) => Number.isSafeInteger(n) && (n as number) >= 0 && (n as number) < lines;
  return object(v) && only(v, ["version", "native_transfers", "allocations"]) && v.version === 1 &&
    optional(v.native_transfers, (t) => Array.isArray(t) && t.every((x) => object(x) && Array.isArray(x.lines) && x.lines.every(index))) &&
    optional(v.allocations, (a) => Array.isArray(a) && a.every((x) =>
      object(x) && amount(x.total) && text(x.method, 64) && Array.isArray(x.parts) &&
      x.parts.every((p) => object(p) && index(p.line) && typeof p.weight === "string" && rate.test(p.weight))));
}

const recordedInstant = (v: unknown) =>
  typeof v === "string" &&
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(v) &&
  Number.isFinite(Date.parse(v));

function recorder(v: unknown): v is JournalRecorder {
  if (!object(v) || typeof v.name !== "string" || !v.name.trim() || [...v.name].length > 80)
    return false;
  if (v.kind === "owner") return only(v, ["kind", "name"]);
  return v.kind === "agent" && only(v, ["kind", "name", "connection_id"]) &&
    typeof v.connection_id === "string" && id.test(v.connection_id);
}

/** Accepts only a complete, balanced entry of this Book. Anything else fails
 * the read: a partial entry is never shown as if it were the whole record. */
export function parseJournalRead(data: unknown, book: Pick<Book, "id" | "currency" | "scale">, entryId: string): JournalRead {
  const fail = (why: string): never => { throw new Error(`This entry could not be read: ${why}.`); };
  if (!object(data) ||
      !only(data, ["schema_version", "version", "book_id", "currency", "scale", "entry",
        "state_revision", "state_captured_at", "state_knowledge_cutoff", "recorded_by"]) ||
      data.version !== 1 || !optional(data.schema_version, (v) => v === 1)) fail("unexpected response");
  const d = data as Value;
  if (!optional(d.state_revision, (v) => typeof v === "string" && /^[a-f0-9]{64}$/.test(v)) ||
      !optional(d.state_captured_at, recordedInstant) || !optional(d.state_knowledge_cutoff, recordedInstant))
    fail("its recorded state is incomplete");
  if (!optional(d.recorded_by, recorder)) fail("its recorder could not be confirmed");
  if (d.book_id !== book.id || d.currency !== book.currency || d.scale !== book.scale) fail("it belongs to another Book or currency");
  const e = d.entry;
  if (!object(e) || e.id !== entryId) fail("a different entry was returned");
  const entry = e as Value;
  if (!only(entry, ["id", "sequence", "receipt_id", "actor_id", "economic_date", "accounting_date", "description", "reversal_of", "recorded_at", "lines", "measurement_policy"]) ||
      !count(entry.sequence) || !text(entry.receipt_id, 128) || !text(entry.actor_id, 128) ||
      typeof entry.economic_date !== "string" || !day.test(entry.economic_date) ||
      typeof entry.accounting_date !== "string" || !day.test(entry.accounting_date) ||
      !text(entry.description) || typeof entry.recorded_at !== "string" || Number.isNaN(Date.parse(entry.recorded_at)) ||
      !optional(entry.reversal_of, (r) => typeof r === "string" && id.test(r)))
    fail("its header is incomplete");
  const lines = entry.lines;
  if (!Array.isArray(lines) || lines.length < 2 || lines.length > 10000 || !lines.every(line)) fail("its postings are incomplete");
  const list = lines as JournalLine[];
  if (new Set(list.map((l) => l.id)).size !== list.length) fail("a posting appears twice");
  const net = list.reduce((sum, l) => sum + (l.side === "debit" ? 1n : -1n) * BigInt(l.amount_minor), 0n);
  if (net !== 0n) fail("debits and credits do not balance");
  if (!optional(entry.measurement_policy, (p) => policy(p, list.length))) fail("its measurement policy is incomplete");
  return data as JournalRead;
}

/** The transaction read answers in one of two shapes. The complete ledger
 * entry is kept whole; it is never squeezed into the flat transaction form. */
export function isJournalRead(data: unknown): boolean {
  return object(data) && data.version === 1 && object(data.entry);
}
