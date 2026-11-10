import { fetchResponse } from "./errors";

/** Retry only a transient, idempotent original-file read, never a write. */
export async function readDocument(src: string, signal: AbortSignal) {
  const read = () => fetchResponse(src, {
    signal,
    credentials: "same-origin",
    cache: "no-store",
  });
  const first = await read();
  if (first.status !== 503) return first;
  const retryAfter = first.headers?.get("Retry-After");
  const seconds = retryAfter === null || retryAfter === undefined
    ? 0.25
    : /^\d+(?:\.\d+)?$/.test(retryAfter) ? Number(retryAfter) : NaN;
  // A longer server cooldown remains an explicit, user-controlled retry.
  if (!Number.isFinite(seconds) || seconds > 2) return first;
  await first.body?.cancel();
  await new Promise<void>((resolve, reject) => {
    const cancel = () => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", cancel);
      resolve();
    }, Math.max(100, seconds * 1000));
    signal.addEventListener("abort", cancel, { once: true });
    if (signal.aborted) cancel();
  });
  signal.throwIfAborted();
  return read();
}
