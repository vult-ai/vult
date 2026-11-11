import { ColumnLayoutScope } from "./design-system/use-column-layout";
import { hydrateBookCatalog, missingBookLink, resolveBookRoute, bookPath, readBookRoute, bookLocator, canonicalBookURL } from "./lib/book-routing";
import { rememberDashboardSelection, rememberedDashboardSelection, readDashboardLocation, resolveDashboardLocationBook } from "./lib/dashboard-location";
import { dashboardRange, type DashboardPeriod } from "./lib/dashboard-period";
import { DashboardPeriodPicker } from "./components/workspaces/dashboard-period-picker";
import { HeaderActions } from "./components/shell/header-actions";
import { PageToolbar } from "./components/workspaces/workspace-frame";
import { today as todayIn } from "./lib/api";
import {
  bookNavigation,
  workspacePath,
  booksWorkspace,
  documentWorkspace,
  readWorkspace,
  type Workspace,
} from "./components/book/book-navigation";
import { BookSwitcher } from "./components/book/book-switcher";
import { OnboardingLayout } from "./components/onboarding/onboarding-layout";
import { FirstRun } from "./components/onboarding/first-run";
import { EmptyVult, Welcome, bookHasEntries, finishWelcome, welcomeEligible } from "./components/onboarding/welcome";
import { LocalEntrance, entranceStarted, finishEntrance } from "./components/onboarding/local-entrance";
import { HostedEntrance } from "./components/onboarding/hosted-entrance";
import { AgentRequestBar } from "./components/onboarding/agent-request-bar";
import { readSession } from "./lib/session";
import { decodeReviewReference, readWaitingCount } from "./lib/proposals";
import { AppStateScreen } from "./components/ui/app-state-screen";
import { focusWithoutScroll } from "./design-system/focus-without-scroll";
import { TransactionsToolbar } from "./components/book/transactions-toolbar";
import { TablePagination } from "./design-system/catalog/components/application/table/table-pagination";
import { transactionView } from "./lib/transaction-view";
import { File06 as EntryIcon, Settings01 } from "./design-system/icons";
import {
  activeBookStorage,
  rememberedBookId,
  readBookState,
} from "./lib/book-state";
import {
  markStartupReady,
  type InitialBook,
  type InitialBookResult,
} from "./lib/startup";
import { lazy, Suspense } from "react";
import { AppShell } from "./components/shell/app-shell";
import { OnboardingCard } from "./components/shell/onboarding-card";
import { UpdateNotice, useUpdateNotice } from "./components/shell/update-notice";
import { UpdatingOverlay } from "./components/shell/updating-overlay";
import { BookFinder } from "./components/shell/book-finder";
import { PersonalSettingsHost, usePersonalAccount } from "./components/shell/personal-settings-host";
import { FocusVisibility } from "./design-system/focus-visibility";
import { useReadRetry } from "./design-system/use-read-retry";
import { errorCopy } from "./lib/error-copy";

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useId,
  useRef,
  useState,
  type ComponentProps,
  type ComponentType,
  type ReactNode,
} from "react";
import { createRoot } from "react-dom/client";
import {
  ArrowUpRight,
  ChevronDown,
} from "./design-system/icons";
import { Button } from "./components/ui/button";
import { Dialog } from "./components/ui/dialog";
import { EntryPreview, ContextualEntryPreview } from "./components/book/entry-preview";
import { ActivitySummary, ActivitySummaryStrip } from "./components/book/activity-summary";
import { AccountsEntryProvider } from "./components/workspaces/accounts-entry-inspector";
import {
  CollectionInspector,
  InspectorCollection,
} from "./design-system/collection-inspector";
import { useEntryPreview, clearEntryPreview } from "./lib/use-entry-preview";
import {
  APIError,
  get,
  names,
  today,
  type Book,
  type BookList,
  type State,
  type Transaction,
} from "./lib/api";
import { ErrorMessage, message } from "./components/form";
import { FailureState } from "./components/ui/failure-state";
import { EmptyState } from "./components/ui/empty-state";
import { LoadingState } from "./components/ui/loading-state";
import { Toaster, toast } from "./components/ui/toaster";

const loadTransactionsTable = () =>
  import("./components/book/transactions-table");
const TransactionsTable = lazy(loadTransactionsTable);
const BookScreens = lazy(() => import("./components/workspaces/book-screens"));

type TableComponent = ComponentType<ComponentProps<typeof TransactionsTable>>;
let preparedTable: TableComponent | undefined;

const DeferredEntryDetail = lazy(() => import("./components/book/entry-detail"));
function EntryDetail(props: ComponentProps<typeof DeferredEntryDetail>) {
  return <Suspense fallback={<LoadingState scope="inline" label="Loading entry…" />}><DeferredEntryDetail {...props}/></Suspense>;
}

