import {
  dashboardPeriods,
  dashboardRange,
  isDashboardPeriod,
  validDashboardRange,
  type DashboardSelection,
  type DashboardRange,
  type DashboardPeriod,
} from "./dashboard-period";
import type { Book } from "./api";
import { rangeToken, tokenRange } from "./period-step";
import {
  legacyBookID,
  legacyBookIDsForPrefix,
  bookAddress,
  bookLocator,
  bookPath,
  readBookRoute,
  resolveBookRoute,
  canonicalBookURL,
} from "./book-routing";
type LinkedBook = Pick<Book, "id" | "name"> & Pick<Partial<Book>, "address">;
const bookName = (name: string) =>
  name
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
export const dashboardBookReference = (book: LinkedBook) =>
  bookLocator(book) ||
  `${bookName(book.name) || "book"}.${book.id.slice(0, 8)}`;
export type { DashboardSelection } from "./dashboard-period";
const presetToURL: Record<DashboardPeriod, string> = {
  today: "today",
  yesterday: "yesterday",
  "last-7-days": "7d",
  "last-30-days": "30d",
  "last-90-days": "90d",
  week: "week",
  "last-week": "last-week",
  month: "month",
  "last-month": "last-month",
  quarter: "quarter",
  "last-quarter": "last-quarter",
  year: "year",
  "last-year": "last-year",
  "year-to-date": "ytd",
  "last-12-months": "12m",
  all: "all",
};
const urlPreset = (value: string | null): DashboardPeriod | undefined =>
  value
    ? (Object.entries(presetToURL).find(([, v]) => v === value)?.[0] as
        DashboardPeriod | undefined) ||
      (isDashboardPeriod(value) ? value : undefined)
    : undefined;
let preferenceScope = "browser";
let legacyPreferenceAllowed = true;
const serverSelections = new Map<string, DashboardSelection>();
export function setDashboardPreferenceScope(
  scope: string,
  selections: Record<string, DashboardSelection> = {},
  localUser = false,
) {
  preferenceScope = scope;
  legacyPreferenceAllowed = localUser || scope === "browser";
  serverSelections.clear();
  for (const [id, selection] of Object.entries(selections))
    if (
      validDashboardRange(selection.range) &&
      (selection.period === "custom" || isDashboardPeriod(selection.period))
    )
      serverSelections.set(id, selection);
}
const selectionStorage = (id: string) =>
  `vult:dashboard-selection:v2:${encodeURIComponent(preferenceScope)}:${id}`;
