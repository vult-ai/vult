import { bookURL, type Receipt } from "./api";
import {
  entryDocumentsQuery,
  isEntryDocumentsRead,
  parseEntryDocuments,
  type EntryDocumentsCursor,
  type EntryDocumentsPage,
} from "./entry-documents";

/** Either a list of files, or the entry's typed document relationships with
 * their own cursors and history. Relationships are never flattened into files. */
export type EntryEvidenceRead =
  | { kind: "files"; files: Receipt[] }
  | { kind: "relationships"; page: EntryDocumentsPage };

const changed = "vult:entry-evidence-changed";
export type EntryEvidenceChange = {
  bookId: string;
  entryId: string;
  sha256: string;
};
export function observeEntryEvidence(
  onChange: (value: EntryEvidenceChange) => void,
) {
  const listener = (event: Event) =>
    onChange((event as CustomEvent<EntryEvidenceChange>).detail);
  window.addEventListener(changed, listener);
  return () => window.removeEventListener(changed, listener);
}
export async function readEntryEvidence(
  bookId: string,
  entryId: string,
  signal?: AbortSignal,
  cursor: EntryDocumentsCursor = { source_after: 0, evidence_after: 0, history: false },
): Promise<EntryEvidenceRead> {
  const query = cursor.source_after || cursor.evidence_after || cursor.history ? `?${entryDocumentsQuery(cursor)}` : "";
  const response = await fetch(
    `/api/v1/${bookURL(`transactions/${encodeURIComponent(entryId)}/documents${query}`, bookId)}`,
    {
      signal,
      credentials: "same-origin",
      cache: "no-store",
    },
  );
  if (!response.ok) throw new Error("Could not load linked sources.");
  const data: unknown = await response.json();
  if (isEntryDocumentsRead(data))
    return { kind: "relationships", page: parseEntryDocuments(data, bookId, entryId) };
  if (!Array.isArray(data))
    throw new Error("Could not read this transaction’s sources.");
  return { kind: "files", files: data as Receipt[] };
}

