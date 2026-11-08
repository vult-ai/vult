import { useRef, useState } from "react";
import {
  captureInspectorScroll,
  restoreInspectorScroll,
  type InspectorScroll,
} from "../../design-system/collection-inspector";
import { money, type Book } from "../../lib/api";
import { useAccountEntryInspection } from "./accounts-entry-inspector";
import { formatDate, formatDateRange } from "../../lib/date-format";
import type {
  AccountLedger,
  AccountLedgerLine,
} from "../../lib/account-ledger";
import { CollectionTable } from "../../design-system/collection-table";
import { NameLink } from "../ui/name-link";
import { RecordDate } from "../ui/record-date";
import "./account-ledger.css";
import { EmptyState } from "../ui/empty-state";

const columns = [
  {
    id: "date",
    label: "Date",
    kind: "date" as const,
  },
  {
    id: "entry",
    label: "Entry & evidence",
    kind: "text" as const,
    width: 220,
    isRowHeader: true,
  },
  { id: "debit", label: "Debit", kind: "money" as const },
  { id: "credit", label: "Credit", kind: "money" as const },
  {
    id: "balance",
    label: "Balance",
    kind: "money" as const,
  },
];
export function AccountLedgerView({
  ledger,
  book,
  name,
  onInspect,
  periodTotalShown = false,
  compact = false,
}: {
  ledger: AccountLedger;
  book: Book;
  name: string;
  onInspect?: (id: string, trigger: HTMLElement) => void;
  periodTotalShown?: boolean;
  compact?: boolean;
}) {
  const entryInspection = useAccountEntryInspection();
  const [selectedLine, setSelectedLine] = useState<AccountLedgerLine | null>(
    null,
  );
  const scrollBeforeFocus = useRef<InspectorScroll | null>(null);
  const captureScroll = (target: EventTarget) => {
    scrollBeforeFocus.current =
      target instanceof HTMLElement ? captureInspectorScroll(target) : null;
  };
  const inspect = (line: AccountLedgerLine, trigger: HTMLElement) => {
    // Grid focus and the new inspector column can both scroll the ledger.
    // Capture before focus and repair its queued layout/focus passes as well.
    restoreInspectorScroll(
      scrollBeforeFocus.current || captureInspectorScroll(trigger),
    );
    scrollBeforeFocus.current = null;
    setSelectedLine(line);
    onInspect?.(line.transactionId, trigger);
  };
  return (
    <section
      className="account-ledger"
      data-vult-context-inspector
      aria-label={`${name} ledger`}
      onPointerDownCapture={(event) => captureScroll(event.target)}
      onKeyDownCapture={(event) => {
        if (event.key === "Enter" || event.key === " ")
          captureScroll(event.target);
      }}
    >
      <div className={compact ? "sr-only" : "account-ledger-heading"}>
        <h3 className="account-workspace-activity-heading">Account ledger</h3>
        <p className="account-detail-identity">
          Accounting dates ·{" "}
          {formatDateRange(ledger.range.from, ledger.range.to)} ·{" "}
          {ledger.positiveSide === "credit" ? "Credit" : "Debit"} balances
          positive
        </p>
      </div>
      {periodTotalShown && (
        <p className="account-detail-identity">
          Opening and closing include earlier recorded postings. The amount
          above is this period’s total.
        </p>
      )}
      <dl
        className={`account-ledger-summary ${compact ? "account-ledger-summary-inline" : ""}`}
      >
        {(
          [
            // Balances are what the recorded postings add up to on each date, not a
            // claim that the history before the Book is complete.
            [`Balance before ${formatDate(ledger.range.from, "full")}`, ledger.opening],
            ["Debits", ledger.debits],
            ["Credits", ledger.credits],
            [`Balance on ${formatDate(ledger.range.to, "full")}`, ledger.closing],
          ] as const
        ).map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd
              className={
                compact && !periodTotalShown && label.startsWith("Balance on")
                  ? "account-group-activity-total"
                  : undefined
              }
            >
              {money(value.toString(), book)}
            </dd>
          </div>
        ))}
      </dl>
      <div className="account-group-activity-table account-ledger-table">
        <CollectionTable<AccountLedgerLine>
          ariaLabel={`${name} ledger postings`}
          testId="account-ledger"
          frameTestId="account-ledger-frame"
          columns={columns}
          rows={ledger.lines}
          pageResetKey={`${book.id}:${ledger.accountCode}:${ledger.range.from}:${ledger.range.to}`}
          rowId={(line) => `ledger-line:${line.id}`}
          activeRowId={
            entryInspection?.id &&
            selectedLine?.transactionId === entryInspection.id
              ? `ledger-line:${selectedLine.id}`
              : null
          }
          rowLabel={(line) =>
            `View entry and source evidence for ${line.description} on ${formatDate(line.date, "full")}`
          }
          selectionEnabled={false}
          onRowAction={
            onInspect
              ? (line) => inspect(line, document.activeElement as HTMLElement)
              : undefined
          }
          renderCell={(line, column) =>
            column.id === "date" ? (
              <div className="account-ledger-date">
                <RecordDate value={line.date} />
                {line.postingDate !== line.date && (
                  <small>
                    {line.postingDate ? (
                      <>
                        Posted <RecordDate value={line.postingDate} />
                      </>
                    ) : (
                      "Posting date unavailable"
                    )}
                  </small>
                )}
              </div>
            ) : column.id === "entry" ? (
              onInspect ? (
                <NameLink
                  name={line.description}
                  className="account-ledger-reference"
                  title={`${line.description}${line.memo ? ` · ${line.memo}` : ""}`}
                  aria-label={`View entry and source evidence: ${line.description}`}
                  onClick={(event) => inspect(line, event.currentTarget)}
                />
              ) : (
                <span title={line.memo || line.description}>
                  {line.description}
                </span>
              )
            ) : (
              <span className="account-ledger-money">
                {(() => {
                  const value = column.id === "debit" ? line.debit : column.id === "credit" ? line.credit : line.balance;
                  // An empty side reads as empty, not as a column of zeros.
                  return column.id !== "balance" && value === 0n ? "" : money(value.toString(), book);
                })()}
              </span>
            )
          }
          emptyState={
            <EmptyState
              scope="region"
              title="No postings in this period"
              detail="Opening and closing balances still include earlier recorded activity."
            />
          }
        />
      </div>
    </section>
  );
}
