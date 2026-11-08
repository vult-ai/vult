import { fetchJSON, fetchResponse, readResponse } from "./errors";
import { writeHeaders } from "./session";

/** A verified original in this Book, as Vult Open records it. Agents add and
 * link documents; people read them. Content is untrusted and only downloaded. */
export type BookDocument = {
  id: string;
  sequence: number;
  name: string;
  media_type: string;
  size: number;
  sha256: string;
  state: string;
  created_at: string;
  ready_at?: string;
  used_at?: string;
};
export type DocumentPage = {
  documents: BookDocument[];
  next_after: number;
  has_more: boolean;
  total?: number;
  revision?: string;
};
/** One entry that currently uses a document: as a cited source, as evidence, or both. */
export type DocumentUse = {
  entry_id: string;
  accounting_date: string;
  description?: string;
  reversed?: boolean;
  citation?: { location: { page?: number; row?: number; line?: number }; revision: number };
  evidence?: { role: string };
};
export type DocumentUsePage = { uses: DocumentUse[]; next_after: number; has_more: boolean };

const book = (id: string) => `book_id=${encodeURIComponent(id)}`;
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v);
const count = (v: unknown) => Number.isSafeInteger(v) && (v as number) >= 0;
const day = (v: unknown) => typeof v === "string" && !Number.isNaN(Date.parse(v));

export const documentsPath = (bookId: string, after = 0, query = "", revision = "") => {
  const params = new URLSearchParams({ book_id: bookId, limit: "50" });
  if (after) params.set("after", String(after));
  if (query) params.set("q", query);
  if (revision) params.set("revision", revision);
  return `/api/v1/documents?${params}`;
};
export const documentPath = (id: string, bookId: string) =>
  `/api/v1/documents/${encodeURIComponent(id)}?${book(bookId)}`;
export const documentContentURL = (id: string, bookId: string) =>
  `/api/v1/documents/${encodeURIComponent(id)}/content?${book(bookId)}`;
export const documentEntriesPath = (id: string, bookId: string, after = 0) =>
  `/api/v1/documents/${encodeURIComponent(id)}/entries?${book(bookId)}&limit=50${after ? `&after=${after}` : ""}`;

export function isBookDocument(v: unknown): v is BookDocument {
  return object(v) && typeof v.id === "string" && !!v.id && count(v.sequence) &&
    typeof v.name === "string" && typeof v.media_type === "string" && count(v.size) &&
    typeof v.sha256 === "string" && /^[a-f0-9]{64}$/.test(v.sha256) && typeof v.state === "string" && day(v.created_at) &&
    (v.used_at === undefined || v.used_at === null || day(v.used_at));
}

export async function readDocuments(bookId: string, after = 0, signal?: AbortSignal, query = "", revision = ""): Promise<DocumentPage> {
  const page = await fetchJSON<unknown>(documentsPath(bookId, after, query, revision), signal ? { signal } : undefined);
  if (!object(page) || !Array.isArray(page.documents) || !page.documents.every(isBookDocument) ||
      typeof page.next_after !== "number" || !count(page.next_after) || typeof page.has_more !== "boolean" ||
      (page.has_more && page.next_after <= after) ||
      (query && (typeof page.total !== "number" || !count(page.total) || page.total < page.documents.length ||
        typeof page.revision !== "string" || !/^[a-f0-9]{64}$/.test(page.revision))))
    throw new Error("The documents in this Book could not be read.");
  return page as DocumentPage;
}

export async function readDocument(id: string, bookId: string, signal?: AbortSignal): Promise<BookDocument> {
  const value = await fetchJSON<unknown>(documentPath(id, bookId), signal ? { signal } : undefined);
  const doc = object(value) && object(value.document) ? value.document : value;
  if (!isBookDocument(doc) || doc.id !== id) throw new Error("This document could not be read.");
  return doc;
}

export async function readDocumentUses(id: string, bookId: string, after = 0, signal?: AbortSignal): Promise<DocumentUsePage> {
  const page = await fetchJSON<unknown>(documentEntriesPath(id, bookId, after), signal ? { signal } : undefined);
  if (!object(page) || !Array.isArray(page.uses) || !count(page.next_after) || typeof page.has_more !== "boolean" ||
      !page.uses.every((u) => object(u) && typeof u.entry_id === "string" && day(u.accounting_date)))
    throw new Error("The entries using this document could not be read.");
  return page as DocumentUsePage;
}

export function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}
const KINDS: Record<string, string> = {
  "application/pdf": "PDF", "image/png": "PNG image", "image/jpeg": "JPEG image", "image/webp": "WebP image",
  "image/gif": "GIF image", "image/heic": "HEIC image", "text/csv": "CSV", "text/plain": "Text",
  "application/json": "JSON", "application/zip": "ZIP archive", "application/xml": "XML", "text/xml": "XML",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "Excel workbook",
  "application/vnd.ms-excel": "Excel workbook",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document": "Word document",
};
/** Plain names for file types; anything unrecognised is just a file, never a raw media type. */
export const mediaLabel = (type: string) => KINDS[type.split(";")[0].trim().toLowerCase()] ?? "File";

/** What the owner can add from the browser: the engine's document types and limit. */
export const UPLOAD_LIMIT = 32 * 1024 * 1024;
const UPLOAD_TYPES: Record<string, string> = {
  pdf: "application/pdf", png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", webp: "image/webp", csv: "text/csv",
};
export const UPLOAD_ACCEPT = ".pdf,.csv,.png,.jpg,.jpeg,.webp";

/** The media type to declare for a file, or a plain reason it can't be added. */
export function uploadType(file: Pick<File, "name" | "type" | "size">): { type: string } | { error: string } {
  const extension = file.name.toLowerCase().split(".").pop() ?? "";
  const type = UPLOAD_TYPES[extension];
  if (!type) return { error: "Vult keeps PDF, CSV, PNG, JPEG and WebP files." };
  if (!file.size) return { error: "This file is empty." };
  if (file.size > UPLOAD_LIMIT) return { error: "Each file must be 32 MiB or smaller." };
  if (/[\\/\r\n\0]/.test(file.name) || [...file.name.trim()].length > 180) return { error: "Use a plain filename of up to 180 characters." };
  return { type };
}

export type UploadResult = { document: BookDocument; reused: boolean };

/** Adds one original to the Book as the signed-in owner. The SHA-256 and length are
 * declared before the bytes, as for agents; the same bytes again return the same
 * document. `key` makes a retry of this attempt safe. */
export async function uploadDocument(bookId: string, file: File, key: string, signal?: AbortSignal): Promise<UploadResult> {
  const checked = uploadType(file);
  if ("error" in checked) throw new Error(checked.error);
  const bytes = await file.arrayBuffer();
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), (b) => b.toString(16).padStart(2, "0")).join("");
  const response = await fetchResponse(`/api/v1/documents?${book(bookId)}`, {
    method: "POST",
    signal,
    headers: {
      ...(await writeHeaders(false)),
      "Content-Type": checked.type,
      "Idempotency-Key": key,
      "X-Vult-Document-Name": encodeURIComponent(file.name.trim()),
      "X-Vult-Document-SHA256": digest,
    },
    body: bytes,
  });
  const value = await readResponse<unknown>(response, "write");
  const doc = object(value) && object(value.document) ? value.document : undefined;
  if (!isBookDocument(doc) || doc.sha256 !== digest || doc.size !== file.size || !object(value) || typeof value.reused !== "boolean")
    throw new Error("Vult didn’t confirm this file. Check Sources before adding it again.");
  return { document: doc, reused: value.reused };
}
