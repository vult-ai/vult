import { ArrowUpRight, ChevronDown } from "../../design-system/icons";
import { EntryPayee } from "./entry-payee";
import { formatDateTime } from "../../lib/date-format";
import { money, rateDisplay, type Book, type State } from "../../lib/api";
import type { CurrencyAmount, JournalLine, JournalRead } from "../../lib/journal-read";
import { RecordDate } from "../ui/record-date";
import { Button } from "../ui/button";
import { EntryDocuments } from "./entry-document";
import { EntryHero, EntryKindAvatar, EntryPostings, EntryRecordedBy, useAccountLook } from "./entry-identity";

/** A measured amount at its own recorded scale. A unit that is not a
 * currency is shown as its number and unit, never forced into one. */
export function measured(value: CurrencyAmount) {
  if (value.scale === null) return `${value.amount_minor} ${value.currency}`;
  try {
    return money(value.amount_minor, { currency: value.currency, scale: value.scale });
  } catch {
    const negative = value.amount_minor.startsWith("-");
    const digits = (negative ? value.amount_minor.slice(1) : value.amount_minor).padStart(value.scale + 1, "0");
    const whole = value.scale ? `${digits.slice(0, -value.scale)}.${digits.slice(-value.scale)}` : digits;
    return `${negative ? "−" : ""}${whole} ${value.currency}`;
  }
}

const words = (code: string) => code.replace(/[_-]+/g, " ").replace(/^./, (c) => c.toUpperCase());
// Plain names for the measurements agents record most often.
const contextNames: Record<string, string> = { transaction_amount: "Charged", invoice_amount: "Invoiced" };
const basisNames: Record<string, string> = {
  manual: "Entered amount",
  actual_source: "Actual amount",
  applied_quote: "Converted",
  historical_carrying: "Carrying value",
  authoritative_allocation: "Allocated",
};

/** An entered amount that only repeats the line's own value says nothing new. */
function restatesLine(v: NonNullable<JournalLine["valuation"]>, line: JournalLine, book: Book) {
  return v.basis === "manual" && !v.source && (!v.input || (v.input.currency === book.currency && v.input.amount_minor === line.amount_minor));
}

function Measurements({ line, book }: { line: JournalLine; book: Book }) {
  const native = line.native;
  const v = line.valuation;
  const showNative = native && (native.currency !== book.currency || native.amount_minor !== line.amount_minor);
  return (
    <>
      {line.memo && <small className="preserve-space">{line.memo}</small>}
      {showNative && (
        <small>
          <span className="vult-money">{measured(native)}</span> native
        </small>
      )}
      {line.contexts?.map((c, i) => (
        <small key={`${c.role}:${i}`}>
          {contextNames[c.role] ?? words(c.role)} <span className="vult-money">{measured(c.amount)}</span>
          {c.source && ` · ${c.source}`}
        </small>
      ))}
      {v && !restatesLine(v, line, book) && (
        <small>
          {basisNames[v.basis] ?? words(v.basis)}
          {v.input && !(v.input.currency === book.currency && v.input.amount_minor === line.amount_minor) && <> from <span className="vult-money">{measured(v.input)}</span></>}
          {v.rate && <> at {rateDisplay(v.rate.rate)} {v.rate.quote} per {v.rate.base}{v.rate.observation_date && <>, observed <RecordDate value={v.rate.observation_date} format="full" /></>}</>}
          {v.carrying && <> · carrying value of <span className="vult-money">{measured(v.carrying.consumed_native)}</span> = <span className="vult-money">{measured(v.carrying.consumed_book)}</span></>}
          {v.source && ` · ${v.source}`}
        </small>
      )}
    </>
  );
}

/** The complete recorded entry: every posting, its separate measurements,
 * all three dates and the exact entry it reverses. Nothing is summarized away. */
