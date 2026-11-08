import { ErrorMessage } from "../form";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Download01, File06 } from "../../design-system/icons";
import { Button } from "../../design-system/catalog/components/vult-ui/button";
import type { Receipt } from "../../lib/api";
import { NativeSelect } from "../../design-system/catalog/components/vult-ui/select-native";
import {
  observeEntryEvidence,
  readEntryEvidence,
} from "../../lib/entry-evidence";
import { mergeEntryDocuments, type EntryDocumentsPage } from "../../lib/entry-documents";
import { EntryRelationships } from "./entry-relationships";
import { documentContentURL, formatSize, mediaLabel } from "../../lib/documents";
import { errorCopy } from "../../lib/error-copy";
import { FailureState } from "../ui/failure-state";

/** The transaction's primary receipt uses the same original and viewer as Documents. */
export function EntryDocument({
  receipt,
  bookId,
  selection,
  count = 1,
  actions,
  empty,
  compact = false,
}: {
  receipt: Receipt | null;
  bookId: string;
  selection?: ReactNode;
  count?: number;
  actions?: ReactNode;
  empty?: ReactNode;
  /** Shows `empty` as one line, with no heading. */
  compact?: boolean;
}) {
  const file = receipt;
  // Nothing linked: one quiet line rather than a heading over an empty state.
  if (!file && compact)
    return (
      <section className="entry-document entry-document-none" aria-label="Attached document">
        <span className="entry-document-tile" aria-hidden="true"><File06 /></span>
        <span className="vult-type-ui text-tertiary">{empty}</span>
      </section>
    );
  return (
    <section className="entry-document" aria-label="Attached document">
      <div className="entry-document-heading">
        <h3 className="vult-type-label text-primary">
          {count > 1 ? `Sources (${count})` : file ? "Source" : "Sources"}
        </h3>
        <div className="flex flex-wrap items-center justify-end gap-2">
          {actions}
          {file && (
            <Button
              color="link"
              size="sm"
              href={documentContentURL(file.id, bookId)}
              iconLeading={Download01}
            >
              Download original
            </Button>
          )}
        </div>
      </div>
      {file && (
        <div className="entry-document-file">
          <span className="entry-document-tile" aria-hidden="true"><File06 /></span>
          <div className="entry-document-file-body">
            {selection || (
              <p className="vult-type-ui text-primary entry-document-filename">{file.filename}</p>
            )}
            <p className="vult-type-metadata text-tertiary">
              {mediaLabel(file.content_type)} · {formatSize(file.size)}
            </p>
          </div>
        </div>
      )}
      {!file && empty}
    </section>
  );
}

