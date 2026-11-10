import { AccountCodeEditor } from "./account-code-editor";
import { AccountChangeDialogs, type AccountChangeMode } from "./account-status-dialogs";
import { Dropdown } from "../../design-system/catalog/components/vult-ui/dropdown";
import { Button as MenuButton } from "../../design-system/catalog/components/vult-ui/button";
import { useEffect, useRef, useState } from "react";
import { DotsHorizontal, InfoCircle, XClose } from "../../design-system/icons";
import { money, type Book, type State } from "../../lib/api";
import {
  accountBasis,
  nodeWeeklyActivity,
  type AccountNode,
  type HierarchyAccount,
} from "../../lib/account-hierarchy";
import { creditKind } from "../../lib/v0-ledger";
import type { DashboardRange } from "../../lib/dashboard-period";
import { formatDate, formatDateRange } from "../../lib/date-format";
import { CollectionTable } from "../../design-system/collection-table";
import { useAccountEntryInspection } from "./accounts-entry-inspector";
import { Dialog } from "../ui/dialog";
import { RecordDate } from "../ui/record-date";
import { ErrorMessage } from "../form";
import { Sparkline } from "./account-activity-sparkline";
import {
  type AccountActivity,
} from "../../lib/account-hierarchy-activity";
import { Button } from "../ui/button";
import {
  Tooltip,
  TooltipTrigger,
} from "../../design-system/catalog/components/base/tooltip/tooltip";
import type { AccountLedger } from "../../lib/account-ledger";
import { readAccountDetail, type AccountDetailPage } from "../../lib/account-detail-read";
import { APIError } from "../../lib/errors";
import { AccountLedgerView } from "./account-ledger";
import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";

type Entry = { id: string; date: string; accounts: string; amount: bigint };
const columns = [
  { id: "date", label: "Date", kind: "date" as const },
  {
    id: "accounts",
    label: "Accounts",
    kind: "text" as const,
    width: 280,
    isRowHeader: true,
  },
  { id: "amount", label: "Net change", kind: "money" as const },
];