export function LedgerEntry({
  read,
  state,
  book,
  onNavigate,
  onChanged,
}: {
  read: JournalRead;
  state: State;
  book: Book;
  onNavigate: (id: string) => void;
  /** Reloads the entry after a change to a separate decision, such as its payee. */
  onChanged?: () => void;
}) {
  const entry = read.entry;
  const chart = new Map(state.accounts.map((account) => [account.code, account]));
  const total = entry.lines.reduce((sum, l) => (l.side === "debit" ? sum + BigInt(l.amount_minor) : sum), 0n).toString();
  const policy = entry.measurement_policy;
  const look = useAccountLook(book.id);
  const postings = entry.lines.map((line) => {
    const account = chart.get(line.account_id);
    return {
      key: line.id,
      code: line.account_id,
      name: account?.name || "Account unavailable",
      glCode: account?.gl_code?.trim() || undefined,
      side: line.side,
      amount: money(line.amount_minor, read),
      extra: <Measurements line={line} book={book} />,
    };
  });
  // The first posting account the owner gave a logo; otherwise the kind of entry.
  const tone = entry.reversal_of ? "reversal" : "neutral";
  const branded = postings.map((p) => ({ p, look: look(p.code, p.name) })).find(({ look }) => look.institution);
  return (
    <>
      <section className="entry-overview" aria-label="Entry summary">
        <EntryHero
          avatar={branded ? branded.look.avatar : <EntryKindAvatar tone={tone} />}
          title={entry.description || (entry.reversal_of ? "Reversal" : "Journal entry")}
          date={entry.economic_date}
          amount={money(total, read)}
          chip={entry.reversal_of ? "Reversal" : "Journal entry"}
          tone={tone}
        />
      </section>
      {read.payee && <EntryPayee book={book} read={read.payee} onChanged={() => onChanged?.()} />}
      <EntryPostings bookId={book.id} postings={postings} />
      <div className="entry-provenance">
        <EntryDocuments key={`${book.id}:${entry.id}`} entryId={entry.id} bookId={book.id} receipt={null} imported={false} />
        {read.recorded_by && <EntryRecordedBy recorder={read.recorded_by} />}
      </div>
      {entry.reversal_of && (
        <div className="notice history">
          <Button color="tertiary" size="sm" onClick={() => onNavigate(entry.reversal_of!)}>
            View original entry <ArrowUpRight className="size-4" />
          </Button>
        </div>
      )}
      <details className="entry-detail-section">
        <summary className="vult-type-ui text-secondary vult-focus-target vult-focus-contained">
          Record details
          <ChevronDown className="entry-disclosure-chevron" aria-hidden="true" />
        </summary>
        <dl className="details">
          <dt>Economic date</dt>
          <dd><RecordDate value={entry.economic_date} format="full" /></dd>
          <dt>Accounting date</dt>
          <dd><RecordDate value={entry.accounting_date} format="full" /></dd>
          <dt>Recorded at (UTC)</dt>
          <dd title={entry.recorded_at}>{formatDateTime(entry.recorded_at, "full")}</dd>
          <dt>Entry number</dt>
          <dd>{entry.sequence}</dd>
          {policy?.native_transfers?.map((t, i) => (
            <div key={`transfer-${i}`} className="quote-detail">
              <dt>Native transfer</dt>
              <dd>Postings {t.lines.map((n) => n + 1).join(" and ")} move the same native amount</dd>
            </div>
          ))}
          {policy?.allocations?.map((a, i) => (
            <div key={`allocation-${i}`} className="quote-detail">
              <dt>Allocated total</dt>
              <dd>
                <span className="vult-money">{measured(a.total)}</span> across postings{" "}
                {a.parts.map((p) => `${p.line + 1} (${p.weight})`).join(", ")}
              </dd>
            </div>
          ))}
          <dt>Entry ID</dt>
          <dd className="vult-code">{entry.id}</dd>
        </dl>
      </details>
    </>
  );
}
