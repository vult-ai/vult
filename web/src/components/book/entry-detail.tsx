import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { ArrowUpRight, ChevronDown } from "../../design-system/icons";
import { formatDate, formatDateTime } from "../../lib/date-format";
import { context, bookURL, get, money,rateDisplay, currencyMoney, type Book, type Currency, type Detail, type State, type Transaction } from "../../lib/api";
import { useBookContext } from "../../lib/use-book-context";
import { RecordDate } from "../ui/record-date";
import { Button } from "../ui/button";
import { useDialogPending } from "../ui/dialog";
import { ErrorMessage, message } from "../form";
import { EntryUserNote } from "./entry-user-note";
import { ImportProvenanceDetails } from "./import-provenance";
import { EntryDocuments } from "./entry-document";
import { EntryOverview } from "./entry-overview";
import { EntryOrigin } from "./entry-identity";
import { EntryAccountingDetails } from "./entry-accounting-details";
import { LoadingState } from "../ui/loading-state";
import { FailureState } from "../ui/failure-state";
import { isJournalRead, parseJournalRead, type JournalRead } from "../../lib/journal-read";
import { LedgerEntry } from "./ledger-entry";

export default function EntryDetail({
  id,
  book,
  currencies,
  state,
  onSaved,
  onNavigate,
  onPendingChange,
  onEditingChange,
}: {
  id: string;
  book: Book;
  currencies: Currency[];
  state: State;
  onSaved: () => void;
  onNavigate: (id: string) => void;
  onPendingChange?: (pending: boolean) => void;
  onEditingChange?: (editing: boolean) => void;
}) {
  const [data, setData] = useState<Detail | null>(null);
  const [journal, setJournal] = useState<JournalRead | null>(null);
  const { context: accountContext, failure: contextFailure, retry: retryContext } = useBookContext(book, state, data !== null);
  const detailState = state.bootstrap ? accountContext : state;
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useDialogPending(busy);
  useLayoutEffect(() => {
    onPendingChange?.(busy);
    return () => onPendingChange?.(false);
  }, [busy, onPendingChange]);
  const [reload, setReload] = useState(0);
  useEffect(() => {
    let current = true;
    if (!reload) {
      setData(null);
      setJournal(null);
    }
    setError("");
    get<unknown>(bookURL(`transactions/${id}`, book.id))
      .then((value) => {
        if (!current) return;
        if (isJournalRead(value)) setJournal(parseJournalRead(value, book, id));
        else setData(value as Detail);
      })
      .catch((e) => {
        if (current) setError(message(e));
      });
    return () => {
      current = false;
    };
  }, [id, book.id, reload]);
  if (journal)
    return (
      <div className="dialog-form">
        <LedgerEntry read={journal} state={state} book={book} onNavigate={onNavigate} onChanged={() => setReload((value) => value + 1)} />
      </div>
    );
  if (!data)
    return (
      <div className="dialog-form">
        {error ? (
          <FailureState scope="region" error={error} title="Couldn’t load this entry" />
        ) : (
          <LoadingState scope="inline" label="Loading entry…" />
        )}
      </div>
    );
  if (!detailState)
    return contextFailure
      ? <FailureState scope="region" error={contextFailure} intent="read" resource="accounts" onRetry={retryContext} />
      : <LoadingState scope="region" label="Loading account details…" />;
  return (
    <div className="dialog-form">
      <EntryOverview
        data={data}
        state={detailState}
        book={book}
        currencies={currencies}
      />
      <EntryUserNote entry={data} />
      <EntryAccountingDetails
        data={data}
        accounts={detailState.accounts}
        book={book}
        currencies={currencies}
      />
      {/* Where the record comes from: its sources, then its origin. */}
      <div className="entry-provenance">
        <EntryDocuments
          key={`${book.id}:${data.id}`}
          entryId={data.id}
          bookId={book.id}
          receipt={data.receipt}
          imported={!!data.import}
        />
        {(data.import || data.import_provenance) && (
          <EntryOrigin
            provider={data.import_provenance?.provider || undefined}
            importedAt={detailState.source_import?.captured_at}
          />
        )}
      </div>
      {(data.reversal_of || data.reversed_by || data.replacement_of) && (
        <div className="notice history">
          {data.reversal_of && (
            <Button
              color="tertiary"
              size="sm"
              onClick={() => onNavigate(data.reversal_of)}
            >
              View original entry <ArrowUpRight className="size-4" />
            </Button>
          )}
          {data.reversed_by && (
            <Button
              color="tertiary"
              size="sm"
              onClick={() => onNavigate(data.reversed_by)}
            >
              View reversing entry <ArrowUpRight className="size-4" />
            </Button>
          )}
          {data.replacement_of && (
            <Button
              color="tertiary"
              size="sm"
              onClick={() => onNavigate(data.replacement_of)}
            >
              View entry replaced <ArrowUpRight className="size-4" />
            </Button>
          )}
        </div>
      )}
      <details className="entry-detail-section">
        <summary className="vult-type-ui text-secondary vult-focus-target vult-focus-contained">
          Record details
          <ChevronDown
            className="entry-disclosure-chevron"
            aria-hidden="true"
          />
        </summary>
        <dl className="details">
          <dt>Description</dt>
          <dd>{data.description}</dd>
          {data.fx.refund_of && (
            <>
              <dt>Refund of purchase</dt>
              <dd>
                <Button
                  color="tertiary"
                  onClick={() => onNavigate(data.fx.refund_of!)}
                >
                  View original purchase
                </Button>
              </dd>
            </>
          )}
          <dt>Economic date</dt>
          <dd><RecordDate value={data.date} format="full" /></dd>
          <dt>Posting date</dt>
          <dd><RecordDate value={data.posting_date || data.date} format="full" /></dd>
          {data.fiscal_year_start && (
            <>
              <dt>Fiscal year</dt>
              <dd>
                <RecordDate value={data.fiscal_year_start} format="full" /> to <RecordDate value={data.fiscal_year_end} format="full" />
              </dd>
            </>
          )}
          <dt>Recorded at (UTC)</dt>
          <dd title={data.recorded_at}>{formatDateTime(data.recorded_at, "full")}</dd>
          <dt>Payer / purpose</dt>
          <dd>{context(data)}</dd>
          {data.counterparty && (
            <>
              <dt>Counterparty</dt>
              <dd>{data.counterparty}</dd>
            </>
          )}
          {data.note && (
            <>
              <dt>Legacy audit text</dt>
              <dd className="preserve-space">{data.note}</dd>
            </>
          )}
          {data.kind !== "journal" && (
            <>
              <dt>Original amount</dt>
              <dd>
                {currencyMoney(
                  data.fx.original_amount_minor,
                  data.fx.original_currency,
                  currencies,
                )}
              </dd>
              <dt>Paid / received</dt>
              <dd>
                {currencyMoney(
                  data.fx.paid_amount_minor,
                  data.fx.paid_currency,
                  currencies,
                )}
              </dd>
              {data.fx.actual_rate && (
                <>
                  <dt>Actual conversion</dt>
                  <dd>
                    {rateDisplay(data.fx.actual_rate)}{" "}
                    {data.fx.actual_quote_currency} per{" "}
                    {data.fx.original_currency}
                    <small>
                      Exact ratio: {data.fx.actual_rate}; actual recorded
                      amounts take precedence.
                    </small>
                  </dd>
                </>
              )}
              <dt>Valuation</dt>
              <dd>
                {data.fx.selection === "actual"
                  ? "Actual amount"
                  : data.fx.selection === "manual"
                    ? "Manual"
                    : data.fx.selection === "same-currency"
                      ? "Same currency · exact 1"
                      : "Reference estimate"}
              </dd>
              {data.fx.quotes?.map((q, i) => (
                <div className="quote-detail" key={q.id || i}>
                  <dt>
                    {q.base} → {q.quote}
                  </dt>
                  <dd>
                    {q.rate} ·{" "}
                    {q.source === "manual"
                      ? "Manual"
                      : q.source === "same-currency"
                        ? "Exact 1"
                        : `${formatDate(q.observation_date, "full")} reference`}{" "}
                    · economic date {q.requested_date}
                  </dd>
                </div>
              ))}
              {data.fx.destination_currency && (
                <>
                  <dt>Transferred to destination</dt>
                  <dd>
                    {currencyMoney(
                      data.fx.destination_amount_minor!,
                      data.fx.destination_currency,
                      currencies,
                    )}
                  </dd>
                </>
              )}
              {data.fx.settlement_currency && (
                <>
                  <dt>Debt covered by this payment</dt>
                  <dd>
                    {currencyMoney(
                      data.fx.settlement_amount_minor!,
                      data.fx.settlement_currency,
                      currencies,
                    )}
                  </dd>
                </>
              )}
            </>
          )}
          {data.import && (
            <>
              <dt>Source reference</dt>
              <dd>{data.import.document_number || data.import.source_key}</dd>
              <dt>Source journal</dt>
              <dd>
                {data.import.import_id} · row {data.import.source_row}
              </dd>
              {data.import.review?.note && (
                <>
                  <dt>Legacy review audit text</dt>
                  <dd className="preserve-space">{data.import.review.note}</dd>
                </>
              )}
            </>
          )}
          <dt>Entry ID</dt>
          <dd className="mono small">{data.id}</dd>
        </dl>
        <ImportProvenanceDetails value={data.import_provenance} />
      </details>
      {data.allocations.length > 0 && (
        <>
          <h3>Related payments</h3>
          <div className="allocation-list">
            {data.allocations.map((a) => (
              <div key={a.id}>
                <Button
                  color="tertiary"
                  size="sm"
                  onClick={() =>
                    onNavigate(a.source_id === id ? a.payment_id : a.source_id)
                  }
                >
                  {a.description}
                  <ArrowUpRight className="size-4" />
                </Button>
                <span className="number">
                  {a.effect === -1 ? "Restored " : "Allocated "}
                  {currencyMoney(
                    a.native_amount_minor,
                    a.native_currency,
                    currencies,
                  )}
                  <small>{money(a.amount_minor, book)} in this Book</small>
                </span>
              </div>
            ))}
          </div>
        </>
      )}
      <ErrorMessage message={error} />
    </div>
  );
}

