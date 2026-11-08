import type { Book, Transaction } from "./api";
import { useResourcePreview } from "./use-resource-preview";
import { clearResourceSelection } from "./resource-links";

export function useEntryPreview(
  book: Book,
  pending: boolean,
  entries: readonly Transaction[] = [],
) {
  return useResourcePreview(book, "entry", pending, entries);
}
export function clearEntryPreview(url: URL) {
  return clearResourceSelection(url, "entry");
}