function BookWorkspace({
  books,
  selectedBook,
  focusHeading,
  onSelect,
  onBooksChanged,
  initialState,
  Table = TransactionsTable,
}: {
  books: Book[];
  selectedBook: Book;
  focusHeading: boolean;
  onSelect: (id: string) => void;
  onBooksChanged: () => void;
  initialState?: State;
  Table?: TableComponent;
}) {
  const updateNotice = useUpdateNotice(usePersonalAccount()?.mode === "local");
  const [state, setState] = useState<State | null>(initialState ?? null);
  const [saveRevision, setSaveRevision] = useState(0);
  // Approvals waiting for the owner, shown on the sidebar. Checked every minute.
  const [waitingApprovals, setWaitingApprovals] = useState(0);
  useEffect(() => {
    let live = true;
    const check = () =>
      readWaitingCount(selectedBook.id)
        .then((count) => live && setWaitingApprovals(count))
        .catch(() => { /* The count is a convenience; Approvals shows any failure itself. */ });
    check();
    const id = window.setInterval(check, 60_000);
    return () => { live = false; window.clearInterval(id); };
  }, [selectedBook.id]);
  const [error, setError] = useState<unknown>("");
  const [loading, setLoading] = useState(!initialState);
  const [detailPending, setDetailPending] = useState(false);
  const [detailEditing, setDetailEditing] = useState(false);
  const { detail, show: setDetail } = useEntryPreview(
    selectedBook,
    detailPending || detailEditing,
    state?.transactions,
  );
  const detailRegionId = useId();
  const statementEntryReturn = useRef<string | undefined>(undefined);
  const accountEntryReturn = useRef<string | undefined>(undefined);
  // The open collection's query lives in the address as ?q=, so Back, reload
  // and shared links keep it (docs/design/search.md).
  const initialQuery = useRef(new URLSearchParams(window.location.search).get("q") ?? "");
  const initialPage = useRef(readWorkspace());
  const [search, setSearch] = useState(() => (initialPage.current === "transactions" ? initialQuery.current : ""));
  const [sourceSearch, setSourceSearch] = useState(() => (initialPage.current === "documents" ? initialQuery.current : ""));
  const [documentSelection, setDocumentSelection] = useState<{ id: string }>();
  const [accountSearch, setAccountSearch] = useState(() => (initialPage.current === "accounts" ? initialQuery.current : ""));
  const [finderSelection, setFinderSelection] = useState(0);
  const [accountSelection, setAccountSelection] = useState<{ bookId: string; id: string } | undefined>(() => {
    const id = initialPage.current === "accounts" && new URLSearchParams(window.location.search).get("account");
    return id ? { bookId: selectedBook.id, id } : undefined;
  });
  const [kind, setKind] = useState("");
  // Activity and the Dashboard share one period per Book: Activity opens on the
  // Dashboard's period, and a date range chosen in Activity becomes the Dashboard's.
  const sharedDates = (bookId: string) => {
    const selection = rememberedDashboardSelection(bookId);
    return !selection || selection.period === "all" ? { from: "", to: "" } : selection.range;
  };
  const [from, setFrom] = useState(() => (readWorkspace() === "transactions" ? sharedDates(selectedBook.id).from : ""));
  const [to, setTo] = useState(() => (readWorkspace() === "transactions" ? sharedDates(selectedBook.id).to : ""));
  const explicitDates = useRef(false);
  const [account, setAccount] = useState("");
  const [workspace, setWorkspace] = useState<Workspace>(readWorkspace);
  const [activityReady, setActivityReady] = useState(workspace === "transactions");
  const pageKey = JSON.stringify([
    selectedBook.id,
    search,
    kind,
    from,
    to,
    account,
  ]);
  const [page, setPage] = useState({ key: pageKey, offset: 0, limit: 25 });
  // Activity pages and the summary bind to the first page's revision. When the
  // Book changes underneath them, the server answers 409 and both refresh together.
  const activityRevision = useRef<{ key: string; revision: string } | undefined>(
    // A startup Activity read already answered for the opening selection. It is
    // unfiltered, so an opening ?q= search reads again.
    !search && !from && !to && (initialState?.read_view === "transactions" || (initialState && workspace === "transactions"))
      ? { key: pageKey, revision: initialState.revision ?? "" }
      : undefined,
  );
  const [activityEpoch, setActivityEpoch] = useState(0);
  const staleActivity = useCallback(() => {
    activityRevision.current = undefined;
    setPage((previous) => ({ ...previous, offset: 0 }));
    setActivityEpoch((n) => n + 1);
  }, []);
  const pageOffset = page.key === pageKey ? page.offset : 0;
  useEffect(() => {
    setPage((previous) =>
      previous.key === pageKey
        ? previous
        : { ...previous, key: pageKey, offset: 0 },
    );
  }, [pageKey]);

  const [screensReady, setScreensReady] = useState(
    workspace !== "transactions",
  );
  useLayoutEffect(() => {
    // Preserve legacy Activity links outside workspaces with contextual readers.
    if (detail && workspace !== "transactions" && workspace !== "statements" && workspace !== "accounts") {
      setWorkspace("transactions");
      const url = new URL(window.location.href);
      if (!["activity", "transactions"].includes(readBookRoute().page)) url.pathname = bookPath(selectedBook, "activity");
      window.history.replaceState(window.history.state, "", url);
    }
  }, [detail, workspace]);
  const scrollPositions = useRef<Partial<Record<Workspace, number>>>({});
  const previousWorkspace = useRef(workspace);
  const generation = useRef(0);
  const readController = useRef<AbortController | null>(null);
  const startedRead = useRef(false);
  useEffect(() => {
    const changed = (event: Event) => { if ((event as CustomEvent).detail === selectedBook.id) void load(); };
    window.addEventListener("vult-account-code-changed", changed);
    window.addEventListener("vult-accounts-changed", changed);
    return () => {
      window.removeEventListener("vult-account-code-changed", changed);
      window.removeEventListener("vult-accounts-changed", changed);
    };
  }, [selectedBook.id, load]);
  const previousSearch = useRef(search);
  const modalReturn = useRef<HTMLElement | null>(null);
  async function load() {
    readController.current?.abort();
    const controller = new AbortController();
    readController.current = controller;
    const seq = ++generation.current;
    setLoading(true);
    setError("");
    const bound = workspace === "transactions" && pageOffset > 0 && activityRevision.current?.key === pageKey
      ? activityRevision.current.revision : undefined;
    try {
      const data = await readBookState(
        selectedBook.id,
        workspace === "transactions" ? { account, search, kind, from, to } : {},
        selectedBook.accounting_timezone,
        {
          view: workspace,
          limit: page.limit,
          offset: pageOffset,
          revision: bound,
          signal: controller.signal,
          ...(workspace === "dashboard" || workspace === "accounts" ? { range: readDashboardLocation(
            selectedBook.coverage_start_date || selectedBook.start_date,
            today(selectedBook.accounting_timezone), selectedBook,
          ).range } : {}),
        },
      );
      if (seq === generation.current) {
        if (workspace === "transactions" && !bound)
          activityRevision.current = { key: pageKey, revision: data.revision ?? "" };
        setState(data);
      }
    } catch (e) {
      if (seq === generation.current && workspace === "transactions" && e instanceof APIError && e.status === 409) {
        staleActivity();
        return;
      }
      if (seq === generation.current) setError(e);
    } finally {
      if (seq === generation.current) setLoading(false);
    }
  }
  const workspaceRetry = useReadRetry(load, loading);
  useEffect(() => {
    const firstRead = !startedRead.current;
    const debounceSearch = !firstRead && previousSearch.current !== search;
    startedRead.current = true;
    previousSearch.current = search;
    let id: ReturnType<typeof setTimeout> | undefined;
    // The startup snapshot has no date filter; with a shared period, read it now.
    if (!(firstRead && initialState && !search && !from && !to)) {
      if (debounceSearch) id = setTimeout(() => void load(), 180);
      else void load();
    }
    return () => {
      clearTimeout(id);
      // Invalidate the previous view immediately, including the debounce window.
      generation.current += 1;
      readController.current?.abort();
    };
  }, [
    selectedBook.id,
    search,
    kind,
    from,
    to,
    account,
    workspace,
    pageOffset,
    page.limit,
    activityEpoch,
  ]);
  useLayoutEffect(() => {
    if (state?.book) markStartupReady("workspace");
  }, [state]);
  function saved() {
    setSaveRevision((value) => value + 1);
    if (workspace === "settings") onBooksChanged();
    // Only pages that show an entry carry it in the address; elsewhere a
    // save must not move the address to Activity.
    setDetail("", ["transactions", "statements", "accounts"].includes(workspace) ? "replace" : "none");
    toast.success("Saved to the Book.");
    void load();
  }
  const book = state?.book;
  // Undefined until this selection's first page has answered; null when the
  // server binds no revision.
  const summaryRevision = activityRevision.current?.key === pageKey ? activityRevision.current.revision || null : undefined;
  const view = useMemo(
    () =>
      transactionView(
        state?.transactions || [],
        kind,
        state?.transaction_counts,
      ),
    [state?.transactions, state?.transaction_counts, kind],
  );
  const focusAfterCreate = useRef(focusHeading);
  const heading = useRef<HTMLHeadingElement>(null);
  useLayoutEffect(() => {
    if (book && focusAfterCreate.current && heading.current) {
      focusAfterCreate.current = false;
      focusWithoutScroll(heading.current);
    }
  }, [book?.id]);
  const locked = detailPending || detailEditing;
  const lockedRef = useRef(locked);
  lockedRef.current = locked;
  function navigate(next: Workspace, filter?: string) {
    if (lockedRef.current) return;
    scrollPositions.current[workspace] =
      document.getElementById("main-content")?.scrollTop ?? 0;
    if (filter !== undefined) {
      setKind(filter);
    }
    if (next !== workspace) setDetail("", "none");
    if (next === "transactions" && workspace !== "transactions" && !explicitDates.current) {
      const dates = sharedDates(selectedBook.id);
      setFrom(dates.from);
      setTo(dates.to);
    }
    explicitDates.current = false;
    setWorkspace(next);
    if (next === "transactions") setActivityReady(true);
    if (next !== "transactions") setScreensReady(true);
    if (window.location.pathname !== workspacePath(next)) {
      const url = clearEntryPreview(new URL(window.location.href));
      url.pathname = workspacePath(next);
      // The Dashboard's period lives in its own address; elsewhere it is kept per Book.
      if (workspace === "dashboard") for (const key of ["range", "preset", "period", "from", "to"]) url.searchParams.delete(key);
      window.history.pushState(window.history.state, "", url);
    }
  }
  useEffect(() => {
    if (workspace !== "transactions" || !!from !== !!to) return;
    const current = rememberedDashboardSelection(selectedBook.id);
    if (!from) {
      if (!current || current.period === "all") return;
      rememberDashboardSelection(selectedBook, {
        period: "all",
        range: dashboardRange("all", selectedBook.start_date, todayIn(selectedBook.accounting_timezone)),
      });
      return;
    }
    if (current && current.period !== "all" && current.range.from === from && current.range.to === to) return;
    rememberDashboardSelection(selectedBook, { period: "custom", range: { from, to } });
  }, [workspace, from, to, selectedBook]);
  // Activity's period sits in the page header, the same control as the Dashboard's.
  const activityToday = todayIn(selectedBook.accounting_timezone);
  const allRange = dashboardRange("all", selectedBook.start_date, activityToday);
  const remembered = rememberedDashboardSelection(selectedBook.id);
  const activityRange = from && to ? { from, to } : allRange;
  const activityPeriodName: DashboardPeriod | "custom" = !from && !to ? "all"
    : remembered && remembered.period !== "all" && remembered.range.from === from && remembered.range.to === to ? remembered.period : "custom";
  const activityPeriod = (
    <DashboardPeriodPicker
      context="Activity"
      range={activityRange}
      period={activityPeriodName}
      onPreset={(period) => {
        const range = dashboardRange(period, selectedBook.start_date, activityToday);
        rememberDashboardSelection(selectedBook, { period, range });
        setFrom(period === "all" ? "" : range.from);
        setTo(period === "all" ? "" : range.to);
      }}
      onRange={(range) => {
        rememberDashboardSelection(selectedBook, { period: "custom", range });
        setFrom(range.from);
        setTo(range.to);
      }}
    />
  );
  const collectionQuery =
    workspace === "transactions" ? search : workspace === "accounts" ? accountSearch : workspace === "documents" ? sourceSearch : "";
  useEffect(() => {
    const url = new URL(window.location.href);
    const query = collectionQuery.trim();
    if ((url.searchParams.get("q") ?? "") === query) return;
    if (query) url.searchParams.set("q", query);
    else url.searchParams.delete("q");
    window.history.replaceState(window.history.state, "", url);
  }, [collectionQuery, workspace]);
  function inspectEntry(id: string, trigger: HTMLElement | null) {
    if (detailPending || detailEditing) return;
    modalReturn.current = trigger;
    if (workspace === "accounts") {
      if (!detail) accountEntryReturn.current = clearEntryPreview(new URL(window.location.href)).href;
      setDetail(id, detail ? "replace" : "push");
      return;
    }
    if (workspace === "statements") {
      statementEntryReturn.current = clearEntryPreview(new URL(window.location.href)).href;
      setDetail(id);
      return;
    }
    if (workspace !== "transactions") {
      scrollPositions.current[workspace] =
        document.getElementById("main-content")?.scrollTop ?? 0;
      const url = clearEntryPreview(new URL(window.location.href));
      url.pathname = "/activity";
      window.history.pushState(window.history.state, "", url);
      setWorkspace("transactions");
      setDetail(id, "replace");
    } else {
      setDetail(id);
    }
  }
  useEffect(() => {
    window.history.replaceState(
      null,
      "",
      canonicalBookURL(selectedBook, readWorkspace()),
    );
  }, []);
  useEffect(() => {
    const change = () => {
      if (lockedRef.current) {
        window.history.replaceState(
          null,
          "",
          `${workspacePath(workspace)}${window.location.search}`,
        );
        return;
      }
      const next = readWorkspace();
      const linkedBook = resolveDashboardLocationBook(books);
      if ((next === "dashboard" || next === "statements") && linkedBook && linkedBook.id !== selectedBook.id) {
        onSelect(linkedBook.id);
        return;
      }
      setWorkspace(next);
      // Back and Forward restore the search the address held (?q=).
      const params = new URLSearchParams(window.location.search);
      const query = params.get("q") ?? "";
      if (next === "transactions") setSearch(query);
      else if (next === "accounts") {
        setAccountSearch(query);
        const id = params.get("account");
        setAccountSelection(id ? { bookId: selectedBook.id, id } : undefined);
      }
      else if (next === "documents") {
        setDocumentSelection(undefined);
        setSourceSearch(query);
      }
      if (next === "transactions") setActivityReady(true);
      if (next !== "transactions") setScreensReady(true);
    };
    window.addEventListener("hashchange", change);
    window.addEventListener("popstate", change);
    return () => {
      window.removeEventListener("hashchange", change);
      window.removeEventListener("popstate", change);
    };
  }, [workspace]);
  useLayoutEffect(() => {
    const main = document.getElementById("main-content");
    if (main) main.scrollTop = scrollPositions.current[workspace] ?? 0;
    if (previousWorkspace.current !== workspace && heading.current)
      focusWithoutScroll(heading.current);
    previousWorkspace.current = workspace;
  }, [workspace]);
  const workspaceLabel =
    workspace === "books" ? "Books" : workspace === "settings"
      ? "Settings"
      : workspace === "documents"
        ? documentWorkspace.label
        : (bookNavigation.find((item) => item.id === workspace)?.label ??
          "Transactions");
  const collection =
    workspace === "transactions" ||
    workspace === "documents" ||
    (workspace === "accounts" && !!state?.account_balances);
  const shellProps = {
    homeHref: workspacePath("dashboard"),
    bookName: book?.name || selectedBook.name,
    founderName: book?.founder || selectedBook.founder,
    bookSwitcher: (
      <BookSwitcher
        books={books}
        selected={selectedBook.id}
        onManage={()=>navigate("books")}
        locked={locked}
        onSelect={(id) => {
          if (!lockedRef.current) onSelect(id);
        }}
      />
    ),
    search:
      state && book ? (
        <BookFinder
          book={book}
          pages={[...bookNavigation, { id: "settings", label: "Settings", icon: Settings01 }]}
          onOpenEntry={(id) => inspectEntry(id, null)}
          onOpenPage={(id) => navigate(id as Workspace)}
          onShowTransactions={(query) => {
            setKind("");
            setSearch(query);
            navigate("transactions");
          }}
          onOpenAccount={(id) => {
            // Accounts opens the account's details (?account=), unfiltered.
            const url = new URL(window.location.href);
            url.searchParams.set("account", id);
            window.history.replaceState(window.history.state, "", url);
            setAccountSelection({ bookId: selectedBook.id, id });
            setAccountSearch("");
            navigate("accounts");
          }}
          onShowAccounts={(query) => {
            setFinderSelection((value) => value + 1);
            setAccountSearch(query);
            navigate("accounts");
          }}
          onShowSources={(query, id) => {
            setDocumentSelection(id ? { id } : undefined);
            setFinderSelection((value) => value + 1);
            setSourceSearch(query);
            navigate("documents");
          }}
        />
      ) : undefined,
    ready: !!book,
    sidebarCard: book ? (
      <>
      <UpdateNotice {...updateNotice} />
      <OnboardingCard
        bookId={book.id}
        onConnectAgent={() => {
          // Settings opens the tab its address names.
          const url = new URL(window.location.href);
          url.searchParams.set("tab", "account");
          window.history.replaceState(window.history.state, "", url);
          navigate("settings");
        }}
        onAddStatement={() => navigate("documents")}
        onReview={() => navigate("approvals")}
      />
      </>
    ) : undefined,
    collection,
    active: workspace,
    currentPage: workspace === "documents" ? documentWorkspace : workspace === "books" ? booksWorkspace : undefined,
    navigation: bookNavigation.map((item) => ({
      ...item,
      href: workspacePath(item.id as Workspace),
      disabled: locked || !book,
      ...(item.id === "approvals" ? { count: waitingApprovals } : {}),
    })),
    onNavigate: (id: string) => navigate(id as Workspace),
    onSettingsPage: () => navigate("settings"),
    onSettings: (trigger: HTMLElement) => {
      if (!book || lockedRef.current) return;
      navigate("settings");
    },
  };
  if (!state || !book) {
    return (
      <>
        <AppShell {...shellProps} failed={!loading}>
          {loading ? (
            <LoadingState scope="page" label={`Opening ${selectedBook.name}…`} />
          ) : (
            <FailureState
              scope="page"
              error={error}
              title={`Couldn’t open ${selectedBook.name}`}
              onRetry={workspaceRetry.retry}
              pending={workspaceRetry.running}
            />
          )}
        </AppShell>
      </>
    );
  }
  return (
    <>
      <div className="workspace-app-frame">
        <AppShell {...shellProps}>
          <ColumnLayoutScope bookId={book.id}>
          <h1 ref={heading} tabIndex={-1} className="sr-only">
            {workspaceLabel}
          </h1>

          <div className="entry-preview-region">
            <div
              className="workspace-pane transactions-pane"
              hidden={workspace !== "transactions"}
              inert={workspace !== "transactions"}
            >
              <section
                className="transactions-section"
                data-book-id={book.id}
                data-book-name={book.name}
                aria-busy={loading}
              >
                <CollectionInspector
                  selected={!!detail}
                  pending={detailPending}
                  onClose={() => setDetail("")}
                >
                  <InspectorCollection>
                    <Suspense
                      fallback={
                        <LoadingState scope="region" label="Loading transactions…" />
                      }
                    >
                      {activityReady && <Table
                        state={{ ...state, transactions: view.rows }}
                        book={book}
                        detail={detail}
                        previewId={detailRegionId}
                        pageStart={pageOffset}
                  pagination={
                    state.coverage && (
                      <TablePagination
                        ariaLabel="Transactions pages"
                        currentPage={
                          Math.floor(
                            (state.coverage.next_offset -
                              state.transactions.length || 0) /
                              state.coverage.limit,
                          ) + 1
                        }
                        pageCount={Math.max(
                          1,
                          Math.ceil(
                            (view.counts[kind] || 0) / state.coverage.limit,
                          ),
                        )}
                        pageSize={state.coverage.limit}
                        recordCount={view.counts[kind] || 0}
                        isDisabled={loading || !!error}
                        onPageChange={(n) =>
                          setPage({
                            key: pageKey,
                            offset: (n - 1) * page.limit,
                            limit: page.limit,
                          })
                        }
                        onPageSizeChange={(limit) =>
                          setPage({ key: pageKey, offset: 0, limit })
                        }
                      />
                    )
                  }
                        refreshKey={saveRevision}
                        active={workspace === "transactions"}
                        loading={loading || detailPending || detailEditing}
                        resetKey={JSON.stringify([
                          search,
                          kind,
                          from,
                          to,
                          account,
                        ])}
                        emptyState={
                          search || kind || from || to ? (
                            <EmptyState
                              scope="region"
                              title="No matching transactions"
                              detail="Adjust the search or filters."
                            />
                          ) : (
                            <EmptyState
                              scope="region"
                              icon={EntryIcon}
                              title="No transactions yet"
                              detail="Add a transaction from Manage."
                            />
                          )
                        }
                        onOpen={(id, trigger) => {
                          modalReturn.current = trigger;
                          if (!detailPending && !detailEditing) setDetail(id);
                        }}
                        toolbar={
                          <>
                          <HeaderActions active={workspace === "transactions"} fallback={<PageToolbar actions={activityPeriod} />}>
                            {activityPeriod}
                          </HeaderActions>
                          <TransactionsToolbar
                            bookId={book.id}
                            profile={book.profile}
                            filters={{ search, kind, from, to, account }}
                            counts={view.counts}
                            loading={loading}
                            onChange={(key, value) => {
                              const setters = {
                                search: setSearch,
                                kind: setKind,
                                from: setFrom,
                                to: setTo,
                                account: setAccount,
                              };
                              setters[key](value);
                            }}
                          />
                          </>
                        }
                        notice={
                          <>
                            <ActivitySummaryStrip book={book} filters={{ search, kind, from, to, account }} revision={summaryRevision} onStale={staleActivity} />
                            <ErrorMessage message={error} />
                            {account && (
                              <div className="dashboard-account-filter">
                                <span>
                                  Account:{" "}
                                  {state.accounts.find(
                                    (a) => a.code === account,
                                  )?.name || "Selected account"}
                                </span>
                                <Button
                                  color="tertiary"
                                  size="sm"
                                  onClick={() => setAccount("")}
                                >
                                  Show all accounts
                                </Button>
                              </div>
                            )}
                          </>
                        }
                      />}
                    </Suspense>
                  </InspectorCollection>
                  <EntryPreview
                    id={workspace === "statements" || workspace === "accounts" ? "" : detail}
                    regionId={detailRegionId}
                    pending={detailPending}
                    returnTo={modalReturn.current}
                    onClose={() => setDetail("")}
                    idleTitle="Summary"
                    idle={
                      workspace === "transactions" ? (
                        <ActivitySummary
                          book={book}
                          filters={{ search, kind, from, to, account }}
                          revision={summaryRevision}
                          onStale={staleActivity}
                          onFilter={(key, value) =>
                            ({ search: setSearch, kind: setKind, from: setFrom, to: setTo, account: setAccount })[key](value)
                          }
                        />
                      ) : undefined
                    }
                  >
                    {(id) => (
                      <EntryDetail
                        key={id}
                        id={id}
                        book={book}
                        currencies={state.currencies}
                        state={state}
                        onSaved={saved}
                        onNavigate={(next) => {
                          if (!detailPending && !detailEditing) setDetail(next);
                        }}
                        onPendingChange={setDetailPending}
                        onEditingChange={setDetailEditing}
                      />
                    )}
                  </EntryPreview>
                </CollectionInspector>
              </section>
            </div>
            {screensReady && (
              <div
                className={`workspace-pane${collection ? " transactions-pane" : ""}`}
                hidden={workspace === "transactions"}
                inert={workspace === "transactions"}
              >
                {workspace !== "dashboard" && <ErrorMessage message={error} />}
                <Suspense
                  fallback={
                    <LoadingState scope="region" label={`Loading ${workspaceLabel.toLowerCase()}…`} />
                  }
                >
                  <AccountsEntryProvider value={{
                    id: workspace === "accounts" ? detail : "",
                    pending: detailPending || detailEditing,
                    returnTo: modalReturn.current,
                    onClose: () => {
                      if (detailPending || detailEditing) return;
                      if (accountEntryReturn.current && clearEntryPreview(new URL(window.location.href)).href === accountEntryReturn.current) {
                        window.history.back();
                      } else setDetail("", "replace");
                    },
                    render: id => <EntryDetail key={id} id={id} book={book} currencies={state.currencies} state={state} onSaved={saved} onNavigate={next => { if (!detailPending && !detailEditing) setDetail(next, "replace"); }} onPendingChange={setDetailPending} onEditingChange={setDetailEditing} />,
                  }}>
                  <BookScreens
                    workspaceRead={{ failure: error, pending: loading, retry: load }}
                    books={books}
                    onSelectBook={id=>{if(!lockedRef.current)onSelect(id);}}
                    onShowTransactions={(range) => {
                      explicitDates.current = true;
                      setFrom(range.from);
                      setTo(range.to);
                      setAccount(range.account || "");
                      setSearch("");
                      setKind("");
                      navigate("transactions");
                    }}
                    saveRevision={saveRevision}
                    onWaitingChange={setWaitingApprovals}
                    finderSelection={finderSelection}
                    accountSearch={accountSearch}
                    accountSelection={accountSelection}
                    sourceSearch={sourceSearch}
                    documentSelection={documentSelection}
                    onAccountSearchChange={setAccountSearch}
                    onSourceSearchChange={(query) => {
                      setDocumentSelection(undefined);
                      setSourceSearch(query);
                    }}
                    onPosted={saved}
                    page={workspace}
                    state={state}
                    book={book}
                    onNavigate={navigate}
                    onInspect={(id, trigger) => {
                      inspectEntry(id, trigger);
                    }}
                    onSettings={(trigger) => {
                      if (!lockedRef.current) {
                        navigate("settings");
                      }
                    }}
                  />
                  </AccountsEntryProvider>
                </Suspense>
              </div>
            )}
          </div>
          <ContextualEntryPreview
            id={workspace === "statements" ? detail : ""}
            pending={detailPending || detailEditing}
            returnTo={modalReturn.current}
            onClose={() => {
              if (detailPending || detailEditing) return;
              if (statementEntryReturn.current && clearEntryPreview(new URL(window.location.href)).href === statementEntryReturn.current) {
                window.history.back();
                return;
              }
              setDetail("", "replace");
            }}
          >{id => <EntryDetail key={id} id={id} book={book} currencies={state.currencies} state={state} onSaved={saved} onNavigate={next => { if (!detailPending && !detailEditing) setDetail(next, "replace"); }} onPendingChange={setDetailPending} onEditingChange={setDetailEditing} />}</ContextualEntryPreview>
          </ColumnLayoutScope>
        </AppShell>
      </div>
    </>
  );
}

