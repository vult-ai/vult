import "./documents.css";
import { SearchFilter } from "../../design-system/search-filter";
import { CollectionToolbar, CollectionTitle } from "../../design-system/collection-toolbar";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowLeft, Download01, File06 } from "../../design-system/icons";
import { APIError, type Book } from "../../lib/api";
import {
  documentContentURL,
  formatSize,
  mediaLabel,
  readDocument,
  readDocuments,
  readDocumentUses,
  type BookDocument,
  type DocumentPage,
  type DocumentUse,
} from "../../lib/documents";
import { CollectionTable } from "../../design-system/collection-table";
import { DocumentViewer } from "./document-viewer";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import { ErrorMessage } from "../form";
import { NameLink } from "../ui/name-link";
import { RecordDate } from "../ui/record-date";
import { EmptyState } from "../ui/empty-state";
import { LoadingState } from "../ui/loading-state";
import { ChooseFiles, DropArea, UploadPanel, useDocumentUpload } from "./document-upload";

type Props = {
  book: Book;
  refreshKey?: number | string;
  query?: string;
  documentSelection?: { id: string };
  /** Shows the toolbar; the Book search (⌘K) sets the query, and this removes it. */
  onQueryChange?: (query: string) => void;
  active?: boolean;
  /** Opens an entry that uses the document. */
  onOpenEntry?: (id: string) => void;
};

/** What the reader can show: PDF pages, images, or nothing for other files. */
function viewerKind(mediaType: string): "pdf" | "image" | "unsupported" {
  const type = mediaType.split(";")[0].trim().toLowerCase();
  if (type === "application/pdf") return "pdf";
  return ["image/jpeg", "image/png", "image/gif", "image/webp", "image/avif"].includes(type) ? "image" : "unsupported";
}

/** The originals kept with this Book. The owner can add files here (drop them
 * anywhere on the page or choose them); agents add and link documents as they
 * record. People inspect each one and the entries it supports. */
export function Documents(props: Props) {
  return <DocumentLibrary key={props.book.id} {...props} />;
}

