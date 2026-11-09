import { NameLink } from "../ui/name-link";
import { ErrorMessage } from "../form";
import { useLayoutEffect, useMemo, type ReactNode } from "react";
import previewStyles from "../../design-system/catalog/components/application/record-preview/record-preview.module.css";
import { markStartupReady } from "../../lib/startup";
import { Paperclip } from "../../design-system/icons";
import { CollectionTable } from "../../design-system/collection-table";
import { transactionColumns, phoneTransactionColumns } from "./table-columns";
import { useBreakpoint } from "../../design-system/catalog/hooks/use-breakpoint";
import { transactionFacts } from "../../lib/transaction-journal";
import { GLCodeField } from "./gl-code-field";
import { useTransactionJournal } from "../../lib/use-transaction-journal";
import { Button } from "../ui/button";
import { ContactAvatar } from "../ui/contact-avatar";
import { counterpartyIdentity } from "../../lib/counterparty-identity";
import { activityBrand } from "../../lib/activity-brands";
import { useInstitutionCatalog } from "../../lib/use-institution-catalog";
import { RecordDate } from "../ui/record-date";
import "./transactions-table.css";
import {
  money,
  currencyMoney,
  type State,
  type Book,
  type Transaction,
} from "../../lib/api";
export default function TransactionsTable({
  state,
  book,
  detail,
  previewId,
  loading,
  resetKey,
  emptyState,
  onOpen,
  toolbar,
  notice,
  refreshKey = 0,
  active = true,
  pagination,
  pageStart = 0,
}: {
  state: State;
  book: Book;
  detail: string;
  previewId?: string;
  loading: boolean;
  resetKey: string;
  emptyState: ReactNode;
  onOpen: (id: string, source: HTMLElement | null) => void;
  toolbar?: ReactNode;
  notice?: ReactNode;
  refreshKey?: number;
  active?: boolean;
  pagination?: ReactNode;
  pageStart?: number;
}) {
  const wide = useBreakpoint("sm");
  const brands = new Map(state.transactions.map(row=>[row.id, row.brand]));
  const catalog = useInstitutionCatalog(
    active && state.transactions.length > 0,
    "brands",
  );
  useLayoutEffect(() => markStartupReady("table"), []);
  const journal = useTransactionJournal(
    book,
    refreshKey,
    active && !!state.account_balances?.length && state.transactions.length > 0,
    state.transactions.map((t) => t.id),
  );
  const facts = useMemo(() => {
    const present = transactionFacts(state, journal.journal);
    return new Map(state.transactions.map((t) => [t.id, present(t)]));
  }, [
    state.transactions,
    state.accounts,
    state.cash_accounts,
    state.account_balances,
    journal.journal,
  ]);
  const avatarOf = (t: Transaction) => {
    const party = counterpartyIdentity(t.counterparty);
    // With no counterparty recorded, the avatar follows the row's title (its
    // description) so it is never a blank disc. It decides the colour and
    // initials only; no brand or logo is looked up from a description.
    const title = t.description.trim();
    const shown = party.name ? party : { ...party, name: title, key: `description:${title.toLowerCase()}` };
    return (
            <ContactAvatar
              brand={brands.get(t.id)}
              pending={(!brands.get(t.id) || ["identified", "proposed"].includes(brands.get(t.id)!.state)) &&
                 catalog.status === "loading" && !catalog.institutions.length}
              identityKey={shown.key}
              name={shown.name}
              base={catalog.base}
              institution={activityBrand(brands.get(t.id), catalog.institutions) || (brands.get(t.id) ? undefined : catalog.institutions.find(
                (item) => item.id === party.institutionId,
              ))}
            />
    );
  };
  const renderParty = (t: Transaction, compact = false) => {
    const attachmentCount = t.attachment_count ?? 0;
    return (
      <span className="book-cell transaction-counterparty">
        <NameLink
          className="entry-link vult-type-label vult-focus-target vult-focus-contained"
          title={t.description}
          aria-label={`View ${t.description} details`}
          aria-controls={previewId}
          aria-describedby={
            attachmentCount > 0 ? `source-indicator-${t.id}` : undefined
          }
          aria-current={detail === t.id ? "true" : undefined}
          aria-expanded={detail === t.id}
          disabled={loading}
          onClick={(e) => {
            e.stopPropagation();
            onOpen(t.id, e.currentTarget);
          }}
          name={t.counterparty?.trim() || t.description}
          nameClassName="transaction-counterparty-name"
          leading={compact ? undefined : avatarOf(t)}
          trailing={
            attachmentCount > 0 && (
              <span
                id={`source-indicator-${t.id}`}
                className="transaction-attachments vult-type-metadata text-tertiary"
                title={`${attachmentCount} attached source${attachmentCount === 1 ? "" : "s"}`}
              >
                <Paperclip aria-hidden="true" className="size-3.5 shrink-0" />
                {attachmentCount > 1 && (
                  <span aria-hidden="true">{attachmentCount}</span>
                )}
                <span className="sr-only">
                  {attachmentCount} attached source
                  {attachmentCount === 1 ? "" : "s"}
                </span>
              </span>
            )
          }
        />
        {t.reversed_by && !compact && (
          <span className="badge reversed">Reversed</span>
        )}
      </span>
    );
  };
  const renderAmount = (t: Transaction, compact = false) => {
    const fact = facts.get(t.id)!;
    const recorded = money(t.amount_minor, book);
    const fx = t.fx;
    const original = t.original_amount && t.original_amount.currency !== book.currency
      ? money(t.original_amount.amount_minor, { currency: t.original_amount.currency, scale: t.original_amount.scale })
      : fx && (fx.original_currency !== book.currency || fx.original_amount_minor !== t.amount_minor)
        ? currencyMoney(fx.original_amount_minor, fx.original_currency, state.currencies)
        : null;
    const originalDetail = original === null ? "" : ` recorded; ${original} original`;
    // Money in carries a plus; money out, transfers and reversals none.
    const shown = fact.direction === "in" ? `+${recorded}` : recorded;
    return (
      <span
        className={`book-cell transaction-money${compact ? " transaction-money-compact" : ""}`}
        title={`${fact.directionLabel} · ${recorded}${originalDetail}`}
        aria-label={`${fact.directionLabel}: ${recorded}${originalDetail}`}
      >
        <span className="transaction-amount-primary">
          <span className="transaction-ledger" data-direction={fact.direction}>{shown}</span>
        </span>
        {original !== null && <small className="transaction-native">{original}</small>}
      </span>
    );
  };
  return (
    <CollectionTable
      ariaLabel="Transactions"
      className={`transactions-ledger ${wide ? "" : "transactions-ledger-compact"} ${previewStyles.activeTable} ${previewStyles.scrollableTable}`}
      testId="book-transactions"
      pagination={pagination}
      pageStart={pageStart}
      selectionEnabled
      isDisabled={loading}
      toolbar={toolbar}
      notice={
        <>
          {notice}
          <ErrorMessage
            message={journal.failure || journal.error}
            intent="read"
            resource="activity"
            pending={journal.pending}
            retained={journal.journal ? "view" : undefined}
            onRetry={journal.retry}
          />
        </>
      }
      columns={wide ? transactionColumns : phoneTransactionColumns}
      rows={state.transactions}
      rowId={(t) => t.id}
      rowLabel={(t) => t.description}
      pageResetKey={pagination ? `${resetKey}:${pageStart}` : resetKey}
      activeRowId={detail || undefined}
      onRowAction={(t) =>
        onOpen(
          t.id,
          document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null,
        )
      }
      emptyState={emptyState}
      renderCell={(t, column) => {
        const fact = facts.get(t.id)!;
        if (column.id === "date") {
          return (
            <RecordDate
              className="book-cell date-cell transaction-date"
              value={t.date}
            />
          );
        }
        if (column.id === "summary") {
          return (
            <span className="book-cell transaction-mobile-summary">
              <span className="transaction-mobile-avatar">{avatarOf(t)}</span>
              <span className="transaction-mobile-top">
                {renderParty(t, true)}
                {renderAmount(t, true)}
              </span>
              <span className="transaction-mobile-context vult-type-metadata text-tertiary">
                <RecordDate className="transaction-date" value={t.date} />
                {fact.category && (
                  <>
                    <span aria-hidden="true">·</span>
                    <span
                      className="transaction-mobile-category"
                      title={fact.categoryTitle}
                    >
                      <GLCodeField
                        book={book.id}
                        account={fact.categoryAccount}
                        value={fact.category}
                        title={fact.categoryTitle}
                        disabled={loading}
                      />
                    </span>
                  </>
                )}
                {t.reversed_by && (
                  <span className="badge reversed">Reversed</span>
                )}
              </span>
            </span>
          );
        }
        if (column.id === "counterparty") return renderParty(t);
        if (column.id === "category") {
          return (
            <span
              className="book-cell transaction-category"
              title={fact.categoryTitle}
            >
              <GLCodeField
                        book={book.id}
                        account={fact.categoryAccount}
                        value={fact.category}
                        title={fact.categoryTitle}
                        disabled={loading}
                      />
            </span>
          );
        }
        if (column.id === "amount") return renderAmount(t);
        return null;
      }}
    />
  );
}
