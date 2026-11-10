import { NameLink } from "../ui/name-link";
import { readAccountSearchIDs } from "../../lib/book-search";
import { AccountsEntryInspector, useAccountEntryInspection } from "./accounts-entry-inspector";
import { narrowHierarchyActivity, parseHierarchyRead, readAccountHierarchyActivity } from "../../lib/account-hierarchy-activity";
import { ErrorMessage } from "../form";
import {
  Dialog as AriaDialog,
  DialogTrigger,
  Popover,
} from "react-aria-components";
import { formatDate as dateLabel, formatDateRange } from "../../lib/date-format";
import {
  useEffect,
  useCallback,
  useLayoutEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  ArrowDown,
  ChevronDownDouble,
  ChevronUpDouble,
  DotsGrid,
  Settings01,
} from "../../design-system/icons";
import { useRowOrder } from "../ui/use-row-order";
import { reorderVisibleAccounts } from "../../lib/account-presentation";
import { APIError, money, nativeMinor, today, type Book, type State } from "../../lib/api";
import {
  activityFrom,
  readAccountActivity,
  type AccountActivity,
} from "../../lib/account-activity";
import { Select } from "../ui/select";
import { Button } from "../ui/button";
import { Button as TreeButton } from "../../design-system/catalog/components/vult-ui/button";
import { SearchFilter } from "../../design-system/search-filter";
import { Tooltip } from "../../design-system/catalog/components/base/tooltip/tooltip";
import type {
  DashboardRange,
  DashboardPeriod,
} from "../../lib/dashboard-period";
import type { FinancialSnapshot } from "../../lib/v0-ledger";
import {
  accountBasis,
  accountHierarchy,
  accountAmounts,
  nodeAmount,
  nodeActivity,
  hierarchyIndex,
  type AccountNode,
} from "../../lib/account-hierarchy";
import { DashboardPeriodPicker } from "./dashboard-period-picker";
import { HeaderActions } from "../shell/header-actions";
import { InspectorCollection } from "../../design-system/collection-inspector";
import { focusWithoutScroll } from "../../design-system/focus-without-scroll";
import { AccountTree, classifiedAccounts } from "./account-tree";
import { AccountsOverview } from "./accounts-overview";
import { Sparkline } from "./account-activity-sparkline";
import { AccountGroupActivity } from "./account-group-activity";
import { AccountAvatar } from "../ui/account-avatar";
import collectionStyles from "../../design-system/catalog/components/application/table/records-table.module.css";
import {
  AccountPresentationDialog,
  AccountPresentationButton,
  useAccountPresentation,
  useInstitutionCatalog,
} from "./account-presentation-controls";
import "./account-activity-list.css";
import { LoadingState } from "../ui/loading-state";
import { nativeUnavailable } from "../../lib/native-money";
import { CollectionTitle } from "../../design-system/collection-toolbar";

export type AccountPosition = NonNullable<State["account_balances"]>[number];
export type AccountListActions =
  ReactNode | ((onViewOptions: (trigger: HTMLElement) => void) => ReactNode);
export const accountKinds: Record<string, string> = {
  asset: "Asset",
  other_asset: "Other asset",
  fixed_asset: "Fixed asset",
  liability: "Liability",
  equity: "Equity",
  revenue: "Income",
  other_income: "Other income",
  expense: "Expense",
  other_expense: "Other expense",
};

type SortField =
  "name" | "type" | "balance" | "activity" | "institution" | "date";
type AccountSort = {
  value: string;
  label: string;
  field?: SortField;
  ascending?: boolean;
};
const accountSorts: readonly AccountSort[] = [
  { value: "custom", label: "Custom order" },
  { value: "name", label: "Account A–Z", field: "name", ascending: true },
  { value: "name-desc", label: "Account Z–A", field: "name", ascending: false },
  { value: "type", label: "Type A–Z", field: "type", ascending: true },
  { value: "type-desc", label: "Type Z–A", field: "type", ascending: false },
  {
    value: "balance",
    label: "Lowest balance",
    field: "balance",
    ascending: true,
  },
  {
    value: "balance-desc",
    label: "Highest balance",
    field: "balance",
    ascending: false,
  },
  {
    value: "activity-asc",
    label: "Least active",
    field: "activity",
    ascending: true,
  },
  {
    value: "activity",
    label: "Most active",
    field: "activity",
    ascending: false,
  },
  {
    value: "institution",
    label: "Institution A–Z",
    field: "institution",
    ascending: true,
  },
  {
    value: "institution-desc",
    label: "Institution Z–A",
    field: "institution",
    ascending: false,
  },
  { value: "date", label: "Oldest entry", field: "date", ascending: true },
  {
    value: "date-desc",
    label: "Newest entry",
    field: "date",
    ascending: false,
  },
];

function ColumnSort({
  field,
  label,
  sort,
  onSort,
  disabled,
  children,
}: {
  field: SortField;
  label: string;
  sort?: AccountSort;
  onSort: (value: string) => void;
  disabled?: boolean;
  children?: ReactNode;
}) {
  const active = sort?.field === field;
  const ascending = active && sort.ascending;
  const nextAscending = active ? !sort.ascending : true;
  const order =
    field === "name" || field === "type"
      ? nextAscending
        ? "A to Z"
        : "Z to A"
      : nextAscending
        ? "lowest to highest"
        : "highest to lowest";
  return (
    <button
      type="button"
      className="account-activity-sort vult-focus-target vult-focus-contained"
      aria-label={`Sort ${label}: ${order}`}
      disabled={disabled}
      onClick={() =>
        onSort(
          accountSorts.find(
            (option) =>
              option.field === field && option.ascending === nextAscending,
          )!.value,
        )
      }
    >
      <span className="account-activity-sort-label">{children || label}</span>
      <ArrowDown
        aria-hidden="true"
        className="account-activity-sort-arrow"
        data-active={active || undefined}
        data-ascending={ascending || undefined}
      />
    </button>
  );
}

function signed(account: AccountPosition, raw: bigint) {
  return ["liability", "equity", "revenue", "other_income"].includes(
    account.kind,
  )
    ? -raw
    : raw;
}
function accountMinor(account: AccountPosition) {
  return signed(account, BigInt(account.balance_minor));
}

