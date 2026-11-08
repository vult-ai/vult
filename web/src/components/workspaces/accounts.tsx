import type { State, Book } from "../../lib/api";
import {
  AccountActivityList,
  type AccountListActions,
} from "./account-activity-list";
import type { DashboardRange } from "../../lib/dashboard-period";

/** Recorded accounts and balances. Accounts are created through agents. */
export function Accounts({
  display,
  displayBook,
  actions,
  refreshKey = 0,
  accountSearch,
  onAccountSearchChange,
  active = true,
  onShowTransactions,
}: {
  display: State;
  displayBook: Book;
  actions?: AccountListActions;
  refreshKey?: number;
  finderSelection?: number;
  accountSearch?: string;
  accountSelection?: { bookId: string; id: string };
  /** Removes the active Book search. */
  onAccountSearchChange?: (query: string) => void;
  active?: boolean;
  onShowTransactions?: (range: DashboardRange) => void;
}) {
  if (!display.account_balances) return null;
  return (
    <AccountActivityList
      accountSearch={accountSearch}
      onAccountSearchChange={onAccountSearchChange}
      accounts={display.account_balances}
      book={displayBook}
      active={active}
      onOpen={onShowTransactions}
      refreshKey={`${refreshKey}:${displayBook.start_date}:${display.account_balances.map((account) => `${account.code}:${account.posting_count}`).join("|")}`}
      actions={actions}
    />
  );
}
