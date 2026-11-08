import { useEffect, useState, type ReactNode } from "react";
import { ArrowUpRight } from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { formatDateTime } from "../../lib/date-format";
import {
  locationLabel,
  relatedDocuments,
  type EntryDocumentsPage,
  type RelatedDocument,
} from "../../lib/entry-documents";
import { documentContentURL, readDocument, type BookDocument } from "../../lib/documents";
import { EmptyState } from "../ui/empty-state";

const words = (code: string) => code.replace(/[_-]+/g, " ").replace(/^./, (c) => c.toUpperCase());

/** The document's own name when it can be read; the relationship stays
 * visible either way, because it is part of the entry's record. */
function useSourceName(bookId: string, documentId: string) {
  const [source, setSource] = useState<BookDocument | null | undefined>();
  useEffect(() => {
    const controller = new AbortController();
    readDocument(documentId, bookId, controller.signal)
      .then((value) => !controller.signal.aborted && setSource(value))
      .catch(() => !controller.signal.aborted && setSource(null));
    return () => controller.abort();
  }, [bookId, documentId]);
  return source;
}

function Related({ bookId, item, history }: { bookId: string; item: RelatedDocument; history: boolean }) {
  const source = useSourceName(bookId, item.document_id);
  const attached = item.evidence.filter((e) => e.action === "attach");
  const detached = new Set(item.evidence.filter((e) => e.action === "detach").map((e) => e.link_id));
  const roles = [...new Set(attached.filter((e) => !detached.has(e.link_id)).map((e) => words(e.role)))];
  return (
    <li className="entry-relationship">
      <div className="entry-relationship-heading">
        <span className="vult-type-ui text-primary entry-relationship-name" title={source ? source.name : item.document_id}>
          {source ? source.name : source === null ? "Document unavailable" : "Loading…"}
        </span>
        {source && (
          <Button color="link" size="sm" href={documentContentURL(source.id, bookId)} iconTrailing={ArrowUpRight}>
            Download
          </Button>
        )}
      </div>
      {roles.length > 0 && <p className="vult-type-metadata text-secondary">{roles.join(" · ")}</p>}
      {item.citations.map((c) => (
        <p key={c.id} className="vult-type-metadata text-tertiary">
          {locationLabel(c.location) || "Whole document"}
          {c.reason && ` · ${c.reason}`}
          {history && c.revision > 1 && ` · revision ${c.revision}`}
        </p>
      ))}
      {history &&
        item.evidence.map((e) => (
          <p key={e.id} className="vult-type-metadata text-tertiary">
            {e.action === "attach" ? "Linked" : "Unlinked"} as {words(e.role).toLowerCase()} · {formatDateTime(e.recorded_at, "full")}
            {e.reason && ` · ${e.reason}`}
          </p>
        ))}
    </li>
  );
}

/** Documents related to an entry, read as relationships rather than files. */
export function EntryRelationships({
  bookId,
  page,
  history,
  pending,
  actions,
  onHistory,
  onMore,
}: {
  bookId: string;
  page: EntryDocumentsPage;
  history: boolean;
  pending: boolean;
  actions?: ReactNode;
  onHistory: (history: boolean) => void;
  onMore: () => void;
}) {
  const items = relatedDocuments(page);
  const more = page.source_has_more || page.evidence_has_more;
  return (
    <section className="entry-document" aria-label="Related documents">
      <div className="entry-document-heading">
        <h3 className="vult-type-label text-primary">{items.length > 1 ? `Sources (${items.length})` : "Sources"}</h3>
        <div className="flex flex-wrap items-center justify-end gap-2">
          {actions}
          <Button color="link" size="sm" isDisabled={pending} onClick={() => onHistory(!history)}>
            {history ? "Hide history" : "Show history"}
          </Button>
        </div>
      </div>
      {items.length ? (
        <ul className="entry-relationships">
          {items.map((item) => (
            <Related key={item.document_id} bookId={bookId} item={item} history={history} />
          ))}
        </ul>
      ) : (
        <EmptyState scope="inline" title="No sources linked" />
      )}
      {more && (
        <Button color="secondary" size="sm" isDisabled={pending} onClick={onMore}>
          Load more
        </Button>
      )}
    </section>
  );
}