/** Selection lives in this tab, never in a mutable server-global book. Each
 * workspace owns a fixed UUID for its entire lifetime, including saved retries. */
function App({
  initial,
  Table = TransactionsTable,
}: { initial?: InitialBook; Table?: TableComponent } = {}) {
  const data = initial?.result;
  const [catalog, setCatalog] = useState<BookList | null>(
    () => data?.catalog ? hydrateBookCatalog(data.catalog) : null,
  );
  const known = useRef<BookList | null>(catalog);
  const [initialState, setInitialState] = useState<State | null>(
    data?.state ?? null,
  );
  // An agent's review link (/auth/approve?operation=…) names one Book and one
  // proposal. The server has already checked the owner's sign-in; open that Book's
  // Approvals with the proposal selected.
  const review = useRef(
    window.location.pathname === "/auth/approve"
      ? decodeReviewReference(new URLSearchParams(window.location.search).get("operation"))
      : null,
  );
  const [active, setActive] = useState(
    () => review.current?.bookId || data?.state?.book?.id || rememberedBookId(),
  );
  const selected = useRef(active);
  const [loading, setLoading] = useState(!data);
  const [error, setError] = useState(data?.error ?? "");
  const [focusHeading, setFocusHeading] = useState(false);
  const generation = useRef(0);

  function select(id: string) {
    const target = known.current?.books.find(book => book.id === id);
    if (target && readBookRoute().locator && bookLocator(target)) {
      const url = new URL(window.location.href);
      const destination = bookPath(target, readWorkspace());
      if (readBookRoute().locator.toLowerCase() !== bookLocator(target)!.toLowerCase()) {
        url.pathname = destination;
        for (const key of ["source", "sourcePreview", "sourceBook", "document", "occurrence", "entry", "entryPreview", "entryBook", "book", "book_id"]) url.searchParams.delete(key);
        window.history.replaceState(window.history.state, "", url);
      }
    }
    selected.current = id;
    setActive(id);
    try {
      localStorage.setItem(activeBookStorage, id);
    } catch {
      /* The selected UUID remains explicit in this tab's requests. */
    }
  }

  async function loadBooks(pending?: Promise<InitialBookResult>) {
    const seq = ++generation.current;
    setLoading(true);
    setError("");
    try {
      const result = pending
        ? await pending
        : {
            catalog: await get<BookList>("books"),
            state: undefined,
            error: undefined,
          };
      if (!result.catalog)
        throw new Error(result.error || errorCopy.unavailable);
      const list = hydrateBookCatalog(result.catalog);
      if (seq !== generation.current) return;
      const linkedBook = !known.current && (resolveBookRoute(list.books) || resolveDashboardLocationBook(list.books));
      const chosen = list.books.find(
        (b) => b.id === (linkedBook ? linkedBook.id : selected.current),
      );
      if (known.current && selected.current && !chosen) {
        throw new Error(
          "The selected Book is unavailable. Retry before continuing.",
        );
      }
      known.current = list;
      setCatalog(list);
      if (result.state) setInitialState(result.state);
      select(chosen?.id || result.state?.book?.id || list.books[0]?.id || "");
    } catch (e) {
      if (seq === generation.current) setError(message(e));
    } finally {
      if (seq === generation.current) setLoading(false);
    }
  }
  const catalogRetry = useReadRetry(() => loadBooks(), loading);
  useEffect(() => {
    if (initial?.result) {
      if (initial.result.catalog)
        select(
          initial.result.state?.book?.id ||
            initial.result.catalog.books[0]?.id ||
            "",
        );
    } else void loadBooks(initial?.pending);
    return () => {
      generation.current += 1;
    };
  }, []);

  useLayoutEffect(() => {
    if (catalog?.books.length === 0) markStartupReady("onboarding");
  }, [catalog]);

  if (!catalog) {
    return (
      <AppStateScreen>
        {loading ? (
          <LoadingState scope="page" label="Opening Books…" />
        ) : (
          <FailureState
            scope="page"
            error={error === missingBookLink ? new APIError("Check the address, or open the Book from the list.", 404) : error}
            resource="app"
            onRetry={catalogRetry.retry}
            pending={catalogRetry.running}
            {...(error === missingBookLink
              ? {
                  // A Book address that matches no Book: retrying never helps.
                  title: "No Book at this address",
                  secondaryAction: (
                    <Button color="primary" onClick={() => window.location.assign("/")}>
                      Open Books
                    </Button>
                  ),
                }
              : {})}
          />
        )}
      </AppStateScreen>
    );
  }
  if (catalog.books.length === 0) {
    return (
      <EmptyVult
        fallback={<FirstRun onCheck={() => void loadBooks()} checking={loading} />}
        onCreated={() => void loadBooks()}
        local={<LocalEntrance onOpen={(book, page) => openFromEntrance(book, page, () => void loadBooks())} />}
        hosted={<HostedEntrance onOpen={(book, page) => openFromEntrance(book, page, () => void loadBooks())} />}
      />
    );
  }
  const allBooks = catalog.books;
  if (review.current) {
    const target = allBooks.find((b) => b.id === review.current!.bookId);
    if (target)
      window.history.replaceState(window.history.state, "", `${bookPath(target, "approvals")}?proposal=${review.current.proposalId}`);
    review.current = null;
  }
  const book = allBooks.find((b) => b.id === active);
  if (!book)
    return (
      <AppStateScreen>
        <FailureState
          scope="page"
          error={new APIError("Book not found", 404)}
          resource="app"
          title="Couldn’t open this Book"
        />
      </AppStateScreen>
    );
  return (
    <NewBookWelcome key={book.id} book={book} onBooksChanged={() => void loadBooks()}>
      <AgentRequestBar book={book} />
      <BookWorkspace
        key={book.id}
        books={allBooks}
        selectedBook={book}
        initialState={
          initialState?.book?.id === book.id ? initialState : undefined
        }
        Table={Table}
        focusHeading={focusHeading}
        onSelect={(id) => {
          if (id === selected.current || !allBooks.some((b) => b.id === id))
            return;
          setInitialState(null);
          setFocusHeading(true);
          select(id);
        }}
        onBooksChanged={() => void loadBooks()}
      />
      <ErrorMessage message={error} />
    </NewBookWelcome>
  );
}