export function accountMoney(
  account: AccountPosition,
  book: Book,
  native = false,
) {
  if (!native) return money(accountMinor(account).toString(), book);
  // Missing native history is unavailable, never a zero balance.
  const minor = nativeMinor(account);
  return minor === null
    ? nativeUnavailable
    : money(signed(account, BigInt(minor)).toString(), account);
}

/** Display hierarchy and explicit masks without changing canonical identity. */
function accountIdentity(name: string) {
  // Flat recorded accounts: the whole name is the account's name.
  const leaf = name;
  const masked = leaf.match(/^(.*?)\s*([•●*·]{2,}\s*\d[\d\s-]*)$/u);
  return {
    leaf,
    label: masked?.[1].trim() || leaf,
    suffix: masked?.[1].trim() ? masked[2].trim() : "",
    parent: "",
  };
}

export function AccountActivityList({
  accounts,
  book,
  refreshKey,
  active = true,
  onOpen,
  actions,
  filterAccount,
  navigation,
  notice,
  showType = true,
  totals,
  finderSelection = 0,
  accountSearch = "",
  accountSelection,
  onAccountSearchChange,
  onInspect,
}: {
  accounts: readonly AccountPosition[];
  finderSelection?: number;
  accountSearch?: string;
  accountSelection?: { bookId: string; id: string };
  onAccountSearchChange?: (query: string) => void;
  book: Book;
  refreshKey: string;
  active?: boolean;
  onOpen?: (range: DashboardRange) => void;
  actions?: AccountListActions;
  filterAccount?: (account: AccountPosition) => boolean;
  navigation?: ReactNode;
  notice?: ReactNode;
  showType?: boolean;
  totals?: {
    snapshot?: FinancialSnapshot;
    range: DashboardRange;
    period: DashboardPeriod | "custom";
    pending: boolean;
    error: string;
    retry: () => void;
    select: (period: DashboardPeriod) => void;
    selectRange: (range: DashboardRange) => void;
    state: State;
  };
  onInspect?: (id: string, trigger: HTMLElement) => void;
}) {
  const tableId = useId();
  const entryInspection = useAccountEntryInspection();
  const [history, setHistory] = useState<AccountActivity | null>(null);
  const [error, setError] = useState<unknown>();
  const [loading, setLoading] = useState(true);
  const [retry, setRetry] = useState(0);
  const weeks = 52;
  const query = accountSearch.trim();
  const [defaultView, setDefaultView] = useState(() =>
    accounts.some((account) => account.posting_count > 0) ? "activity" : "all",
  );
  const [searchView, setSearchView] = useState<string>();
  useEffect(() => setSearchView(undefined), [query, finderSelection]);
  const view = query ? searchView ?? "all" : defaultView;
  const setView = query ? setSearchView : setDefaultView;
  const [showParents, setShowParents] = useState(true);
  const [optionsOpen, setOptionsOpen] = useState(false);
  const [expanded, setExpanded] = useState<{
    bookId: string;
    keys: Set<string>;
  }>({ bookId: "", keys: new Set() });
  const [groupInspection, setGroupInspection] = useState<{
    bookId: string;
    node: AccountNode;
    trigger: HTMLElement;
  } | null>(null);
  const typeVisible = showType && view !== "cash";
  const presentation = useAccountPresentation(book.id);
  const [sortChoice, setSort] = useState<{
    bookId: string;
    value: string;
  } | null>(null);
  const sort =
    sortChoice?.bookId === book.id
      ? sortChoice.value
      : presentation.value.order.length
        ? "custom"
        : "activity";
  const sortOption = accountSorts.find((option) => option.value === sort);
  const sortState = (field: SortField) =>
    sortOption?.field === field
      ? sortOption.ascending
        ? ("ascending" as const)
        : ("descending" as const)
      : undefined;
  const changeSort = (value: string) => setSort({ bookId: book.id, value });
  const preserveSortScroll = useRef(false);
  const catalog = useInstitutionCatalog(active);
  const [preferenceError, setPreferenceError] = useState("");
  const [editing, setEditing] = useState<{
    bookId: string;
    code: string;
    trigger: HTMLElement;
  } | null>(null);
  const canvas = useRef<HTMLElement>(null);
  const setCanvas = useCallback((element: HTMLElement | null) => {
    canvas.current = element;
  }, []);
  const inspectorLayout = useRef<HTMLDivElement>(null);
  const [desktopInspector, setDesktopInspector] = useState(false);
  const [dockedEntry, setDockedEntry] = useState(false);
  useLayoutEffect(() => {
    const measure = () => {
      const width = inspectorLayout.current?.getBoundingClientRect().width || 0;
      if (width) {
        setDesktopInspector(width >= 960);
        setDockedEntry(width >= 1440);
      }
    };
    measure();
    const observer = new ResizeObserver(measure);
    if (inspectorLayout.current) observer.observe(inspectorLayout.current);
    return () => observer.disconnect();
  }, []);
  const closeDetails = () => {
    if (entryInspection?.pending) return;
    if (entryInspection?.id) entryInspection.onClose();
    setGroupInspection(null);
    if (desktopInspector && groupInspection?.trigger.isConnected)
      focusWithoutScroll(groupInspection.trigger);
  };
  const loaded = useRef("");
  const asOf =
    totals?.snapshot?.through ||
    totals?.range.to ||
    book.accounting_today ||
    today(book.accounting_timezone);
  const snapshot = totals?.snapshot;
  const amounts = useMemo(() => snapshot ? accountAmounts(snapshot, accounts) : null, [snapshot, accounts]);
  const stateRevision = totals?.state.state_revision;
  const searchKey = JSON.stringify([book.id, query, finderSelection, stateRevision ?? refreshKey, retry]);
  const [searchRead, setSearchRead] = useState<{ key: string; ids?: ReadonlySet<string>; error?: unknown }>();
  const matches = searchRead?.key === searchKey ? searchRead.ids : undefined;
  const searchError = searchRead?.key === searchKey ? searchRead.error : undefined;
  const searching = !!query && !matches && !searchError;
  useEffect(() => {
    if (!active || !query || searchRead?.key === searchKey) return;
    const controller = new AbortController();
    readAccountSearchIDs(book.id, query, controller.signal, stateRevision).then(
      (ids) => {
        if (!controller.signal.aborted) setSearchRead({ key: searchKey, ids });
      },
      (error) => {
        if (!controller.signal.aborted) setSearchRead({ key: searchKey, error });
      },
    );
    return () => controller.abort();
  }, [active, book.id, query, searchKey, searchRead?.key]);
  const filtered = accounts.filter(
    (account) =>
      (!filterAccount || filterAccount(account)) &&
      (!query || matches?.has(account.code)) &&
      (view === "all"
        ? true
        : view === "cash"
          ? account.usage === "cash" ||
            (account.usage === "legacy" &&
              account.kind === "asset" &&
              account.currency)
          : account.posting_count > 0),
  );
  // A recorded parent stays in the tree whenever one of its accounts is shown.
  const recorded = new Map(accounts.map((account) => [account.code, account]));
  const shown = new Set(filtered.map((account) => account.code));
  const withParents = [...filtered];
  for (const account of filtered)
    for (let p = account.parent_id ? recorded.get(account.parent_id) : undefined; p && !shown.has(p.code); p = p.parent_id ? recorded.get(p.parent_id) : undefined) {
      shown.add(p.code);
      withParents.push(p);
    }
  const roots = accountHierarchy(withParents);
  const treeRoots = classifiedAccounts(roots);
  // A Finder selection targets a recorded account independently of tree filters.
  const opened = useRef<typeof accountSelection>(undefined);
  const refreshingSelection = useRef<typeof accountSelection>(undefined);
  const [refreshedSelection, setRefreshedSelection] = useState<typeof accountSelection>();
  const [failedSelection, setFailedSelection] = useState<typeof accountSelection>();
  useEffect(() => {
    if (!active || !accountSelection || accountSelection.bookId !== book.id || opened.current === accountSelection) return;
    const account = accounts.find((row) => row.code === accountSelection.id);
    if (!account && totals && refreshedSelection !== accountSelection) {
      if (!totals.pending && refreshingSelection.current !== accountSelection) {
        refreshingSelection.current = accountSelection;
        void Promise.resolve(totals.retry()).then(() => setRefreshedSelection(accountSelection));
      }
      return;
    }
    opened.current = accountSelection;
    const url = new URL(window.location.href);
    if (url.searchParams.get("account") === accountSelection.id) {
      url.searchParams.delete("account");
      window.history.replaceState(window.history.state, "", url);
    }
    if (!account) {
      setFailedSelection(accountSelection);
      return;
    }
    setGroupInspection({
      bookId: book.id,
      node: accountHierarchy([account])[0],
      trigger: (document.activeElement as HTMLElement | null) ?? document.body,
    });
  }, [active, accountSelection, accounts, book.id, refreshedSelection, totals?.pending]);
  const scopeCodes = filtered.map(account => account.code).sort();
  const scopeKey = scopeCodes.join("\0");
  const wholeKey = JSON.stringify([book.id, asOf, stateRevision ?? refreshKey]);
  const requestKey = JSON.stringify([wholeKey, retry, scopeKey]);
  const wholeActivity = useRef<{ key: string; data: AccountActivity }>(undefined);
  const historyReady = (!totals || Boolean(totals.snapshot)) && (!query || !!matches);
  useEffect(() => {
    if (!active) setEditing(null);
    if (!active) setGroupInspection(null);
    setPreferenceError("");
  }, [active, book.id]);
  useEffect(() => {
    if (!active || !historyReady || loaded.current === requestKey) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setHistory((previous) =>
      previous?.bookId === book.id && previous.asOf === asOf ? previous : null,
    );
    const groupsShown = [...hierarchyIndex(treeRoots).values()].map((node) => node.codes);
    const scoped = async () => {
      if (!totals) return readAccountActivity(book, asOf, controller.signal);
      const inline = totals.state.account_activity;
      let whole = wholeActivity.current?.key === wholeKey ? wholeActivity.current.data : undefined;
      if (!whole && (inline || !query)) {
        whole = inline
          ? parseHierarchyRead(inline, book, asOf, accounts.map((account) => account.code))
          : await readAccountHierarchyActivity(book, asOf, controller.signal);
        confirmRevision(whole);
        if (!controller.signal.aborted) wholeActivity.current = { key: wholeKey, data: whole };
      }
      if (whole) {
        const narrowed = narrowHierarchyActivity(whole, scopeCodes, groupsShown);
        if (narrowed) return narrowed;
      }
      const named = await readAccountHierarchyActivity(book, asOf, controller.signal, scopeCodes);
      confirmRevision(named);
      if (groupsShown.some((codes) => !named.groups?.has([...codes].sort().join("\0"))))
        throw new Error("The account groups could not be confirmed.");
      return named;
    };
    function confirmRevision(result: AccountActivity) {
      if (stateRevision && result.stateRevision !== stateRevision)
        throw new APIError("The Book changed. Refresh to see the latest activity.", 409);
    }
    (scopeCodes.length || !totals ? scoped() : Promise.resolve<AccountActivity>({ bookId: book.id, asOf, from: asOf, accounts: {}, emptyWeekly: [], groups: new Map(), scope: "" }))
      .then((result) => {
        if (!controller.signal.aborted) {
          loaded.current = requestKey;
          setHistory(result);
        }
      })
      .catch((reason) => {
        if (!controller.signal.aborted) {
          setHistory(null);
          setError(reason instanceof Error ? reason : new Error("Account activity is unavailable."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [active, historyReady, requestKey, book.id, asOf]);
  const confirmed =
    historyReady && loaded.current === requestKey && history?.bookId === book.id && history.asOf === asOf &&
    (!totals || history.scope === scopeKey) ? history : null;
  const series = (code: string) =>
    confirmed
      ? (confirmed.accounts[code]?.weekly || confirmed.emptyWeekly).slice(
          -weeks,
        )
      : null;
  const entries = (code: string) =>
    series(code)?.reduce<number>((sum, value) => sum + (value || 0), 0) ?? 0;
  const max = confirmed
    ? Object.values(confirmed.accounts).reduce(
        (highest, account) =>
          account.weekly
            .slice(-weeks)
            .reduce<number>(
              (value, count) => Math.max(value, count || 0),
              highest,
            ),
        1,
      )
    : 1;
  // A search opens every branch, so each matching account shows.
  useEffect(() => {
    if (query && matches) setExpanded({ bookId: book.id, keys: new Set(hierarchyIndex(treeRoots).keys()) });
  }, [query, matches, book.id, scopeKey]);
  if (sortOption?.field === "type")
    treeRoots.sort(
      (a, b) =>
        (accountKinds[a.kind] || a.kind).localeCompare(
          accountKinds[b.kind] || b.kind,
        ) * (sortOption.ascending ? 1 : -1),
    );
  const allNodes = useMemo(() => hierarchyIndex(classifiedAccounts(accountHierarchy(accounts))), [accounts]);
  const partialTotal = (node: AccountNode) =>
    !!amounts &&
    !!allNodes
      .get(node.key)
      ?.codes.some(
        (code) =>
          !node.codes.includes(code) && (amounts.get(code) || 0n) !== 0n,
      );
  const grouped = !!totals && showParents;
  const customOrder = new Map(
    presentation.value.order.map((code, index) => [code, index]),
  );
  const accountPreference = (code: string) =>
    presentation.value.accounts[code] || { alias: "", institutionId: "" };
  const institutions = new Map(
    catalog.institutions.map((item) => [item.id, item.name]),
  );
  const nameOrder = (a: AccountPosition, b: AccountPosition) =>
    (
      accountPreference(a.code).alias || accountIdentity(a.name).leaf
    ).localeCompare(
      accountPreference(b.code).alias || accountIdentity(b.name).leaf,
    ) ||
    a.name.localeCompare(b.name) ||
    a.code.localeCompare(b.code);
  // Missing dates/institutions remain last in either direction.
  const optionalOrder = (a: string, b: string, direction: number) =>
    !a || !b ? (a ? -1 : b ? 1 : 0) : a.localeCompare(b) * direction;
  const ranked = [...accounts].sort((a, b) => {
    if (sort === "custom") {
      const aRank = customOrder.get(a.code),
        bRank = customOrder.get(b.code);
      if (aRank !== undefined || bRank !== undefined)
        return (
          (aRank ?? Number.MAX_SAFE_INTEGER) -
          (bRank ?? Number.MAX_SAFE_INTEGER)
        );
    }
    const field = sort === "custom" ? "activity" : sortOption?.field;
    const direction = sortOption?.ascending ? 1 : -1;
    let comparison = 0;
    if (field === "name") {
      comparison =
        (
          accountPreference(a.code).alias || accountIdentity(a.name).leaf
        ).localeCompare(
          accountPreference(b.code).alias || accountIdentity(b.name).leaf,
        ) * direction;
    } else if (field === "type") {
      comparison =
        (accountKinds[a.kind] || a.kind).localeCompare(
          accountKinds[b.kind] || b.kind,
        ) * direction;
    } else if (field === "balance") {
      const left = totals ? amounts?.get(a.code) || 0n : accountMinor(a),
        right = totals ? amounts?.get(b.code) || 0n : accountMinor(b);
      comparison = (left < right ? -1 : left > right ? 1 : 0) * direction;
    } else if (field === "institution") {
      comparison = optionalOrder(
        institutions.get(accountPreference(a.code).institutionId) || "",
        institutions.get(accountPreference(b.code).institutionId) || "",
        direction,
      );
    } else if ((field === "activity" || field === "date") && confirmed) {
      if (field === "activity")
        comparison = (entries(a.code) - entries(b.code)) * direction;
      if (!comparison)
        comparison = optionalOrder(
          confirmed.accounts[a.code]?.lastDate || "",
          confirmed.accounts[b.code]?.lastDate || "",
          direction,
        );
    }
    return comparison || nameOrder(a, b);
  });
  const selected = new Set(filtered.map((account) => account.code));
  const visible = ranked.filter((account) => selected.has(account.code));
  const visibleIds = visible.map((account) => account.code);
  const completeIds =
    sort === "custom"
      ? [
          ...new Set([
            ...presentation.value.order,
            ...ranked.map((account) => account.code),
          ]),
        ]
      : ranked.map((account) => account.code);
  const reordering = useRowOrder({
    ids: grouped ? [] : visibleIds,
    scope: `${book.id}:${view}:${sort}:${active}:${grouped}`,
    scroll: canvas,
    onCommit: (moved, position) => {
      try {
        presentation.update((current) => ({
          ...current,
          order: reorderVisibleAccounts(
            completeIds,
            visibleIds,
            moved,
            position,
          ),
        }));
        if (sort !== "custom") preserveSortScroll.current = true;
        setSort({ bookId: book.id, value: "custom" });
        setPreferenceError("");
      } catch (error) {
        setPreferenceError(
          error instanceof Error
            ? error.message
            : "Account order could not be saved.",
        );
      }
    },
  });
  const byCode = new Map(visible.map((account) => [account.code, account]));
  const ordered = (reordering.previewIds || visibleIds).map((code) =>
    byCode.get(code)!,
  );
  const from = activityFrom(asOf, weeks);
  const rankedIds = new Map(ranked.map((a, index) => [a.code, index]));
  const nodeName = (node: AccountNode) =>
    node.own.length === 1
      ? accountPreference(node.own[0].code).alias || node.name
      : node.name;
  const nodeInstitution = (node: AccountNode) => {
    const names = node.codes.map(
      (code) => institutions.get(accountPreference(code).institutionId) || "",
    );
    return names.length && names.every((name) => name && name === names[0])
      ? names[0]
      : "";
  };
  const compareNodes = (a: AccountNode, b: AccountNode) => {
    let comparison = 0;
    const direction = sortOption?.ascending ? 1 : -1,
      field = sortOption?.field;
    if (sort === "custom")
      comparison =
        Math.min(...a.codes.map((code) => rankedIds.get(code)!)) -
        Math.min(...b.codes.map((code) => rankedIds.get(code)!));
    else if (field === "name")
      comparison = nodeName(a).localeCompare(nodeName(b)) * direction;
    else if (field === "type")
      comparison =
        (accountKinds[a.kind] || a.kind).localeCompare(
          accountKinds[b.kind] || b.kind,
        ) * direction;
    else if (field === "balance" && amounts) {
      const left = nodeAmount(a, amounts),
        right = nodeAmount(b, amounts);
      comparison = (left < right ? -1 : left > right ? 1 : 0) * direction;
    } else if (field === "institution")
      comparison = optionalOrder(
        nodeInstitution(a),
        nodeInstitution(b),
        direction,
      );
    else if ((field === "activity" || field === "date") && confirmed) {
      const left = nodeActivity(a, confirmed),
        right = nodeActivity(b, confirmed);
      if (field === "activity")
        comparison = (left.count - right.count) * direction;
      if (!comparison)
        comparison = optionalOrder(left.lastDate, right.lastDate, direction);
    }
    return (
      comparison ||
      nodeName(a).localeCompare(nodeName(b)) ||
      a.key.localeCompare(b.key)
    );
  };
  const openKeys =
    expanded.bookId === book.id
      ? expanded.keys
      : new Set(treeRoots.filter((node) => node.children.length).map((node) => node.key));
  const branchKeys = [...hierarchyIndex(treeRoots).values()]
    .filter((node) => node.children.length)
    .map((node) => node.key);
  const allExpanded = branchKeys.length > 0 && branchKeys.every((key) => openKeys.has(key));
  const treeToggleLabel = allExpanded ? "Collapse all" : "Expand all";
  const rows = ordered.map((account) => ({
    key: account.code,
    depth: 0,
    account,
    node: undefined,
    direct: false,
  }));
  const amountLabel = (kind: string) =>
    accountBasis(kind) === "period" ? "Period total" : "Balance";
  const editingAccount =
    editing?.bookId === book.id
      ? accounts.find((account) => account.code === editing.code) || null
      : null;
  useEffect(() => {
    if (preserveSortScroll.current) preserveSortScroll.current = false;
    else canvas.current?.scrollTo({ top: 0 });
  }, [sort, view, book.id, filterAccount]);
  const optionsTrigger = useRef<HTMLElement>(null);
  const managedActions =
    typeof actions === "function"
      ? actions((trigger) => {
          optionsTrigger.current = trigger;
          setOptionsOpen(true);
        })
      : actions;
  const optionsPopover = (
    <Popover
      triggerRef={typeof actions === "function" ? optionsTrigger : undefined}
      isOpen={optionsOpen}
      onOpenChange={setOptionsOpen}
      placement="bottom end"
      offset={6}
      containerPadding={12}
      className="account-tree-options vult-surface-paper"
    >
      <AriaDialog aria-label="Accounts view options">
        <h3>View options</h3>
        <div className="account-tree-option-fields">
          <Select
            size="sm"
            aria-label="Account view"
            value={view}
            onChange={(event) => setView(event.currentTarget.value)}
            options={[
              { value: "activity", label: "With entries" },
              { value: "all", label: "All accounts" },
              { value: "cash", label: "Cash accounts" },
            ]}
          />
          <Select
            size="sm"
            aria-label="Sort accounts"
            value={sort}
            onChange={(event) => changeSort(event.currentTarget.value)}
            options={accountSorts.map((option) => ({
              ...option,
              label:
                totals && option.field === "balance"
                  ? option.ascending
                    ? "Lowest amount"
                    : "Highest amount"
                  : option.label,
              disabled:
                option.field === "balance" && totals
                  ? !amounts
                  : option.field === "activity" || option.field === "date"
                    ? !confirmed
                    : option.field === "institution"
                      ? !catalog.institutions.length
                      : false,
            }))}
          />
        </div>
        <Button
          size="sm"
          color="tertiary"
          onClick={() => {
            changeSort("custom");
            setShowParents(false);
            setOptionsOpen(false);
          }}
        >
          Reorder accounts
        </Button>
      </AriaDialog>
    </Popover>
  );
  const viewOptions =
    typeof actions === "function" ? (
      optionsPopover
    ) : (
      <DialogTrigger isOpen={optionsOpen} onOpenChange={setOptionsOpen}>
        <TreeButton
          color="tertiary"
          size="sm"
          className="account-tree-options-trigger"
          aria-label="Accounts view options"
          iconLeading={Settings01}
        />
        {optionsPopover}
      </DialogTrigger>
    );
  const periodPicker = totals && (
    <DashboardPeriodPicker
      context="Accounts"
      range={totals.range}
      period={totals.period}
      onPreset={totals.select}
      onRange={totals.selectRange}
    />
  );
  const details =
    totals && groupInspection?.bookId === book.id && active ? (
      <AccountGroupActivity
        key={`${book.id}:${groupInspection.node.key}:${totals.range.from}:${totals.range.to}`}
        embedded={desktopInspector}
        book={book}
        state={totals.state}
        revision={refreshKey}
        accounts={accounts}
        node={groupInspection.node}
        name={nodeName(groupInspection.node)}
        range={totals.range}
        amount={amounts ? nodeAmount(groupInspection.node, amounts) : null}
        returnTo={groupInspection.trigger}
        onClose={closeDetails}
        onInspect={onInspect}
        filtered={partialTotal(groupInspection.node)}
        onRefresh={totals.retry}
        onShowLedger={onOpen}
        onCustomize={
          groupInspection.node.codes.length === 1
            ? () => {
                setEditing({
                  bookId: book.id,
                  code: groupInspection.node.codes[0],
                  trigger: groupInspection.trigger,
                });
                setGroupInspection(null);
              }
            : undefined
        }
      />
    ) : null;
  const collection = (
    <section
      ref={totals ? undefined : setCanvas}
      {...reordering.container}
      data-reordering-active={!!reordering.dragging || undefined}
      className="accounts-activity-page"
      aria-label="Accounts activity list"
      data-testid="accounts-activity-list"
      data-tree-view={!!totals || undefined}
    >
      <div className="account-activity-surface">
        {!totals && (
          <header
            className={`${collectionStyles.toolbar} account-activity-controls`}
          >
            <div className="account-activity-navigation">
              {navigation || (
                <span className="vult-type-label">Chart of accounts</span>
              )}
            </div>
            <div className="account-activity-options">
              <Select
                size="sm"
                aria-label="Account view"
                value={view}
                onChange={(event) => setView(event.currentTarget.value)}
                options={[
                  { value: "activity", label: "With entries" },
                  { value: "all", label: "All accounts" },
                  { value: "cash", label: "Cash accounts" },
                ]}
              />
              <Select
                size="sm"
                aria-label="Sort accounts"
                value={sort}
                onChange={(event) => changeSort(event.currentTarget.value)}
                options={accountSorts.map((option) => ({
                  ...option,
                  label:
                    totals && option.field === "balance"
                      ? option.ascending
                        ? "Lowest amount"
                        : "Highest amount"
                      : option.label,
                  disabled:
                    option.field === "balance" && totals
                      ? !amounts
                      : option.field === "activity" || option.field === "date"
                        ? !confirmed
                        : option.field === "institution"
                          ? !catalog.institutions.length
                          : false,
                }))}
              />
            </div>
            <div className="account-activity-actions">{managedActions}</div>
          </header>
        )}
        {notice}
        <ErrorMessage
          message={searchError || (historyReady ? error : "")}
          intent="read"
          resource={searchError ? "accounts" : "activity"}
          pending={searching || (historyReady && loading)}
          retained={confirmed ? "view" : undefined}
          onRetry={() => {
            const failure = searchError || error;
            const refresh = failure instanceof APIError && failure.status === 409 ? totals?.retry() : undefined;
            void Promise.resolve(refresh).then(() => setRetry((value) => value + 1));
          }}
        />
        <ErrorMessage
          message={failedSelection && failedSelection === accountSelection ? "The selected account is unavailable." : ""}
          intent="read"
          resource="accounts"
        />
        <ErrorMessage
          message={preferenceError || presentation.error}
          intent="write"
          resource="settings"
        />
        <div
          className={
            totals ? "account-tree-workspace" : "account-activity-table-frame"
          }
        >
          {totals && (
            <>
              <ErrorMessage
                message={totals.error}
                intent="read"
                resource="balances"
                pending={totals.pending}
                retained={totals.snapshot ? "view" : undefined}
                onRetry={totals.retry}
              />
              <HeaderActions
                active={active}
                fallback={
                  <div className="account-activity-context">{periodPicker}</div>
                }
              >
                {periodPicker}
              </HeaderActions>
            </>
          )}
          {totals ? (
            <div className="account-tree-layout">
              <aside
                className="account-tree-pane"
                aria-label="Accounts navigation"
              >
                <div className="account-tree-pane-header">
                  {/* For screen readers: income and expenses cover the selected dates;
                      every other account shows its balance at the end. */}
                  <p className="account-tree-convention sr-only" id={`${tableId}-basis`}>
                    Activity {formatDateRange(totals.range.from, asOf)} · Balances as of {dateLabel(asOf)}
                  </p>
                  <div className="account-tree-pane-heading">
                    <span className="account-tree-pane-title">
                      <CollectionTitle
                        label={navigation || "Accounts"}
                        count={ordered.length.toLocaleString()}
                        countLabel={`${ordered.length} ${ordered.length === 1 ? "account" : "accounts"}`}
                        meta={<span className="account-tree-currency" aria-label={`Amounts in ${book.currency}`}>{book.currency}</span>}
                      />
                    </span>
                    <div className="account-tree-pane-actions">
                      {viewOptions}
                      <div className="account-tree-controls">
                        {showParents ? (
                          <Tooltip title={treeToggleLabel} placement="bottom" motion="none">
                            <TreeButton
                              size="sm"
                              color="secondary"
                              aria-label={treeToggleLabel}
                              iconLeading={allExpanded ? ChevronUpDouble : ChevronDownDouble}
                              isDisabled={!branchKeys.length}
                              onClick={() =>
                                setExpanded({
                                  bookId: book.id,
                                  keys: allExpanded ? new Set() : new Set(branchKeys),
                                })
                              }
                            />
                          </Tooltip>
                        ) : (
                          <Button
                            size="sm"
                            color="tertiary"
                            onClick={() => setShowParents(true)}
                          >
                            Done reordering
                          </Button>
                        )}
                      </div>
                      {managedActions}
                    </div>
                  </div>
                  {/* The Book search (⌘K) sets this; the pane shows it as a filter
                      on its own row, too narrow to share one with the title. */}
                  {onAccountSearchChange && accountSearch.trim() && (
                    <div className="account-tree-search-filter">
                      <SearchFilter value={accountSearch.trim()} onRemove={() => onAccountSearchChange("")} />
                    </div>
                  )}
                </div>
                {searching ? <LoadingState scope="region" label="Searching accounts…" /> : !searchError && <AccountTree
                  scrollRef={setCanvas}
                  roots={treeRoots}
                  flat={showParents ? undefined : ordered}
                  book={book}
                  amounts={amounts}
                  expanded={openKeys}
                  onExpanded={(keys) => setExpanded({ bookId: book.id, keys })}
                  compare={compareNodes}
                  nodeName={nodeName}
                  accountName={(account) =>
                    accountPreference(account.code).alias ||
                    accountIdentity(account.name).label
                  }
                  accountSuffix={(account) =>
                    [accountIdentity(account.name).suffix, account.status === "retired" ? "Retired" : ""].filter(Boolean).join(" · ")
                  }
                  icon={(account) => (
                    <AccountAvatar
                      density="compact"
                      accountKey={`${book.id}:${account.code}`}
                      name={
                        accountPreference(account.code).alias ||
                        accountIdentity(account.name).label
                      }
                      base={catalog.base}
                      institution={catalog.institutions.find(
                        (item) =>
                          item.id ===
                          accountPreference(account.code).institutionId,
                      )}
                    />
                  )}
                  customize={(account) => (
                    <AccountPresentationButton
                      name={account.name}
                      onEdit={(trigger) =>
                        setEditing({
                          bookId: book.id,
                          code: account.code,
                          trigger,
                        })
                      }
                    />
                  )}
                  grip={(account) => (
                    <button
                      className="account-row-grip"
                      {...reordering.handle(
                        account.code,
                        accountPreference(account.code).alias ||
                          accountIdentity(account.name).leaf,
                      )}
                    >
                      <DotsGrid aria-hidden="true" width={16} height={16} />
                    </button>
                  )}
                  partial={partialTotal}
                  onOpen={(node, trigger) => {
                    if (entryInspection?.pending) return;
                    if (entryInspection?.id && groupInspection?.node.key !== node.key) entryInspection.onClose();
                    setGroupInspection({ bookId: book.id, node, trigger });
                  }}
                  contextId={`${tableId}-basis`}
                  selected={
                    groupInspection?.bookId === book.id
                      ? groupInspection.node.key
                      : undefined
                  }
                />}
              </aside>
            </div>
          ) : (
            <table
              className="account-activity-table"
              data-show-type={typeVisible || undefined}
              role="table"
              aria-label="Chart of accounts"
            >
              <thead role="rowgroup">
                <tr className="account-activity-columns" role="row">
                  <th
                    id={`${tableId}-account`}
                    scope="col"
                    role="columnheader"
                    aria-sort={sortState("name")}
                  >
                    <ColumnSort
                      field="name"
                      label="Account"
                      sort={sortOption}
                      onSort={changeSort}
                    />
                    <span
                      className="account-activity-count"
                      role="status"
                      aria-label={`${ordered.length.toLocaleString()} ${ordered.length === 1 ? "account" : "accounts"}`}
                    >
                      ({ordered.length.toLocaleString()})
                    </span>
                  </th>
                  {typeVisible && (
                    <th
                      id={`${tableId}-type`}
                      scope="col"
                      role="columnheader"
                      aria-sort={sortState("type")}
                    >
                      <ColumnSort
                        field="type"
                        label="Type"
                        sort={sortOption}
                        onSort={changeSort}
                      />
                    </th>
                  )}
                  <th
                    id={`${tableId}-balance`}
                    scope="col"
                    role="columnheader"
                    title={
                      totals
                        ? `${book.currency} · period totals for income and expenses; balances as of ${dateLabel(asOf, "full")}`
                        : `${book.currency} recorded ledger balance`
                    }
                    aria-sort={sortState("balance")}
                  >
                    <ColumnSort
                      field="balance"
                      label={totals ? "Amount" : "Balance"}
                      sort={sortOption}
                      onSort={changeSort}
                      disabled={!!totals && !amounts}
                    >
                      {totals ? "Amount" : "Balance"}{" "}
                      <span className="account-activity-header-currency">
                        {book.currency}
                      </span>
                    </ColumnSort>
                  </th>
                  <th
                    id={`${tableId}-activity`}
                    scope="col"
                    role="columnheader"
                    aria-label="Activity: weekly transactions over the last 52 weeks"
                    title="Recorded transactions per week over the last 52 weeks"
                    aria-sort={confirmed ? sortState("activity") : undefined}
                  >
                    <ColumnSort
                      field="activity"
                      label="Activity"
                      sort={sortOption}
                      onSort={changeSort}
                      disabled={!confirmed}
                    />
                  </th>
                </tr>
              </thead>
              <tbody
                className="account-activity-body"
                role="rowgroup"
                aria-busy={loading}
              >
                {rows.map((row) => {
                  const account = row.account!;
                  const values = series(account.code);
                  const last = confirmed?.accounts[account.code]?.lastDate;
                  const identity = accountIdentity(account.name);
                  const preference = accountPreference(account.code);
                  const institution = catalog.institutions.find(
                    (item) => item.id === preference.institutionId,
                  );
                  const kind = accountKinds[account.kind] || account.kind;
                  const native = !totals
                    ? account
                    : accountBasis(account.kind) === "balance" &&
                        confirmed?.nativeBalances
                      ? {
                          ...account,
                          native_minor:
                            confirmed.nativeBalances[account.code] || "0",
                        }
                      : null;
                  const context = [
                    identity.parent,
                    preference.alias ? identity.label : "",
                  ]
                    .filter(
                      (value) =>
                        value && value.toLowerCase() !== kind.toLowerCase(),
                    )
                    .filter(
                      (value, index, parts) =>
                        parts.findIndex(
                          (part) => part.toLowerCase() === value.toLowerCase(),
                        ) === index,
                    );
                  const metadata = context.join(" • ");
                  return (
                    <tr
                      key={`${book.id}:${row.key}`}
                      className="account-activity-row"
                      role="row"
                      data-account-code={account.code}
                      data-reordering={
                        reordering.dragging === account.code || undefined
                      }
                      data-drag-pointer={
                        reordering.pointerDragging === account.code || undefined
                      }
                    >
                      <td
                        className="account-activity-identity"
                        headers={`${tableId}-account`}
                        role="cell"
                        style={{ paddingInlineStart: row.depth * 12 }}
                      >
                        {!grouped && (
                          <button
                            className="account-row-grip"
                            {...reordering.handle(
                              account.code,
                              preference.alias || identity.leaf,
                            )}
                          >
                            <DotsGrid
                              aria-hidden="true"
                              width={16}
                              height={16}
                            />
                          </button>
                        )}
                        <div className="account-activity-title">
                          <NameLink
                            type="button"
                            className="entry-link account-activity-name vult-focus-target vult-focus-contained"
                            title={account.name}
                            disabled={!onOpen}
                            aria-label={`View ${preference.alias ? `${preference.alias} (${account.name})` : account.name} transactions`}
                            onClick={() =>
                              onOpen?.({
                                from,
                                to: asOf,
                                account: account.code,
                              })
                            }
                            name={row.direct ? "Direct postings" : preference.alias || identity.label}
                            nameClassName="account-activity-label"
                            leading={
                              <AccountAvatar
                                density="compact"
                                accountKey={`${book.id}:${account.code}`}
                                name={preference.alias || identity.label}
                                base={catalog.base}
                                institution={institution}
                              />
                            }
                            trailing={
                              identity.suffix && (
                                <span className="account-activity-suffix">{identity.suffix}</span>
                              )
                            }
                          />
                          {context.length > 0 && (
                            <div
                              className="account-activity-meta"
                              title={metadata}
                            >
                              {context.map((value) => (
                                <span
                                  className="account-activity-meta-context"
                                  key={value}
                                >
                                  <span>{value}</span>
                                </span>
                              ))}
                            </div>
                          )}
                          <AccountPresentationButton
                            name={account.name}
                            onEdit={(trigger) =>
                              setEditing({
                                bookId: book.id,
                                code: account.code,
                                trigger,
                              })
                            }
                          />
                        </div>
                      </td>
                      {typeVisible && (
                        <td
                          className="account-activity-type"
                          headers={`${tableId}-type`}
                          role="cell"
                        >
                          {kind}
                        </td>
                      )}
                      <td
                        className="account-activity-balance"
                        headers={`${tableId}-balance`}
                        role="cell"
                      >
                        <span
                          className="account-activity-value"
                          title={`${book.currency} ${totals ? amountLabel(account.kind).toLowerCase() : "ledger balance"}`}
                        >
                          {totals
                            ? amounts
                              ? money(
                                  (amounts.get(account.code) || 0n).toString(),
                                  book,
                                )
                              : "—"
                            : accountMoney(account, book)}
                        </span>
                        {!totals &&
                        account.currency &&
                        account.currency !== book.currency ? (
                          <small className="account-activity-native">
                            {accountMoney(account, book, true)}
                          </small>
                        ) : null}
                        {totals && (
                          <small className="account-activity-amount-context">
                            {amountLabel(account.kind)}
                            {native &&
                              account.currency &&
                              account.currency !== book.currency && (
                                <> · {accountMoney(native, book, true)}</>
                              )}
                          </small>
                        )}
                      </td>
                      <td
                        className="account-activity-chart"
                        headers={`${tableId}-activity`}
                        role="cell"
                      >
                        <div className="account-activity-plot">
                          {values && (
                            <Sparkline
                              values={values}
                              max={max}
                              name={account.name}
                              from={from}
                              to={asOf}
                              last={last}
                            />
                          )}
                        </div>
                        {!confirmed && (
                          <span className="account-activity-summary">
                            {loading
                              ? "Loading activity…"
                              : "Activity unavailable"}
                          </span>
                        )}
                      </td>
                    </tr>
                  );
                })}
                {!rows.length && (
                  <tr className="account-activity-empty-row" role="row">
                    <td
                      colSpan={typeVisible ? 4 : 3}
                      className="account-activity-empty"
                      role="cell"
                    >
                      No accounts in this view.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {!desktopInspector && details}
      <span className="sr-only" id={reordering.instructionsId}>
        Drag to reorder, or press Space to pick up, arrow keys to move, and
        Enter to save. Escape cancels.
      </span>
      <span className="sr-only" role="status" aria-live="polite">
        {reordering.announcement}
      </span>
      <AccountPresentationDialog
        key={book.id}
        account={editingAccount}
        initial={
          editingAccount
            ? accountPreference(editingAccount.code)
            : { alias: "", institutionId: "" }
        }
        catalog={catalog}
        returnTo={editing?.trigger || null}
        onClose={() => setEditing(null)}
        onSave={(value) => {
          if (!editingAccount || editing?.bookId !== book.id)
            throw new Error(
              "The selected Book changed. Reopen account customization.",
            );
          presentation.update((current) => {
            const next = {
              ...current,
              accounts: Object.assign(Object.create(null), current.accounts),
            };
            if (value.alias || value.institutionId)
              next.accounts[editingAccount.code] = value;
            else delete next.accounts[editingAccount.code];
            return next;
          });
          setEditing(null);
        }}
      />
    </section>
  );
  const inspector = (
    <section
      key={groupInspection?.node.key || "empty"}
      id={`${tableId}-inspector`}
      className="account-main-workspace"
      aria-label="Account workspace"
      data-testid="account-inspector"
      data-empty={(!details && !(totals?.snapshot && amounts)) || undefined}
    >
      {details || (totals?.snapshot && amounts ? (
        <AccountsOverview
          book={book}
          range={totals.range}
          snapshot={totals.snapshot}
          roots={treeRoots}
          accounts={filtered}
          amounts={amounts}
          activity={confirmed}
          unused={accounts.filter((account) => account.posting_count === 0).length}
          filtered={!!matches}
          nodeName={nodeName}
          accountName={(account) => accountPreference(account.code).alias || accountIdentity(account.name).label}
          onOpenNode={(node, trigger) => setGroupInspection({ bookId: book.id, node, trigger })}
          onOpenAccount={(code, trigger) => {
            // As a click on its row does: the node that holds the account.
            const holders = [...hierarchyIndex(treeRoots).values()].filter((n) => n.own.some((a) => a.code === code));
            const node = holders.find((n) => !n.children.length) ?? holders[0];
            if (node) setGroupInspection({ bookId: book.id, node, trigger });
          }}
          onShowUnused={view === "all" ? undefined : () => setView("all")}
        />
      ) : (
        // Until the balances arrive the overview waits, shown only after a short
        // delay so a quick load never flashes; a failed read is explained beside the tree.
        <div className="collection-inspector-empty">
          {!totals?.error && <LoadingState scope="region" label="Loading overview…" />}
        </div>
      ))}
    </section>
  );
  return (
    <div
      ref={inspectorLayout}
      className="accounts-inspector-layout"
      data-desktop-inspector={(desktopInspector && !!totals) || undefined}
    >
      {totals ? (
        <div
          className="collection-inspector accounts-inspector-frame"
          data-entry-inspector={(dockedEntry && !!details && !!entryInspection?.id) || undefined}
          data-compact={!desktopInspector || undefined}
          data-selected={(desktopInspector && !!details) || undefined}
          onKeyDown={(event) => {
            if (
              desktopInspector &&
              details &&
              event.key === "Escape" &&
              !event.defaultPrevented &&
              !(event.target instanceof Element && event.target.closest('[role="dialog"]'))
            ) {
              event.stopPropagation();
              if (entryInspection?.id) {
                if (!entryInspection.pending) entryInspection.onClose();
              } else closeDetails();
            }
          }}
        >
          <InspectorCollection>{collection}</InspectorCollection>
          {desktopInspector && inspector}
          <AccountsEntryInspector docked={dockedEntry} />
        </div>
      ) : (
        collection
      )}
    </div>
  );
}
