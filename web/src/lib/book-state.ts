import { hydrateBookCatalog, resolveBookRoute } from "./book-routing";
import { readWorkspace, type Workspace } from "./workspace";
import { get, today, type BookList, type State } from "./api";
import { dashboardRange, type DashboardRange } from "./dashboard-period";
import { calendarUnit, parseDashboardSnapshot, type DashboardSnapshotWire } from "./dashboard-projection";
import { cachedInstitutionCatalog, configuredSharedAssetOrigin, institutionAssetURL, readInstitutionCatalog } from "./shared-institutions";
import { activityBrand } from "./activity-brands";
import { warmCachedMark } from "./circular-mark-fit";
import {
  dashboardLocationBook,
  dashboardLocationRange,
  isDashboardLocation,
  resolveDashboardLocationBook,
  dashboardLocationMatchesBook,
  rememberedDashboardSelection,
  readDashboardLocation,
} from "./dashboard-location";

export const activeBookStorage = "vult:active-book:v1";
export function rememberedBookId() {
  const linked =
    isDashboardLocation() &&
    new URLSearchParams(window.location.search).get("book_id");
  if (linked) return linked;
  try {
    return localStorage.getItem(activeBookStorage) || "";
  } catch {
    return "";
  }
}

export type InitialBooks = {
  catalog: BookList;
  state?: State;
};

/** Discover UUIDs before any financial read; download application code in parallel. */
export async function readInitialBooks(): Promise<InitialBooks> {
  const catalog = hydrateBookCatalog(await get<BookList>("books"));
  const linked =
    resolveBookRoute(catalog.books) ||
    (isDashboardLocation() &&
      resolveDashboardLocationBook(catalog.books));
  const remembered = linked
    ? linked.id
    : catalog.book_aliases?.[rememberedBookId()] || rememberedBookId();
  if (
    isDashboardLocation() &&
    dashboardLocationBook(window.location.search) &&
    !linked
  )
    throw new Error(
      "The linked Book is unavailable. Open a Book you can access.",
    );
  const selected =
    catalog.books.find((book) => book.id === remembered) || catalog.books[0];
  return {
    catalog,
    state: selected && selected.onboarding_state !== "pending"
      ? await readBookState(
          selected.id,
          {},
          selected.accounting_timezone,
          readDashboardLocation(
            (selected.coverage_start_date || selected.start_date),
            today(selected.accounting_timezone),
            selected,
          ).range,
        )
      : undefined,
  };
}

export type BookFilters = {
  account?: string;
  search: string;
  kind: string;
  from: string;
  to: string;
};

/** Startup and later reads share one request shape; all amounts stay server-owned. */
export async function readBookState(
  bookId: string,
  filters: Partial<BookFilters> = {},
  timeZone = "UTC",
  selection:
    | DashboardRange
    | {
        view: Workspace;
        limit?: number;
        offset?: number;
        /** The revision of the first page; later pages and the summary bind to it. */
        revision?: string;
        signal?: AbortSignal;
        range?: DashboardRange;
      } = { view: readWorkspace() },
): Promise<State> {
  if (!bookId)
    throw new Error("Select a Book before reading financial records.");
  const page =
    "view" in selection
      ? selection
      : { view: readWorkspace(), range: selection };
  const confirmedRange = page.range;
  // Books and Approvals read Book metadata through views the server has.
  const view =
    page.view === "books"
        ? "settings"
        : page.view === "approvals"
          ? "documents"
          : page.view;
  const date = today(timeZone);
  const linkedRange =
    isDashboardLocation() &&
    dashboardLocationMatchesBook({ id: bookId, name: "" }) &&
    dashboardLocationRange(window.location.search);
  const range =
    confirmedRange ||
    linkedRange ||
    rememberedDashboardSelection(bookId)?.range ||
    dashboardRange("last-30-days", date, date);
  // Transaction filters belong to Activity only; positions and metadata reads
  // refuse them rather than silently narrowing.
  const activity = view === "transactions";
  const query = new URLSearchParams({
    dashboard_from: range.from,
    dashboard_to: range.to,
    ...(activity
      ? Object.fromEntries(Object.entries({ account: "", kind: "", from: "", to: "", ...filters }).filter(([, v]) => v).map(([k, v]) => [k === "search" ? "q" : k, v]))
      : {}),
    view,
    ...(view === "dashboard" ? { dashboard_unit: calendarUnit(range.from, range.to) } : {}),
    ...(["documents", "settings", "statements"].includes(view) ? { bootstrap: "1" } : {}),
    ...(activity
      ? { limit: String(page.limit ?? 25), offset: String(page.offset ?? 0), ...(page.revision ? { revision: page.revision } : {}) }
      : {}),
    book_id: bookId,
  });
  if (view === "transactions" && typeof document !== "undefined") {
    const base = configuredSharedAssetOrigin();
    if (base) void readInstitutionCatalog(base, false, "brand-logos.v1.json").catch(() => {});
  }
  const { dashboard_snapshot, ...state } = await get<Omit<State, "dashboard_snapshot"> & {
    dashboard_snapshot?: DashboardSnapshotWire;
  }>(`state?${query}`, page.signal);
  if (state.book?.id !== bookId)
    throw new Error(
      "The selected Book could not be confirmed. Retry opening it.",
    );
  if (view === "transactions" && typeof document !== "undefined") {
    const base = configuredSharedAssetOrigin();
    const catalog = cachedInstitutionCatalog(base, "brand-logos.v1.json");
    const warm = catalog ? state.transactions.flatMap(row => {
      const institution = activityBrand(row.brand, catalog.institutions);
      const ready = institution && warmCachedMark(institutionAssetURL(base, institution));
      return ready ? [ready] : [];
    }) : [];
    if (warm.length) {
      let timer: ReturnType<typeof setTimeout>;
      try {
        await Promise.race([Promise.all(warm),new Promise(resolve=>{timer=setTimeout(resolve,64);})]);
      } finally { clearTimeout(timer!); }
    }
  }
  if (view === "dashboard" && state.capabilities?.dashboard !== false) {
    return { ...state, dashboard_snapshot: parseDashboardSnapshot(dashboard_snapshot, state.book, range, state.state_revision) };
  }
  return state;
}