export function AccountGroupActivity({
  book,
  state,
  accounts,
  node,
  name,
  range,
  amount,
  returnTo,
  onClose,
  onInspect,
  filtered = false,
  onRefresh,
  onShowLedger,
  onCustomize,
  embedded = false,
  revision = 0,
}: {
  book: Book;
  state: State;
  accounts: readonly HierarchyAccount[];
  node: AccountNode;
  name: string;
  range: DashboardRange;
  amount: bigint | null;
  returnTo: HTMLElement;
  onClose: () => void;
  onInspect?: (id: string, trigger: HTMLElement) => void;
  filtered?: boolean;
  onRefresh?: () => void;
  onShowLedger?: (range: DashboardRange) => void;
  onCustomize?: () => void;
  embedded?: boolean;
  revision?: string | number;
}) {
  const entryInspection = useAccountEntryInspection();
  const [codeOpen, setCodeOpen] = useState(false);
  const [changeMode, setChangeMode] = useState<AccountChangeMode | null>(null);
  const toolsTrigger = useRef<HTMLButtonElement>(null);
  const [showContextHelp, setShowContextHelp] = useState(false);
  useEffect(() => setShowContextHelp(false), [node.key]);
  const [entries, setEntries] = useState<Entry[] | null>(null);
  const [activity, setActivity] = useState<AccountActivity | null>(null);
  const [ledger, setLedger] = useState<AccountLedger | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [retry, setRetry] = useState(0);
  const period = accountBasis(node.kind) === "period";
  const account =
    node.codes.length === 1 && node.own.length === 1 && !node.children.length
      ? accounts.find((account) => account.code === node.codes[0])
      : undefined;
  const codes = node.codes.join("\0");
  const history =
    activity?.bookId === book.id && activity.asOf === range.to
      ? nodeWeeklyActivity(node, activity)
      : null;
  const [page, setPage] = useState<AccountDetailPage | null>(null);
  const [morePending, setMorePending] = useState(false);
  const readController = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    readController.current = controller;
    setEntries(null); setActivity(null); setLedger(null); setPage(null); setError(null); setMorePending(false);
    if (amount === null) return () => controller.abort();
    readAccountDetail(book,node.codes,range,period ? "period" : "balance",amount,controller.signal,account ? "ledger" : "group")
      .then(result => {
        if (controller.signal.aborted) return;
        setPage(result); setEntries(result.entries); setActivity(result.activity); setLedger(result.ledger);
      })
      .catch(reason => {if (!controller.signal.aborted) setError(reason);});
    return () => controller.abort();
  }, [book.id, revision, codes, range.from, range.to, amount, retry]);
  const loadedRows = ledger?.lines.length || entries?.length || 0;
  const loadMore = async () => {
    const controller=readController.current;
    if(!page||amount===null||!controller||controller.signal.aborted||morePending)return;
    setMorePending(true); setError(null);
    try {
      const next=await readAccountDetail(book,node.codes,range,period ? "period" : "balance",amount,controller.signal,account ? "ledger" : "group",{offset:loadedRows,revision:page.revision,balance:ledger?.lines.at(-1)?.balance});
      if(controller.signal.aborted)return;
      if(next.ledger){
        const ids=new Set(ledger?.lines.map(line=>line.id));
        if(next.ledger.lines.some(line=>ids.has(line.id)))throw new Error("The account activity could not be confirmed.");
        setLedger({...next.ledger,lines:[...(ledger?.lines||[]),...next.ledger.lines]});
      } else {
        const ids=new Set(entries?.map(entry=>entry.id));
        if(next.entries.some(entry=>ids.has(entry.id)))throw new Error("The account activity could not be confirmed.");
        setEntries([...(entries||[]),...next.entries]);
      }
    }catch(reason){
      if(controller.signal.aborted)return;
      // The account changed since the first page: start again from the top.
      if(reason instanceof APIError&&reason.status===409){setRetry(value=>value+1);return;}
      setError(reason);
    }
    finally{if(!controller.signal.aborted)setMorePending(false);}
  };

  const description = `${period ? `Period total · ${formatDateRange(range.from, range.to, "full")}` : `Balance as of ${formatDate(range.to, "full")}`} · ${book.currency} Book currency`;
  const group = !account;
  const foreign =
    !period && account?.currency && account.currency !== book.currency;
  const native =
    foreign &&
    activity?.nativeCurrencies?.[account.code] === account.currency &&
    activity?.nativeBalances &&
    Object.prototype.hasOwnProperty.call(activity.nativeBalances, account.code)
      ? money(
          (
            BigInt(activity.nativeBalances[account.code]) *
            (creditKind(account.kind) ? -1n : 1n)
          ).toString(),
          account,
        )
      : null;
  const balance = (
    <div className="account-group-activity-total">
      {amount === null ? "—" : money(amount.toString(), book)}
    </div>
  );
  const groupHelp = group ? (
    <Tooltip
      title="Group net changes"
      description={`${filtered ? "Filtered account scope · " : ""}${description}. Net effect on accounts in this group. Offsetting postings can show ${money("0", book)}.`}
      placement="bottom start"
      motion="none"
      isOpen={showContextHelp}
      onOpenChange={setShowContextHelp}
    >
      <TooltipTrigger
        className="account-activity-help"
        aria-label="About group net changes"
        aria-expanded={showContextHelp}
        onPress={() => setShowContextHelp((value) => !value)}
        onKeyDown={(event) => {
          if (event.key === "Escape" && showContextHelp) {
            event.preventDefault();
            setShowContextHelp(false);
          }
        }}
      >
        <InfoCircle aria-hidden="true" className="size-4" />
      </TooltipTrigger>
    </Tooltip>
  ) : null;
  const content = (
    <>
      {!embedded && balance}
      {account && (!embedded || foreign) && (
        <p className="account-detail-identity">
          {account.gl_code ? `${account.gl_code} · ` : ""}
          {account.name}
          {foreign &&
            ` · Native balance (${account.currency}): ${native ?? (activity || error ? "Unavailable" : "Loading…")}`}
        </p>
      )}
      {foreign && (
        <p className="account-detail-identity">
          Book value reflects recorded postings, not a live currency conversion.
        </p>
      )}
      {!embedded && (
        <div className="account-detail-activity">
          <div className="account-detail-activity-label">
            Weekly transactions · Last 52 weeks
          </div>
          {history && activity ? (
            <Sparkline
              values={history.weekly}
              max={Math.max(1, ...history.weekly.map((value) => value || 0))}
              name={name}
              from={activity.from}
              to={activity.asOf}
              last={history.lastDate}
              shared={false}
            />
          ) : (
            <span className="account-detail-activity-label">
              {error ? "Activity unavailable." : "Loading activity…"}
            </span>
          )}
        </div>
      )}
      {account && !embedded && (
        <div className="account-detail-actions">
          {onShowLedger && (
            <Button
              size="sm"
              color="secondary"
              disabled={!entries}
              onClick={() => {
                onClose();
                onShowLedger({
                  from: period
                    ? range.from
                    : page?.earliest ||
                      (book.coverage_start_date || book.start_date),
                  to: range.to,
                  account: node.codes[0],
                });
              }}
            >
              View account activity
            </Button>
          )}
          {onCustomize && (
            <Button size="sm" color="tertiary" onClick={onCustomize}>
              Customize account
            </Button>
          )}
          <AccountCodeEditor book={book.id} account={account} />
          {account.status !== "retired" && (
            <Button size="sm" color="tertiary" onClick={() => setChangeMode("parent")}>
              Set parent account
            </Button>
          )}
          {account.status !== "retired" && (
            <Button size="sm" color="tertiary" onClick={() => setChangeMode("heading")}>
              {account.heading ? "Allow postings" : "Use as a heading"}
            </Button>
          )}
          <Button size="sm" color="tertiary" onClick={() => setChangeMode(account.status === "retired" ? "reactivate" : "retire")}>
            {account.status === "retired" ? "Reactivate account" : "Retire account"}
          </Button>
          <AccountChangeDialogs book={book} account={account} accounts={accounts} mode={changeMode} onClose={() => setChangeMode(null)} />
        </div>
      )}
      {!account && node.own.length === 1 && (
        <div className="account-detail-actions">
          <AccountCodeEditor book={book.id} account={node.own[0]} />
        </div>
      )}
      <ErrorMessage
        message={error}
        intent="read"
        resource={account ? "accounts" : "activity"}
        pending={!entries && !error}
        onRetry={() => {
          onRefresh?.();
          setRetry((value) => value + 1);
        }}
      />
      {account && ledger && (
        <AccountLedgerView
          ledger={ledger}
          book={book}
          name={name}
          periodTotalShown={period}
          compact={embedded}
          onInspect={
            onInspect
              ? (id, trigger) => {
                  if (!embedded && !entryInspection) onClose();
                  onInspect(id, trigger);
                }
              : undefined
          }
        />
      )}
      {page && loadedRows < page.count && (
        <div className="account-detail-actions">
          <Button size="sm" color="secondary" busy={morePending} onClick={() => void loadMore()}>
            Show more
          </Button>
        </div>
      )}
      {group && !embedded && (
        <div className="account-workspace-activity-toolbar">
          <h3 className="account-workspace-activity-heading">
            Recorded activity
          </h3>
          {groupHelp}
        </div>
      )}
      {!account && (
        <div
          className="account-group-activity-table"
          aria-busy={!entries && !error}
        >
          <CollectionTable<Entry>
            ariaLabel={`${name} recorded activity`}
            testId="account-group-activity"
            frameTestId="account-group-activity-frame"
            columns={
              group
                ? columns.map((column) =>
                    column.id === "amount"
                      ? { ...column, label: "Group net change" }
                      : column,
                  )
                : columns
            }
            rows={entries || []}
            rowId={(entry) => entry.id}
            activeRowId={entryInspection?.id || null}
            rowLabel={(entry) =>
              `Inspect entry on ${formatDate(entry.date, "full")}`
            }
            selectionEnabled={false}
            onRowAction={
              onInspect
                ? (entry) => {
                    if (!embedded && !entryInspection) onClose();
                    onInspect(
                      entry.id,
                      document.activeElement instanceof HTMLElement
                        ? document.activeElement
                        : returnTo,
                    );
                  }
                : undefined
            }
            renderCell={(entry, column) =>
              column.id === "date" ? (
                <RecordDate value={entry.date} />
              ) : column.id === "accounts" ? (
                <span title={entry.accounts}>{entry.accounts}</span>
              ) : (
                money(entry.amount.toString(), book)
              )
            }
            emptyState={
              entries ? (
                <EmptyState scope="region" title="No activity in this scope" />
              ) : error ? null : (
                <LoadingState scope="region" label="Loading activity…" />
              )
            }
          />
        </div>
      )}
    </>
  );
  const editableAccount =
    account || (node.own.length === 1 ? node.own[0] : undefined);
  const identity = account
    ? [
        account.gl_code,
        account.name === name
          ? ""
          : account.name.startsWith(`${name} `)
            ? account.name.slice(name.length).trim()
            : account.name,
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  const accountTools = editableAccount ? (
    <>
      <Dropdown.Root>
        <MenuButton
          ref={toolsTrigger}
          size="sm"
          color="tertiary"
          iconLeading={DotsHorizontal}
          aria-label="Account actions"
          className="account-detail-tools"
        />
        <Dropdown.Popover
          appearance="control"
          placement="bottom end"
        >
          <Dropdown.Menu aria-label="Account actions" selectionMode="none">
            {account && onShowLedger && (
              <Dropdown.Item
                id="activity"
                appearance="control"
                onAction={() => {
                  onClose();
                  onShowLedger({
                    from: period
                      ? range.from
                      : page?.earliest ||
                        (book.coverage_start_date || book.start_date),
                    to: range.to,
                    account: node.codes[0],
                  });
                }}
              >
                View account activity
              </Dropdown.Item>
            )}
            {account && onCustomize && (
              <Dropdown.Item
                id="customize"
                appearance="control"
                onAction={onCustomize}
              >
                Customize account
              </Dropdown.Item>
            )}
            <Dropdown.Item
              id="code"
              appearance="control"
              onAction={() => setCodeOpen(true)}
            >
              Edit account code
            </Dropdown.Item>
            {account && account.status !== "retired" && (
              <Dropdown.Item id="parent" appearance="control" onAction={() => setChangeMode("parent")}>
                Set parent account
              </Dropdown.Item>
            )}
            {account && account.status !== "retired" && (
              <Dropdown.Item id="heading" appearance="control" onAction={() => setChangeMode("heading")}>
                {account.heading ? "Allow postings" : "Use as a heading"}
              </Dropdown.Item>
            )}
            {account && (
              <Dropdown.Item
                id="status"
                appearance="control"
                onAction={() => setChangeMode(account.status === "retired" ? "reactivate" : "retire")}
              >
                {account.status === "retired" ? "Reactivate account" : "Retire account"}
              </Dropdown.Item>
            )}
          </Dropdown.Menu>
        </Dropdown.Popover>
      </Dropdown.Root>
      <AccountCodeEditor
        book={book.id}
        account={editableAccount}
        open={codeOpen}
        onOpenChange={setCodeOpen}
        returnTo={toolsTrigger.current}
      />
      {account && (
        <AccountChangeDialogs
          book={book}
          account={account}
          accounts={accounts}
          mode={changeMode}
          returnTo={toolsTrigger.current}
          onClose={() => setChangeMode(null)}
        />
      )}
    </>
  ) : null;
  return embedded ? (
    <section
      className="account-detail-panel"
      data-vult-context-inspector
      data-testid="account-details"
      aria-label={`${name} account details`}
    >
      <header
        className={`account-detail-header ${account ? "account-detail-header-minimal" : "account-detail-header-group"}`}
      >
        <div className="account-detail-title">
          <h2>{name}</h2>
          {account?.status === "retired" && <span className="account-status-badge">Retired</span>}
          {account?.heading && <span className="account-status-badge" title="Takes no new entries; its recorded lines and balance stay">Heading</span>}
          {account && identity && (
            <span className="account-detail-reference">{identity}</span>
          )}
        </div>
        {(!account || period) &&
          (account && period ? (
            <div className="account-period-total">
              <span className="account-detail-reference">Period total</span>
              {balance}
            </div>
          ) : (
            balance
          ))}
        {account && (
          <div className="account-detail-scope">
            <span>{book.currency}</span>
            <Tooltip
              title="Ledger context"
              description={`${name}${identity ? ` · ${identity}` : ""}. ${book.currency} Book currency · Accounting dates: ${formatDateRange(range.from, range.to, "full")}. ${creditKind(account.kind) ? "Credit" : "Debit"} balances positive.`}
              placement="bottom end"
              motion="none"
              isOpen={showContextHelp}
              onOpenChange={setShowContextHelp}
            >
              <TooltipTrigger
                className="account-activity-help"
                aria-label="About account ledger"
                aria-expanded={showContextHelp}
                onPress={() => setShowContextHelp((value) => !value)}
                onKeyDown={(event) => {
                  if (event.key === "Escape" && showContextHelp) {
                    event.preventDefault();
                    setShowContextHelp(false);
                  }
                }}
              >
                <InfoCircle aria-hidden="true" className="size-4" />
              </TooltipTrigger>
            </Tooltip>
          </div>
        )}
        {account && accountTools}
        <Tooltip title="Close account details" placement="bottom" motion="none">
          <TooltipTrigger
            className="account-activity-help"
            aria-label="Close account details"
            isDisabled={entryInspection?.pending}
            onPress={onClose}
          >
            <XClose aria-hidden="true" className="size-4" />
          </TooltipTrigger>
        </Tooltip>
        {group && (
          <div className="account-group-scope">
            <span>
              {filtered ? "Filtered · " : ""}
              {period
                ? `Period total · ${formatDateRange(range.from, range.to)}`
                : `As of ${formatDate(range.to)}`}
              {` · ${book.currency}`}
            </span>
            {groupHelp}
          </div>
        )}
        <p className="sr-only">
          {filtered ? "Filtered account scope · " : ""}
          {description}
        </p>
      </header>
      {content}
    </section>
  ) : (
    <Dialog
      open
      title={`${name} activity`}
      description={`${filtered ? "Filtered account scope · " : ""}${description}`}
      returnTo={returnTo}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      {/* The dialog body has no inset of its own; its content keeps the
          standard dialog padding. */}
      <div className="account-group-activity-dialog">{content}</div>
    </Dialog>
  );
}
