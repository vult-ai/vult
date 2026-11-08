import type { Book } from "./api";
import { bookPath, readBookRoute, resolveBookRoute } from "./book-routing";

export type ResourceKind = "entry" | "source";
export const readableReference = /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const selections = [
  "entryPreview",
  "entryBook",
  "entry",
  "sourcePreview",
  "sourceBook",
  "source",
  "document",
  "occurrence",
];

export function resourceSelection(
  book: Book,
  kind: ResourceKind,
  href = window.location.href,
) {
  const url = new URL(href),
    route = readBookRoute(url.pathname);
  const page = kind === "entry" ? "activity" : "sources";
  const names =
    kind === "entry"
      ? ["entryPreview", "entry"]
      : ["sourcePreview", "source", "document", "occurrence"];
  if (
    route.resource &&
    [page, kind === "entry" ? "transactions" : "documents"].includes(route.page)
  ) {
    if (resolveBookRoute([book], url.pathname)?.id !== book.id) return "";
    const existing = names
      .flatMap((key) => url.searchParams.getAll(key))
      .filter(Boolean);
    const oldScope = url.searchParams.get(
      kind === "entry" ? "entryBook" : "sourceBook",
    );
    if (
      (oldScope && oldScope !== book.id) ||
      existing.some(
        (value) => value.toUpperCase() !== route.resource!.toUpperCase(),
      )
    )
      throw new Error("This link isn’t available.");
    return route.resource;
  }
  if (route.resource) return "";
  const scope = url.searchParams.get(
    kind === "entry" ? "entryBook" : "sourceBook",
  );
  if (scope && scope !== book.id) return "";
  if (!scope && !route.locator) return "";
  const selected = names
    .flatMap((key) => url.searchParams.getAll(key))
    .filter(Boolean);
  if (!selected.length) return "";
  if (
    new Set(selected).size !== 1 ||
    (!uuid.test(selected[0]) &&
      !readableReference.test(selected[0].toUpperCase()))
  )
    throw new Error("This link isn’t available.");
  return selected[0];
}

export function resourceURL(
  book: Book,
  kind: ResourceKind,
  reference: string,
  href = window.location.href,
) {
  const url = new URL(href);
  url.pathname =
    bookPath(book, kind === "entry" ? "activity" : "sources") +
    (reference ? `/${encodeURIComponent(reference.toUpperCase())}` : "");
  for (const key of [...selections, "book", "book_id"])
    url.searchParams.delete(key);
  return url;
}

export function clearResourceSelection(url: URL, kind: ResourceKind) {
  const route = readBookRoute(url.pathname);
  const pages =
    kind === "entry" ? ["activity", "transactions"] : ["sources", "documents"];
  if (route.resource && pages.includes(route.page))
    url.pathname = url.pathname.slice(0, url.pathname.lastIndexOf("/"));
  for (const key of kind === "entry"
    ? ["entryPreview", "entryBook", "entry"]
    : ["sourcePreview", "sourceBook", "source", "document", "occurrence"])
    url.searchParams.delete(key);
  return url;
}