/** Evidence edges, rather than a legacy primary-receipt field, own this list. */
export function EntryDocuments({
  entryId,
  bookId,
  receipt,
}: {
  entryId: string;
  bookId: string;
  receipt: Receipt | null;
  imported?: boolean;
}) {
  const [attempt, setAttempt] = useState(0);
  const [read, setRead] = useState<{
    files: Receipt[];
    error: boolean;
    loaded: boolean;
  }>({ files: [], error: false, loaded: false });
  // The loaded page remembers the entry and history mode it was read for, so
  // rows are never shown under a mode or entry they don't belong to.
  const [loaded, setLoaded] = useState<{ scope: string; history: boolean; page: EntryDocumentsPage } | null>(null);
  const [history, setHistory] = useState(false);
  const [more, setMore] = useState<{ pending: boolean; error: unknown }>({ pending: false, error: undefined });
  const [selected, setSelected] = useState("");
  const preferredHash = useRef("");
  const scope = `${bookId}:${entryId}`;
  const previousScope = useRef(scope);
  useEffect(
    () =>
      observeEntryEvidence((event) => {
        if (event.bookId === bookId && event.entryId === entryId) {
          preferredHash.current = event.sha256;
          setAttempt((value) => value + 1);
        }
      }),
    [bookId, entryId],
  );
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    const sameScope = previousScope.current === scope;
    previousScope.current = scope;
    setRead((previous) =>
      sameScope
        ? { ...previous, loaded: false }
        : { files: [], error: false, loaded: false },
    );
    void (async () => {
      try {
        const result = await readEntryEvidence(bookId, entryId, controller.signal, {
          source_after: 0,
          evidence_after: 0,
          history,
        });
        if (current && result.kind === "relationships") {
          setLoaded({ scope, history, page: result.page });
          setRead({ files: [], error: false, loaded: true });
          return;
        }
        if (!current || result.kind !== "files") return;
        setLoaded(null);
        const files = result.files;
        {
          const preferred = files.find(
            (file) => file.sha256 === preferredHash.current,
          );
          if (preferred) setSelected(preferred.id);
          preferredHash.current = "";
          setRead({ files, error: false, loaded: true });
        }
      } catch {
        if (current) setRead((previous) => ({ files: sameScope ? previous.files : [], error: true, loaded: true }));
      }
    })();
    return () => {
      current = false;
      controller.abort();
    };
  }, [bookId, entryId, attempt, history]);
  const shown = loaded?.scope === scope ? loaded : null;
  async function loadMore() {
    if (!shown || more.pending) return;
    const base = shown;
    setMore({ pending: true, error: undefined });
    try {
      const next = await readEntryEvidence(bookId, entryId, undefined, {
        source_after: base.page.next_source_after,
        evidence_after: base.page.next_evidence_after,
        history: base.history,
      });
      if (next.kind !== "relationships") throw new Error("This entry’s documents changed while loading.");
      setLoaded((previous) =>
        previous && previous.scope === base.scope && previous.history === base.history
          ? { ...previous, page: mergeEntryDocuments(previous.page, next.page) }
          : previous);
      setMore({ pending: false, error: undefined });
    } catch (error) {
      setMore({ pending: false, error });
    }
  }
  if (shown)
    return (
      <>
        <EntryRelationships
          bookId={bookId}
          page={shown.page}
          // The rows' own mode, not the one being requested.
          history={shown.history}
          pending={!read.loaded || more.pending}
          onHistory={(next) => {
            setHistory(next);
            // Asking again for the mode that just failed reads it again.
            setAttempt((value) => value + 1);
          }}
          onMore={loadMore}
        />
        <ErrorMessage
          message={read.error ? (history ? "History could not be loaded." : "Current sources could not be reloaded.") : more.error}
          intent="read"
          resource="sources"
          pending={!read.loaded || more.pending}
          onRetry={read.error ? () => setAttempt((value) => value + 1) : loadMore}
        />
      </>
    );
  const files = read.files.map((file) =>
    receipt && file.sha256 === receipt.sha256 ? receipt : file,
  );
  if (
    receipt &&
    !files.some(
      (file) => file.id === receipt.id || file.sha256 === receipt.sha256,
    )
  )
    files.unshift(receipt);
  const active = files.find((file) => file.id === selected) || files[0];
  return (
    <>
      <EntryDocument
        receipt={active || null}
        bookId={bookId}
        count={files.length}
        empty={
          read.error ? (
            <FailureState
              scope="region"
              error={errorCopy.unavailable}
              intent="read"
              resource="sources"
            />
          ) : read.loaded ? (
            "No source linked"
          ) : (
            "Loading sources…"
          )
        }
        compact={!read.error}
        selection={
          files.length > 1 ? (
            <NativeSelect
              size="sm"
              aria-label="Attached source"
              value={active.id}
              options={files.map((file) => ({
                value: file.id,
                label: file.filename,
              }))}
              selectClassName="truncate pr-8"
              onChange={(event) => setSelected(event.target.value)}
            />
          ) : undefined
        }
      />
      <ErrorMessage
        message={
          read.error ? "Additional sources could not be loaded." : undefined
        }
        intent="read"
        resource="sources"
        pending={!read.loaded}
        onRetry={() => setAttempt((value) => value + 1)}
      />
    </>
  );
}