export function rememberedDashboardSelection(
  bookId: string,
): DashboardSelection | undefined {
  try {
    const raw =
      localStorage.getItem(selectionStorage(bookId)) ||
      (legacyPreferenceAllowed
        ? localStorage.getItem(
            `vult:dashboard-selection:v2:browser:${bookId}`,
          ) || localStorage.getItem(`vult:dashboard-selection:v1:${bookId}`)
        : null);
    const saved = JSON.parse(raw || "null");
    if (
      saved?.bookId === bookId &&
      validDashboardRange(saved.range) &&
      (saved.period === "custom" || isDashboardPeriod(saved.period))
    ) {
      if (!saved.pending && serverSelections.has(bookId))
        return serverSelections.get(bookId);
      return {
        period: saved.period,
        range: { from: saved.range.from, to: saved.range.to },
      };
    }
  } catch {
    /* A URL remains usable when browser storage is unavailable. */
  }
  return serverSelections.get(bookId);
}
export function rememberDashboardSelection(
  book: LinkedBook,
  selection: DashboardSelection,
) {
  if (!book.id || !validDashboardRange(selection.range)) return;
  selection = {
    period: selection.period,
    range: { from: selection.range.from, to: selection.range.to },
  };
  serverSelections.set(book.id, selection);
  try {
    localStorage.setItem(
      selectionStorage(book.id),
      JSON.stringify({
        bookId: book.id,
        ...selection,
        pending: preferenceScope !== "browser",
      }),
    );
  } catch {
    /* The current report remains usable. */
  }
  window.dispatchEvent(
    new CustomEvent("vult:remember-period", {
      detail: { bookId: book.id, selection },
    }),
  );
}
/** A server save finished: this browser's copy is no longer ahead of the account. */
export function dashboardPreferenceSaved(
  scope: string,
  bookId: string,
  selection: DashboardSelection,
) {
  if (scope !== preferenceScope) return;
  const current = serverSelections.get(bookId);
  if (
    !current ||
    current.period !== selection.period ||
    current.range.from !== selection.range.from ||
    current.range.to !== selection.range.to
  )
    return;
  try {
    localStorage.setItem(
      selectionStorage(bookId),
      JSON.stringify({ bookId, ...selection, pending: false }),
    );
  } catch {
    /* Cache is optional. */
  }
}
export function dashboardLocationRange(
  search: string,
): DashboardRange | undefined {
  const q = new URLSearchParams(search);
  // A whole year, quarter or month reads as ?range=2020, 2020-Q3 or 2020-10.
  const whole = q.has("range") ? tokenRange(q.get("range") || "") : undefined;
  if (whole) return validDashboardRange(whole) ? whole : undefined;
  const compact = q.get("range")?.split("..");
  const from = compact
    ? compact.length === 2
      ? compact[0]
      : ""
    : q.get("from") || "";
  const to = compact
    ? compact.length === 2
      ? compact[1]
      : ""
    : q.get("to") || "";
  return validDashboardRange({ from, to }) ? { from, to } : undefined;
}
export function dashboardLocationBook(search: string) {
  const q = new URLSearchParams(search),
    id = q.get("book") || q.get("book_id") || "";
  return /^[a-zA-Z0-9.-]{1,160}$/.test(id) ? id : "";
}
export function resolveDashboardLocationBook<T extends LinkedBook>(
  books: readonly T[],
  search = window.location.search,
) {
  const addressed = resolveBookRoute(books),
    query = new URLSearchParams(search);
  for (const key of ["book", "book_id"])
    if (query.getAll(key).length > 1)
      throw new Error("Choose one Book for this view.");
  const refs = [query.get("book"), query.get("book_id")].filter(
    (v): v is string => v !== null,
  );
  const resolved = refs.map((ref) => {
    if (!ref || !/^[a-zA-Z0-9._-]{1,160}$/.test(ref))
      throw new Error("This Book link isn’t available.");
    const exact = books.find((b) => b.id === legacyBookID(ref));
    const suffix = ref.includes(".") ? ref.slice(ref.lastIndexOf(".") + 1) : "";
    const matches = exact
      ? [exact]
      : books.filter((b) =>
          suffix
            ? b.id.startsWith(suffix) ||
              legacyBookIDsForPrefix(suffix).includes(b.id)
            : bookName(b.name) === ref ||
              dashboardBookReference(b).toLowerCase() === ref.toLowerCase(),
        );
    if (matches.length !== 1)
      throw new Error("This Book link isn’t available.");
    return matches[0];
  });
  const identities = new Set(
    [addressed?.id, ...resolved.map((b) => b.id)].filter(Boolean),
  );
  if (identities.size > 1)
    throw new Error("This link selects two different Books.");
  return addressed || resolved[0];
}