function DocumentLibrary({ book, refreshKey = 0, query = "", documentSelection, onQueryChange, active = true, onOpenEntry }: Props) {
  const needle = query.trim();
  const [result, setResult] = useState<{ query: string; page: DocumentPage }>();
  const page = result?.query === needle ? result.page : undefined;
  const rows = page?.documents || [];
  const after = page?.next_after ?? 0;
  const hasMore = page?.has_more ?? false;
  const count = page?.total ?? rows.length;
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState<{ query: string; error: unknown; after: number; revision?: string }>();
  const failedRead = failure?.query === needle ? failure : undefined;
  const error = failedRead?.error;
  const [selected, setSelected] = useState<BookDocument | null>(null);
  const [cursorReset, setCursorReset] = useState(0);
  const selectionController = useRef<AbortController | null>(null);
  const controller = useRef<AbortController | null>(null);
  const load = useCallback(function load(from: number, revision?: string) {
    controller.current?.abort();
    const c = (controller.current = new AbortController());
    setLoading(true);
    setFailure(undefined);
    readDocuments(book.id, from, c.signal, needle, revision)
      .then((next) => {
        if (c.signal.aborted) return;
        setResult((before) => {
          if (!from || before?.query !== needle) return { query: needle, page: next };
          const seen = new Set(before.page.documents.map((d) => d.id));
          return { query: needle, page: {
            ...next,
            documents: [...before.page.documents, ...next.documents.filter((d) => !seen.has(d.id))],
          } };
        });
      })
      .catch((error) => {
        if (c.signal.aborted) return;
        if (from && needle && error instanceof APIError && error.status === 409) {
          setResult(undefined);
          setCursorReset((value) => value + 1);
          selectionController.current?.abort();
          setSelected(null);
          load(0);
          return;
        }
        setFailure({ query: needle, error, after: from, revision });
      })
      .finally(() => {
        if (!c.signal.aborted) setLoading(false);
      });
  }, [book.id, needle]);
  // Refresh after the queue is quiet, with the current search and page scope.
  const [uploadRevision, setUploadRevision] = useState(0);
  const refresh = useRef<ReturnType<typeof setTimeout>>(undefined);
  const upload = useDocumentUpload(book.id, useCallback(() => {
    clearTimeout(refresh.current);
    refresh.current = setTimeout(() => setUploadRevision((n) => n + 1), 250);
  }, []));
  useEffect(() => () => clearTimeout(refresh.current), []);
  useEffect(() => {
    if (!active) return;
    load(0);
    return () => controller.current?.abort();
  }, [active, refreshKey, uploadRevision, load]);
  useEffect(() => setSelected(null), [needle]);
  const [selectionFailure, setSelectionFailure] = useState<unknown>();
  const inspected = useRef<typeof documentSelection>(undefined);
  const selectDocument = (doc: BookDocument | null) => {
    selectionController.current?.abort();
    inspected.current = documentSelection;
    setSelectionFailure(undefined);
    setSelected(doc);
  };
  useEffect(() => {
    if (!active) return;
    setSelectionFailure(undefined);
    if (!documentSelection || inspected.current === documentSelection) return;
    const c = (selectionController.current = new AbortController());
    readDocument(documentSelection.id, book.id, c.signal).then((doc) => {
      if (c.signal.aborted) return;
      inspected.current = documentSelection;
      if (doc.state === "ready") setSelected(doc);
      else setSelectionFailure(new Error("This source is no longer available."));
    }).catch((error) => {
      if (!c.signal.aborted) {
        inspected.current = documentSelection;
        setSelectionFailure(error);
      }
    });
    return () => c.abort();
  }, [active, book.id, documentSelection]);
  const waiting = loading || (!page && !error);
  // Narrow workspaces show one column: the list, or the selected source's
  // details. Uploads in progress appear under the list.
  const frame = useRef<HTMLDivElement>(null);
  const [compact, setCompact] = useState(false);
  useLayoutEffect(() => {
    const measure = () => {
      const width = frame.current?.getBoundingClientRect().width || 0;
      if (width) setCompact(width < 760);
    };
    measure();
    const observer = new ResizeObserver(measure);
    if (frame.current) observer.observe(frame.current);
    return () => observer.disconnect();
  }, []);
  const showPanel = !compact || !!selected || upload.items.length > 0;

  return (
    <div ref={frame} className="sources-workspace collection-inspector" data-selected={selected ? "true" : undefined} data-compact={compact || undefined}
      onKeyDown={(e) => { if (e.key === "Escape" && selected) { e.preventDefault(); selectDocument(null); } }}>
      <DropArea onFiles={(files) => { selectDocument(null); upload.add(files); }} label={`Drop to add to ${book.name}`} className="sources-library collection-inspector-collection" hidden={compact && !!selected}>
        <CollectionTable
          ariaLabel="Sources"
          testId="book-documents"
          toolbar={onQueryChange ? (
            <CollectionToolbar
              label="Sources"
              lead={<span className="sources-toolbar-lead">
                <CollectionTitle label="Sources" count={needle && !page ? (waiting ? "…" : "—") : `${count.toLocaleString()}${page?.total === undefined && hasMore ? "+" : ""}`}
                  countLabel={needle && !page ? (waiting ? "Searching sources" : "Matching source count unavailable") : `${count} ${count === 1 ? "source" : "sources"}`} />
                {needle && <SearchFilter value={needle} onRemove={() => onQueryChange("")} />}
              </span>}
              actions={<ChooseFiles onFiles={(files) => { selectDocument(null); upload.add(files); }}>Add files</ChooseFiles>}
            />
          ) : undefined}
          selectionEnabled={false}
          activeRowId={selected?.id}
          pageResetKey={JSON.stringify([needle, refreshKey, cursorReset])}
          onRowAction={selectDocument}
          // A narrow workspace shows one column that holds the whole row: the
          // name, then type, size, when it was added and whether it is used.
          columns={compact ? [
            { id: "summary", label: "Source", kind: "text" as const, width: 320, fill: true, isRowHeader: true },
          ] : [
            { id: "name", label: "Name", kind: "text" as const, width: 280, minWidth: 180, isRowHeader: true },
            { id: "type", label: "Type", kind: "status" as const },
            { id: "size", label: "Size", kind: "money" as const },
            { id: "added", label: "Added", kind: "date" as const },
            { id: "used", label: "Used", kind: "status" as const },
          ]}
          rows={rows}
          rowId={(d) => d.id}
          rowLabel={(d) => d.name}
          isDisabled={waiting && !rows.length}
          notice={<>
            <ErrorMessage message={error} intent="read" resource="sources" pending={waiting} onRetry={() => load(failedRead?.after ?? 0, failedRead?.revision)} />
            <ErrorMessage message={selectionFailure} intent="read" resource="sources" />
          </>}
          remotePagination={{ hasMore, loading, onNextPage: () => load(after, page?.revision) }}
          emptyState={
            waiting ? <LoadingState scope="region" label={needle ? "Searching sources…" : "Loading sources…"} />
            // A failed read says nothing about whether sources exist; the notice above explains.
            : error && !rows.length ? null
            : needle ? <EmptyState scope="region" title={`No sources match “${needle}”`} detail="Try fewer words, or remove the search to see every source." />
            : <EmptyState scope="region" icon={File06} title={`No sources in ${book.name} yet`} detail="Drop receipts, invoices and statements here, or let the agent add them as it records the Book." />
          }
          renderCell={(d, column) =>
            column.id === "summary" ? (
              <span className="book-cell source-row-summary">
                <span className="document-preview-name">
                  <File06 className="size-4" aria-hidden />
                  <NameLink type="button" className="entry-link vult-type-label vult-focus-target vult-focus-contained" title={d.name}
                    aria-label={`Inspect ${d.name}`} aria-current={selected?.id === d.id ? "true" : undefined}
                    onClick={(e) => { e.stopPropagation(); selectDocument(d); }} name={d.name} />
                </span>
                <span className="source-row-meta vult-type-metadata text-tertiary">
                  {mediaLabel(d.media_type)} · {formatSize(d.size)} · <RecordDate value={d.created_at.slice(0, 10)} /> · {d.used_at ? "In use" : "Not yet used"}
                </span>
              </span>
            ) : column.id === "name" ? (
              <span className="book-cell document-preview-name">
                <File06 className="size-4" aria-hidden />
                <NameLink type="button" className="entry-link vult-type-label vult-focus-target vult-focus-contained" title={d.name}
                  aria-label={`Inspect ${d.name}`} aria-current={selected?.id === d.id ? "true" : undefined}
                  onClick={(e) => { e.stopPropagation(); selectDocument(d); }} name={d.name} />
              </span>
            ) : column.id === "type" ? <span className="book-cell text-tertiary">{mediaLabel(d.media_type)}</span>
            : column.id === "size" ? <span className="book-cell number">{formatSize(d.size)}</span>
            : column.id === "added" ? <RecordDate className="book-cell date-cell" value={d.created_at.slice(0, 10)} />
            : <span className="book-cell text-tertiary">{d.used_at ? "In use" : "Not yet used"}</span>
          }
        />
      </DropArea>
      <aside className="source-sidebar collection-inspector-panel" aria-label={selected ? "Source details" : "Add files"} hidden={!showPanel}>
        <header className="source-sidebar-header collection-inspector-header">
          {compact && selected && (
            <Button size="sm" color="tertiary" onClick={() => selectDocument(null)} aria-label="Back to sources"><ArrowLeft aria-hidden /></Button>
          )}
          <h2 className="vult-type-label text-primary">{selected ? selected.name : compact ? "Adding files" : "Add files"}</h2>
        </header>
        <div className="collection-inspector-body source-sidebar-body">
          {selected ? <DocumentDetail key={selected.id} doc={selected} book={book} onOpenEntry={onOpenEntry} />
            : <UploadPanel bookName={book.name} items={upload.items} onFiles={upload.add} onClear={upload.clear} />}
        </div>
      </aside>
    </div>
  );
}

