import { APIError, bookURL, get } from "./api";

/** One group of Book search results. `total` is null when only "more" is known.
 * `status: "not_recorded"` marks a group the ledger has no facts for. */
export type SearchGroup<T> = { items: T[]; total: number | null; has_more?: boolean; status?: string };

export type BookSearchResult = {
  query: string;
  state_revision?: string;
  transactions: SearchGroup<{
    id: string;
    date: string;
    /** Absent until the ledger records counterparties; none is inferred. */
    counterparty?: string;
    description: string;
    kind?: "journal" | "reversal";
    /** Integer minor units, read with amount_role. "book_debit_total" is the
     * entry's unsigned Book-currency debit total, as in Activity: not money in or out. */
    amount_minor: string;
    amount_role?: string;
    accounts?: string[];
  }>;
  accounts: SearchGroup<{ id: string; code: string; name: string; class: string }>;
  counterparties: SearchGroup<{ name: string; entries: number }>;
  sources: SearchGroup<{ id: string; name: string; kind: string; date: string }>;
};

/** Queries the server accepts: at most 200 characters and 8 words. */
export const SEARCH_LIMITS = { characters: 200, words: 8 } as const;

export function searchable(query: string) {
  const text = query.trim();
  return (
    !!text &&
    [...text].length <= SEARCH_LIMITS.characters &&
    [...text.matchAll(/"([^"]*)"?|(\S+)/g)].filter((term) => (term[1] ?? term[2]).trim()).length <= SEARCH_LIMITS.words
  );
}

const GROUPS = ["transactions", "accounts", "counterparties", "sources"] as const;

/** Book-wide search, the same operation agents use through MCP. A response
 * without every group is a failure, never a partial result. */
export async function searchBook(
  bookId: string,
  query: string,
  signal?: AbortSignal,
  limit = 5,
) {
  const params = new URLSearchParams({ q: query.trim(), limit: String(limit) });
  const result = await get<BookSearchResult>(bookURL(`search?${params}`, bookId), signal);
  if (!result || GROUPS.some((group) => !Array.isArray(result[group]?.items)))
    throw new Error("Search returned an unexpected response.");
  return result;
}


/** Fetch the complete account selection only when opening the filtered chart. */
export async function readAccountSearchIDs(bookId: string, query: string, signal: AbortSignal, stateRevision?: string) {
  const params = new URLSearchParams({ q: query.trim(), types: "accounts", account_ids: "1" });
  const result = await get<BookSearchResult & { account_ids?: string[] }>(bookURL(`search?${params}`, bookId), signal);
  if (stateRevision && result?.state_revision !== stateRevision)
    throw new APIError("The Book changed. Refresh to see the latest accounts.", 409);
  const ids = result?.account_ids ?? [];
  if (!Array.isArray(ids) || ids.length > 10_000 || ids.length !== result?.accounts?.total ||
      ids.some((id) => typeof id !== "string" || !id) || new Set(ids).size !== ids.length)
    throw new Error("The matching accounts could not be confirmed.");
  return new Set(ids);
}