export function dashboardLocationMatchesBook(book: LinkedBook) {
  const route = readBookRoute();
  if (route.locator) {
    const address = bookAddress(book);
    return (
      !!address &&
      [address.code, address.slug, ...address.aliases].some(
        (v) => v.toLowerCase() === route.locator.toLowerCase(),
      )
    );
  }
  try {
    return (
      !dashboardLocationBook(window.location.search) ||
      resolveDashboardLocationBook([book])?.id === book.id
    );
  } catch {
    return false;
  }
}
function validateDateQuery(q: URLSearchParams) {
  for (const key of [
    "from",
    "to",
    "range",
    "period",
    "preset",
    "book",
    "book_id",
  ])
    if (q.getAll(key).length > 1)
      throw new Error("Choose one date range for this view.");
  const explicit = q.has("from") || q.has("to"),
    compact = q.has("range");
  const legacy = !readBookRoute().locator;
  if (
    (explicit && compact) ||
    ((explicit || compact) && q.has("period") && !legacy) ||
    (q.has("preset") && q.has("period"))
  )
    throw new Error("Choose either a period or exact dates.");
  if ((explicit || compact) && !dashboardLocationRange(q.toString()))
    throw new Error("Choose a valid start and end date.");
  if (q.has("period") && !urlPreset(q.get("period")))
    throw new Error("Choose a supported period.");
}
export function readDashboardLocation(
  start: string,
  today: string,
  book?: LinkedBook,
): DashboardSelection {
  const q = new URLSearchParams(window.location.search);
  if (isDashboardLocation()) validateDateQuery(q);
  const applies =
    isDashboardLocation() && (!book || dashboardLocationMatchesBook(book));
  const range = applies && dashboardLocationRange(window.location.search);
  if (range) {
    if (
      q.has("from") &&
      !q.has("range") &&
      !q.has("preset") &&
      !q.has("period")
    )
      return { period: "custom", range };
    const intended = q.get("preset") || urlPreset(q.get("period"));
    const match = dashboardPeriods.find(({ value }) => {
      const r = dashboardRange(value, start, today);
      return r.from === range.from && r.to === range.to;
    });
    const period =
      intended && (intended === "custom" || isDashboardPeriod(intended))
        ? intended
        : match?.value || "custom";
    if (period !== "custom") {
      const r = dashboardRange(period, start, today);
      if (r.from !== range.from || r.to !== range.to)
        return { period: "custom", range };
    }
    return { period, range };
  }
  const preset = applies && urlPreset(q.get("period"));
  if (preset)
    return { period: preset, range: dashboardRange(preset, start, today) };
  const saved = book && rememberedDashboardSelection(book.id);
  if (saved)
    return saved.period === "custom"
      ? saved
      : {
          period: saved.period,
          range: dashboardRange(saved.period, start, today),
        };
  // A Book opens on the last 30 days, compared with the 30 before; a period
  // the owner chooses is remembered for that Book instead.
  const fallback = "last-30-days";
  return { period: fallback, range: dashboardRange(fallback, start, today) };
}
export function isDashboardLocation() {
  const r = readBookRoute();
  return !r.invalid && ["dashboard", "accounts", "statements"].includes(r.page);
}
const dateKeys = ["range", "preset", "period", "from", "to"];
function clearInactiveReportQuery(url: URL) {
  const page = readBookRoute(url.pathname).page;
  if (page !== "dashboard" && page !== "accounts") return;
  for (const key of ["as_of", "report", "compare", "compare_from", "compare_to", "review_report", "columns"])
    url.searchParams.delete(key);
}
export function writeDashboardLocation(
  book: LinkedBook,
  selection: DashboardSelection,
  mode: "push" | "replace",
) {
  rememberDashboardSelection(book, selection);
  if (!isDashboardLocation()) return;
  const url = canonicalBookURL(book, readBookRoute().page);
  clearInactiveReportQuery(url);
  url.searchParams.delete("book_id");
  // Preserve a bare personal entrypoint on initial confirmation. Fixed links keep
  // exact dates even if those dates happen to equal a current preset.
  const bare = !dateKeys.some((key) => url.searchParams.has(key));
  const fixed = url.searchParams.has("from") || url.searchParams.has("range");
  for (const key of dateKeys) url.searchParams.delete(key);
  if (!(mode === "replace" && bare && selection.period !== "custom")) {
    if (selection.period === "custom" || (mode === "replace" && fixed)) {
      setDates(url, selection.range);
    } else url.searchParams.set("period", presetToURL[selection.period]);
  }
  if (!bookLocator(book))
    url.searchParams.set("book", dashboardBookReference(book));
  if (url.href !== window.location.href)
    window.history[mode === "push" ? "pushState" : "replaceState"](
      window.history.state,
      "",
      url,
    );
}
/** A fixed reporting window, not an immutable snapshot of ledger contents. */
export function currentViewURL(
  book: LinkedBook,
  range: DashboardRange,
  href = window.location.href,
) {
  if (!validDashboardRange(range))
    throw new Error("Wait for this view to finish loading.");
  const url = canonicalBookURL(
    book,
    readBookRoute(new URL(href).pathname).page,
    href,
  );
  clearInactiveReportQuery(url);
  for (const key of [...dateKeys, "book", "book_id"])
    url.searchParams.delete(key);
  if (!bookLocator(book))
    url.searchParams.set("book", dashboardBookReference(book));
  setDates(url, range);
  return url.href;
}
/** Exact dates in a link: a whole year, quarter or month as one short token,
 * any other range as from and to. */
function setDates(url: URL, range: DashboardRange) {
  const token = rangeToken(range);
  if (token) url.searchParams.set("range", token);
  else {
    url.searchParams.set("from", range.from);
    url.searchParams.set("to", range.to);
  }
}
