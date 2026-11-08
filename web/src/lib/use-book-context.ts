import { useEffect, useState } from "react";
import type { Book, State } from "./api";
import { readBookState } from "./book-state";

/** A form/inspector needs more context than a metadata-only workspace. Its
 * Book-keyed host owns this read; no global cache or new financial calculation. */
export function useBookContext(book: Book, state: State, enabled: boolean) {
  const [context, setContext] = useState<State | undefined>(() => !state.bootstrap ? state : undefined);
  const [failure, setFailure] = useState<unknown>();
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!enabled || context) return;
    const controller = new AbortController();
    setFailure(undefined);
    readBookState(book.id, {}, book.accounting_timezone, { view: "accounts", signal: controller.signal })
      .then(value => {
        if (controller.signal.aborted) return;
        if (value.bootstrap) throw new Error("The account details aren't available yet.");
        setContext(value);
      })
      .catch(error => { if (!controller.signal.aborted) setFailure(error); });
    return () => controller.abort();
  }, [book.id, book.accounting_timezone, attempt, context, enabled]);
  return { context, failure, retry: () => setAttempt(n => n + 1) };
}
