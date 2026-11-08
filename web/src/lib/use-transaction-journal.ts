import { errorMessage, responseFailure } from "./errors";
import { useEffect, useRef, useState } from "react";
import {
  readTransactionJournal,
  type TransactionJournal,
} from "./transaction-journal";
import type { Book } from "./api";

/** Retain complete immutable entry facts, not the full Book journal. The cache
 * is bounded and scoped to Book/currency/save revision; old pages can be evicted. */
export function useTransactionJournal(
  book: Book,
  revision: number,
  enabled: boolean,
  recordIds: readonly string[] = [],
) {
  const [snapshot, setSnapshot] = useState<{
    key: string;
    journal: TransactionJournal;
  } | null>(null);
  const [failure, setError] = useState<unknown>("");
  const failureScope = useRef("");
  const [pending, setPending] = useState(false);
  const [retry, setRetry] = useState(0);
  const loaded = useRef<{ key: string; journal: TransactionJournal } | null>(
    null,
  );
  const key = `${book.id}:${book.currency}:${revision}`;
  const requiredKey = JSON.stringify(recordIds);
  useEffect(() => {
    const required = JSON.parse(requiredKey) as string[];
    if (!required.length) return;
    if (
      !enabled ||
      (loaded.current?.key === key &&
        required.every((id) => loaded.current!.journal.has(id)))
    )
      return;
    const controller = new AbortController();
    setPending(true);
    setError("");
    const missing = required.filter(id => loaded.current?.key !== key || !loaded.current.journal.has(id));
    readTransactionJournal(book, controller.signal, missing)
      .then((journal) => {
        if (!controller.signal.aborted) {
          const combined = new Map(loaded.current?.key === key ? loaded.current.journal : []);
          for (const [id, lines] of journal) combined.set(id, lines);
          if (!required.every((id) => combined.has(id)))
            throw responseFailure(503, null, "read");
          // Touch visible entries last; never evict the page being displayed.
          for (const id of required) { const lines = combined.get(id)!; combined.delete(id); combined.set(id, lines); }
          while (combined.size > 400) combined.delete(combined.keys().next().value!);
          loaded.current = { key, journal: combined };
          setSnapshot(loaded.current);
        }
      })
      .catch((reason) => {
        if (!controller.signal.aborted) {
          loaded.current = null;
          failureScope.current = key;
          setError(
            reason instanceof Error
              ? reason
              : "Transaction categories could not be loaded.",
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setPending(false);
      });
    return () => controller.abort();
  }, [book.id, book.currency, revision, enabled, retry, requiredKey]);
  const activeFailure = enabled && failureScope.current === key ? failure : undefined;
  return {
    journal: snapshot?.key === key ? snapshot.journal : null,
    error: activeFailure ? errorMessage(activeFailure) : "",
    failure: activeFailure,
    pending,
    retry: () => setRetry((value) => value + 1),
  };
}
