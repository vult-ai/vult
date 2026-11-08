// Decoder boundary for DOM tests. Real PDF/image decoding is qualified in Chrome.
export const decoded = [];
export async function openDocument(blob, kind, signal) {
  const text = await blob.text();
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  if (text.includes("INVALID_DOCUMENT")) throw new Error("Invalid fixture");
  const entry = { kind, destroyed: false, paints: [] };
  decoded.push(entry);
  return {
    pages: text.includes("PAGES=3") ? 3 : 1,
    page: async (number) => ({
      width: 612,
      height: 792,
      text: `Accessible original page ${number}`,
      paint: async (canvas, scale, abort) => {
        if (abort.aborted) throw new DOMException("Aborted", "AbortError");
        canvas.width = 612 * scale;
        canvas.height = 792 * scale;
        entry.paints.push(number);
      },
    }),
    destroy: () => {
      entry.destroyed = true;
    },
  };
}
