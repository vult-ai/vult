import { APIError, get, isConfirmedRejection, type Book } from "./api";
import { settingsPost } from "./settings-requests";

/** One settings change, frozen when it is first sent. Retrying sends these
 * exact bytes under the same key, so the server can never apply it twice. */
export type SettingsRequest = {
  book_id: string;
  idempotency_key: string;
  revision: number;
  name: string;
  founder: string;
};

export type SettingsOutcome =
  | { status: "recorded"; book: Book }
  // Sent, but no answer came back. It may or may not have been applied.
  | { status: "unresolved"; request: SettingsRequest; error: unknown }
  | { status: "rejected"; error: unknown };

const storageKey = (bookId: string) => `vult.settings-request.${bookId}`;

export function settingsRequest(book: Book, name: string, founder: string): SettingsRequest {
  return {
    book_id: book.id,
    idempotency_key: crypto.randomUUID(),
    // A Book that has never been revised is at revision 0.
    revision: book.metadata_revision ?? 0,
    name,
    founder,
  };
}

/** A request whose answer was lost survives a reload, so it can still be checked. */
export function retainedSettingsRequest(bookId: string): SettingsRequest | null {
  try {
    const raw = window.sessionStorage.getItem(storageKey(bookId));
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<SettingsRequest>;
    return value &&
      value.book_id === bookId &&
      typeof value.idempotency_key === "string" &&
      Number.isSafeInteger(value.revision) &&
      typeof value.name === "string" &&
      typeof value.founder === "string"
      ? (value as SettingsRequest)
      : null;
  } catch {
    return null;
  }
}
function retain(request: SettingsRequest | null, bookId: string) {
  try {
    if (request) window.sessionStorage.setItem(storageKey(bookId), JSON.stringify(request));
    else window.sessionStorage.removeItem(storageKey(bookId));
  } catch {}
}

function isBook(value: unknown, request: SettingsRequest): value is Book {
  const b = value as Partial<Book> | null;
  return !!b && b.id === request.book_id && typeof b.name === "string" && Number.isSafeInteger(b.metadata_revision);
}

/** Sends the frozen request. Only a confirmed rejection releases it; anything
 * else leaves it unresolved, to be checked or sent again unchanged. */
export async function sendSettings(request: SettingsRequest): Promise<SettingsOutcome> {
  retain(request, request.book_id);
  try {
    const book = await settingsPost<unknown>("settings", request);
    if (!isBook(book, request)) throw new Error("The saved settings could not be read back.");
    retain(null, request.book_id);
    return { status: "recorded", book };
  } catch (error) {
    // For this request a conflict is final: either the Book changed since it
    // was read, or the key was already used for different values. Neither
    // applied this change.
    if (isConfirmedRejection(error) || (error instanceof APIError && error.status === 409)) {
      retain(null, request.book_id);
      return { status: "rejected", error };
    }
    return { status: "unresolved", request, error };
  }
}

/** Asks whether a request whose answer was lost was applied. */
export async function checkSettings(request: SettingsRequest): Promise<SettingsOutcome> {
  try {
    const outcome = await get<{ status?: string; result?: unknown }>(
      `book-request-outcome?${new URLSearchParams({ book_id: request.book_id, key: request.idempotency_key })}`,
    );
    if (outcome?.status === "recorded" && isBook(outcome.result, request)) {
      retain(null, request.book_id);
      return { status: "recorded", book: outcome.result };
    }
    // Not recorded yet is still unresolved: it may be in flight.
    return { status: "unresolved", request, error: undefined };
  } catch (error) {
    return { status: "unresolved", request, error };
  }
}

/** The person chose to drop an unresolved change. Only the retained copy
 * is forgotten; if it was applied, the Book already shows it. */
export function forgetSettings(request: SettingsRequest) {
  retain(null, request.book_id);
}
