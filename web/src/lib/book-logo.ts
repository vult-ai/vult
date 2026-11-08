import type { Book } from "./api";
import { fetchResponse, readResponse } from "./errors";
import { writeHeaders } from "./session";
import { readProfilePhoto } from "./user-profile";

/** The Book's own image: a company logo, or a person's photo for a personal Book.
 * Stored by the engine as a 256 by 256 JPEG; referenced by id and digest. */
export type BookLogo = { id: string; sha256: string; content_type: "image/jpeg" | "image/png"; size: number };

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);

/** The Book's logo reference, when the engine returned a valid one. */
export function bookLogo(book: Pick<Book, "logo">): BookLogo | undefined {
  const v = book.logo;
  if (!object(v) || typeof v.id !== "string" || !uuid.test(v.id) || typeof v.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(v.sha256) ||
    (v.content_type !== "image/jpeg" && v.content_type !== "image/png") || !Number.isInteger(v.size) || (v.size as number) < 1 || (v.size as number) > 147456)
    return undefined;
  return v as BookLogo;
}

/** Where the Book's logo is read; the digest makes a changed logo a new address. */
export function bookLogoURL(book: Pick<Book, "id" | "logo">) {
  const logo = bookLogo(book);
  return logo ? `/api/v1/book-identity/logo/${logo.id}?book_id=${encodeURIComponent(book.id)}&revision=${logo.sha256}` : undefined;
}

/** Sets the Book's logo as the signed-in owner. The image is cropped square and
 * scaled to 256 pixels in the browser, as the profile photo is, then sent as JPEG. */
export async function saveBookLogo(book: Pick<Book, "id">, file: File, signal?: AbortSignal): Promise<BookLogo> {
  const dataURL = await readProfilePhoto(file);
  const bytes = Uint8Array.from(atob(dataURL.slice(dataURL.indexOf(",") + 1)), (c) => c.charCodeAt(0));
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (b) => b.toString(16).padStart(2, "0")).join("");
  const response = await fetchResponse(`/api/v1/book-identity/logo?book_id=${encodeURIComponent(book.id)}`, {
    method: "PUT", signal,
    headers: { ...(await writeHeaders(false)), "Content-Type": "image/jpeg", "X-Vult-Image-SHA256": digest },
    body: bytes,
  });
  const value = await readResponse<unknown>(response, "write");
  const logo = object(value) ? bookLogo({ logo: value.logo as Book["logo"] }) : undefined;
  if (!logo || logo.sha256 !== digest) throw new Error("Vult didn’t confirm the new logo. Reload Settings to check it.");
  return logo;
}

/** Removes the Book's logo; the Book shows its initials again. */
export async function removeBookLogo(book: Pick<Book, "id">, signal?: AbortSignal) {
  const response = await fetchResponse(`/api/v1/book-identity/logo?book_id=${encodeURIComponent(book.id)}`, {
    method: "DELETE", signal, headers: await writeHeaders(false),
  });
  await readResponse<unknown>(response, "write");
}