function DocumentDetail({ doc, book, onOpenEntry }: { doc: BookDocument; book: Book; onOpenEntry?: (id: string) => void }) {
  const [uses, setUses] = useState<DocumentUse[] | null>(null);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState<unknown>();
  useEffect(() => {
    const c = new AbortController();
    readDocumentUses(doc.id, book.id, 0, c.signal)
      .then((page) => !c.signal.aborted && setUses(page.uses))
      .catch((e) => {
        if (c.signal.aborted) return;
        if (e instanceof APIError && e.status === 404) setUnsupported(true);
        else setError(e);
      });
    return () => c.abort();
  }, [doc.id, book.id]);
  return (
    <div className="source-detail">
      {/* The original itself first: PDF pages and images open in the passive
          reader, so the owner sees the document without downloading it. */}
      <DocumentViewer
        key={`${book.id}:${doc.id}:${doc.sha256}`}
        src={documentContentURL(doc.id, book.id)}
        name={doc.name}
        kind={viewerKind(doc.media_type)}
        presentation="preview"
      />
      <dl className="source-detail-facts vult-type-ui">
        <div><dt className="text-tertiary">Type</dt><dd>{mediaLabel(doc.media_type)}</dd></div>
        <div><dt className="text-tertiary">Size</dt><dd>{formatSize(doc.size)}</dd></div>
        <div><dt className="text-tertiary">Added</dt><dd><RecordDate value={doc.created_at.slice(0, 10)} format="full" /></dd></div>
        <div><dt className="text-tertiary">SHA-256</dt><dd className="vult-code" title={doc.sha256}>{doc.sha256.slice(0, 16)}…</dd></div>
      </dl>
      <Button color="secondary" size="sm" href={documentContentURL(doc.id, book.id)} iconLeading={Download01}>Download original</Button>
      {!unsupported && (
        <section className="source-detail-uses" aria-label="Used by">
          <h3 className="vult-type-label text-primary">Used by</h3>
          <ErrorMessage message={error} intent="read" resource="sources" />
          {uses === null && !error ? <LoadingState scope="inline" label="Loading entries…" />
            : uses && !uses.length ? <p className="vult-type-metadata text-tertiary">No entry uses this source yet.</p>
            : uses && (
              <ul className="source-uses">
                {uses.map((u) => (
                  <li key={u.entry_id} className="source-use vult-type-ui">
                    <NameLink type="button" className="entry-link vult-focus-target" onClick={() => onOpenEntry?.(u.entry_id)}
                      name={u.description || `Entry ${u.entry_id.slice(0, 8)}`} />
                    <span className="source-use-meta vult-type-metadata text-tertiary">
                      <RecordDate value={u.accounting_date} />
                      {u.evidence && ` · ${u.evidence.role}`}
                      {u.citation && " · cited"}
                      {u.reversed && " · reversed"}
                    </span>
                  </li>
                ))}
              </ul>
            )}
        </section>
      )}
    </div>
  );
}
