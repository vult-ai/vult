import { NameLink } from "../ui/name-link";
import { StarterChartOffer } from "../workspaces/starter-chart-offer";
import {
  useEffect,
  useState,
  useMemo,
  useCallback,
  type ComponentProps,
} from "react";
import type { Workspace } from "../book/book-navigation";
import type { Accounts } from "../workspaces/accounts";
import { AccountActivityList } from "../workspaces/account-activity-list";
import { Button } from "../ui/button";
import { inAccountGroup, recordedAccountSnapshot, type AccountGroup } from "../../lib/v0-ledger";
import { useDashboard } from "../../lib/use-dashboard";
import { readBookState } from "../../lib/book-state";
import { readDashboardLocation } from "../../lib/dashboard-location";
import { today, type State } from "../../lib/api";
import type { DashboardRange } from "../../lib/dashboard-period";
import "./v0.css";
const groups = [
  { id: "all", label: "All accounts" },
  { id: "asset", label: "Assets" },
  { id: "liability", label: "Liabilities" },
  { id: "equity", label: "Equity" },
  { id: "income", label: "Income" },
  { id: "expense", label: "Expenses" },
] as const;
function readGroup(): AccountGroup {
  const g = new URLSearchParams(window.location.search).get("group");
  return g === "cash" || groups.some((x) => x.id === g)
    ? (g as AccountGroup)
    : "all";
}
function accountPeriodData(state: State) {
  if (state.read_view !== "accounts" || !state.book || !state.period || !state.account_activity) return;
  try {
    const snapshot = recordedAccountSnapshot(state);
    if (!snapshot) return;
    return { book_id: state.book.id, period: state.period, version: `${state.revision}:${state.state_revision}`, state, snapshot };
  } catch {
    // Unconfirmed figures never become an Accounts snapshot.
    return undefined;
  }
}

export function V0Accounts(
  props: ComponentProps<typeof Accounts> & {
    onNavigate?: (page: Workspace) => void;
    onInspect?: (id: string, trigger: HTMLElement) => void;
  },
) {
  const book = props.displayBook;
  const load = useCallback(async (range: DashboardRange, signal: AbortSignal) => {
    const data = accountPeriodData(await readBookState(book.id, {}, book.accounting_timezone, { view: "accounts", range, signal }));
    if (!data) throw new Error("The account snapshot could not be confirmed.");
    return data;
  }, [book.id, book.accounting_timezone]);
  const incoming = useMemo(() => accountPeriodData(props.display), [props.display]);
  const report = useDashboard(book, incoming, undefined, load, props.active !== false, true);
  const confirmed = report.data?.book_id === book.id ? report.data : undefined;
  const state = confirmed?.state ?? props.display;
  const initial = readDashboardLocation(
    (props.displayBook.coverage_start_date || props.displayBook.start_date),
    props.displayBook.accounting_today ||
      today(props.displayBook.accounting_timezone),
    props.displayBook,
  );
  const range = confirmed?.period || initial.range;
  const [storedGroup, setGroup] = useState<AccountGroup>(readGroup);
  const group = props.accountSearch?.trim() ? "all" : storedGroup;
  useEffect(() => {
    if (props.active) setGroup(readGroup());
  }, [props.active, props.displayBook.id]);
  useEffect(() => {
    const change = () => setGroup(readGroup());
    window.addEventListener("popstate", change);
    return () => window.removeEventListener("popstate", change);
  }, []);
  const accounts = state.account_balances || [];
  const filter = useCallback(
    (a: (typeof accounts)[number]) => inAccountGroup(a, group),
    [group],
  );
  // A Book with no accounts at all can start from a starter chart.
  if (confirmed && !accounts.length && !props.accountSearch?.trim())
    return <StarterChartOffer book={props.displayBook} />;
  return (
    <div className="v0-accounts-page">
      <div className="v0-account-list">
        <AccountActivityList
          accounts={accounts}
          finderSelection={props.finderSelection}
          accountSearch={props.accountSearch}
          accountSelection={props.accountSelection}
          onAccountSearchChange={props.onAccountSearchChange}
          book={props.displayBook}
          active={props.active}
          onOpen={props.onShowTransactions}
          refreshKey={`${props.refreshKey || 0}:${state.state_revision || state.revision || ""}`}
          filterAccount={filter}
          showType={group === "all"}
          totals={{
            snapshot: confirmed?.snapshot,
            range,
            period: confirmed ? report.period : initial.period,
            pending: report.pending,
            error: report.error,
            retry: report.retry,
            select: report.select,
            selectRange: report.selectRange,
            state,
          }}
          onInspect={props.onInspect}
          navigation={
            group !== "all" ? (
              <span className="account-tree-scope">
                <Button
                  size="sm"
                  color="tertiary"
                  onClick={() => {
                    setGroup("all");
                    const url = new URL(window.location.href);
                    url.searchParams.delete("group");
                    window.history.replaceState(window.history.state, "", url);
                  }}
                >
                  All accounts
                </Button>
                <span aria-hidden="true">/</span>
                <span>
                  {group === "cash"
                    ? "Cash"
                    : groups.find((item) => item.id === group)?.label}
                </span>
              </span>
            ) : undefined
          }

          notice={
            group === "equity" && (
              <p className="v0-account-note">
                Total equity also includes income and expenses.{" "}
                <NameLink
                  className="v0-row-link"
                  onClick={() => props.onNavigate?.("statements")}
                  name="View the full breakdown in Statements."
                />
              </p>
            )
          }
          actions={props.actions}
        />
      </div>
    </div>
  );
}
