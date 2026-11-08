/** How an entry relates to documents: what a document says about it (source
 * citations) and why a document supports it (evidence links). The two are
 * separate records with separate pages, and both keep their history. */
export type CitationLocation = { page?: number; row?: number; line?: number };
export type SourceCitation = {
  id: string;
  source_id: string;
  revision: number;
  sequence: number;
  entry_id: string;
  position: number;
  document_id: string;
  location: CitationLocation;
  actor_id: string;
  receipt_id: string;
  reason: string;
  recorded_at: string;
};
export type EvidenceLink = {
  id: string;
  link_id: string;
  revision: number;
  sequence: number;
  entry_id: string;
  document_id: string;
  role: string;
  action: "attach" | "detach";
  actor_id: string;
  receipt_id: string;
  reason: string;
  recorded_at: string;
};
export type EntryDocumentsPage = {
  sources: SourceCitation[];
  next_source_after: number;
  source_has_more: boolean;
  evidence: EvidenceLink[];
  next_evidence_after: number;
  evidence_has_more: boolean;
};
export type EntryDocumentsCursor = { source_after: number; evidence_after: number; history: boolean };

type Value = Record<string, unknown>;
const object = (v: unknown): v is Value => !!v && typeof v === "object" && !Array.isArray(v);
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const ref = (v: unknown) => typeof v === "string" && uuid.test(v);
const count = (v: unknown) => Number.isSafeInteger(v) && (v as number) >= 0;
const text = (v: unknown) => typeof v === "string" && v.length <= 4096;
const time = (v: unknown) => typeof v === "string" && !Number.isNaN(Date.parse(v));
const place = (v: unknown) => v === undefined || (Number.isSafeInteger(v) && (v as number) >= 1);

function citation(v: unknown, entry: string): v is SourceCitation {
  return object(v) && ref(v.id) && ref(v.source_id) && count(v.revision) && count(v.sequence) &&
    v.entry_id === entry && count(v.position) && ref(v.document_id) &&
    object(v.location) && Object.keys(v.location).every((k) => ["page", "row", "line"].includes(k)) &&
    place(v.location.page) && place(v.location.row) && place(v.location.line) &&
    text(v.actor_id) && text(v.receipt_id) && text(v.reason) && time(v.recorded_at);
}
function evidence(v: unknown, entry: string): v is EvidenceLink {
  return object(v) && ref(v.id) && ref(v.link_id) && count(v.revision) && count(v.sequence) &&
    v.entry_id === entry && ref(v.document_id) && typeof v.role === "string" && /^[a-z][a-z0-9_]{0,63}$/.test(v.role) &&
    (v.action === "attach" || v.action === "detach") &&
    text(v.actor_id) && text(v.receipt_id) && text(v.reason) && time(v.recorded_at);
}

/** Accepts only this Book's relationships for this entry, with both cursors. */
export function parseEntryDocuments(data: unknown, bookId: string, entryId: string): EntryDocumentsPage {
  const fail = (): never => { throw new Error("This entry’s documents could not be read."); };
  if (!object(data) || data.version !== 1 || data.book_id !== bookId || data.entry_id !== entryId) return fail();
  const d = data.documents;
  if (!object(d) || !Array.isArray(d.sources) || !Array.isArray(d.evidence) ||
      !count(d.next_source_after) || typeof d.source_has_more !== "boolean" ||
      !count(d.next_evidence_after) || typeof d.evidence_has_more !== "boolean" ||
      d.sources.length > 100 || d.evidence.length > 100 ||
      !d.sources.every((c) => citation(c, entryId)) || !d.evidence.every((e) => evidence(e, entryId)))
    return fail();
  return d as EntryDocumentsPage;
}

export function isEntryDocumentsRead(data: unknown) {
  return object(data) && data.version === 1 && object(data.documents);
}

/** Appends a further page. Each list keeps its own cursor, so a list that
 * has ended is not read again while the other continues. */
export function mergeEntryDocuments(previous: EntryDocumentsPage, next: EntryDocumentsPage): EntryDocumentsPage {
  const seen = (list: { id: string }[]) => new Set(list.map((item) => item.id));
  const sources = seen(previous.sources), links = seen(previous.evidence);
  return {
    sources: [...previous.sources, ...next.sources.filter((c) => !sources.has(c.id))],
    next_source_after: next.next_source_after,
    source_has_more: next.source_has_more,
    evidence: [...previous.evidence, ...next.evidence.filter((e) => !links.has(e.id))],
    next_evidence_after: next.next_evidence_after,
    evidence_has_more: next.evidence_has_more,
  };
}

export function entryDocumentsQuery(cursor: EntryDocumentsCursor) {
  const params = new URLSearchParams({ limit: "100", history: String(cursor.history) });
  if (cursor.source_after) params.set("source_after", String(cursor.source_after));
  if (cursor.evidence_after) params.set("evidence_after", String(cursor.evidence_after));
  return params;
}

/** One row per document, carrying every way the entry relates to it. */
export type RelatedDocument = { document_id: string; citations: SourceCitation[]; evidence: EvidenceLink[] };
export function relatedDocuments(page: EntryDocumentsPage): RelatedDocument[] {
  const byDocument = new Map<string, RelatedDocument>();
  const of = (id: string) => {
    let row = byDocument.get(id);
    if (!row) byDocument.set(id, (row = { document_id: id, citations: [], evidence: [] }));
    return row;
  };
  for (const c of page.sources) of(c.document_id).citations.push(c);
  for (const e of page.evidence) of(e.document_id).evidence.push(e);
  return [...byDocument.values()];
}

/** "Page 3, row 2", or empty when the citation names the whole document. */
export function locationLabel(location: CitationLocation) {
  return [
    location.page && `page ${location.page}`,
    location.row && `row ${location.row}`,
    location.line && `line ${location.line}`,
  ].filter(Boolean).join(", ").replace(/^./, (c) => c.toUpperCase());
}
