import { get, post, type Book, type BookList } from "./api";
import {
  dashboardPreferenceSaved,
  setDashboardPreferenceScope,
  type DashboardSelection,
} from "./dashboard-location";
export type BookAddress = {
  book_id: string;
  code: string;
  slug: string;
  revision: number;
  aliases: string[];
};
export type RoutableBook = Pick<Book, "id" | "name"> & {
  address?: BookAddress;
};
const pages = new Set([
  "dashboard",
  "accounts",
  "activity",
  "transactions",
  "sources",
  "documents",
  "statements",
  "settings",
  "books",
  "approvals",
]);
const reserved = new Set([
  ...pages,
  "api",
  "assets",
  "auth",
  "login",
  "logout",
  "register",
  "healthz",
  "readyz",
  "admin",
  "tests",
  "public",
  "help",
  "docs",
]);
const addressBook = new Map<string, BookAddress>();
let legacyBookAliases: Record<string, string> = {};
export function legacyBookID(ref: string) {
  return legacyBookAliases[ref] || ref;
}
export function legacyBookIDsForPrefix(prefix: string) {
  return Object.entries(legacyBookAliases)
    .filter(([id]) => id.startsWith(prefix))
    .map(([, id]) => id);
}
export function registerBookAddresses(addresses: readonly BookAddress[]) {
  const next = new Map<string, BookAddress>(),
    locators = new Map<string, string>();
  for (const value of addresses) {
    if (
      !value ||
      typeof value.book_id !== "string" ||
      !/^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$/.test(value.code) ||
      reserved.has(value.code.toLowerCase()) ||
      typeof value.slug !== "string" ||
      !Number.isSafeInteger(value.revision) ||
      value.revision < 0 ||
      !Array.isArray(value.aliases) ||
      value.aliases.some(
        (a) =>
          typeof a !== "string" ||
          !/^[a-z0-9]+(-[a-z0-9]+)*$/.test(a) ||
          a.length < 2 ||
          a.length > 63,
      ) ||
      !value.aliases.includes(value.code.toLowerCase()) ||
      (value.slug && !value.aliases.includes(value.slug)) ||
      next.has(value.book_id)
    )
      throw new Error("We couldn’t verify the Book addresses.");
    for (const locator of [
      value.code.toLowerCase(),
      value.slug,
      ...value.aliases,
    ].filter(Boolean)) {
      if (locators.has(locator) && locators.get(locator) !== value.book_id)
        throw new Error("We couldn’t verify the Book addresses.");
      locators.set(locator, value.book_id);
    }
    next.set(value.book_id, value);
  }
  addressBook.clear();
  for (const [id, address] of next) addressBook.set(id, address);
}
let routeLocked = false;
export function setBookNavigationLocked(value: boolean) {
  routeLocked = value;
}
export function bookNavigationLocked() {
  return routeLocked;
}

export function updateBookAddress(address: BookAddress) {
  registerBookAddresses(
    [...addressBook.values()]
      .filter((a) => a.book_id !== address.book_id)
      .concat(address),
  );
}

