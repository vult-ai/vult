import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { Book } from "./api";
import { bookLocator, readBookRoute, resolveBookRoute } from "./book-routing";
import {
  clearResourceSelection,
  readableReference,
  resourceSelection,
  resourceURL,
  type ResourceKind,
} from "./resource-links";

type Item = { id: string; reference?: string };
type Reference = Item & { book_id: string; reference: string };

/** URL locators never become mutation identities. Resolve once, retain the same
 * UUID and mounted inspector, and preserve history state and busy drafts. */
export function useResourcePreview(
  book: Book,
  kind: ResourceKind,
  pending: boolean,
  items: readonly Item[] = [],
  active = true,
) {
  const key = `${book.id}:${kind}`;
  const local = useRef({ key, cache: new Map<string, string>() });
  if (local.current.key !== key) local.current = { key, cache: new Map() };
  const cache = local.current.cache;
  for (const item of items)
    if (item.reference && readableReference.test(item.reference))
      cache.set(item.id, item.reference);
  const read = () => resourceSelection(book, kind);
  const initial = () => {
    try {
      const value = read();
      return readableReference.test(value.toUpperCase())
        ? [...cache!].find(([, ref]) => ref === value.toUpperCase())?.[0] || ""
        : kind === "source" && bookLocator(book)
          ? ""
          : value;
    } catch {
      return "";
    }
  };
  const [detail, setDetail] = useState(initial),
    [error, setError] = useState("");
  const busy = useRef(pending),
    location = useRef(window.location.href),
    generation = useRef(0);
  busy.current = pending;
  useLayoutEffect(() => {
    location.current = window.location.href;
  });
  const write = (id: string, reference: string, mode: "push" | "replace") => {
    let url: URL;
    if (kind === "entry" && ["statements", "accounts"].includes(readBookRoute().page)) {
      url = clearResourceSelection(new URL(window.location.href), kind);
      if (id) {
        url.searchParams.set("entry", reference || id);
        // A verified Book route already carries the scope. Legacy flat routes
        // still need an explicit Book selector to remain unambiguous.
        if (resolveBookRoute([book], url.pathname)?.id !== book.id)
          url.searchParams.set("entryBook", book.id);
      }
    } else if (bookLocator(book) && (reference || !id))
      url = resourceURL(book, kind, reference);
    else {
      url = clearResourceSelection(new URL(window.location.href), kind);
      if (id) {
        url.searchParams.set(
          kind === "entry" ? "entryPreview" : "sourcePreview",
          id,
        );
        url.searchParams.set(
          kind === "entry" ? "entryBook" : "sourceBook",
          book.id,
        );
      }
    }
    if (url.href !== window.location.href)
      window.history[mode === "replace" ? "replaceState" : "pushState"](
        window.history.state,
        "",
        url,
      );
    location.current = window.location.href;
  };
  // Links carry the record ID itself; there is no reference lookup to call.
  const resolve = async (
    _locator: string,
    _signal?: AbortSignal,
  ): Promise<Reference> => {
    throw new Error("This link isn’t available.");
  };
  const show = useCallback(
    (id: string, mode: "push" | "replace" | "none" = "push") => {
      const seq = ++generation.current;
      setDetail(id);
      setError("");
      if (mode === "none") return;
      const reference = cache!.get(id) || "";
      if (!id || reference || !bookLocator(book) || !cache!.size) {
        write(id, reference, mode);
        return;
      }
      void resolve(id).then(
        (result) => {
          if (seq === generation.current) write(id, result.reference, mode);
        },
        () => {
          if (seq === generation.current)
            setError("This link isn’t available.");
        },
      );
    },
    [book.id, book.address?.revision, kind],
  );
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    const restore = () => {
      if (busy.current) {
        window.history.replaceState(window.history.state, "", location.current);
        return;
      }
      const seq = ++generation.current;
      let locator: string;
      try {
        locator = read();
      } catch {
        setError("This link isn’t available.");
        return;
      }
      setError("");
      if (!locator) {
        setDetail("");
        location.current = window.location.href;
        return;
      }
      const known = [...cache!].find(
        ([id, ref]) => id === locator || ref === locator.toUpperCase(),
      );
      if (known) {
        setDetail(known[0]);
        write(known[0], known[1], "replace");
        return;
      }
      if (!readableReference.test(locator.toUpperCase())) {
        if (kind === "entry" || !bookLocator(book)) setDetail(locator);
        // A legacy backend/fixture keeps its original URL until references exist.
        if (!bookLocator(book)) {
          location.current = window.location.href;
          return;
        }
      }
      if (readableReference.test(locator.toUpperCase())) setDetail("");
      void resolve(locator, controller.signal).then(
        (result) => {
          if (seq !== generation.current || busy.current) return;
          setDetail(result.id);
          write(result.id, result.reference, "replace");
        },
        () => {
          if (seq === generation.current && !controller.signal.aborted)
            setError("This link isn’t available.");
        },
      );
    };
    restore();
    window.addEventListener("popstate", restore);
    return () => {
      generation.current++;
      controller.abort();
      window.removeEventListener("popstate", restore);
    };
  }, [book.id, book.address?.revision, kind, active]);
  return { detail, show, error };
}
