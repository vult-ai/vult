import { lazy, Suspense, useCallback } from "react";
import type { Book, State } from "../../lib/api";
import { readBookState } from "../../lib/book-state";
import type { DashboardSnapshot } from "../../lib/dashboard-projection";
import type { AccountGroup } from "../../lib/v0-ledger";
import { mostUrgentFailure } from "../../lib/errors";
import { useDashboard } from "../../lib/use-dashboard";
import type { DashboardRange } from "../../lib/dashboard-period";
import { DashboardPeriodPicker } from "../workspaces/dashboard-period-picker";
import { PageToolbar } from "../workspaces/workspace-frame";
import { HeaderActions } from "../shell/header-actions";
import { ErrorMessage } from "../form";
import { BalanceSheetPanel, CashPanel, IncomeExpensesPanel, OwedPanel, SpendingPanel, SummaryPanel } from "./dashboard-panels";
import type { Workspace } from "../book/book-navigation";
import type { WorkspaceReadState } from "../../lib/workspace";
import "./v0.css";
import "./dashboard-layout.css";
import "./dashboard-v04.css";
const StatementsWorkspace = lazy(() => import("../statements/statements-workspace").then(m => ({default:m.StatementsWorkspace})));
type Props = {
  workspaceRead?: WorkspaceReadState;
  page: Workspace;
  state: State;
  book: Book;
  onNavigate: (page: Workspace) => void;
  onShowTransactions: (range: DashboardRange) => void;
  onInspect: (id: string, trigger: HTMLElement) => void;
};
function DashboardOverview({
  snapshot,
  book,
  onNavigate,
  onShowTransactions,
  workspaceRead,
}: Omit<Props, "state"> & { snapshot: DashboardSnapshot }) {
  const load = useCallback(
    async (range: DashboardRange, signal: AbortSignal) => {
      const state = await readBookState(book.id, {}, book.accounting_timezone, { view: "dashboard", range, signal });
      if (!state.dashboard_snapshot) throw new Error("The Dashboard is unavailable.");
      return state.dashboard_snapshot;
    },
    [book.id, book.accounting_timezone],
  );
  const { data, period, pending, failure, select, selectRange, retry } =
    useDashboard(book, snapshot, undefined, load, true, true);
  const view = data ?? snapshot;
  const range = view.period;
  const picker = (
    <DashboardPeriodPicker
      range={range}
      period={period}
      onPreset={(p) => void select(p)}
      onRange={(r) => void selectRange(r)}
    />
  );
  const accounts = (group: AccountGroup) => {
    const url = new URL(location.href);
    url.searchParams.set("group", group);
    history.replaceState(history.state, "", url);
    onNavigate("accounts");
  };
  const inspectAccount = (code: string, all = false) =>
    onShowTransactions({
      from: all ? "" : range.from,
      to: range.to,
      account: code,
    });
  return (
    <div
      className="workspace-page v0-overview"
      data-testid="v0-book-overview"
      aria-busy={pending || workspaceRead?.pending}
    >
      <HeaderActions
        active
        fallback={<PageToolbar actions={picker} />}
      >
        {picker}
      </HeaderActions>
      <ErrorMessage
        message={mostUrgentFailure(workspaceRead?.failure, failure)}
        intent="read"
        resource="dashboard"
        retained="view"
        pending={pending || workspaceRead?.pending}
        onRetry={workspaceRead?.failure ? workspaceRead.retry : retry}
      />
      <div className="dash-grid-layout">
        <SummaryPanel book={book} snapshot={view} />
        <IncomeExpensesPanel book={book} performance={view.performance} range={range} onShowPeriod={onShowTransactions} />
        <SpendingPanel book={book} performance={view.performance} range={range} onOpen={(code) => inspectAccount(code)} onViewStatement={() => onNavigate("statements")} />
        <BalanceSheetPanel book={book} position={view.financial} comparison={view.overviewComparison} range={range} onOpen={accounts} />
        <CashPanel
          book={book}
          accounts={view.cash_accounts}
          total={view.cash_minor}
          status={view.cash_status}
          range={range}
          onOpen={(code) => inspectAccount(code, true)}
          onViewAccounts={() => accounts("cash")}
        />
        <OwedPanel book={book} position={view.financial} range={range} onOpen={(code) => inspectAccount(code, true)} onViewAccounts={() => accounts("liability")} />
      </div>
    </div>
  );
}


export function V0Overview(props: Props) {
  return props.page === "statements"
    ? <Suspense fallback={null}><StatementsWorkspace key={props.book.id} book={props.book} state={props.state} onShowTransactions={props.onShowTransactions} onInspect={props.onInspect} /></Suspense>
        : props.state.dashboard_snapshot && <DashboardOverview key={props.book.id} {...props} snapshot={props.state.dashboard_snapshot} />;
}