export function bookAddress(book: RoutableBook) {
  return addressBook.get(book.id) || book.address;
}
export function bookLocator(book: RoutableBook) {
  const address = bookAddress(book);
  return address?.slug || address?.code;
}
export function readBookRoute(pathname = window.location.pathname): {
  locator: string;
  page: string;
  resource?: string;
  invalid?: boolean;
} {
  let parts: string[];
  try {
    parts = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  } catch {
    return { locator: "", page: "dashboard", invalid: true };
  }
  if (!parts.length) return { locator: "", page: "dashboard" };
  if (parts.length === 1 && pages.has(parts[0]))
    return { locator: "", page: parts[0] };
  if (
    reserved.has(parts[0]) ||
    parts[0].startsWith("@") ||
    !/^[a-zA-Z0-9-]{2,63}$/.test(parts[0]) ||
    parts.length > 3 ||
    (parts[2] &&
      (!["activity", "transactions", "sources", "documents"].includes(
        parts[1],
      ) ||
        !/^(?:[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/i.test(
          parts[2],
        ))) ||
    (parts[1] && !pages.has(parts[1]))
  )
    return { locator: "", page: "dashboard", invalid: true };
  return {
    locator: parts[0],
    page: parts[1] || "dashboard",
    ...(parts[2] ? { resource: parts[2] } : {}),
  };
}
export function bookPath(book: RoutableBook, page = "dashboard") {
  const tail =
    page === "transactions"
      ? "activity"
      : page === "documents"
        ? "sources"
        : page;
  const locator = bookLocator(book);
  return locator
    ? `/${encodeURIComponent(locator)}${tail === "dashboard" ? "" : `/${tail}`}`
    : `/${tail}`;
}
/** Shown when an address names no Book the owner can open. */
export const missingBookLink = "This Book link isn’t available.";

export function resolveBookRoute<T extends RoutableBook>(
  books: readonly T[],
  pathname = window.location.pathname,
) {
  const route = readBookRoute(pathname);
  if (route.invalid) throw new Error(missingBookLink);
  if (!route.locator) return undefined;
  const key = route.locator.toLowerCase();
  const matches = books.filter((book) => {
    const address = bookAddress(book);
    return (
      address &&
      [address.code, address.slug, ...address.aliases].some(
        (v) => v.toLowerCase() === key,
      )
    );
  });
  if (matches.length !== 1) throw new Error(missingBookLink);
  return matches[0];
}

export function hydrateBookCatalog(
  catalog: BookList & {
    addresses?: BookAddress[];
    view_scope?: string;
    view_scope_local?: boolean;
    views?: Record<string, DashboardSelection>;
  },
): BookList {
  legacyBookAliases = catalog.book_aliases || {};
  const allowed = new Set(catalog.books.map((book) => book.id));
  registerBookAddresses(
    (catalog.addresses || []).filter((address) => allowed.has(address.book_id)),
  );
  // The owner's Dashboard period is saved for them across sessions when the
  // catalog names their preference scope; otherwise it stays in this browser.
  persistScope = catalog.view_scope || "";
  setDashboardPreferenceScope(
    persistScope || "browser",
    catalog.views,
    catalog.view_scope_local,
  );
  return {
    ...catalog,
    books: catalog.books.map((book) =>
      bookAddress(book) ? { ...book, address: bookAddress(book) } : book,
    ),
  };
}

export function canonicalBookURL(
  book: RoutableBook,
  page: string,
  href = window.location.href,
) {
  const url = new URL(href);
  const route = readBookRoute(url.pathname);
  const target =
    page === "transactions"
      ? "activity"
      : page === "documents"
        ? "sources"
        : page;
  let retain = false;
  try {
    retain =
      !!route.resource &&
      [target, page].includes(route.page) &&
      resolveBookRoute([book], url.pathname)?.id === book.id;
  } catch {
    /* A Book switch cannot retain another Book's inspector. */
  }
  url.pathname =
    bookPath(book, page) +
    (retain ? `/${encodeURIComponent(route.resource!)}` : "");
  if (bookLocator(book)) {
    url.searchParams.delete("book");
    url.searchParams.delete("book_id");
  }
  return url;
}

let persistScope = "";
const viewWrites = new Map<string, Promise<unknown>>();
if (typeof window !== "undefined")
  window.addEventListener("vult:remember-period", ((
    event: CustomEvent<{ bookId: string; selection: DashboardSelection }>,
  ) => {
    if (!persistScope) return;
    const scope = persistScope,
      { bookId, selection } = event.detail;
    // One save at a time per Book, in the order chosen; a failed save leaves the
    // choice in this browser and is retried by the next change.
    const previous = viewWrites.get(bookId) || Promise.resolve();
    const next = previous
      .catch(() => undefined)
      .then(() => {
        if (scope !== persistScope) return;
        return post("book-view-preference", {
          book_id: bookId,
          scope,
          selection,
        }).then(() => dashboardPreferenceSaved(scope, bookId, selection));
      })
      .catch(() => undefined);
    viewWrites.set(bookId, next);
    void next.finally(() => {
      if (viewWrites.get(bookId) === next) viewWrites.delete(bookId);
    });
  }) as EventListener);