/** Leaves the first-run scene for the new Book, or its entries awaiting review. */
function openFromEntrance(book: Book, page: "dashboard" | "approvals", reload: () => void) {
  finishWelcome(book.id);
  window.history.replaceState(window.history.state, "", bookPath(book, page));
  reload();
}

/** A Book with no entries opens on the welcome instead of an empty dashboard,
 * until the owner finishes or skips it. */
function NewBookWelcome({
  book,
  onBooksChanged,
  children,
}: {
  book: Book;
  onBooksChanged: () => void;
  children: ReactNode;
}) {
  const [status, setStatus] = useState<"checking" | "welcome" | "entrance" | "hosted" | "book">(() =>
    entranceStarted(book.id) || (readWorkspace() === "dashboard" && welcomeEligible(book)) ? "checking" : "book",
  );
  useEffect(() => {
    if (status !== "checking") return;
    const controller = new AbortController();
    bookHasEntries(book, controller.signal).then(
      async (has) => {
        // Once entries exist the first run is over; the Book takes over.
        if (has) {
          finishWelcome(book.id);
          finishEntrance(book.id);
          setStatus("book");
          return;
        }
        // The first run is the entrance scene, on this Mac and on a Vult server.
        const mode = await readSession(controller.signal).then((s) => s.mode ?? "", () => "");
        if (!controller.signal.aborted) setStatus(mode === "local" ? "entrance" : mode ? "hosted" : entranceStarted(book.id) ? "entrance" : "welcome");
      },
      () => { if (!controller.signal.aborted) setStatus("book"); },
    );
    return () => controller.abort();
  }, []);
  if (status === "checking")
    return (
      <AppStateScreen>
        <LoadingState scope="page" label="Opening the Book…" />
      </AppStateScreen>
    );
  if (status === "entrance")
    return <LocalEntrance book={book} onOpen={(b, page) => openFromEntrance(b, page, () => { setStatus("book"); onBooksChanged(); })} />;
  if (status === "hosted")
    return <HostedEntrance book={book} onOpen={(b, page) => openFromEntrance(b, page, () => { setStatus("book"); onBooksChanged(); })} />;
  if (status === "welcome")
    return (
      <OnboardingLayout>
        <Welcome
          book={book}
          onBookSaved={onBooksChanged}
          onOpenBook={() => setStatus("book")}
          onUpload={() => {
            window.history.replaceState(window.history.state, "", bookPath(book, "documents"));
            setStatus("book");
          }}
        />
      </OnboardingLayout>
    );
  return <>{children}</>;
}

export async function prepareBook() {
  if (readWorkspace() === "transactions")
    preparedTable = (await loadTransactionsTable()).default;
}

export function mount(initial: InitialBook) {
  createRoot(document.getElementById("root")!).render(
    <PersonalSettingsHost>
      <FocusVisibility />
      <App initial={initial} Table={preparedTable} />
      <Toaster />
      <UpdatingOverlay />
    </PersonalSettingsHost>,
  );
}
