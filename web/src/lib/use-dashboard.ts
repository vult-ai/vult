import { bookNavigationLocked } from "./book-routing";
import { errorMessage } from "./errors";
import { useEffect, useRef, useState } from "react";
import { bookURL, get, today, type Book, type DashboardData } from "./api";
import {
  dashboardRange,
  type DashboardPeriod,
  type DashboardRange,
} from "./dashboard-period";

import {
  readDashboardLocation,
  writeDashboardLocation,
  isDashboardLocation,
  dashboardLocationMatchesBook,
  rememberDashboardSelection,
  type DashboardSelection as Selection,
} from "./dashboard-location";
type PeriodSnapshot = Pick<DashboardData, "book_id" | "period"> & { version: string | number };

const matches = (
  data: PeriodSnapshot | undefined,
  bookId: string,
  range: DashboardRange,
) =>
  data?.book_id === bookId &&
  data.period.from === range.from &&
  (!range.to || data.period.to === range.to);

/** Commit the selected period and its complete snapshot together. Failed or
 * out-of-order reads cannot relabel old figures as belonging to another period. */
export function useDashboard<T extends PeriodSnapshot = DashboardData>(
  book: Book,
  incoming?: T,
  enrich?: (data: T) => Promise<T>,
  load?: (range: DashboardRange, signal: AbortSignal) => Promise<T>,
  active = true,
  preferIncoming = false,
) {
  const readSelection = () =>
    readDashboardLocation(
      (book.coverage_start_date || book.start_date),
      today(book.accounting_timezone),
      book,
    );
  const [initial] = useState(readSelection);
  const [current, setCurrent] = useState<{
    period: Selection["period"];
    data?: T;
  }>({
    period: initial.period,
    data: matches(incoming, book.id, initial.range) ? incoming : undefined,
  });
  const [pending, setPending] = useState(false);
  const [failure, setError] = useState<unknown>("");
  const failureBook = useRef(book.id);
  const confirmed = useRef(current);
  confirmed.current = current;
  const sequence = useRef(0);
  const controller = useRef<AbortController | null>(null);
  const desired = useRef<Selection>(initial);
  const version = useRef(
    incoming?.book_id === book.id ? incoming.version : undefined,
  );
  const cache = useRef(new Map<string, T>());
  const previousEnrich = useRef<typeof enrich>(undefined);
  const previousLoad = useRef<typeof load>(undefined);
  const initialized = useRef(false);
  const wasActive = useRef(active);
  async function request(
    period: Selection["period"],
    range: DashboardRange,
    force = false,
    history: "push" | "replace" | "none" = "push",
  ) {
    controller.current?.abort();
    const requestController = new AbortController();
    controller.current = requestController;
    desired.current = { period, range };
    const seq = ++sequence.current;
    setPending(true);
    setError("");
    const key = `${book.id}:${range.from}:${range.to}`;
    try {
      const params = new URLSearchParams({ from: range.from, to: range.to });
      const cached = !force && cache.current.get(key);
      const raw =
        cached ||
        (load
          ? await load(range, requestController.signal)
          : await get<T>(bookURL(`dashboard?${params}`, book.id), requestController.signal));
      const data = enrich && !cached ? await enrich(raw) : raw;
      if (
        data.book_id !== book.id ||
        data.period.from !== range.from ||
        (range.to && data.period.to !== range.to)
      )
        throw new Error(
          "The selected Book and period could not be confirmed. Try again.",
        );
      if (seq === sequence.current) {
        if (preferIncoming && confirmed.current.data?.version !== data.version) cache.current.clear();
        cache.current.set(key, data);
        setCurrent({ period, data });
        if (history !== "none")
          writeDashboardLocation(book, { period, range: data.period }, history);
        else rememberDashboardSelection(book, { period, range: data.period });
      }
    } catch (e) {
      if (seq === sequence.current) {
        failureBook.current = book.id;
        if (history === "none" && confirmed.current.data)
          writeDashboardLocation(
            book,
            {
              period: confirmed.current.period,
              range: confirmed.current.data.period,
            },
            "replace",
          );
        setError(
          e instanceof Error
            ? e
            : "Could not load this period. Try again.",
        );
      }
    } finally {
      if (seq === sequence.current) {
        if (preferIncoming) controller.current = null;
        setPending(false);
      }
    }
  }
  useEffect(
    () => () => {
      controller.current?.abort();
      sequence.current++;
    },
    [],
  );
  useEffect(() => {
    if (!active) {
      wasActive.current = false;
      initialized.current = false;
      controller.current?.abort();
      sequence.current++;
      setPending(false);
      return;
    }
    if (preferIncoming && incoming && controller.current && !controller.current.signal.aborted &&
        incoming.version === confirmed.current.data?.version && matches(incoming, book.id, desired.current.range)) {
      // The visible snapshot can already be rendered when a child asks to
      // refresh it. Adopting that same version must leave the refresh running.
      initialized.current = true;
      version.current = incoming.version;
      return;
    }
    if (!wasActive.current) {
      wasActive.current = true;
      try {
        desired.current = readSelection();
      } catch (error) {
        failureBook.current = book.id;
        setError(error instanceof Error ? error : "Choose a valid date range.");
        return;
      }
    }
    if (preferIncoming && initialized.current && incoming && confirmed.current.data &&
        matches(incoming, book.id, confirmed.current.data.period) &&
        !matches(incoming, book.id, desired.current.range)) {
      // A refresh of the visible period may finish while a new period loads.
      version.current = incoming.version;
      cache.current.clear();
      cache.current.set(`${book.id}:${incoming.period.from}:${incoming.period.to}`, incoming);
      setCurrent((view) => ({ ...view, data: incoming }));
      return;
    }
    if (load && !(preferIncoming && matches(incoming, book.id, desired.current.range))) {
      if (initialized.current && incoming?.version === version.current && previousLoad.current === load) return;
      initialized.current = true;
      previousLoad.current = load;
      version.current = incoming?.book_id === book.id ? incoming.version : undefined;
      cache.current.clear();
      void request(desired.current.period, desired.current.range, true, "replace");
      return;
    }
    if (
      !incoming ||
      incoming.book_id !== book.id ||
      (initialized.current &&
        incoming.version === version.current &&
        previousEnrich.current === enrich)
    )
      return;
    version.current = incoming.version;
    initialized.current = true;
    previousEnrich.current = enrich;
    cache.current.clear();
    if (matches(incoming, book.id, desired.current.range)) {
      const period = desired.current.period;
      const seq = ++sequence.current;
      if (!enrich) {
        if (preferIncoming) {
          controller.current?.abort();
          controller.current = null;
          cache.current.set(`${book.id}:${incoming.period.from}:${incoming.period.to}`, incoming);
        }
        setCurrent({ period, data: incoming });
        writeDashboardLocation(
          book,
          { period, range: incoming.period },
          "replace",
        );
        setPending(false);
        setError("");
      } else {
        setPending(true);
        void enrich(incoming)
          .then(
            (data) => {
              if (seq === sequence.current) {
                setCurrent({ period, data });
                writeDashboardLocation(
                  book,
                  { period, range: data.period },
                  "replace",
                );
                setError("");
              }
            },
            (error) => {
              if (seq === sequence.current) {
                failureBook.current = book.id;
                setError(error instanceof Error ? error : "Account movements could not be loaded.");
              }
            },
          )
          .finally(() => {
            if (seq === sequence.current) setPending(false);
          });
      }
    } else
      void request(
        desired.current.period,
        desired.current.range,
        true,
        "replace",
      );
  }, [incoming?.version, incoming?.book_id, incoming?.period.from, incoming?.period.to, book.id, enrich, load, active, preferIncoming]);
  useEffect(() => {
    const restore = () => {
      if (!active || bookNavigationLocked() || !isDashboardLocation() || !dashboardLocationMatchesBook(book)) return;
      try {
        const next = readSelection();
        void request(next.period, next.range, false, "none");
      } catch (error) {
        failureBook.current = book.id;
        setError(error instanceof Error ? error : "Choose a valid date range.");
      }
    };
    window.addEventListener("popstate", restore);
    return () => window.removeEventListener("popstate", restore);
  }, [book.id, enrich, load, active, preferIncoming]);
  return {
    ...current,
    pending,
    error: failureBook.current === book.id && failure ? errorMessage(failure) : "",
    failure: failureBook.current === book.id ? failure : undefined,
    select: (period: DashboardPeriod) =>
      request(
        period,
        dashboardRange(
          period,
          (book.coverage_start_date || book.start_date),
          today(book.accounting_timezone),
        ),
      ),
    selectRange: (range: DashboardRange) => request("custom", range),
    retry: () =>
      request(desired.current.period, desired.current.range, true, "replace"),
  };
}
