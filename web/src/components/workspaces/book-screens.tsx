import { lazy, Suspense, useEffect, useRef, useState } from "react";
import type { State, Book, Transaction } from "../../lib/api";
import type { Workspace } from "../book/book-navigation";
import type { DashboardRange } from "../../lib/dashboard-period";
import type { WorkspaceReadState } from "../../lib/workspace";
import { responseFailure } from "../../lib/errors";
import { FailureState } from "../ui/failure-state";
import { LoadingState } from "../ui/loading-state";
const dashboardUnavailable = responseFailure(501, { code: "FEATURE_UNAVAILABLE" });
const V0Overview = lazy(() => import("../v0/v0-overview").then(m => ({default:m.V0Overview})));
const V0Accounts = lazy(() => import("../v0/v0-accounts").then(m => ({default:m.V0Accounts})));
const V0Books = lazy(() => import("../v0/v0-books").then(m => ({default:m.V0Books})));
const Settings = lazy(() => import("./settings").then(m => ({default:m.Settings})));
const Documents = lazy(() => import("../book/documents").then(m => ({default:m.Documents})));
const Approvals = lazy(() => import("../approvals/approvals").then(m => ({default:m.Approvals})));
type Props = {
  workspaceRead?: WorkspaceReadState;
  books?: Book[];
  onSelectBook?: (id: string) => void;
  saveRevision?: number;
  finderSelection?: number;
  sourceSearch?: string;
  documentSelection?: { id: string };
  accountSearch?: string;
  accountSelection?: { bookId: string; id: string };
  onAccountSearchChange?: (query: string) => void;
  onSourceSearchChange?: (query: string) => void;
  onPosted: () => void;
  page: Workspace;
  state: State;
  book: Book;
  onNavigate: (page: Workspace, kind?: string) => void;
  onShowTransactions: (range: DashboardRange) => void;
  onInspect: (id: string, trigger: HTMLElement) => void;
  onSettings: (trigger: HTMLElement) => void;
  onWaitingChange?: (count: number) => void;
};
export default function BookScreens(props: Props) {
  // Keep previously visited Accounts mounted when the active workspace does
  // not load the account positions it needs.
  const financial = useRef<State | undefined>(undefined);
  if (financial.current?.book?.id !== props.book.id) financial.current = undefined;
  if (!props.state.bootstrap && props.state.book?.id === props.book.id && (props.state.account_balances || props.state.personal)) financial.current = props.state;
  const financialState = financial.current || props.state;
  const financialReady = !financialState.bootstrap;
  const [documentsReady, setDocumentsReady] = useState(
    props.page === "documents",
  );
  const [accountsReady, setAccountsReady] = useState(props.page === "accounts");
  const [settingsReady, setSettingsReady] = useState(props.page === "settings");
  const [approvalsReady, setApprovalsReady] = useState(props.page === "approvals");
  const accountState = props.state.read_view === "accounts" ? props.state : undefined;
  const accountSnapshot = useRef<State | undefined>(undefined);
  if (accountSnapshot.current?.book?.id !== props.book.id) accountSnapshot.current = undefined;
  if (accountState) accountSnapshot.current = accountState;
  useEffect(() => {
    if (props.page === "documents") setDocumentsReady(true);
    if (props.page === "accounts") setAccountsReady(true);
    if (props.page === "settings") setSettingsReady(true);
    if (props.page === "approvals") setApprovalsReady(true);
  }, [props.page]);
  return (
    <Suspense fallback={null}>
      <div className="workspace-pane" hidden={props.page !== "approvals"} inert={props.page !== "approvals"}>
        {approvalsReady && (
          <Approvals book={props.book} state={props.state.bootstrap ? financialState : props.state} active={props.page === "approvals"} refreshKey={props.saveRevision}
            initialProposal={new URLSearchParams(window.location.search).get("proposal")} onWaitingChange={props.onWaitingChange} />
        )}
      </div>
      <div
        className="workspace-pane transactions-pane"
        hidden={props.page !== "documents"}
        inert={props.page !== "documents"}
      >
        {documentsReady && (
          <Documents book={props.book} refreshKey={`${props.saveRevision ?? 0}:${props.finderSelection ?? 0}`} query={props.sourceSearch} documentSelection={props.documentSelection} onQueryChange={props.onSourceSearchChange} active={props.page === "documents"} onOpenEntry={(id) => props.onInspect(id, document.activeElement as HTMLElement)} />
        )}
      </div>
      <div
        className="workspace-pane"
        hidden={props.page !== "dashboard" && props.page !== "statements"}
        inert={props.page !== "dashboard" && props.page !== "statements"}
      >
        {(props.page === "dashboard" || props.page === "statements") && (
          props.page === "dashboard" && props.state.capabilities?.dashboard === false
            ? <FailureState scope="page" error={dashboardUnavailable} intent="read" resource="dashboard" />
            : props.page === "statements" || props.state.dashboard_snapshot
            ? <V0Overview page={props.page} state={props.state} book={props.book} onNavigate={props.onNavigate} onShowTransactions={props.onShowTransactions} onInspect={props.onInspect} workspaceRead={props.workspaceRead}/>
            : props.workspaceRead?.failure
              ? <FailureState scope="page" error={props.workspaceRead.failure} intent="read" resource="dashboard" pending={props.workspaceRead.pending} onRetry={props.workspaceRead.retry}/>
              : <LoadingState scope="page" label="Loading your dashboard…" />
        )}
      </div>
      <div
        className="workspace-pane transactions-pane"
        hidden={props.page !== "accounts"}
        inert={props.page !== "accounts"}
      >
        {props.page === "accounts" && !accountState && (
          props.workspaceRead?.failure
            ? <FailureState scope="page" error={props.workspaceRead.failure} intent="read" resource="accounts" pending={props.workspaceRead.pending} onRetry={props.workspaceRead.retry} />
            : <LoadingState scope="page" label="Loading accounts…" />
        )}
        {accountsReady && accountSnapshot.current && (
          <div hidden={!accountState && props.page === "accounts"}>
            <V0Accounts
              finderSelection={props.finderSelection}
              accountSearch={props.accountSearch}
              accountSelection={props.accountSelection}
              onAccountSearchChange={props.onAccountSearchChange}
              key={props.book.id}
              onNavigate={props.onNavigate}
              display={accountSnapshot.current}
              displayBook={props.book}
              onInspect={props.onInspect}
              refreshKey={props.saveRevision}
              active={props.page === "accounts" && !!accountState}
              onShowTransactions={props.onShowTransactions}
            />
          </div>
        )}
      </div>
      {props.page === "books" && <V0Books books={props.books || [props.book]} selected={props.book.id} onSelect={props.onSelectBook || (()=>{})} onCreate={props.onSettings}/>}
      <div className="workspace-pane" hidden={props.page !== "settings"} inert={props.page !== "settings"}>
      {settingsReady && (
        <Settings
          displayBook={props.book}
          book={props.book}
          key={props.book.id}
          onSaved={props.onPosted}
          refreshKey={props.saveRevision}
          visible={props.page === "settings"}
        />
      )}
      </div>
    </Suspense>
  );
}
